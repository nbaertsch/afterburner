import { execFileSync, spawnSync } from "node:child_process";
import { cpSync, existsSync, mkdirSync, readFileSync, readdirSync, statSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, delimiter, join, resolve } from "node:path";
import process from "node:process";
import pty from "node-pty";

const afterburn = resolve(process.argv[2] ?? process.env.AFTERBURNER_EXE ?? ".native-build/afterburn.exe");
const captureDirectory = resolve(process.argv[3] ?? join(process.cwd(), "artifacts", "openai-server-menu-tui"));
const timeoutMs = Number(process.argv[4] ?? process.env.AFTERBURNER_REAL_TUI_TIMEOUT_MS ?? 90_000);
const repoRoot = process.cwd();
const root = join(tmpdir(), `afterburn-openai-menu-${process.pid}-${Date.now()}`);
const afterburnerHome = join(root, "afterburner");
const normalCopilotHome = join(root, "normal-copilot");
const isolatedUserHome = join(root, "user");
const workspace = join(root, "workspace");
const startedAt = Date.now();
const terminalColumns = 140;
const terminalRows = 40;

mkdirSync(captureDirectory, { recursive: true });
mkdirSync(join(afterburnerHome, "config"), { recursive: true });
mkdirSync(join(afterburnerHome, "copilot-home"), { recursive: true });
mkdirSync(normalCopilotHome, { recursive: true });
mkdirSync(isolatedUserHome, { recursive: true });
mkdirSync(workspace, { recursive: true });

