import { spawnSync } from "node:child_process";
import { existsSync, mkdirSync, readFileSync, statSync, writeFileSync } from "node:fs";
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
let commandInputStartedAt = 0;
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
let escapeCommandInputStartedAt = 0;
let escapeCommandSentAt = 0;
let escapeCommandRawLength = 0;
let escapeModalSeenAt = 0;
let escapeSentAt = 0;
let escapeRawLength = 0;
let escapeRestoredAt = 0;
let finished = false;

const artifactPath = name => join(captureDirectory, name);
const slugTitle = title => String(title).toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "");

const result = (status, extra = {}) => ({
  schemaVersion: 1,
  status,
  afterburn,
  startedAt: new Date(scriptStartedAt).toISOString(),
  completedAt: new Date().toISOString(),
  commandInputStarted: Boolean(commandInputStartedAt),
  commandSubmitted: Boolean(commandSentAt),
  modalSeen: Boolean(modalSeenAt),
  scroll: Object.fromEntries(scrollSteps.map(step => [step.name, {
    passed: Boolean(scrollSeenAt[step.name]),
    latencyMs: scrollSentAt[step.name] && scrollSeenAt[step.name] ? scrollSeenAt[step.name] - scrollSentAt[step.name] : null
  }])),
  refreshAction: Boolean(refreshSeenAt),
  refreshLatencyMs: refreshSentAt && refreshSeenAt ? refreshSeenAt - refreshSentAt : null,
  doctorAction: doctorSeen,
  closeRestored: Boolean(closeRestoredAt),
  escapeClose: Boolean(escapeRestoredAt),
  openLatencyMs: commandSentAt && modalSeenAt ? modalSeenAt - commandSentAt : null,
  reopenLatencyMs: escapeCommandSentAt && escapeModalSeenAt ? escapeModalSeenAt - escapeCommandSentAt : null,
  closeLatencyMs: closeRequestedAt && closeRestoredAt ? closeRestoredAt - closeRequestedAt : null,
  escapeCloseLatencyMs: escapeSentAt && escapeRestoredAt ? escapeRestoredAt - escapeSentAt : null,
  visibleSelfNoise: visibleSelfNoise(),
  visualInspection: visualInspectionChecks(),
  visualArtifacts: {
    raw: artifactPath("blackbox-modal-tui.raw"),
    text: artifactPath("blackbox-modal-tui.txt"),
    result: artifactPath("blackbox-modal-tui-result.json"),
    pngReport: artifactPath("blackbox-modal-tui-report.png"),
    screenPngs: capturedScreens().map(([title]) => artifactPath(`${slugTitle(title)}.png`))
  },
  ...extra
});

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
let escapeModalRaw = "";
let escapeRestoreRaw = "";

const extractModalBlock = (value, titlePattern) => {
  const text = stripAnsi(value).replace(/\r/g, "");
  const matches = [...text.matchAll(titlePattern)];
  const titleIndex = matches.at(-1)?.index ?? -1;
  if (titleIndex < 0) return "";
  const start = text.lastIndexOf("╭", titleIndex);
  const end = text.indexOf("╰", titleIndex);
  if (start < 0 || end < 0) return "";
  const lineEnd = text.indexOf("\n", end);
  return text.slice(start, lineEnd < 0 ? undefined : lineEnd).trimEnd();
};

const visualScreen = (value, titlePattern = null) => {
  if (!value) return "";
  const modalBlock = titlePattern ? extractModalBlock(value, titlePattern) : "";
  const rendered = renderTerminalScreen(value);
  return modalBlock || rendered;
};

const capturedScreens = () => [
  ["Modal open screen", visualScreen(modalOpenRaw, /Afterburner Black Box Live/gi)],
  ...scrollSteps.map(step => [step.title, visualScreen(scrollRaw[step.name], /Afterburner Black Box Live/gi)]),
  ["Refresh action screen", visualScreen(refreshRaw, /Afterburner Black Box Live/gi)],
  ["Doctor action screen", visualScreen(doctorRaw, /Afterburner Black Box Doctor/gi)],
  ["Q close restore screen", visualScreen(closeRestoreRaw)],
  ["Escape close modal screen", visualScreen(escapeModalRaw, /Afterburner Black Box Live/gi)],
  ["Escape close restore screen", visualScreen(escapeRestoreRaw)]
].filter(([, body]) => body);

