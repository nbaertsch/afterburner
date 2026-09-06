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
let modalCaptureScheduled = false;
const scrollSteps = [
  { name: "arrowDown", title: "Arrow down scroll screen", key: "\x1b[40;0;0;1;0;1_", want: /lines 2-\d+ of/i },
  { name: "arrowUp", title: "Arrow up scroll screen", key: "\x1b[38;0;0;1;0;1_", want: /lines 1-\d+ of/i },
  { name: "pageDown", title: "Page down scroll screen", key: "\x1b[34;0;0;1;0;1_", want: /lines (?:[2-9]|1\d)-\d+ of/i },
  { name: "pageUp", title: "Page up scroll screen", key: "\x1b[33;0;0;1;0;1_", want: /lines 1-\d+ of/i },
  { name: "end", title: "End scroll screen", key: "\x1b[35;0;0;1;0;1_", want: /lines (?:[2-9]|1\d)-\d+ of/i },
  { name: "home", title: "Home scroll screen", key: "\x1b[36;0;0;1;0;1_", want: /lines 1-\d+ of/i }
];
let scrollIndex = 0;
let activeScrollRawLength = 0;
const scrollSentAt = {};
const scrollSeenAt = {};
const scrollRaw = {};
let refreshSentAt = 0;
let refreshSeenAt = 0;
let refreshRawLength = 0;
let refreshRaw = "";
let doctorSeen = false;
let doctorCaptureScheduled = false;
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
  scroll: Object.fromEntries(scrollSteps.map(step => [step.name, {
    passed: Boolean(scrollSeenAt[step.name]),
    latencyMs: scrollSentAt[step.name] && scrollSeenAt[step.name] ? scrollSeenAt[step.name] - scrollSentAt[step.name] : null
  }])),
  refreshAction: Boolean(refreshSeenAt),
  refreshLatencyMs: refreshSentAt && refreshSeenAt ? refreshSeenAt - refreshSentAt : null,
  doctorAction: doctorSeen,
  closeRestored: Boolean(closeRestoredAt),
  openLatencyMs: commandSentAt && modalSeenAt ? modalSeenAt - commandSentAt : null,
  closeLatencyMs: closeRequestedAt && closeRestoredAt ? closeRestoredAt - closeRequestedAt : null,
  visibleSelfNoise: visibleSelfNoise(),
  ...extra
});

const escapeHtml = value => String(value)
  .replaceAll("&", "&amp;")
  .replaceAll("<", "&lt;")
  .replaceAll(">", "&gt;")
  .replaceAll('"', "&quot;");

const renderTerminalScreen = (value, columns = 140, rows = 40) => {
  const screen = Array.from({ length: rows }, () => Array(columns).fill(" "));
  let row = 0;
  let column = 0;
  const clamp = () => {
    row = Math.max(0, Math.min(rows - 1, row));
    column = Math.max(0, Math.min(columns - 1, column));
  };
  const clear = () => screen.forEach(line => line.fill(" "));
  const newline = () => {
    row++;
    column = 0;
    if (row >= rows) {
      screen.shift();
      screen.push(Array(columns).fill(" "));
      row = rows - 1;
    }
  };
  for (let index = 0; index < value.length; index++) {
    const ch = value[index];
    if (ch === "\x1b") {
      const next = value[++index];
      if (next === "]") {
        while (index < value.length && value[index] !== "\x07" && !(value[index] === "\x1b" && value[index + 1] === "\\")) index++;
        if (value[index] === "\x1b") index++;
        continue;
      }
      if (next !== "[") continue;
      let sequence = "";
      while (++index < value.length) {
        sequence += value[index];
        if (/[ -~]/.test(value[index]) && value.charCodeAt(index) >= 0x40) break;
      }
      const final = sequence.at(-1);
      const params = sequence.slice(0, -1).replace(/^\?/, "").split(";").map(part => Number(part || 0));
      if (final === "H" || final === "f") {
        row = Math.max(0, (params[0] || 1) - 1);
        column = Math.max(0, (params[1] || 1) - 1);
      } else if (final === "J" && (params[0] || 0) === 2) {
        clear();
        row = 0;
        column = 0;
      } else if (final === "K") {
        screen[row].fill(" ", column);
      } else if (final === "A") row -= params[0] || 1;
      else if (final === "B") row += params[0] || 1;
      else if (final === "C") column += params[0] || 1;
      else if (final === "D") column -= params[0] || 1;
      else if (final === "G") column = Math.max(0, (params[0] || 1) - 1);
      else if (final === "d") row = Math.max(0, (params[0] || 1) - 1);
      clamp();
      continue;
    }
    if (ch === "\r") { column = 0; continue; }
    if (ch === "\n") { newline(); continue; }
    if (ch === "\b") { column = Math.max(0, column - 1); continue; }
    if (ch < " " || ch === "\x7f") continue;
    screen[row][column] = ch;
    column++;
    if (column >= columns) newline();
  }
  return screen.map(line => line.join("").trimEnd()).join("\n").trimEnd();
};

