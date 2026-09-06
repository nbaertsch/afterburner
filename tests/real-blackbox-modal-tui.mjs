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
const positional = [];
for (let index = 2; index < process.argv.length; index++) {
  const value = process.argv[index];
  if (value.startsWith("--")) { index++; continue; }
  positional.push(value);
}

const positionalAfterburn = /(?:^|[\\/])[^\\/]+\.exe$/i.test(positional[0] ?? "") ? positional.shift() : undefined;
const afterburn = resolve(option("--afterburn") ?? positionalAfterburn ?? process.env.AFTERBURNER_EXE ?? join(homedir(), ".afterburner", "bin", "afterburn.exe"));
const captureDirectory = resolve(option("--capture-dir") ?? positional[0] ?? join(process.cwd(), "artifacts", "real-tui"));
const timeoutMs = Number(option("--timeout-ms") ?? positional[1] ?? process.env.AFTERBURNER_REAL_TUI_TIMEOUT_MS ?? 90_000);
const scriptStartedAt = Date.now();
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
let terminalSetupDeclined = false;
let commandSentAt = 0;
let modalSeenAt = 0;
let doctorSeen = false;
let closeSent = false;
let closeRequestedAt = 0;
let closeRawLength = 0;
let closeRestoredAt = 0;
let finished = false;

const result = (status, extra = {}) => ({
  schemaVersion: 1,
  status,
  afterburn,
  startedAt: new Date(scriptStartedAt).toISOString(),
  completedAt: new Date().toISOString(),
  commandSent: Boolean(commandSentAt),
  modalSeen: Boolean(modalSeenAt),
  doctorAction: doctorSeen,
  closeRestored: Boolean(closeRestoredAt),
  openLatencyMs: commandSentAt && modalSeenAt ? modalSeenAt - commandSentAt : null,
  closeLatencyMs: closeRequestedAt && closeRestoredAt ? closeRestoredAt - closeRequestedAt : null,
  ...extra
});

const escapeHtml = value => String(value)
  .replaceAll("&", "&amp;")
  .replaceAll("<", "&lt;")
  .replaceAll(">", "&gt;")
  .replaceAll('"', "&quot;");

const excerptAround = (text, pattern, radius = 2200) => {
  const index = text.search(pattern);
  if (index < 0) return "";
  return text.slice(Math.max(0, index - radius), Math.min(text.length, index + radius));
};

const visualReport = capture => {
  const text = stripAnsi(raw);
  const sections = [
    ["Modal open", excerptAround(text, /Afterburner Black Box Live/i)],
    ["Doctor action", excerptAround(text, /Afterburner Black Box Doctor/i)],
    ["Close restore", closeRawLength ? stripAnsi(raw.slice(closeRawLength)).slice(0, 3000) : ""]
  ].filter(([, body]) => body);
  const body = sections.map(([title, content]) => `
    <section>
      <h2>${escapeHtml(title)}</h2>
      <pre>${escapeHtml(content)}</pre>
    </section>`).join("\n");
  return `<!doctype html>
<html lang="en">
<meta charset="utf-8">
<title>Black Box Modal TUI Visual Report</title>
<style>
  :root { color-scheme: dark; font-family: ui-sans-serif, system-ui, sans-serif; background: #0d1117; color: #e6edf3; }
  body { margin: 24px; }
  h1, h2 { color: #f0f6fc; }
  .summary { border: 1px solid #30363d; border-radius: 8px; padding: 12px 16px; background: #161b22; }
  pre { overflow: auto; white-space: pre-wrap; border: 1px solid #30363d; border-radius: 8px; padding: 16px; background: #010409; line-height: 1.25; }
  code { color: #7ee787; }
</style>
<h1>Black Box Modal TUI Visual Report</h1>
<div class="summary"><pre>${escapeHtml(JSON.stringify(capture, null, 2))}</pre></div>
${body}
</html>
`;
};

const writeCaptures = (status = "running", extra = {}) => {
  const capture = result(status, extra);
  writeFileSync(join(captureDirectory, "blackbox-modal-tui.raw"), raw, "utf8");
  writeFileSync(join(captureDirectory, "blackbox-modal-tui.txt"), stripAnsi(raw), "utf8");
  writeFileSync(join(captureDirectory, "blackbox-modal-tui-result.json"), `${JSON.stringify(capture, null, 2)}\n`, "utf8");
  writeFileSync(join(captureDirectory, "blackbox-modal-tui-report.html"), visualReport(capture), "utf8");
};

