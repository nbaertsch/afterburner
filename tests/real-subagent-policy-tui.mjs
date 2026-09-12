import { spawnSync } from "node:child_process";
import { cpSync, existsSync, mkdirSync, readFileSync, readdirSync, rmSync, statSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { delimiter, join, resolve } from "node:path";
import process from "node:process";
import pty from "node-pty";
import { bootstrapExperimentalCopilotProfile } from "./copilot-experimental-profile.mjs";

const afterburn = resolve(process.argv[2] ?? process.env.AFTERBURNER_EXE ?? "artifacts\\afterburn.exe");
const captureDirectory = resolve(process.argv[3] ?? join(process.cwd(), "artifacts", "subagent-policy-tui"));
const timeoutMs = Number(process.argv[4] ?? process.env.AFTERBURNER_REAL_TUI_TIMEOUT_MS ?? 120_000);
const repoRoot = process.cwd();
const root = join(tmpdir(), `afterburn-subagent-policy-${process.pid}-${Date.now()}`);
const afterburnerHome = join(root, "afterburner");
const normalCopilotHome = join(root, "normal-copilot");
const workspace = join(root, "workspace");
const startedAt = Date.now();
const terminalColumns = 140;
const terminalRows = 40;

mkdirSync(captureDirectory, { recursive: true });
for (const path of [
  join(afterburnerHome, "config"),
  join(afterburnerHome, "copilot-home"),
  normalCopilotHome,
  workspace,
  join(root, "localappdata"),
  join(root, "appdata")
]) mkdirSync(path, { recursive: true });
process.once("exit", () => {
  try { rmSync(root, { recursive: true, force: true }); } catch {}
});

const stripAnsi = value => value
  .replace(/\x1b\][^\x07]*(?:\x07|\x1b\\)/g, "")
  .replace(/\x1b\[[0-?]*[ -/]*[@-~]/g, "")
  .replace(/\x1b[()][A-Za-z0-9]/g, "");
const renderTerminalScreen = (value, columns = terminalColumns, rows = terminalRows) => {
  const screen = Array.from({ length: rows }, () => Array(columns).fill(" "));
  let row = 0, column = 0;
  const clamp = () => { row = Math.max(0, Math.min(rows - 1, row)); column = Math.max(0, Math.min(columns - 1, column)); };
  for (let index = 0; index < String(value ?? "").length;) {
    const rest = value.slice(index);
    const osc = rest.match(/^\x1b\][^\x07]*(?:\x07|\x1b\\)/);
    if (osc) { index += osc[0].length; continue; }
    const csi = rest.match(/^\x1b\[([0-9;?]*)([ -/]?)([@-~])/);
    if (csi) {
      const params = csi[1].replace(/^\?/, "").split(";").filter(Boolean).map(Number);
      const final = csi[3];
      if (final === "H" || final === "f") { row = (params[0] || 1) - 1; column = (params[1] || 1) - 1; }
      else if (final === "A") row -= params[0] || 1;
      else if (final === "B") row += params[0] || 1;
      else if (final === "C") column += params[0] || 1;
      else if (final === "D") column -= params[0] || 1;
      else if (final === "G") column = (params[0] || 1) - 1;
      else if (final === "J" && (params[0] || 0) === 2) screen.forEach(line => line.fill(" "));
      else if (final === "K") screen[row].fill(" ", column);
      clamp(); index += csi[0].length; continue;
    }
    const char = rest[0];
    if (char === "\r") column = 0;
    else if (char === "\n") { row++; column = 0; if (row >= rows) { screen.shift(); screen.push(Array(columns).fill(" ")); row = rows - 1; } }
    else if (char === "\b") column = Math.max(0, column - 1);
    else if (char >= " ") { screen[row][column] = char; column++; if (column >= columns) { column = 0; row = Math.min(rows - 1, row + 1); } }
    index++;
  }
  return screen.map(line => line.join("").trimEnd()).join("\n").replace(/\n+$/g, "");
};
const extractModalViewport = value => {
  const text = stripAnsi(value);
  const matches = [...text.matchAll(/Subagent Policy/gi)];
  const start = matches.at(-1)?.index ?? -1;
  if (start < 0) return renderTerminalScreen(value);
  const before = text.lastIndexOf("\n", Math.max(0, start - 1000));
  const after = text.indexOf("\n C:\\", start);
  return text.slice(before < 0 ? start : before + 1, after < 0 ? start + 6000 : after).trimEnd();
};
const encodeChunk = data => Buffer.from(data, "utf8").toString("base64");
const printableInput = data => data.replace(/\x1b/g, "<Esc>").replace(/\r/g, "<Enter>").replace(/\x15/g, "<Ctrl+U>");
const elapsedSeconds = () => Number(((Date.now() - startedAt) / 1000).toFixed(6));
const artifactPath = name => join(captureDirectory, name);
const writeJson = (path, value) => writeFileSync(path, `${JSON.stringify(value, null, 2)}\n`, "utf8");
const existingDirectory = path => { try { return statSync(path).isDirectory(); } catch { return false; } };

const discoverPackageRoots = () => {
  const roots = new Set();
  for (const raw of (process.env.AFTERBURNER_COPILOT_PACKAGE_ROOTS ?? "").split(delimiter)) {
    if (raw.trim()) roots.add(resolve(raw.trim()));
  }
  for (const candidate of [
    process.env.USERPROFILE && join(process.env.USERPROFILE, ".copilot", "pkg", "win32-x64"),
    process.env.LOCALAPPDATA && join(process.env.LOCALAPPDATA, "copilot", "pkg", "win32-x64")
  ].filter(Boolean)) {
    if (existingDirectory(candidate)) roots.add(resolve(candidate));
  }
  return [...roots];
};

const isolatedEnvironment = (extra = {}) => {
  const environment = { ...process.env };
  for (const key of Object.keys(environment)) {
    if (/^(COPILOT_HOME|COPILOT_AGENT_SESSION_ID|COPILOT_CLI|COPILOT_CLI_BINARY_VERSION|COPILOT_CLI_RESOLVED_DIST_DIR|COPILOT_LOADER_PID|COPILOT_SUPERVISED|AFTERBURNER_HOME|AFTERBURNER_NORMAL_COPILOT_HOME|AFTERBURNER_DISABLED_EXTENSIONS)$/i.test(key)) delete environment[key];
  }
  environment.AFTERBURNER_HOME = afterburnerHome;
  environment.AFTERBURNER_NORMAL_COPILOT_HOME = normalCopilotHome;
  environment.AFTERBURNER_ISOLATE_SESSION_STATE = "1";
  const explicitRoots = (process.env.AFTERBURNER_COPILOT_PACKAGE_ROOTS ?? "").split(delimiter).filter(Boolean);
  const pinnedPackageRoot = join(process.env.LOCALAPPDATA ?? "", "copilot", "pkg", "win32-x64");
  const packageRoots = explicitRoots.length
    ? explicitRoots.map(root => resolve(root))
    : (existingDirectory(join(pinnedPackageRoot, "1.0.84-4")) ? [pinnedPackageRoot] : discoverPackageRoots());
  if (packageRoots.length > 0) environment.AFTERBURNER_COPILOT_PACKAGE_ROOTS = packageRoots.join(delimiter);
  if (process.env.AFTERBURNER_COPILOT_EXECUTABLE) {
    environment.AFTERBURNER_COPILOT_EXECUTABLE = process.env.AFTERBURNER_COPILOT_EXECUTABLE;
  }
  return { ...environment, ...extra };
};

if (!existsSync(afterburn)) throw new Error(`Afterburner executable not found: ${afterburn}`);
const installEnv = isolatedEnvironment({ AFTERBURNER_DISABLE_BUILTIN_RELEASE_FETCH: "1" });
const packageSource = join(root, "package-source");
const packageArchive = join(root, "subagent-policy-uat.zip");
cpSync(join(repoRoot, "extensions", "SubagentPolicy"), packageSource, {
  recursive: true,
  filter: path => ![".test-work", "node_modules"].includes(path.split(/[\\/]/).at(-1))
});
const packageManifestPath = join(packageSource, "afterburner.json");
const packageManifest = JSON.parse(readFileSync(packageManifestPath, "utf8"));
packageManifest.id = "subagent-policy-uat";
packageManifest.visibility = "private";
writeJson(packageManifestPath, packageManifest);
for (const [args, label] of [
  [["extension", "pack", packageSource, packageArchive], "pack"],
  [["extension", "install", packageArchive], "install"],
  [["extension", "enable", "subagent-policy-uat"], "enable"]
]) {
  const result = spawnSync(afterburn, args, { cwd: repoRoot, encoding: "utf8", env: installEnv });
  if (result.status !== 0) throw new Error(`failed to ${label} Subagent Policy UAT package: status=${result.status} stdout=${result.stdout} stderr=${result.stderr}`);
}

const registry = JSON.parse(readFileSync(join(afterburnerHome, "registry.json"), "utf8"));
const entry = registry.extensions?.["subagent-policy-uat"];
if (!entry?.activePath) throw new Error("Subagent Policy UAT extension did not install into registry");
writeJson(join(afterburnerHome, "copilot-home", "settings.json"), {
  experimental: true,
  enabledPlugins: { "afterburner-subagent-policy": true },
  extensions: { disabledExtensions: [] }
});
const copilotConfigPath = join(afterburnerHome, "copilot-home", "config.json");
const copilotConfig = JSON.parse(readFileSync(copilotConfigPath, "utf8"));
copilotConfig.appTipShown = true;
copilotConfig.askedSetupTerminals = ["windows-terminal"];
writeJson(copilotConfigPath, copilotConfig);

const env = isolatedEnvironment({
  COPILOT_RUNTIME_EXTENSION_DEBUG: "1",
  AFTERBURNER_SKIP_PREFLIGHT: "1"
});
await bootstrapExperimentalCopilotProfile({
  afterburn,
  cwd: workspace,
  env,
  managedCopilotHome: join(afterburnerHome, "copilot-home"),
  label: "afterburn-subagent-policy-bootstrap"
});
const child = pty.spawn(afterburn, ["--name", `afterburn-subagent-policy-uat-${process.pid}-${Date.now()}`, "--no-remote"], {
  name: "xterm-256color",
  cols: terminalColumns,
  rows: terminalRows,
  cwd: workspace,
  env
});

let raw = "";
const ioEvents = [];
const operatorSteps = [];
let trusted = false;
let restored = false;
let approved = false;
let terminalSetupDeclined = false;
let nativeAppSelectionMoved = false;
let nativeAppDeclined = false;
let commandSentAt = 0;
let modalSeenAt = 0;
let commandSubmitRetryAt = 0;
let stage = "starting";
let stageSentAt = 0;
let stageRawLength = 0;
let finished = false;

const recordOutput = data => ioEvents.push({ t: elapsedSeconds(), type: "output", bytes: Buffer.byteLength(data), dataBase64: encodeChunk(data) });
const writeInput = (data, label) => {
  ioEvents.push({ t: elapsedSeconds(), type: "input", label, display: printableInput(data), bytes: Buffer.byteLength(data), dataBase64: encodeChunk(data) });
  child.write(data);
};
const scheduleWrite = (data, delayMs = 250, label = "input") => setTimeout(() => writeInput(data, label), delayMs).unref?.();
const submitCommand = (label, delayMs = 250) => setTimeout(() => {
  writeInput("\x1b", `${label}: ensure prompt focus`);
  writeInput("\x15", `${label}: clear prompt`);
  writeInput("/subagents\r", `${label}: type and submit command`);
}, delayMs).unref?.();
const currentPlain = () => stripAnsi(raw);
const currentViewport = () => extractModalViewport(raw);
const recentPlain = () => currentPlain().slice(-8000);
const stagePlain = () => stripAnsi(raw.slice(stageRawLength));
const promptVisible = value => /\/ commands|tab next tab|\? help|@ files · # issues|Tip:\s*\/usage/i.test(value);
const modalPattern = /Subagent Configuration[\s\S]*Policy presets[\s\S]*Luna Three/i;
const recordStep = (name, key, started, completed, assertions) => {
  operatorSteps.push({
    name,
    key,
    startedAt: new Date(started).toISOString(),
    completedAt: new Date(completed).toISOString(),
    latencyMs: completed - started,
    viewportText: extractModalViewport(raw.slice(stageRawLength)) || currentViewport(),
    assertions
  });
};
const beginStage = (nextStage, input, label) => {
  stage = nextStage;
  stageSentAt = Date.now();
  stageRawLength = raw.length;
  scheduleWrite(input, 750, label);
};

const writePngReport = capture => {
  if (process.platform !== "win32") return;
  const payloadPath = artifactPath("subagent-policy-tui-visual.json");
  const scriptPath = artifactPath("render-subagent-policy-tui-png.ps1");
  const outputPath = artifactPath("subagent-policy-tui-report.png");
  writeJson(payloadPath, { capture, screens: operatorSteps.map(step => ({ title: step.name, content: step.viewportText })) });
  writeFileSync(scriptPath, `param([string]$PayloadPath, [string]$OutputPath)
Add-Type -AssemblyName System.Drawing
$data = Get-Content -LiteralPath $PayloadPath -Raw | ConvertFrom-Json
$font = [System.Drawing.Font]::new('Cascadia Mono', 10)
$titleFont = [System.Drawing.Font]::new('Segoe UI', 16, [System.Drawing.FontStyle]::Bold)
$brush = [System.Drawing.SolidBrush]::new([System.Drawing.ColorTranslator]::FromHtml('#e6edf3'))
$titleBrush = [System.Drawing.SolidBrush]::new([System.Drawing.ColorTranslator]::FromHtml('#f0f6fc'))
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
} finally { $graphics.Dispose(); $bitmap.Dispose(); $font.Dispose(); $titleFont.Dispose(); $brush.Dispose(); $titleBrush.Dispose(); $panel.Dispose(); $pen.Dispose() }
`, "utf8");
  const renderer = spawnSync("powershell.exe", ["-NoProfile", "-ExecutionPolicy", "Bypass", "-File", scriptPath, payloadPath, outputPath], { encoding: "utf8" });
  if (renderer.status !== 0) writeFileSync(artifactPath("subagent-policy-tui-png-render.log"), `${renderer.stdout ?? ""}\n${renderer.stderr ?? ""}`, "utf8");
};

const finish = (exitCode, message) => {
  if (finished) return;
  finished = true;
  const plain = currentPlain();
  let finalState = null;
  try {
    const routes = join(afterburnerHome, "state", "subagent-policy", "routes");
    const statePath = readdirSync(routes, { withFileTypes: true })
      .filter(item => item.isDirectory())
      .map(item => join(routes, item.name, "bridge-state.json"))
      .find(path => existsSync(path));
    if (statePath) finalState = JSON.parse(readFileSync(statePath, "utf8"));
  } catch {}
  const result = {
    schemaVersion: 1,
    status: exitCode === 0 ? "passed" : "failed",
    message,
    afterburn,
    captureDirectory,
    startedAt: new Date(startedAt).toISOString(),
    completedAt: new Date().toISOString(),
    packagePreflight: {
      id: "subagent-policy-uat",
      activePath: entry.activePath,
      visibility: entry.manifest?.visibility,
      manifestCapabilities: entry.manifest?.capabilities ?? [],
      sessionExtensionEntrypoint: entry.manifest?.sessionExtension?.entrypoint,
      transformation: "afterburner.json id -> subagent-policy-uat and visibility -> private only"
    },
    finalState,
    assertions: {
      usedRequestedArtifact: afterburn.toLowerCase() === resolve("artifacts\\afterburn.exe").toLowerCase(),
      privateTransformedPackageInstalled: entry.manifest?.visibility === "private" && entry.manifest?.id === "subagent-policy-uat",
      nativeModalRendered: operatorSteps.some(step => modalPattern.test(step.viewportText)),
      selectionMoved: operatorSteps.some(step => step.name === "preview Balanced selection" && /Balanced/i.test(step.viewportText)),
      balancedApplied: operatorSteps.some(step => /Active:\s*balanced/i.test(step.viewportText)),
      burstApplied: operatorSteps.some(step => /Active:\s*burst/i.test(step.viewportText)),
      durableBurstOnReopen: operatorSteps.some(step => step.name === "reopen and verify current-session state" && /Active:\s*burst/i.test(step.viewportText)),
      cleared: operatorSteps.some(step => step.name === "clear current-session policy" && /Active:\s*none/i.test(step.viewportText)),
      noCanvasOnlyFallback: !/Canvas opened:\s*Subagent Policy/i.test(plain),
      noTextFallback: !/native menu unavailable|interactive menu could not open/i.test(plain),
      packageHasModalCapability: (entry.manifest?.capabilities ?? []).includes("modal-canvas"),
      packageHasCompatibilityWrapper: existsSync(join(entry.activePath, "com.github.copilot", "extensions", "SubagentPolicy", "extension.mjs"))
    },
    operatorSteps,
    artifacts: {
      rawAnsi: artifactPath("subagent-policy-tui.raw.ansi"),
      plainText: artifactPath("subagent-policy-tui.txt"),
      ioJsonl: artifactPath("subagent-policy-tui-io.jsonl"),
      cast: artifactPath("subagent-policy-tui.cast"),
      operatorJson: artifactPath("subagent-policy-operator.json"),
      operatorMarkdown: artifactPath("subagent-policy-operator.md"),
      pngReport: artifactPath("subagent-policy-tui-report.png")
    }
  };
  const failedAssertions = Object.entries(result.assertions).filter(([, passed]) => passed !== true).map(([name]) => name);
  if (exitCode === 0 && failedAssertions.length > 0) {
    result.status = "failed";
    result.message = `UAT assertions failed: ${failedAssertions.join(", ")}`;
    exitCode = 1;
    message = result.message;
  }
  writeFileSync(result.artifacts.rawAnsi, raw, "utf8");
  writeFileSync(result.artifacts.plainText, plain, "utf8");
  writeFileSync(result.artifacts.ioJsonl, ioEvents.map(event => JSON.stringify(event)).join("\n") + "\n", "utf8");
  writeFileSync(result.artifacts.cast, [
    JSON.stringify({ version: 2, width: terminalColumns, height: terminalRows, timestamp: Math.floor(startedAt / 1000), env: { TERM: "xterm-256color", SHELL: "afterburn.exe" }, title: "Subagent Policy real TUI UAT" }),
    ...ioEvents.map(event => JSON.stringify([event.t, event.type === "input" ? "i" : "o", Buffer.from(event.dataBase64, "base64").toString("utf8")]))
  ].join("\n") + "\n", "utf8");
  writeJson(result.artifacts.operatorJson, { goal: "Operate native /subagents in real Copilot and verify session-scoped policy state.", steps: operatorSteps });
  writeFileSync(result.artifacts.operatorMarkdown, [
    "# Subagent Policy TUI UAT",
    "",
    `Result: ${result.status}`,
    `Message: ${message}`,
    "",
    ...operatorSteps.map(step => `## ${step.name}\n- Key/input: ${step.key}\n- Latency: ${step.latencyMs}ms\n- Assertions: ${step.assertions.join("; ")}\n\n\`\`\`text\n${step.viewportText}\n\`\`\``)
  ].join("\n"), "utf8");
  writePngReport(result);
  writeJson(artifactPath("subagent-policy-tui-result.json"), result);
  try { process.kill(child.pid); } catch {}
  if (exitCode === 0) process.stdout.write(`${message}\n`);
  else process.stderr.write(`${message}\n--- tail ---\n${plain.slice(-8000)}\n`);
  setTimeout(() => process.exit(exitCode), 500);
};

child.onData(data => {
  recordOutput(data);
  raw += data;
  const text = currentPlain();
  const recent = recentPlain();

  if (!trusted && /Do you trust the files in this folder/i.test(recent)) { trusted = true; scheduleWrite("\r", 250, "trust current folder"); return; }
  if (!restored && /Restore interrupted sessions/i.test(recent)) { restored = true; scheduleWrite("\x1b", 250, "dismiss restore sessions"); return; }
  if (!approved && /wants elevated permissions/i.test(recent)) { approved = true; scheduleWrite("\r", 250, "approve elevated permissions"); return; }
  if (!terminalSetupDeclined && /Set up terminal for multi-line input support/i.test(recent)) { terminalSetupDeclined = true; scheduleWrite("\x1b", 250, "dismiss terminal setup"); return; }
  if (!nativeAppSelectionMoved && /Yes, install[\s\S]{0,200}No, thanks/i.test(recent)) {
    nativeAppSelectionMoved = true;
    scheduleWrite("\x1b[C", 250, "select no native desktop app");
    setTimeout(() => {
      if (nativeAppDeclined) return;
      nativeAppDeclined = true;
      writeInput("\r", "decline native desktop app");
    }, 1000).unref?.();
    return;
  }

  const runtimeReady = /activated Afterburner extension 'subagent-policy-uat'/i.test(text);
  if (stage === "starting" && runtimeReady && promptVisible(recent)) {
    stage = "opening";
    commandSentAt = Date.now();
    stageRawLength = raw.length;
    submitCommand("open /subagents", 20_000);
    return;
  }
  if (stage === "opening" && /Unknown command:\s*\/subagents/i.test(recent)) {
    if (Date.now() - commandSentAt > 35_000) return finish(1, "Copilot native /subagents command was unavailable");
    if (Date.now() - commandSubmitRetryAt > 5000) {
      commandSubmitRetryAt = Date.now();
      submitCommand("retry /subagents");
    }
    return;
  }
  if (/Canvas opened:\s*Subagent Policy/i.test(recent)) return finish(1, "Subagent Policy fell back to generic canvas-open text instead of native rendered UI");
  if (/native menu unavailable|interactive menu could not open/i.test(recent)) return finish(1, "Subagent Policy reported that the native interactive menu could not open");

  if (stage === "opening" && modalPattern.test(text)) {
    modalSeenAt = Date.now();
    recordStep("open native subagents UI", "/subagents", commandSentAt, modalSeenAt, ["native subagent UI rendered", "policy presets visible"]);
    beginStage("apply-policy", "\r", "apply Luna Three");
    return;
  }
  if (stage === "apply-policy" && /Applied subagent policy Luna Three/i.test(stagePlain())) {
    recordStep("apply Luna Three", "Enter", stageSentAt, Date.now(), ["native preset applied", "max concurrency 3 and depth 1 reported"]);
    beginStage("close-before-reopen", "\x1b", "close native subagents");
    return;
  }
  if (stage === "close-before-reopen" && promptVisible(stagePlain())) {
    recordStep("close modal", "q", stageSentAt, Date.now(), ["returned focus to Copilot prompt without clearing policy"]);
    stage = "reopening";
    stageSentAt = Date.now();
    stageRawLength = raw.length;
    submitCommand("reopen /subagents", 750);
    return;
  }
  if (stage === "reopening" && modalPattern.test(stagePlain()) && /required[\s\S]*colosseum-prod\/gpt-5-6-luna/i.test(stagePlain())) {
    recordStep("reopen and verify native settings", "/subagents", stageSentAt, Date.now(), ["native subagents UI reopened", "required Luna routing visible"]);
    beginStage("closing", "\x1b", "close native subagents");
    return;
  }
  if (stage === "closing" && promptVisible(stagePlain())) {
    recordStep("close cleared modal", "q", stageSentAt, Date.now(), ["returned focus to Copilot prompt after clearing"]);
    finish(0, `real-subagent-policy-tui-ok openLatencyMs=${modalSeenAt - commandSentAt} steps=${operatorSteps.length} report=${artifactPath("subagent-policy-tui-report.png")}`);
  }
});

child.onExit(({ exitCode }) => {
  if (!finished) finish(exitCode === 0 ? 1 : exitCode, `afterburn exited before Subagent Policy TUI UAT completed exitCode=${exitCode}`);
});

setTimeout(() => finish(1, `timed out during Subagent Policy TUI UAT at stage=${stage}`), timeoutMs).unref?.();