let modalOpenRaw = "";
let doctorRaw = "";
let closeRestoreRaw = "";

const capturedScreens = () => [
  ["Modal open screen", modalOpenRaw ? renderTerminalScreen(modalOpenRaw) : ""],
  ...scrollSteps.map(step => [step.title, scrollRaw[step.name] ? renderTerminalScreen(scrollRaw[step.name]) : ""]),
  ["Refresh action screen", refreshRaw ? renderTerminalScreen(refreshRaw) : ""],
  ["Doctor action screen", doctorRaw ? renderTerminalScreen(doctorRaw) : ""],
  ["Close restore screen", closeRestoreRaw ? renderTerminalScreen(closeRestoreRaw) : ""]
].filter(([, body]) => body);

const visibleSelfNoise = () => capturedScreens()
  .filter(([title]) => title !== "Close restore screen")
  .flatMap(([title, body]) => [...body.matchAll(/\bui\.(?:modal_canvas|host)\.[a-z0-9_.-]+\b/gi)]
    .map(match => ({ title, eventType: match[0] })));

const visualReport = capture => {
  const body = capturedScreens().map(([title, content]) => `
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

const svgReport = capture => {
  const screens = capturedScreens();
  const charWidth = 8;
  const lineHeight = 16;
  const margin = 24;
  const titleHeight = 28;
  const summaryLines = JSON.stringify(capture, null, 2).split("\n");
  const blocks = [["Result", summaryLines.join("\n")], ...screens];
  const width = 1180;
  let y = margin;
  const parts = [`<svg xmlns="http://www.w3.org/2000/svg" width="${width}" height="${blocks.reduce((sum, [, content]) => sum + titleHeight + Math.max(1, content.split("\n").length) * lineHeight + margin, margin)}" viewBox="0 0 ${width} ${blocks.reduce((sum, [, content]) => sum + titleHeight + Math.max(1, content.split("\n").length) * lineHeight + margin, margin)}">`,
    `<rect width="100%" height="100%" fill="#0d1117"/>`];
  for (const [title, content] of blocks) {
    const lines = content.split("\n");
    const blockHeight = titleHeight + Math.max(1, lines.length) * lineHeight + 16;
    parts.push(`<text x="${margin}" y="${y + 18}" fill="#f0f6fc" font-family="Segoe UI, Arial, sans-serif" font-size="18" font-weight="700">${escapeHtml(title)}</text>`);
    parts.push(`<rect x="${margin}" y="${y + titleHeight}" width="${width - margin * 2}" height="${blockHeight - titleHeight}" rx="8" fill="#010409" stroke="#30363d"/>`);
    lines.forEach((line, index) => {
      parts.push(`<text x="${margin + 16}" y="${y + titleHeight + 22 + index * lineHeight}" fill="#e6edf3" font-family="Cascadia Mono, Consolas, monospace" font-size="13" xml:space="preserve">${escapeHtml(line.slice(0, Math.floor((width - margin * 2 - 32) / charWidth)))}</text>`);
    });
    y += blockHeight + margin;
  }
  parts.push("</svg>");
  return parts.join("\n");
};

const writeCaptures = (status = "running", extra = {}) => {
  const capture = result(status, extra);
  writeFileSync(join(captureDirectory, "blackbox-modal-tui.raw"), raw, "utf8");
  writeFileSync(join(captureDirectory, "blackbox-modal-tui.txt"), stripAnsi(raw), "utf8");
  writeFileSync(join(captureDirectory, "blackbox-modal-tui-result.json"), `${JSON.stringify(capture, null, 2)}\n`, "utf8");
  writeFileSync(join(captureDirectory, "blackbox-modal-tui-report.html"), visualReport(capture), "utf8");
  writeFileSync(join(captureDirectory, "blackbox-modal-tui-report.svg"), svgReport(capture), "utf8");
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
  }
  if (modalSeenAt && !modalCaptureScheduled) {
    modalCaptureScheduled = true;
    setTimeout(() => {
      modalOpenRaw = raw;
      const step = scrollSteps[scrollIndex];
      scrollSentAt[step.name] = Date.now();
      activeScrollRawLength = raw.length;
      child.write(step.key);
    }, 500).unref?.();
    return;
  }
  const activeStep = scrollSteps[scrollIndex];
  if (activeStep && scrollSentAt[activeStep.name] && !scrollSeenAt[activeStep.name] && activeStep.want.test(stripAnsi(raw.slice(activeScrollRawLength)))) {
    scrollSeenAt[activeStep.name] = Date.now();
    scrollRaw[activeStep.name] = raw;
    scrollIndex++;
    const nextStep = scrollSteps[scrollIndex];
    setTimeout(() => {
      if (nextStep) {
        scrollSentAt[nextStep.name] = Date.now();
        activeScrollRawLength = raw.length;
        child.write(nextStep.key);
      } else {
        refreshSentAt = Date.now();
        refreshRawLength = raw.length;
        child.write("r");
      }
    }, 250).unref?.();
    return;
  }
  if (refreshSentAt && !refreshSeenAt && /Afterburner Black Box Live/i.test(stripAnsi(raw.slice(refreshRawLength)))) {
    refreshSeenAt = Date.now();
    refreshRaw = raw;
    setTimeout(() => child.write("d"), 250).unref?.();
    return;
  }
  if (modalSeenAt && !doctorSeen && /Afterburner Black Box Doctor/i.test(text)) {
    const missing = scrollSteps.filter(step => !scrollSeenAt[step.name]).map(step => step.name);
    if (missing.length > 0) {
      finish(1, `Doctor action rendered before scroll validation completed: ${missing.join(", ")}`);
      return;
    }
    if (!refreshSeenAt) {
      finish(1, "Doctor action rendered before refresh validation completed");
      return;
    }
    doctorSeen = true;
  }
  if (doctorSeen && !doctorCaptureScheduled) {
    doctorCaptureScheduled = true;
    setTimeout(() => {
      doctorRaw = raw;
      closeSent = true;
      closeRequestedAt = Date.now();
      closeRawLength = raw.length;
      child.write("q");
    }, 500).unref?.();
    return;
  }
  if (closeSent && !closeRestoredAt) {
    const afterCloseText = stripAnsi(raw.slice(closeRawLength));
    if (/\/ commands|tab next tab|\? help/i.test(afterCloseText)) {
      closeRestoredAt = Date.now();
      closeRestoreRaw = raw;
      const selfNoise = visibleSelfNoise();
      if (selfNoise.length > 0) {
        finish(1, `Black Box modal displayed self-noise events: ${selfNoise.map(item => `${item.title}:${item.eventType}`).join(", ")}`);
        return;
      }
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
  else if (scrollSteps.some(step => !scrollSeenAt[step.name])) {
    const missing = scrollSteps.filter(step => !scrollSeenAt[step.name]).map(step => step.name).join(", ");
    finish(1, `timed out before Black Box scroll validation completed: ${missing}`);
  }
  else if (!refreshSeenAt) finish(1, "timed out before Black Box refresh action rendered");
  else if (!doctorSeen) finish(1, "timed out before Black Box doctor action rendered");
  else if (!closeSent) finish(1, "timed out before Black Box close key was sent");
  else finish(1, "timed out before Black Box modal close restored the Copilot prompt");
}, timeoutMs);
timeout.unref?.();