const finish = (code, message) => {
  if (finished) return;
  finished = true;
  clearTimeout(timeout);
  writeCaptures(code === 0 ? "passed" : "failed", { message });
  try { child.kill(); } catch {}
  if (code === 0) process.stdout.write(`${message}\n`);
  else process.stderr.write(`${message}\n--- tail ---\n${stripAnsi(raw).slice(-6000)}\n`);
  process.exit(code);
};

const scheduleWrite = (data, delayMs = 150) => setTimeout(() => child.write(data), delayMs).unref?.();
const scheduleCommand = (command, delayMs = 150) => {
  [...command].forEach((character, index) => scheduleWrite(character, delayMs + index * 35));
  scheduleWrite("\r", delayMs + command.length * 35 + 1200);
};

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
  if (!terminalSetupDeclined && /Set up terminal for multi-line input support/i.test(recent)) {
    terminalSetupDeclined = true;
    scheduleWrite("\x1b", 250);
    return;
  }

  const runtimeReady = /\[runtime-extension-host\] loaded from/i.test(text) &&
    /activated Afterburner extension 'black-box'/i.test(text) &&
    /registered picker adapter/i.test(text);
  const promptReady = /\/ commands|tab next tab|\? help/i.test(recent);
  if (!commandSentAt && runtimeReady && promptReady) {
    commandSentAt = Date.now();
    scheduleCommand("/black-box-modal", 2500);
    return;
  }
  if (commandSentAt && !modalSeenAt && /Unknown command:\s*\/black-box-modal/i.test(recent)) {
    finish(1, "Copilot rejected /black-box-modal as an unknown command");
    return;
  }
  if (commandSentAt && !modalSeenAt && /Black Box modal unavailable/i.test(recent)) {
    finish(1, "Black Box reported modal unavailable instead of opening native modal");
    return;
  }
  if (commandSentAt && !modalSeenAt && /Canvas opened:\s*Afterburner Black Box/i.test(recent)) {
    finish(1, "Copilot opened the Black Box canvas instead of the native modal");
    return;
  }

  if (commandSentAt && !modalSeenAt && /Afterburner Black Box Live/i.test(text)) {
    modalSeenAt = Date.now();
    scheduleWrite("d", 250);
    return;
  }
  if (modalSeenAt && !doctorSeen && /Afterburner Black Box Doctor/i.test(text)) {
    doctorSeen = true;
    closeSent = true;
    closeRequestedAt = Date.now() + 250;
    closeRawLength = raw.length;
    scheduleWrite("q", 250);
    return;
  }
  if (closeSent && !closeRestoredAt) {
    const afterCloseText = stripAnsi(raw.slice(closeRawLength));
    if (/\/ commands|tab next tab|\? help/i.test(afterCloseText)) {
      closeRestoredAt = Date.now();
      finish(0, `real-blackbox-modal-tui-ok openLatencyMs=${modalSeenAt - commandSentAt} doctorAction=true closeRestored=true`);
    }
  }
});

child.onExit(({ exitCode }) => {
  if (finished) return;
  if (modalSeenAt && doctorSeen && closeSent && closeRestoredAt) {
    finish(0, `real-blackbox-modal-tui-ok openLatencyMs=${modalSeenAt - commandSentAt} doctorAction=true closeRestored=true exitCode=${exitCode}`);
    return;
  }
  finish(1, `afterburn exited before modal validation completed: ${exitCode}`);
});

const timeout = setTimeout(() => {
  if (!commandSentAt) finish(1, "timed out before Copilot prompt accepted /black-box-modal");
  else if (!modalSeenAt) finish(1, "timed out before Black Box modal appeared");
  else if (!doctorSeen) finish(1, "timed out before Black Box doctor action rendered");
  else if (!closeSent) finish(1, "timed out before Black Box close key was sent");
  else finish(1, "timed out before Black Box modal close restored the Copilot prompt");
}, timeoutMs);
timeout.unref?.();