const stripAnsi = value => value
  .replace(/\x1b\][^\x07]*(?:\x07|\x1b\\)/g, "")
  .replace(/\x1b\[[0-?]*[ -/]*[@-~]/g, "")
  .replace(/\x1b[()][A-Za-z0-9]/g, "");
const encodeChunk = data => Buffer.from(data, "utf8").toString("base64");
const printableInput = data => data.replace(/\x1b/g, "<Esc>").replace(/\r/g, "<Enter>").replace(/\x15/g, "<Ctrl+U>");
const elapsedSeconds = () => Number(((Date.now() - startedAt) / 1000).toFixed(6));
const artifactPath = name => join(captureDirectory, name);
const writeJson = (path, value) => writeFileSync(path, `${JSON.stringify(value, null, 2)}\n`, "utf8");
const existingDirectory = path => { try { return statSync(path).isDirectory(); } catch { return false; } };

const platformPackageRoot = () => join("pkg", "win32-x64");
const discoverPackageRoots = () => {
  const roots = new Set();
  for (const raw of (process.env.AFTERBURNER_COPILOT_PACKAGE_ROOTS ?? "").split(delimiter)) {
    if (raw.trim()) roots.add(resolve(raw.trim()));
  }
  for (const candidate of [
    process.env.USERPROFILE && join(process.env.USERPROFILE, ".copilot", platformPackageRoot()),
    process.env.LOCALAPPDATA && join(process.env.LOCALAPPDATA, "copilot", platformPackageRoot())
  ].filter(Boolean)) {
    if (existingDirectory(candidate)) roots.add(resolve(candidate));
  }
  return [...roots];
};
const findCopilotSdk = () => {
  for (const packageRoot of discoverPackageRoots()) {
    let versions;
    try { versions = [...new Set(readdirSync(packageRoot))]; } catch { continue; }
    for (const version of versions.sort().reverse()) {
      const sdkPath = join(packageRoot, version, "copilot-sdk");
      if (existingDirectory(sdkPath)) return sdkPath;
    }
  }
  return undefined;
};
const installCopilotSdkShim = activePath => {
  const sdkSource = findCopilotSdk();
  if (!sdkSource) return false;
  const sdkTarget = join(activePath, "node_modules", "@github", "copilot-sdk");
  mkdirSync(sdkTarget, { recursive: true });
  cpSync(sdkSource, sdkTarget, { recursive: true });
  writeJson(join(sdkTarget, "package.json"), {
    name: "@github/copilot-sdk",
    type: "module",
    exports: { ".": "./index.js", "./extension": "./extension.js" }
  });
  return true;
};

const isolatedEnvironment = (extra = {}) => {
  const environment = { ...process.env };
  for (const key of Object.keys(environment)) {
    if (/^(COPILOT_HOME|COPILOT_AGENT_SESSION_ID|COPILOT_LOADER_PID|COPILOT_SUPERVISED|AFTERBURNER_HOME|AFTERBURNER_NORMAL_COPILOT_HOME|AFTERBURNER_OPENAI_SERVER_CONFIG|AFTERBURNER_COPILOT_OPENAI_CONFIG|AFTERBURNER_DISABLED_EXTENSIONS|AFTERBURNER_COPILOT_EXECUTABLE)$/i.test(key)) delete environment[key];
  }
  environment.USERPROFILE = isolatedUserHome;
  environment.HOME = isolatedUserHome;
  environment.LOCALAPPDATA = join(root, "localappdata");
  environment.APPDATA = join(root, "appdata");
  environment.AFTERBURNER_HOME = afterburnerHome;
  environment.AFTERBURNER_NORMAL_COPILOT_HOME = normalCopilotHome;
  const packageRoots = discoverPackageRoots();
  if (packageRoots.length > 0) environment.AFTERBURNER_COPILOT_PACKAGE_ROOTS = packageRoots.join(delimiter);
  return { ...environment, ...extra };
};
mkdirSync(join(root, "localappdata"), { recursive: true });
mkdirSync(join(root, "appdata"), { recursive: true });

const installEnv = isolatedEnvironment({ AFTERBURNER_DISABLE_BUILTIN_RELEASE_FETCH: "1" });
const installResult = spawnSync(afterburn, ["install", "openai-server"], {
  cwd: repoRoot,
  encoding: "utf8",
  env: installEnv
});
if (installResult.status !== 0) {
  throw new Error(`failed to install OpenAI Server built-in extension: status=${installResult.status} stdout=${installResult.stdout} stderr=${installResult.stderr}`);
}
const enableResult = spawnSync(afterburn, ["enable", "openai-server"], {
  cwd: repoRoot,
  encoding: "utf8",
  env: installEnv
});
if (enableResult.status !== 0) {
  throw new Error(`failed to enable OpenAIServer local extension: status=${enableResult.status} stdout=${enableResult.stdout} stderr=${enableResult.stderr}`);
}
const registry = JSON.parse(readFileSync(join(afterburnerHome, "registry.json"), "utf8"));
const entry = registry.extensions?.["openai-server"];
const activePath = entry?.activePath;
if (!activePath) throw new Error("OpenAIServer extension did not install into registry");
const sdkShimInstalled = installCopilotSdkShim(activePath);
writeJson(join(afterburnerHome, "copilot-home", "settings.json"), {
  experimental: true,
  enabledPlugins: { "afterburner-openai-server": true },
  extensions: { disabledExtensions: [] }
});
writeJson(join(afterburnerHome, "copilot-home", "config.json"), {
  appTipShown: true,
  askedSetupTerminals: ["windows-terminal"]
});
const bridgeConfigPath = join(afterburnerHome, "config", "openai-server.json");
writeJson(bridgeConfigPath, { enabled: false, port: 0, requireApiKey: false });

const env = isolatedEnvironment({
  COPILOT_RUNTIME_EXTENSION_DEBUG: "1",
  AFTERBURNER_OPENAI_SERVER_CONFIG: bridgeConfigPath,
  AFTERBURNER_SKIP_PREFLIGHT: "1"
});
const child = pty.spawn(afterburn, ["--name", `afterburn-openai-menu-uat-${process.pid}-${Date.now()}`, "--no-remote"], {
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
let commandInputStartedAt = 0;
let commandSentAt = 0;
let commandSubmitRetryAt = 0;
let menuSeenAt = 0;
const actionSteps = [
  { name: "status action", key: "r", want: /status refreshed/i, assertions: ["Status shortcut refreshed visible state"] },
  { name: "doctor action", key: "d", want: /Sanitized diagnostics|diagnostics refreshed/i, assertions: ["Doctor shortcut displayed sanitized diagnostics"] },
  { name: "stop action", key: "x", want: /bridge stopped|State:\s*stopped/i, assertions: ["Stop shortcut stopped the bridge"] },
  { name: "start action", key: "s", want: /bridge ready|State:\s*running|Endpoint:\s*127\.0\.0\.1:\d+/i, assertions: ["Start shortcut started or reused the bridge"] }
];
let actionIndex = 0;
let activeActionRawLength = 0;
const actionSentAt = {};
const actionSeenAt = {};
let closeSentAt = 0;
let closeSeenAt = 0;
let finished = false;
let pendingCommand = null;
let nextCommandAllowedAt = 0;

const recordOutput = data => ioEvents.push({ t: elapsedSeconds(), type: "output", bytes: Buffer.byteLength(data), dataBase64: encodeChunk(data) });
const writeInput = (data, label) => {
  ioEvents.push({ t: elapsedSeconds(), type: "input", label, display: printableInput(data), bytes: Buffer.byteLength(data), dataBase64: encodeChunk(data) });
  child.write(data);
};
const scheduleWrite = (data, delayMs = 150, label = "input") => setTimeout(() => writeInput(data, label), delayMs).unref?.();
const scheduleCommand = (command, delayMs, onSubmit, label) => {
  pendingCommand = { command, label };
  setTimeout(() => {
    if (finished || pendingCommand?.command !== command) return;
    pendingCommand = null;
    onSubmit?.();
    writeInput("\x1b", `${label}: ensure prompt focus`);
    writeInput("\x15", `${label}: clear prompt`);
    writeInput(`${command}\r`, `${label}: type and submit command`);
  }, delayMs).unref?.();
};
const flushPendingCommandIfReady = () => false;
const recordStep = (name, key, started, completed, assertions) => {
  const text = stripAnsi(raw).slice(-8000);
  operatorSteps.push({
    name,
    key,
    startedAt: new Date(started).toISOString(),
    completedAt: new Date(completed).toISOString(),
    latencyMs: completed - started,
    viewportText: text,
    assertions
  });
};
const currentPlain = () => stripAnsi(raw);
const recentPlain = () => currentPlain().slice(-6000);

const writePngReport = capture => {
  if (process.platform !== "win32") return;
  const payloadPath = artifactPath("openai-server-menu-visual.json");
  const scriptPath = artifactPath("render-openai-server-menu-png.ps1");
  const outputPath = artifactPath("openai-server-menu-tui-report.png");
  const payload = { capture, screens: operatorSteps.map(step => ({ title: step.name, content: step.viewportText })) };
  writeJson(payloadPath, payload);
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
  if (renderer.status !== 0) writeFileSync(artifactPath("openai-server-menu-png-render.log"), `${renderer.stdout ?? ""}\n${renderer.stderr ?? ""}`, "utf8");
};

const finish = (exitCode, message) => {
  if (finished) return;
  finished = true;
  const plain = currentPlain();
  const result = {
    schemaVersion: 1,
    status: exitCode === 0 ? "passed" : "failed",
    message,
    afterburn,
    captureDirectory,
    startedAt: new Date(startedAt).toISOString(),
    completedAt: new Date().toISOString(),
    latencyMs: {
      menuOpen: commandSentAt && menuSeenAt ? menuSeenAt - commandSentAt : null,
      ready: commandSentAt && menuSeenAt ? menuSeenAt - commandSentAt : null
    },
    packagePreflight: {
      activePath,
      sdkShimInstalled,
      manifestCapabilities: entry?.manifest?.capabilities ?? [],
      sessionExtensionEntrypoint: entry?.manifest?.sessionExtension?.entrypoint,
      wrapperFiles: existsSync(join(activePath, "com.github.copilot", "extensions")) ? readdirSync(join(activePath, "com.github.copilot", "extensions")) : []
    },
    assertions: {
      oneSlashCommandMenuVisible: ioEvents.some(event => event.type === "input" && event.display.includes("/openai-server")),
      nativeModalRendered: /OpenAI Server[\s\S]*Endpoint:\s*(?:127\.0\.0\.1:\d+|not allocated)/i.test(plain),
      noCanvasOnlyFallback: !/Canvas opened:\s*OpenAI Server/i.test(plain),
      noTextFallback: !/interactive menu could not open|native menu unavailable/i.test(plain),
      everyAdvertisedActionExercised: actionSteps.every(step => Boolean(actionSeenAt[step.name])) && Boolean(closeSeenAt),
      packageHasModalCapability: (entry?.manifest?.capabilities ?? []).includes("modal-canvas"),
      packageHasCompatibilityWrappers: ["CopilotOpenAI", "OpenAIServer"].every(name => (existsSync(join(activePath, "com.github.copilot", "extensions")) ? readdirSync(join(activePath, "com.github.copilot", "extensions")) : []).includes(name))
    },
    operatorSteps,
    artifacts: {
      rawAnsi: artifactPath("openai-server-menu-tui.raw.ansi"),
      plainText: artifactPath("openai-server-menu-tui.txt"),
      ioJsonl: artifactPath("openai-server-menu-tui-io.jsonl"),
      cast: artifactPath("openai-server-menu-tui.cast"),
      operatorJson: artifactPath("openai-server-menu-operator.json"),
      operatorMarkdown: artifactPath("openai-server-menu-operator.md"),
      pngReport: artifactPath("openai-server-menu-tui-report.png")
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
  const cast = [
    JSON.stringify({ version: 2, width: terminalColumns, height: terminalRows, timestamp: Math.floor(startedAt / 1000), env: { TERM: "xterm-256color", SHELL: "afterburn.exe" }, title: "OpenAI Server menu real TUI UAT" }),
    ...ioEvents.map(event => JSON.stringify([event.t, event.type === "input" ? "i" : "o", Buffer.from(event.dataBase64, "base64").toString("utf8")]))
  ].join("\n") + "\n";
  writeFileSync(result.artifacts.cast, cast, "utf8");
  writeJson(result.artifacts.operatorJson, { goal: "Operate /openai-server like a real user and verify every advertised menu action.", steps: operatorSteps });
  writeFileSync(result.artifacts.operatorMarkdown, [`# OpenAI Server menu UAT`, "", `Result: ${result.status}`, `Message: ${message}`, "", ...operatorSteps.map(step => `## ${step.name}\n- Key/input: ${step.key}\n- Latency: ${step.latencyMs}ms\n- Assertions: ${step.assertions.join("; ")}\n\n\`\`\`text\n${step.viewportText}\n\`\`\``)].join("\n"), "utf8");
  writePngReport(result);
  writeJson(artifactPath("openai-server-menu-result.json"), result);
  try { child.kill(); } catch {}
  if (exitCode === 0) process.stdout.write(`${message}\n`);
  else process.stderr.write(`${message}\n--- tail ---\n${plain.slice(-6000)}\n`);
  process.exit(exitCode);
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

  const runtimeReady = /activated Afterburner extension 'openai-server'/i.test(text) && /runtime-extension-host/i.test(text);
  const promptReady = /\/ commands|tab next tab|\? help|@ files · # issues/i.test(recent);
  if (flushPendingCommandIfReady(promptReady)) return;
  if (!commandInputStartedAt && runtimeReady && promptReady) {
    commandInputStartedAt = Date.now();
    scheduleCommand("/openai-server", 1000, () => { commandSentAt = Date.now(); }, "submit /openai-server");
    return;
  }
  if (commandSentAt && !menuSeenAt && /\/openai-server/i.test(recent) && Date.now() - commandSubmitRetryAt > 4000) {
    commandSubmitRetryAt = Date.now();
    writeInput("\r\n", "retry /openai-server submit");
  }
  if (commandSentAt && !menuSeenAt && /Unknown command:\s*\/openai-server/i.test(recent)) return finish(1, "Copilot rejected /openai-server as an unknown command");
  if (commandSentAt && !menuSeenAt && /native menu unavailable|interactive menu could not open/i.test(recent)) return finish(1, "OpenAIServer reported that the native interactive menu could not open");
  if (/Canvas opened:\s*OpenAI Server/i.test(recent)) return finish(1, "OpenAIServer fell back to generic canvas-open text instead of native rendered UI");

  const menuReady = /OpenAI Server[\s\S]*Endpoint:\s*(?:127\.0\.0\.1:\d+|not allocated)/i.test(text);
  if (commandSentAt && !menuSeenAt && menuReady) {
    menuSeenAt = Date.now();
    recordStep("open native modal menu", "/openai-server", commandSentAt, menuSeenAt, ["single slash command accepted", "native modal title rendered", "endpoint status visible"]);
    const step = actionSteps[actionIndex];
    actionSentAt[step.name] = Date.now();
    activeActionRawLength = raw.length;
    setTimeout(() => writeInput(step.key, step.name), 300).unref?.();
    return;
  }

  const activeStep = actionSteps[actionIndex];
  if (menuSeenAt && activeStep && actionSentAt[activeStep.name] && !actionSeenAt[activeStep.name] && activeStep.want.test(stripAnsi(raw.slice(activeActionRawLength)))) {
    actionSeenAt[activeStep.name] = Date.now();
    recordStep(activeStep.name, activeStep.key, actionSentAt[activeStep.name], actionSeenAt[activeStep.name], activeStep.assertions);
    actionIndex++;
    const nextStep = actionSteps[actionIndex];
    if (nextStep) {
      setTimeout(() => {
        actionSentAt[nextStep.name] = Date.now();
        activeActionRawLength = raw.length;
        writeInput(nextStep.key, nextStep.name);
      }, 300).unref?.();
      return;
    }
    setTimeout(() => {
      closeSentAt = Date.now();
      activeActionRawLength = raw.length;
      writeInput("q", "close action");
    }, 300).unref?.();
    return;
  }

  if (closeSentAt && !closeSeenAt && /\/ commands|tab next tab|\? help|@ files · # issues/i.test(stripAnsi(raw.slice(activeActionRawLength)))) {
    closeSeenAt = Date.now();
    recordStep("close action", "q", closeSentAt, closeSeenAt, ["Close shortcut returned focus to Copilot prompt"]);
    const actionSummary = actionSteps.map(step => `${step.name}:${actionSeenAt[step.name] - actionSentAt[step.name]}ms`).join(",");
    finish(0, `real-openai-server-menu-tui-ok openLatencyMs=${menuSeenAt - commandSentAt} actions=${actionSummary} closeLatencyMs=${closeSeenAt - closeSentAt} report=${artifactPath("openai-server-menu-tui-report.png")}`);
  }
});

child.onExit(({ exitCode }) => {
  if (!finished) finish(exitCode === 0 ? 1 : exitCode, `afterburn exited before OpenAIServer menu UAT completed exitCode=${exitCode}`);
});

setTimeout(() => {
  finish(1, "timed out before completing OpenAI Server one-command menu UAT");
}, timeoutMs).unref?.();