const visibleSelfNoise = () => capturedScreens()
  .filter(([title]) => title !== "Q close restore screen" && title !== "Escape close restore screen")
  .flatMap(([title, body]) => [...body.matchAll(/\bui\.(?:modal_canvas|host)\.[a-z0-9_.-]+\b/gi)]
    .map(match => ({ title, eventType: match[0] })));

const visualInspectionChecks = () => {
  const screens = Object.fromEntries(capturedScreens());
  const checks = {
    modalHasBoxChrome: /╭/.test(screens["Modal open screen"] ?? "") && /╰/.test(screens["Modal open screen"] ?? ""),
    modalShowsTitle: /Afterburner Black Box Live/i.test(screens["Modal open screen"] ?? ""),
    modalShowsActionBar: /\[r\] Refresh\s+\[d\] Doctor\s+\[q\] Close/i.test(screens["Modal open screen"] ?? ""),
    modalAdvertisesAllScrollKeys: /↑\/↓ PgUp\/PgDn Home\/End/.test(screens["Modal open screen"] ?? ""),
    everyScrollScreenCaptured: scrollSteps.every(step => Boolean(screens[step.title])),
    refreshScreenCaptured: /Afterburner Black Box Live/i.test(screens["Refresh action screen"] ?? ""),
    doctorScreenCaptured: /Afterburner Black Box Doctor/i.test(screens["Doctor action screen"] ?? ""),
    promptRestoredAfterQ: /\/ commands|tab next tab|\? help/i.test(screens["Q close restore screen"] ?? ""),
    promptRestoredAfterEscape: /\/ commands|tab next tab|\? help/i.test(screens["Escape close restore screen"] ?? ""),
    noVisibleSelfNoise: visibleSelfNoise().length === 0
  };
  return { passed: Object.values(checks).every(Boolean), checks };
};

const failIfVisualInspectionFailed = () => {
  const inspection = visualInspectionChecks();
  if (inspection.passed) return false;
  const failed = Object.entries(inspection.checks).filter(([, passed]) => !passed).map(([name]) => name);
  finish(1, `visual inspection checks failed: ${failed.join(", ")}`);
  return true;
};

const expectedPngArtifacts = () => [
  artifactPath("blackbox-modal-tui-report.png"),
  ...capturedScreens().map(([title]) => artifactPath(`${slugTitle(title)}.png`))
];

const validatePngArtifacts = () => {
  if (process.platform !== "win32") return { passed: false, message: "PNG visual artifacts are only rendered on Windows" };
  const signature = Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]);
  const invalid = expectedPngArtifacts().filter(path => {
    if (!existsSync(path)) return true;
    const stat = statSync(path);
    if (stat.size < 1024) return true;
    return !readFileSync(path).subarray(0, 8).equals(signature);
  });
  return invalid.length === 0
    ? { passed: true, message: "PNG visual artifacts are present and valid" }
    : { passed: false, message: `missing or invalid PNG visual artifacts: ${invalid.join(", ")}` };
};

