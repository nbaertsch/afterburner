import { mkdirSync, writeFileSync } from "node:fs";
import { homedir } from "node:os";
import { join, resolve } from "node:path";
import process from "node:process";
import pty from "node-pty";

const stripAnsi = value => value
  .replace(/\x1b\][^\x07]*(?:\x07|\x1b\\)/g, "")
  .replace(/\x1b\[[0-?]*[ -/]*[@-~]/g, "")
  .replace(/\x1b[()][A-Za-z0-9]/g, "");

const option = name => {
  const index = process.argv.indexOf(name);
  return index >= 0 ? process.argv[index + 1] : undefined;
};
const positional = process.argv.slice(2).filter((value, index, args) => !value.startsWith("--") && !args[index - 1]?.startsWith("--"));

const afterburn = resolve(option("--afterburn") ?? process.env.AFTERBURNER_EXE ?? join(homedir(), ".afterburner", "bin", "afterburn.exe"));
const captureDirectory = resolve(option("--capture-dir") ?? positional[0] ?? join(process.cwd(), "artifacts", "real-tui"));
const timeoutMs = Number(option("--timeout-ms") ?? positional[1] ?? process.env.AFTERBURNER_REAL_TUI_TIMEOUT_MS ?? 90_000);
mkdirSync(captureDirectory, { recursive: true });

const env = { ...process.env, COPILOT_RUNTIME_EXTENSION_DEBUG: "1" };
delete env.COPILOT_AGENT_SESSION_ID;
delete env.COPILOT_LOADER_PID;
delete env.COPILOT_SUPERVISED;

const child = pty.spawn(afterburn, [], {
  name: "xterm-256color",
  cols: 140,
  rows: 40,
  cwd: process.cwd(),
  env
});

let raw = "";
let trusted = false;
let restored = false;
let approved = false;
let commandSentAt = 0;
let modalSeenAt = 0;
let doctorSeen = false;
let closeSent = false;
let finished = false;

const writeCaptures = () => {
  writeFileSync(join(captureDirectory, "blackbox-modal-tui.raw"), raw, "utf8");
  writeFileSync(join(captureDirectory, "blackbox-modal-tui.txt"), stripAnsi(raw), "utf8");
};

const finish = (code, message) => {
  if (finished) return;
  finished = true;
  clearTimeout(timeout);
  writeCaptures();
  try { child.kill(); } catch {}
  if (code === 0) process.stdout.write(`${message}\n`);
  else process.stderr.write(`${message}\n--- tail ---\n${stripAnsi(raw).slice(-6000)}\n`);
  process.exit(code);
};

const scheduleWrite = (data, delayMs = 150) => setTimeout(() => child.write(data), delayMs).unref?.();

child.onData(data => {
  raw += data;
  const text = stripAnsi(raw);
  const recent = text.slice(-5000);

  if (!trusted && /Do you trust the files in this folder/i.test(recent)) {
    trusted = true;
    scheduleWrite("\r", 250);
    return;
  }
  if (!restored && /Restore interrupted sessions/i.test(recent)) {
    restored = true;
    scheduleWrite("\x1b", 250);
    return;
  }
  if (!approved && /wants elevated permissions/i.test(recent)) {
    approved = true;
    scheduleWrite("\r", 250);
    return;
  }

  const runtimeReady = /\[runtime-extension-host\] loaded from/i.test(text) &&
    /activated Afterburner extension 'black-box'/i.test(text) &&
    /registered picker adapter/i.test(text);
  const promptReady = /\/ commands|tab next tab|\? help/i.test(recent);
  if (!commandSentAt && runtimeReady && promptReady) {
    commandSentAt = Date.now();
    scheduleWrite("/black-box-modal\r", 500);
    return;
  }

  if (commandSentAt && !modalSeenAt && /Afterburner Black Box Live/i.test(text)) {
    modalSeenAt = Date.now();
    scheduleWrite("d", 250);
    return;
  }
  if (modalSeenAt && !doctorSeen && /Afterburner Black Box Doctor/i.test(text)) {
    doctorSeen = true;
    scheduleWrite("q", 250);
    closeSent = true;
    setTimeout(() => finish(0, `real-blackbox-modal-tui-ok openLatencyMs=${modalSeenAt - commandSentAt} doctorAction=true`), 750).unref();
  }
});

child.onExit(({ exitCode }) => {
  if (finished) return;
  if (modalSeenAt && doctorSeen && closeSent) {
    finish(0, `real-blackbox-modal-tui-ok openLatencyMs=${modalSeenAt - commandSentAt} doctorAction=true exitCode=${exitCode}`);
    return;
  }
  finish(1, `afterburn exited before modal validation completed: ${exitCode}`);
});

const timeout = setTimeout(() => {
  if (!commandSentAt) finish(1, "timed out before Copilot prompt accepted /black-box-modal");
  else if (!modalSeenAt) finish(1, "timed out before Black Box modal appeared");
  else if (!doctorSeen) finish(1, "timed out before Black Box doctor action rendered");
  else finish(1, "timed out before Black Box modal closed");
}, timeoutMs);
timeout.unref?.();