const writePngReport = capture => {
  if (process.platform !== "win32") return;
  const payloadPath = join(captureDirectory, "blackbox-modal-tui-visual.json");
  const scriptPath = join(captureDirectory, "render-blackbox-modal-png.ps1");
  const outputPath = join(captureDirectory, "blackbox-modal-tui-report.png");
  const payload = { capture, screens: capturedScreens().map(([title, content]) => ({ title, content, file: `${slugTitle(title)}.png` })) };
  writeFileSync(payloadPath, `${JSON.stringify(payload, null, 2)}\n`, "utf8");
  writeFileSync(scriptPath, `param([string]$PayloadPath, [string]$OutputPath)
Add-Type -AssemblyName System.Drawing
$data = Get-Content -LiteralPath $PayloadPath -Raw | ConvertFrom-Json
$font = [System.Drawing.Font]::new('Cascadia Mono', 10)
$titleFont = [System.Drawing.Font]::new('Segoe UI', 16, [System.Drawing.FontStyle]::Bold)
$brush = [System.Drawing.SolidBrush]::new([System.Drawing.ColorTranslator]::FromHtml('#e6edf3'))
$titleBrush = [System.Drawing.SolidBrush]::new([System.Drawing.ColorTranslator]::FromHtml('#f0f6fc'))
$bg = [System.Drawing.SolidBrush]::new([System.Drawing.ColorTranslator]::FromHtml('#0d1117'))
$panel = [System.Drawing.SolidBrush]::new([System.Drawing.ColorTranslator]::FromHtml('#010409'))
$pen = [System.Drawing.Pen]::new([System.Drawing.ColorTranslator]::FromHtml('#30363d'))
$lineHeight = 16
$margin = 24
$width = 1200
$blocks = @(@{ title = 'Result'; content = ($data.capture | ConvertTo-Json -Depth 8) }) + @($data.screens)
$height = $margin
foreach ($block in $blocks) { $height += 36 + (($block.content -split [char]10).Count * $lineHeight) + $margin }
$bitmap = [System.Drawing.Bitmap]::new($width, [Math]::Max($height, 200))
$graphics = [System.Drawing.Graphics]::FromImage($bitmap)
try {
  $graphics.Clear([System.Drawing.ColorTranslator]::FromHtml('#0d1117'))
  $graphics.TextRenderingHint = [System.Drawing.Text.TextRenderingHint]::ClearTypeGridFit
  $y = $margin
  foreach ($block in $blocks) {
    $graphics.DrawString([string]$block.title, $titleFont, $titleBrush, $margin, $y)
    $y += 30
    $lines = [string]$block.content -split [char]10
    $blockHeight = [Math]::Max(1, $lines.Count) * $lineHeight + 16
    $graphics.FillRectangle($panel, $margin, $y, $width - ($margin * 2), $blockHeight)
    $graphics.DrawRectangle($pen, $margin, $y, $width - ($margin * 2), $blockHeight)
    $textY = $y + 8
    foreach ($line in $lines) { $graphics.DrawString($line, $font, $brush, $margin + 12, $textY); $textY += $lineHeight }
    $y += $blockHeight + $margin
  }
  $bitmap.Save($OutputPath, [System.Drawing.Imaging.ImageFormat]::Png)
  $outputDirectory = Split-Path -Parent $OutputPath
  foreach ($screen in @($data.screens)) {
    $lines = [string]$screen.content -split [char]10
    $screenHeight = 36 + ([Math]::Max(1, $lines.Count) * $lineHeight) + 16
    $screenBitmap = [System.Drawing.Bitmap]::new($width, [Math]::Max($screenHeight + $margin, 200))
    $screenGraphics = [System.Drawing.Graphics]::FromImage($screenBitmap)
    try {
      $screenGraphics.Clear([System.Drawing.ColorTranslator]::FromHtml('#0d1117'))
      $screenGraphics.TextRenderingHint = [System.Drawing.Text.TextRenderingHint]::ClearTypeGridFit
      $screenGraphics.DrawString([string]$screen.title, $titleFont, $titleBrush, $margin, $margin)
      $screenGraphics.FillRectangle($panel, $margin, $margin + 30, $width - ($margin * 2), $screenHeight - 30)
      $screenGraphics.DrawRectangle($pen, $margin, $margin + 30, $width - ($margin * 2), $screenHeight - 30)
      $textY = $margin + 38
      foreach ($line in $lines) { $screenGraphics.DrawString($line, $font, $brush, $margin + 12, $textY); $textY += $lineHeight }
      $screenBitmap.Save((Join-Path $outputDirectory ([string]$screen.file)), [System.Drawing.Imaging.ImageFormat]::Png)
    } finally { $screenGraphics.Dispose(); $screenBitmap.Dispose() }
  }
} finally {
  $graphics.Dispose(); $bitmap.Dispose(); $font.Dispose(); $titleFont.Dispose(); $brush.Dispose(); $titleBrush.Dispose(); $bg.Dispose(); $panel.Dispose(); $pen.Dispose()
}
`, "utf8");
  const renderer = spawnSync("powershell.exe", ["-NoProfile", "-ExecutionPolicy", "Bypass", "-File", scriptPath, payloadPath, outputPath], { encoding: "utf8" });
  if (renderer.status !== 0) {
    writeFileSync(join(captureDirectory, "blackbox-modal-png-render.log"), `${renderer.stdout ?? ""}\n${renderer.stderr ?? ""}`, "utf8");
  }
};

const writeCaptures = (status = "running", extra = {}) => {
  const capture = result(status, extra);
  writeFileSync(join(captureDirectory, "blackbox-modal-tui.raw"), raw, "utf8");
  writeFileSync(join(captureDirectory, "blackbox-modal-tui.txt"), stripAnsi(raw), "utf8");
  writeFileSync(join(captureDirectory, "blackbox-modal-tui-result.json"), `${JSON.stringify(capture, null, 2)}\n`, "utf8");
  writePngReport(capture);
};

const finish = (code, message) => {
  if (finished) return;
  finished = true;
  clearTimeout(timeout);
  writeCaptures(code === 0 ? "passed" : "failed", { message });
  let exitCode = code;
  let finalMessage = message;
  if (code === 0) {
    const pngValidation = validatePngArtifacts();
    if (!pngValidation.passed) {
      exitCode = 1;
      finalMessage = pngValidation.message;
      writeCaptures("failed", { message: finalMessage });
    }
  }
  try { child.kill(); } catch {}
  if (exitCode === 0) process.stdout.write(`${finalMessage}\n`);
  else process.stderr.write(`${finalMessage}\n--- tail ---\n${stripAnsi(raw).slice(-6000)}\n`);
  process.exit(exitCode);
};

const scheduleWrite = (data, delayMs = 150) => setTimeout(() => child.write(data), delayMs).unref?.();
const scheduleCommand = (command, delayMs = 150, onSubmit = () => {}) => {
  [...command].forEach((character, index) => scheduleWrite(character, delayMs + index * 35));
  setTimeout(() => {
    onSubmit();
    child.write("\r");
  }, delayMs + command.length * 35 + 1200).unref?.();
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
  if (!commandInputStartedAt && runtimeReady && promptReady) {
    commandInputStartedAt = Date.now();
    scheduleCommand("/black-box-modal", 2500, () => { commandSentAt = Date.now(); });
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
      escapeCommandInputStartedAt = Date.now();
      scheduleCommand("/black-box-modal", 500, () => {
        escapeCommandSentAt = Date.now();
        escapeCommandRawLength = raw.length;
      });
      return;
    }
  }
  if (escapeCommandSentAt && !escapeModalSeenAt && /Afterburner Black Box Live/i.test(stripAnsi(raw.slice(escapeCommandRawLength)))) {
    escapeModalSeenAt = Date.now();
    escapeModalRaw = raw;
    setTimeout(() => {
      escapeSentAt = Date.now();
      escapeRawLength = raw.length;
      child.write("\x1b[27;1;0;1;0;1_");
    }, 500).unref?.();
    return;
  }
  if (escapeSentAt && !escapeRestoredAt) {
    const afterEscapeText = stripAnsi(raw.slice(escapeRawLength));
    if (/\/ commands|tab next tab|\? help/i.test(afterEscapeText)) {
      escapeRestoredAt = Date.now();
      escapeRestoreRaw = raw;
      if (failIfVisualInspectionFailed()) return;
      const scrollSummary = scrollSteps.map(step => `${step.name}:${scrollSeenAt[step.name] - scrollSentAt[step.name]}ms`).join(",");
      finish(0, `real-blackbox-modal-tui-ok openLatencyMs=${modalSeenAt - commandSentAt} scroll=${scrollSummary} refreshLatencyMs=${refreshSeenAt - refreshSentAt} doctorAction=true qCloseLatencyMs=${closeRestoredAt - closeRequestedAt} escapeCloseLatencyMs=${escapeRestoredAt - escapeSentAt} report=${join(captureDirectory, "blackbox-modal-tui-report.png")}`);
    }
  }
});

child.onExit(({ exitCode }) => {
  if (finished) return;
  if (modalSeenAt && doctorSeen && closeSent && closeRestoredAt && escapeRestoredAt) {
    if (failIfVisualInspectionFailed()) return;
    const scrollSummary = scrollSteps.map(step => `${step.name}:${scrollSeenAt[step.name] - scrollSentAt[step.name]}ms`).join(",");
    finish(0, `real-blackbox-modal-tui-ok openLatencyMs=${modalSeenAt - commandSentAt} scroll=${scrollSummary} refreshLatencyMs=${refreshSeenAt - refreshSentAt} doctorAction=true qCloseLatencyMs=${closeRestoredAt - closeRequestedAt} escapeCloseLatencyMs=${escapeRestoredAt - escapeSentAt} report=${join(captureDirectory, "blackbox-modal-tui-report.png")} exitCode=${exitCode}`);
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
  else if (!closeSent) finish(1, "timed out before Black Box q close key was sent");
  else if (!closeRestoredAt) finish(1, "timed out before Black Box q close restored the Copilot prompt");
  else if (!escapeCommandSentAt) finish(1, "timed out before reopening Black Box modal for Escape close validation");
  else if (!escapeModalSeenAt) finish(1, "timed out before reopened Black Box modal appeared");
  else if (!escapeSentAt) finish(1, "timed out before Black Box Escape close key was sent");
  else finish(1, "timed out before Black Box Escape close restored the Copilot prompt");
}, timeoutMs);
timeout.unref?.();
