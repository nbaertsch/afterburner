import { spawnSync } from "node:child_process";
import { cpSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, statSync, writeFileSync } from "node:fs";
import { homedir, tmpdir } from "node:os";
import { inflateSync } from "node:zlib";
import { join, resolve } from "node:path";
import process from "node:process";
import pty from "node-pty";
import { bootstrapExperimentalCopilotProfile } from "./copilot-experimental-profile.mjs";

const stripAnsi = value => value
  .replace(/\x1b\][^\x07]*(?:\x07|\x1b\\)/g, "")
  .replace(/\x1b\[[0-?]*[ -/]*[@-~]/g, "")
  .replace(/\x1b[()][A-Za-z0-9]/g, "");

const option = name => {
  const index = process.argv.indexOf(name);
  return index >= 0 ? process.argv[index + 1] : undefined;
};
const hasFlag = name => process.argv.includes(name);

if (hasFlag("--help") || hasFlag("-h")) {
  process.stdout.write(`Usage: node tests\\real-blackbox-modal-tui.mjs [afterburn.exe] [capture-dir] [timeout-ms] [options]\n\nOptions:\n  --afterburn <path>          Afterburner executable to launch.\n  --capture-dir <path>       Directory for raw/text/json/png visual artifacts.\n  --timeout-ms <ms>          End-to-end UAT timeout.\n  --max-open-ms <ms>         Native modal first-open latency budget.\n  --max-reopen-ms <ms>       Native modal reopen latency budget.\n  --max-scroll-ms <ms>       Per-key scroll response budget.\n  --max-refresh-ms <ms>      Refresh action response budget.\n  --max-export-ms <ms>       Export action response budget.\n  --max-close-ms <ms>        q close response budget.\n  --max-escape-close-ms <ms> Escape close response budget.\n\nArtifacts include raw ANSI, reconstructed text, an asciinema-compatible .cast,\ninput/output JSONL, operator JSON/Markdown transcripts, result/evidence JSON,\nand secondary PNG raster captures.\nEnvironment overrides use AFTERBURNER_REAL_TUI_* names matching each option.\n`);
  process.exit(0);
}

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
const latencyBudgets = {
  openMs: Number(option("--max-open-ms") ?? process.env.AFTERBURNER_REAL_TUI_MAX_OPEN_MS ?? 5_000),
  reopenMs: Number(option("--max-reopen-ms") ?? process.env.AFTERBURNER_REAL_TUI_MAX_REOPEN_MS ?? 5_000),
  scrollMs: Number(option("--max-scroll-ms") ?? process.env.AFTERBURNER_REAL_TUI_MAX_SCROLL_MS ?? 250),
  refreshMs: Number(option("--max-refresh-ms") ?? process.env.AFTERBURNER_REAL_TUI_MAX_REFRESH_MS ?? 5_000),
  exportMs: Number(option("--max-export-ms") ?? process.env.AFTERBURNER_REAL_TUI_MAX_EXPORT_MS ?? 8_000),
  closeMs: Number(option("--max-close-ms") ?? process.env.AFTERBURNER_REAL_TUI_MAX_CLOSE_MS ?? 500),
  escapeCloseMs: Number(option("--max-escape-close-ms") ?? process.env.AFTERBURNER_REAL_TUI_MAX_ESCAPE_CLOSE_MS ?? 500)
};
const scriptStartedAt = Date.now();
mkdirSync(captureDirectory, { recursive: true });
const isolatedAfterburnerHome = mkdtempSync(join(tmpdir(), "afterburner-blackbox-"));
const normalCopilotHome = join(isolatedAfterburnerHome, "normal-copilot");
mkdirSync(normalCopilotHome, { recursive: true });
writeFileSync(join(normalCopilotHome, "settings.json"), `${JSON.stringify({ experimental: true }, null, 2)}\n`, "utf8");
process.once("exit", () => {
  try { rmSync(isolatedAfterburnerHome, { recursive: true, force: true }); } catch {}
});
const packageSource = join(isolatedAfterburnerHome, "package-source");
const packageArchive = join(isolatedAfterburnerHome, "black-box-uat.zip");
cpSync(join(process.cwd(), "extensions", "BlackBox"), packageSource, {
  recursive: true,
  filter: path => ![".test-work", "node_modules"].includes(path.split(/[\\/]/).at(-1))
});
const packageManifestPath = join(packageSource, "afterburner.json");
const packageManifest = JSON.parse(readFileSync(packageManifestPath, "utf8"));
packageManifest.id = "black-box-uat";
packageManifest.visibility = "private";
writeFileSync(packageManifestPath, `${JSON.stringify(packageManifest, null, 2)}\n`, "utf8");
const testEnvironment = {
  ...process.env,
  AFTERBURNER_HOME: isolatedAfterburnerHome,
  AFTERBURNER_NORMAL_COPILOT_HOME: normalCopilotHome,
  AFTERBURNER_ISOLATE_SESSION_STATE: "1"
};
const packResult = spawnSync(afterburn, ["extension", "pack", packageSource, packageArchive], {
  cwd: process.cwd(),
  encoding: "utf8",
  env: testEnvironment
});
if (packResult.status !== 0) {
  throw new Error(`failed to pack Black Box visual UAT package: status=${packResult.status} stdout=${packResult.stdout} stderr=${packResult.stderr}`);
}
const installResult = spawnSync(afterburn, ["extension", "install", packageArchive], {
  cwd: process.cwd(),
  encoding: "utf8",
  env: testEnvironment
});
if (installResult.status !== 0) {
  throw new Error(`failed to install local built-ins for visual UAT: status=${installResult.status} stdout=${installResult.stdout} stderr=${installResult.stderr}`);
}
const enableResult = spawnSync(afterburn, ["extension", "enable", "black-box-uat"], {
  cwd: process.cwd(),
  encoding: "utf8",
  env: testEnvironment
});
if (enableResult.status !== 0) {
  throw new Error(`failed to enable Black Box visual UAT package: status=${enableResult.status} stdout=${enableResult.stdout} stderr=${enableResult.stderr}`);
}
const registry = JSON.parse(readFileSync(join(isolatedAfterburnerHome, "registry.json"), "utf8"));
const blackBoxEntry = registry.extensions?.["black-box-uat"];
const blackBoxActivePath = blackBoxEntry?.activePath;
if (!blackBoxActivePath) throw new Error("visual UAT did not install an active Black Box package");
const forbiddenCanvasFiles = [
  join(blackBoxActivePath, "lib", "session-extension.mjs"),
  join(blackBoxActivePath, "extensions", "BlackBox", "extension.mjs"),
  join(blackBoxActivePath, "afterburner.json")
];
const forbiddenCanvasMatches = forbiddenCanvasFiles.flatMap(file => {
  if (!existsSync(file)) return [];
  const source = readFileSync(file, "utf8");
  const matches = [...source.matchAll(/createCanvas|openModalCanvas|canvasRpc\.open|canvases:\s*canvas|"canvas"/g)].map(match => match[0]);
  return matches.map(match => ({ file, match }));
});
const packagePreflight = {
  afterburnerHome: isolatedAfterburnerHome,
  blackBoxActivePath,
  source: blackBoxEntry?.source ?? null,
  identity: blackBoxEntry?.identity ? {
    extensionId: blackBoxEntry.identity.extensionId,
    sourceType: blackBoxEntry.identity.sourceType,
    sourceVersion: blackBoxEntry.identity.sourceVersion,
    builtinSigned: blackBoxEntry.identity.builtinSigned === true,
    treeHash: blackBoxEntry.identity.treeHash,
    manifestHash: blackBoxEntry.identity.manifestHash
  } : null,
  manifestCapabilities: blackBoxEntry?.manifest?.capabilities ?? [],
  checkedFiles: forbiddenCanvasFiles,
  forbiddenPatterns: ["createCanvas", "openModalCanvas", "canvasRpc.open", "canvases: canvas", "\\\"canvas\\\""],
  noGenericCanvasFallback: forbiddenCanvasMatches.length === 0,
  forbiddenCanvasMatches
};
if (forbiddenCanvasMatches.length > 0) {
  throw new Error(`installed Black Box still exposes generic canvas fallback: ${JSON.stringify(forbiddenCanvasMatches)}`);
}
if (packagePreflight.manifestCapabilities.includes("canvas")) {
  throw new Error("installed Black Box manifest still advertises generic canvas capability");
}
for (const capability of ["modal-canvas"]) {
  if (!packagePreflight.manifestCapabilities.includes(capability)) {
    throw new Error(`installed Black Box manifest is missing ${capability}`);
  }
}

const env = {
  ...process.env,
  COPILOT_RUNTIME_EXTENSION_DEBUG: "1",
  AFTERBURNER_HOME: isolatedAfterburnerHome,
  AFTERBURNER_NORMAL_COPILOT_HOME: normalCopilotHome,
  AFTERBURNER_ISOLATE_SESSION_STATE: "1",
  AFTERBURNER_SKIP_PREFLIGHT: "1"
};
for (const key of [
  "COPILOT_AGENT_SESSION_ID",
  "COPILOT_CLI",
  "COPILOT_CLI_BINARY_VERSION",
  "COPILOT_CLI_RESOLVED_DIST_DIR",
  "COPILOT_HOME",
  "COPILOT_LOADER_PID",
  "COPILOT_SUPERVISED"
]) delete env[key];

const terminalColumns = 140;
const terminalRows = 40;

await bootstrapExperimentalCopilotProfile({
  afterburn,
  cwd: process.cwd(),
  env,
  managedCopilotHome: join(isolatedAfterburnerHome, "copilot-home"),
  label: "afterburn-blackbox-bootstrap"
});

const child = pty.spawn(afterburn, ["--name", `afterburn-blackbox-uat-${process.pid}-${Date.now()}`, "--no-remote"], {
  name: "xterm-256color",
  cols: terminalColumns,
  rows: terminalRows,
  cwd: process.cwd(),
  env
});

let raw = "";
const ioEvents = [];
const operatorSteps = [];
const elapsedSeconds = () => Number(((Date.now() - scriptStartedAt) / 1000).toFixed(6));
const encodeChunk = data => Buffer.from(data, "utf8").toString("base64");
const printableInput = data => data
  .replace(/\x1b/g, "<Esc>")
  .replace(/\r/g, "<Enter>")
  .replace(/\x15/g, "<Ctrl+U>")
  .replace(/\t/g, "<Tab>");
const recordOutput = data => ioEvents.push({ t: elapsedSeconds(), type: "output", bytes: Buffer.byteLength(data), dataBase64: encodeChunk(data) });
const writeInput = (data, label) => {
  ioEvents.push({ t: elapsedSeconds(), type: "input", label, display: printableInput(data), bytes: Buffer.byteLength(data), dataBase64: encodeChunk(data) });
  child.write(data);
};
const recordOperatorStep = ({ name, key, startedAt, completedAt, screenTitle, screenRaw, assertions = [] }) => {
  const screen = screenRaw ? visualScreen(screenRaw, /Afterburner Black Box (?:Live|Doctor|Export)/gi) : "";
  operatorSteps.push({
    name,
    key,
    startedAt: startedAt ? new Date(startedAt).toISOString() : null,
    completedAt: completedAt ? new Date(completedAt).toISOString() : null,
    latencyMs: startedAt && completedAt ? completedAt - startedAt : null,
    screenTitle,
    viewportText: screen,
    assertions
  });
};
let trusted = false;
let restored = false;
let restoreDismissCount = 0;
let lastRestoreDismissAt = 0;
let approved = false;
let terminalSetupDeclined = false;
let nativeAppSelectionMoved = false;
let nativeAppDeclined = false;
let commandInputStartedAt = 0;
let commandSentAt = 0;
let commandSubmitRetryAt = 0;
let modalSeenAt = 0;
let modalCaptureScheduled = false;
const focusSteps = [
  { name: "focusRefresh", title: "Focus Refresh action screen", key: "\t", want: /▶\s*\[r\] Refresh\s*◀/i, assertions: ["Tab focused the first modal action"] },
  { name: "spaceRefresh", title: "Space activates focused Refresh screen", key: "\x1b[32;57;32;1;0;1_", want: /Afterburner Black Box Live[\s\S]*Storage usage/i, assertions: ["Space activated the focused Refresh action"] },
];
const scrollSteps = [
  { name: "arrowDown", title: "Arrow down scroll screen", key: "\x1b[40;0;0;1;0;1_", want: /lines 2-\d+ of/i },
  { name: "arrowUp", title: "Arrow up scroll screen", key: "\x1b[38;0;0;1;0;1_", want: /lines 1-\d+ of/i },
  { name: "pageDown", title: "Page down scroll screen", key: "\x1b[34;0;0;1;0;1_", want: /lines (?:[2-9]|[1-9]\d+)-\d+ of/i },
  { name: "pageUp", title: "Page up scroll screen", key: "\x1b[33;0;0;1;0;1_", want: /lines 1-\d+ of/i },
  { name: "end", title: "End scroll screen", key: "\x1b[35;0;0;1;0;1_", want: /lines (?:[2-9]|[1-9]\d+)-\d+ of/i },
  { name: "home", title: "Home scroll screen", key: "\x1b[36;0;0;1;0;1_", want: /lines 1-\d+ of/i }
];
let focusIndex = 0;
let activeFocusRawLength = 0;
const focusSentAt = {};
const focusSeenAt = {};
const focusRaw = {};
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
let exportSentAt = 0;
let exportSeenAt = 0;
let exportRawLength = 0;
let exportRaw = "";
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

const result = (status, extra = {}) => {
  const screenPngs = capturedScreens().map(([title]) => artifactPath(`${slugTitle(title)}.png`));
  return {
    schemaVersion: 1,
    status,
    afterburn,
    startedAt: new Date(scriptStartedAt).toISOString(),
    completedAt: new Date().toISOString(),
    commandInputStarted: Boolean(commandInputStartedAt),
    commandSubmitted: Boolean(commandSentAt),
    modalSeen: Boolean(modalSeenAt),
    focusControls: Object.fromEntries(focusSteps.map(step => [step.name, {
      screen: `${slugTitle(step.title)}.png`,
      key: step.key,
      proves: step.assertions,
      latencyMs: focusSentAt[step.name] && focusSeenAt[step.name] ? focusSeenAt[step.name] - focusSentAt[step.name] : null
    }])),
    scroll: Object.fromEntries(scrollSteps.map(step => [step.name, {
      passed: Boolean(scrollSeenAt[step.name]),
      latencyMs: scrollSentAt[step.name] && scrollSeenAt[step.name] ? scrollSeenAt[step.name] - scrollSentAt[step.name] : null
    }])),
    refreshAction: Boolean(refreshSeenAt),
    refreshLatencyMs: refreshSentAt && refreshSeenAt ? refreshSeenAt - refreshSentAt : null,
    doctorAction: doctorSeen,
    exportAction: Boolean(exportSeenAt),
    closeRestored: Boolean(closeRestoredAt),
    escapeClose: Boolean(escapeRestoredAt),
    openLatencyMs: commandSentAt && modalSeenAt ? modalSeenAt - commandSentAt : null,
    reopenLatencyMs: escapeCommandSentAt && escapeModalSeenAt ? escapeModalSeenAt - escapeCommandSentAt : null,
    closeLatencyMs: closeRequestedAt && closeRestoredAt ? closeRestoredAt - closeRequestedAt : null,
    escapeCloseLatencyMs: escapeSentAt && escapeRestoredAt ? escapeRestoredAt - escapeSentAt : null,
    visibleSelfNoise: visibleSelfNoise(),
    latencyBudget: validateLatencyBudgets(),
    visualInspection: visualInspectionChecks(),
    visualGeometry: {
      modalOverlay: modalGeometry(capturedScreenMap()["Modal overlay full screen"] ?? ""),
      doctorOverlay: modalGeometry(capturedScreenMap()["Doctor overlay full screen"] ?? "")
    },
    visualEvidence: visualEvidenceManifest(),
    visualEvidenceValidation: validateVisualEvidenceManifest(),
    replayValidation: validateReplayArtifacts(),
    packagePreflight,
    visualArtifacts: {
      raw: artifactPath("blackbox-modal-tui.raw"),
      text: artifactPath("blackbox-modal-tui.txt"),
      result: artifactPath("blackbox-modal-tui-result.json"),
      evidenceManifest: artifactPath("blackbox-modal-tui-evidence.json"),
      operatorJourney: artifactPath("blackbox-modal-tui-operator.json"),
      operatorTranscript: artifactPath("blackbox-modal-tui-operator.md"),
      replayCast: artifactPath("blackbox-modal-tui.cast"),
      ioEvents: artifactPath("blackbox-modal-tui-io.jsonl"),
      pngReport: artifactPath("blackbox-modal-tui-report.png"),
      screenPngs
    },
    ...extra
  };
};

const renderTerminalScreen = (value, columns = terminalColumns, rows = terminalRows) => {
  value = String(value ?? "");
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

const extractOverlayEvidence = (value, titlePattern) => {
  const text = stripAnsi(value).replace(/\r/g, "");
  const matches = [...text.matchAll(titlePattern)];
  const titleIndex = matches.at(-1)?.index ?? -1;
  if (titleIndex < 0) return "";
  const before = text.lastIndexOf("\n", Math.max(0, titleIndex - 6000));
  const after = text.indexOf("\n", titleIndex + 2800);
  return text.slice(before < 0 ? 0 : before + 1, after < 0 ? undefined : after).trimEnd();
};

const visualScreen = (value, titlePattern = null) => {
  if (!value) return "";
  const modalBlock = titlePattern ? extractModalBlock(value, titlePattern) : "";
  const rendered = renderTerminalScreen(value);
  return modalBlock || rendered;
};

const capturedScreens = () => [
  ["Modal overlay full screen", extractOverlayEvidence(modalOpenRaw, /Afterburner Black Box Live/gi)],
  ["Modal open screen", visualScreen(modalOpenRaw, /Afterburner Black Box Live/gi)],
  ...focusSteps.map(step => [step.title, step.name === "spaceRefresh"
    ? operatorSteps.find(entry => entry.name === step.name)?.viewportText ?? renderTerminalScreen(focusRaw[step.name])
    : renderTerminalScreen(focusRaw[step.name])]),
  ...scrollSteps.map(step => [step.title, visualScreen(scrollRaw[step.name], /Afterburner Black Box Live/gi)]),
  ["Refresh action screen", visualScreen(refreshRaw, /Afterburner Black Box Live/gi)],
  ["Doctor overlay full screen", extractOverlayEvidence(doctorRaw, /Afterburner Black Box Doctor/gi)],
  ["Doctor action screen", visualScreen(doctorRaw, /Afterburner Black Box Doctor/gi)],
  ["Export action screen", visualScreen(exportRaw, /Afterburner Black Box Export/gi)],
  ["Q close restore screen", visualScreen(closeRestoreRaw)],
  ["Escape close modal screen", visualScreen(escapeModalRaw, /Afterburner Black Box Live/gi)],
  ["Escape close restore screen", visualScreen(escapeRestoreRaw)]
].filter(([, body]) => body);

const capturedScreenMap = () => Object.fromEntries(capturedScreens());

const visibleSelfNoise = () => capturedScreens()
  .filter(([title]) => title !== "Q close restore screen" && title !== "Escape close restore screen")
  .flatMap(([title, body]) => [...body.matchAll(/\bui\.(?:modal_canvas|host)\.[a-z0-9_.-]+\b/gi)]
    .map(match => ({ title, eventType: match[0] })));

const modalGeometry = screen => {
  const lines = screen.split("\n");
  const frameLines = lines
    .map((line, index) => ({ line, index, left: line.search(/[╭│╰]/), right: Math.max(line.lastIndexOf("╮"), line.lastIndexOf("│"), line.lastIndexOf("╯")) }))
    .filter(item => item.left >= 0 && item.right > item.left);
  if (frameLines.length === 0) return { present: false, left: null, right: null, top: null, bottom: null, width: null, height: null };
  const common = values => [...values.reduce((counts, value) => counts.set(value, (counts.get(value) ?? 0) + 1), new Map()).entries()]
    .sort((left, right) => right[1] - left[1])[0][0];
  const left = common(frameLines.map(item => item.left));
  const right = common(frameLines.map(item => item.right));
  const modalLines = frameLines.filter(item => Math.abs(item.left - left) <= 1 && Math.abs(item.right - right) <= 1);
  const top = Math.min(...modalLines.map(item => item.index));
  const bottom = Math.max(...modalLines.map(item => item.index));
  return { present: true, left, right, top, bottom, width: right - left + 1, height: bottom - top + 1 };
};

const geometryLooksOverlay = geometry => geometry.present && geometry.left >= 2 && geometry.right < terminalColumns - 2 && geometry.width >= 80 && geometry.width < terminalColumns && geometry.height >= 10 && geometry.height < terminalRows;

const visualInspectionChecks = () => {
  const screens = Object.fromEntries(capturedScreens());
  const modalOverlayScreen = screens["Modal overlay full screen"] ?? "";
  const doctorOverlayScreen = screens["Doctor overlay full screen"] ?? "";
  const modalScreen = screens["Modal open screen"] ?? "";
  const doctorScreen = screens["Doctor action screen"] ?? "";
  const modalFlowScreens = Object.entries(screens)
    .filter(([title]) => !/restore screen/i.test(title))
    .map(([, screen]) => screen)
    .join("\n");
  const modalOverlayGeometry = modalGeometry(modalScreen);
  const doctorOverlayGeometry = modalGeometry(doctorScreen);
  const focusScreens = Object.fromEntries(focusSteps.map(step => [step.name, screens[step.title] ?? ""]));
  const checks = {
    modalPreservesBackdrop: /Afterburner Black Box Live/i.test(modalOverlayScreen) && /(?:Copilot v|\/ commands|open sidebar)/i.test(modalOverlayScreen),
    doctorPreservesBackdrop: /Afterburner Black Box Doctor/i.test(doctorOverlayScreen) && /(?:Copilot v|\/ commands|open sidebar)/i.test(doctorOverlayScreen),
    modalUsesBoundedOverlayGeometry: geometryLooksOverlay(modalOverlayGeometry),
    doctorUsesBoundedOverlayGeometry: geometryLooksOverlay(doctorOverlayGeometry),
    modalHasBoxChrome: /╭/.test(modalScreen) && /╰/.test(modalScreen),
    modalShowsTitle: /Afterburner Black Box Live/i.test(modalScreen),
    modalShowsNativeOverlaySubtitle: /Native Afterburner modal overlay/i.test(modalScreen),
    modalShowsShortcutSummary: /Shortcuts:\s*r Refresh\s+·\s+d Doctor\s+·\s+e Export\s+·\s+q\/Esc Close/i.test(modalScreen),
    modalShowsActionBar: /(?:▶\s*)?\[r\] Refresh(?:\s*◀)?\s+(?:▶\s*)?\[d\] Doctor(?:\s*◀)?\s+(?:▶\s*)?\[e\] Export(?:\s*◀)?\s+(?:▶\s*)?\[q\] Close(?:\s*◀)?/i.test(modalScreen),
    modalAdvertisesCloseKeys: /Esc\/q closes/i.test(modalScreen),
    modalAdvertisesMetadataOnlyFallback: /metadata-only[\s\S]*\/black-box-tail/i.test(modalScreen),
    modalAdvertisesAllScrollKeys: /↑\/↓ PgUp\/PgDn Home\/End/.test(modalScreen),
    modalShowsStorageProgress: /Storage usage:\s*(?:[█░]+\s*)?\d+% (?:of|used)/i.test(modalScreen),
    modalShowsSignalTrend: /Signal trend:\s*[▁▃▆█]+/i.test(modalScreen),
    modalShowsStatusCards: /Status cards/i.test(modalScreen) && /Recorder/i.test(modalScreen) && /Storage/i.test(modalScreen) && /Signals/i.test(modalScreen) && /Queue/i.test(modalScreen),
    modalShowsActionableHealthCallout: /Needs attention:|Health: no active issues/i.test(modalScreen),
    modalShowsSelectedEventSummary: /Selected event/i.test(modalFlowScreens) && /session\.info|extension\.discovered|session\.model_change|event|milestone/i.test(modalFlowScreens),
    modalShowsTimelineTable: /Metadata timeline table/i.test(modalScreen) && /Time\s+│\s+Kind\s+│\s+Event\s+│\s+Severity\s+│\s+Duration\s+│\s+Success/i.test(modalScreen),
    modalShowsScrollPosition: /lines \d+-\d+ of \d+/i.test(modalScreen),
    everyFocusScreenCaptured: focusSteps.every(step => Boolean(screens[step.title])),
    focusTabShowsRefresh: /▶\s*\[r\] Refresh\s*◀/i.test(focusScreens.focusRefresh),
    focusSpaceActivatesRefresh: /Storage usage:[\s\S]*▶\s*\[r\] Refresh\s*◀/i.test(focusScreens.spaceRefresh),
    everyScrollScreenCaptured: scrollSteps.every(step => Boolean(screens[step.title])),
    refreshScreenCaptured: /Afterburner Black Box Live/i.test(screens["Refresh action screen"] ?? ""),
    doctorScreenCaptured: /Afterburner Black Box Doctor/i.test(doctorScreen),
    doctorShowsRecorderHealth: /Recorder|Storage|Queue/i.test(doctorScreen),
    exportScreenCaptured: /Afterburner Black Box Export/i.test(screens["Export action screen"] ?? "") && /pathRef|manifest|recordCount/i.test(screens["Export action screen"] ?? ""),
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

const visualEvidenceManifest = () => ({
  modalOverlay: {
    screen: "modal-overlay-full-screen.png",
    proves: ["native modal is visible", "Copilot backdrop remains visible behind the overlay", "modal uses bounded centered overlay geometry"]
  },
  primaryModal: {
    screen: "modal-open-screen.png",
    proves: ["title", "native overlay subtitle", "shortcut summary", "action bar", "all keyboard hints", "storage progress", "status cards", "actionable health callout", "selected event summary", "timeline table", "scroll position"]
  },
  focusControls: Object.fromEntries(focusSteps.map(step => [step.name, {
    screen: `${slugTitle(step.title)}.png`,
    key: step.key,
    proves: step.assertions,
    latencyMs: focusSentAt[step.name] && focusSeenAt[step.name] ? focusSeenAt[step.name] - focusSentAt[step.name] : null
  }])),
  scrollControls: Object.fromEntries(scrollSteps.map(step => [step.name, {
    screen: `${slugTitle(step.title)}.png`,
    key: step.key,
    proves: [`${step.title} responds`],
    latencyMs: scrollSentAt[step.name] && scrollSeenAt[step.name] ? scrollSeenAt[step.name] - scrollSentAt[step.name] : null
  }])),
  refresh: { screen: "refresh-action-screen.png", key: "r", proves: ["Refresh action re-renders the live modal"], latencyMs: refreshSentAt && refreshSeenAt ? refreshSeenAt - refreshSentAt : null },
  doctor: { screen: "doctor-action-screen.png", key: "d", proves: ["Doctor view opens", "recorder/storage/queue health is visible", "health warning alert is represented in extension-facing UI"] },
  doctorOverlay: { screen: "doctor-overlay-full-screen.png", proves: ["Doctor view preserves Copilot backdrop", "Doctor view uses bounded centered overlay geometry"] },
  export: { screen: "export-action-screen.png", key: "e", proves: ["Export action creates a sanitized local bundle", "Export result is visible in the modal"], latencyMs: exportSentAt && exportSeenAt ? exportSeenAt - exportSentAt : null },
  close: { screen: "q-close-restore-screen.png", key: "q", proves: ["q closes modal and restores Copilot prompt"], latencyMs: closeRequestedAt && closeRestoredAt ? closeRestoredAt - closeRequestedAt : null },
  escapeClose: { screen: "escape-close-restore-screen.png", key: "Escape", proves: ["Escape closes modal and restores Copilot prompt"], latencyMs: escapeSentAt && escapeRestoredAt ? escapeRestoredAt - escapeSentAt : null }
});

const flattenVisualEvidence = (evidence = visualEvidenceManifest(), path = []) => Object.entries(evidence).flatMap(([name, value]) => {
  const nextPath = [...path, name];
  if (!value || typeof value !== "object") return [];
  if (typeof value.screen === "string") return [{ id: nextPath.join("."), ...value }];
  return flattenVisualEvidence(value, nextPath);
});

const validateVisualEvidenceManifest = () => {
  const generated = new Set(generatedScreenPngArtifacts().map(path => resolve(path).toLowerCase()));
  const entries = flattenVisualEvidence();
  const failures = entries.filter(entry => !entry.screen || !generated.has(resolve(captureDirectory, entry.screen).toLowerCase()) || !Array.isArray(entry.proves) || entry.proves.length === 0 || entry.proves.some(proof => typeof proof !== "string" || proof.trim() === ""));
  return { passed: failures.length === 0, entries, failures };
};

const measuredLatencies = () => ({
  openMs: commandSentAt && modalSeenAt ? modalSeenAt - commandSentAt : null,
  reopenMs: escapeCommandSentAt && escapeModalSeenAt ? escapeModalSeenAt - escapeCommandSentAt : null,
  scrollMs: Object.fromEntries(scrollSteps.map(step => [step.name, scrollSentAt[step.name] && scrollSeenAt[step.name]
    ? scrollSeenAt[step.name] - scrollSentAt[step.name]
    : null])),
  refreshMs: refreshSentAt && refreshSeenAt ? refreshSeenAt - refreshSentAt : null,
  exportMs: exportSentAt && exportSeenAt ? exportSeenAt - exportSentAt : null,
  closeMs: closeRequestedAt && closeRestoredAt ? closeRestoredAt - closeRequestedAt : null,
  escapeCloseMs: escapeSentAt && escapeRestoredAt ? escapeRestoredAt - escapeSentAt : null
});

const validateLatencyBudgets = () => {
  const measured = measuredLatencies();
  const failures = [];
  for (const name of ["openMs", "reopenMs", "refreshMs", "exportMs", "closeMs", "escapeCloseMs"]) {
    if (measured[name] === null || measured[name] > latencyBudgets[name]) failures.push({ name, measuredMs: measured[name], budgetMs: latencyBudgets[name] });
  }
  for (const [name, measuredMs] of Object.entries(measured.scrollMs)) {
    if (measuredMs === null || measuredMs > latencyBudgets.scrollMs) failures.push({ name: `scroll.${name}`, measuredMs, budgetMs: latencyBudgets.scrollMs });
  }
  return { passed: failures.length === 0, budgets: latencyBudgets, measured, failures };
};

const evidenceScreenNames = evidence => {
  const names = [];
  const visit = value => {
    if (!value || typeof value !== "object") return;
    if (typeof value.screen === "string") names.push(value.screen);
    for (const child of Object.values(value)) visit(child);
  };
  visit(evidence);
  return [...new Set(names)];
};

const generatedScreenPngArtifacts = () => capturedScreens().map(([title]) => artifactPath(`${slugTitle(title)}.png`));

const expectedPngArtifacts = () => [...new Set([
  artifactPath("blackbox-modal-tui-report.png"),
  ...generatedScreenPngArtifacts(),
  ...evidenceScreenNames(visualEvidenceManifest()).map(artifactPath)
])];

const paeth = (a, b, c) => {
  const p = a + b - c;
  const pa = Math.abs(p - a);
  const pb = Math.abs(p - b);
  const pc = Math.abs(p - c);
  if (pa <= pb && pa <= pc) return a;
  return pb <= pc ? b : c;
};

const pngPixelStats = buffer => {
  const width = buffer.readUInt32BE(16);
  const height = buffer.readUInt32BE(20);
  const bitDepth = buffer[24];
  const colorType = buffer[25];
  const channels = colorType === 6 ? 4 : colorType === 2 ? 3 : 0;
  const supportedPixelFormat = bitDepth === 8 && channels > 0;
  if (!supportedPixelFormat) return { width, height, colorType, bitDepth, supportedPixelFormat, distinctColors: 0, nonBackgroundPixels: 0 };
  const idat = [];
  for (let offset = 8; offset + 12 <= buffer.length;) {
    const length = buffer.readUInt32BE(offset);
    const chunkStart = offset + 8;
    const chunkEnd = chunkStart + length;
    if (chunkEnd + 4 > buffer.length) break;
    const type = buffer.toString("ascii", offset + 4, chunkStart);
    if (type === "IDAT") idat.push(buffer.subarray(chunkStart, chunkEnd));
    offset = chunkEnd + 4;
  }
  if (idat.length === 0) return { width, height, colorType, bitDepth, supportedPixelFormat, distinctColors: 0, nonBackgroundPixels: 0 };
  const data = inflateSync(Buffer.concat(idat));
  const stride = width * channels;
  const colors = new Set();
  let nonBackgroundPixels = 0;
  let input = 0;
  let previous = Buffer.alloc(stride);
  for (let y = 0; y < height; y++) {
    const filter = data[input++];
    const row = Buffer.alloc(stride);
    for (let x = 0; x < stride; x++) {
      const left = x >= channels ? row[x - channels] : 0;
      const up = previous[x] ?? 0;
      const upLeft = x >= channels ? previous[x - channels] : 0;
      const rawByte = data[input++];
      row[x] = (rawByte + (filter === 1 ? left : filter === 2 ? up : filter === 3 ? Math.floor((left + up) / 2) : filter === 4 ? paeth(left, up, upLeft) : 0)) & 0xff;
    }
    for (let x = 0; x < width; x++) {
      const base = x * channels;
      const key = `${row[base]},${row[base + 1]},${row[base + 2]}`;
      if (colors.size < 512) colors.add(key);
      if (key !== "13,17,23" && key !== "1,4,9") nonBackgroundPixels++;
    }
    previous = row;
  }
  return { width, height, colorType, bitDepth, supportedPixelFormat, distinctColors: colors.size, nonBackgroundPixels };
};

const readPngMetadata = path => {
  if (!existsSync(path)) return { path, exists: false, validSignature: false, bytes: 0, width: null, height: null, supportedPixelFormat: false, distinctColors: 0, nonBackgroundPixels: 0 };
  const stat = statSync(path);
  const buffer = readFileSync(path);
  const signature = Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]);
  const validSignature = buffer.length >= 24 && buffer.subarray(0, 8).equals(signature);
  const stats = validSignature ? pngPixelStats(buffer) : { width: null, height: null, supportedPixelFormat: false, distinctColors: 0, nonBackgroundPixels: 0 };
  return {
    path,
    exists: true,
    validSignature,
    bytes: stat.size,
    ...stats
  };
};

const pngArtifactFailures = item => [
  !item.exists ? "missing" : null,
  item.exists && !item.validSignature ? "invalid signature" : null,
  item.exists && item.bytes < 1024 ? `too small (${item.bytes} bytes)` : null,
  item.validSignature && !item.supportedPixelFormat ? `unsupported PNG pixel format (colorType=${item.colorType}, bitDepth=${item.bitDepth})` : null,
  item.validSignature && (item.width ?? 0) < 800 ? `width ${item.width}px < 800px` : null,
  item.validSignature && (item.height ?? 0) < 150 ? `height ${item.height}px < 150px` : null,
  item.supportedPixelFormat && item.distinctColors < 3 ? `only ${item.distinctColors} distinct colors` : null,
  item.supportedPixelFormat && item.nonBackgroundPixels < 100 ? `only ${item.nonBackgroundPixels} non-background pixels` : null
].filter(Boolean);

const validatePngArtifacts = () => {
  if (process.platform !== "win32") return { passed: false, message: "PNG visual artifacts are only rendered on Windows", artifacts: [] };
  const artifacts = expectedPngArtifacts().map(readPngMetadata).map(item => ({ ...item, failures: pngArtifactFailures(item) }));
  const invalid = artifacts.filter(item => item.failures.length > 0);
  const generated = new Set(generatedScreenPngArtifacts().map(path => resolve(path).toLowerCase()));
  const referenced = new Set(evidenceScreenNames(visualEvidenceManifest()).map(name => resolve(captureDirectory, name).toLowerCase()));
  const missingEvidenceRefs = [...referenced].filter(path => !generated.has(path));
  return invalid.length === 0 && missingEvidenceRefs.length === 0
    ? { passed: true, message: "PNG visual artifacts are present with valid raster dimensions, pixel diversity, and evidence references", artifacts, missingEvidenceRefs }
    : { passed: false, message: `PNG visual artifact validation failed: ${[...invalid.map(item => `${item.path} (${item.failures.join("; ")})`), ...missingEvidenceRefs.map(path => `${path} (not generated by capturedScreens)`)].join(", ")}`, artifacts, missingEvidenceRefs };
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

const writeReplayArtifacts = capture => {
  const castEvents = ioEvents.map(event => [event.t, event.type === "input" ? "i" : "o", Buffer.from(event.dataBase64, "base64").toString("utf8")]);
  const cast = [
    JSON.stringify({ version: 2, width: terminalColumns, height: terminalRows, timestamp: Math.floor(scriptStartedAt / 1000), env: { TERM: "xterm-256color", SHELL: "afterburn.exe" }, title: "Afterburner Black Box real TUI UAT" }),
    ...castEvents.map(event => JSON.stringify(event))
  ].join("\n") + "\n";
  writeFileSync(join(captureDirectory, "blackbox-modal-tui.cast"), cast, "utf8");
  writeFileSync(join(captureDirectory, "blackbox-modal-tui-io.jsonl"), ioEvents.map(event => JSON.stringify(event)).join("\n") + "\n", "utf8");
  const operator = {
    schemaVersion: 1,
    generatedAt: capture.completedAt,
    afterburn,
    terminal: { columns: terminalColumns, rows: terminalRows, kind: "Windows ConPTY via node-pty" },
    packagePreflight: capture.packagePreflight,
    goal: "Operate /black-box-modal like a keyboard-only user and preserve what the user saw after every action.",
    inputCount: ioEvents.filter(event => event.type === "input").length,
    outputChunkCount: ioEvents.filter(event => event.type === "output").length,
    steps: operatorSteps
  };
  writeFileSync(join(captureDirectory, "blackbox-modal-tui-operator.json"), `${JSON.stringify(operator, null, 2)}\n`, "utf8");
  writeFileSync(join(captureDirectory, "blackbox-modal-tui-operator.md"), [
    "# Afterburner Black Box real TUI operator transcript",
    "",
    `- Terminal: ${terminalColumns}x${terminalRows} Windows ConPTY via node-pty`,
    `- Installed Black Box: ${capture.packagePreflight?.source?.version ?? "unknown"} (${capture.packagePreflight?.identity?.sourceType ?? "unknown source"})`,
    `- Black Box tree: ${capture.packagePreflight?.identity?.treeHash ?? "unknown"}`,
    `- Generic canvas fallback scan: ${capture.packagePreflight?.noGenericCanvasFallback ? "passed" : "failed"}`,
    `- Input events: ${operator.inputCount}`,
    `- Output chunks: ${operator.outputChunkCount}`,
    "",
    ...operator.steps.flatMap((step, index) => [
      `## ${index + 1}. ${step.name}`,
      "",
      `- Key/input: ${step.key}`,
      `- Latency: ${step.latencyMs ?? "n/a"} ms`,
      `- Assertions: ${step.assertions.join("; ")}`,
      "",
      "```text",
      step.viewportText,
      "```",
      ""
    ])
  ].join("\n"), "utf8");
};

const validateReplayArtifacts = () => {
  const inputLabels = ioEvents.filter(event => event.type === "input").map(event => event.label);
  const requiredLabels = ["submit /black-box-modal", ...focusSteps.map(step => step.name), "arrowDown", "arrowUp", "pageDown", "pageUp", "end", "home", "refresh", "doctor", "export", "q close", "submit /black-box-modal for Escape", "escape close"];
  const missingInputs = requiredLabels.filter(label => !inputLabels.some(input => input === label || input.startsWith(`${label}:`)));
  const requiredSteps = ["open modal", ...focusSteps.map(step => step.name), ...scrollSteps.map(step => step.name), "refresh", "doctor", "export", "q close", "reopen modal", "escape close"];
  const stepNames = operatorSteps.map(step => step.name);
  const missingSteps = requiredSteps.filter(name => !stepNames.includes(name));
  const emptyViewports = operatorSteps.filter(step => !String(step.viewportText ?? "").trim()).map(step => step.name);
  const outputChunks = ioEvents.filter(event => event.type === "output").length;
  const requiredFiles = ["blackbox-modal-tui.cast", "blackbox-modal-tui-io.jsonl", "blackbox-modal-tui-operator.json", "blackbox-modal-tui-operator.md"];
  const missingFiles = requiredFiles.filter(name => !existsSync(join(captureDirectory, name)));
  return {
    passed: missingInputs.length === 0 && missingSteps.length === 0 && emptyViewports.length === 0 && outputChunks > 0 && missingFiles.length === 0,
    inputCount: inputLabels.length,
    outputChunkCount: outputChunks,
    missingInputs,
    missingSteps,
    emptyViewports,
    missingFiles
  };
};

const writeCaptures = (status = "running", extra = {}) => {
  const capture = result(status, extra);
  writeFileSync(join(captureDirectory, "blackbox-modal-tui.raw"), raw, "utf8");
  writeFileSync(join(captureDirectory, "blackbox-modal-tui.txt"), stripAnsi(raw), "utf8");
  writeReplayArtifacts(capture);
  writeFileSync(join(captureDirectory, "blackbox-modal-tui-result.json"), `${JSON.stringify(capture, null, 2)}\n`, "utf8");
  writePngReport(capture);
  capture.replayValidation = validateReplayArtifacts();
  capture.visualEvidenceValidation = validateVisualEvidenceManifest();
  capture.visualArtifacts.pngValidation = validatePngArtifacts();
  writeFileSync(join(captureDirectory, "blackbox-modal-tui-evidence.json"), `${JSON.stringify({
    schemaVersion: 1,
    generatedAt: capture.completedAt,
    captureDirectory,
    packagePreflight: capture.packagePreflight,
    visualInspection: capture.visualInspection,
    latencyBudget: capture.latencyBudget,
    evidence: capture.visualEvidence,
    evidenceValidation: capture.visualEvidenceValidation,
    replayValidation: capture.replayValidation,
    pngValidation: capture.visualArtifacts.pngValidation
  }, null, 2)}\n`, "utf8");
  writeFileSync(join(captureDirectory, "blackbox-modal-tui-result.json"), `${JSON.stringify(capture, null, 2)}\n`, "utf8");
};

const finish = (code, message) => {
  if (finished) return;
  finished = true;
  clearTimeout(timeout);
  writeCaptures(code === 0 ? "passed" : "failed", { message });
  let exitCode = code;
  let finalMessage = message;
  if (code === 0) {
    const latencyValidation = validateLatencyBudgets();
    if (!latencyValidation.passed) {
      exitCode = 1;
      finalMessage = `Black Box modal latency budget exceeded: ${latencyValidation.failures.map(item => `${item.name}=${item.measuredMs}ms>${item.budgetMs}ms`).join(", ")}`;
      writeCaptures("failed", { message: finalMessage });
    }
  }
  if (exitCode === 0) {
    const evidenceValidation = validateVisualEvidenceManifest();
    if (!evidenceValidation.passed) {
      exitCode = 1;
      finalMessage = `visual evidence manifest validation failed: ${evidenceValidation.failures.map(item => item.id).join(", ")}`;
      writeCaptures("failed", { message: finalMessage });
    }
  }
  if (exitCode === 0) {
    const replayValidation = validateReplayArtifacts();
    if (!replayValidation.passed) {
      exitCode = 1;
      finalMessage = `interactive replay validation failed: missingInputs=${replayValidation.missingInputs.join(",")}; missingSteps=${replayValidation.missingSteps.join(",")}; emptyViewports=${replayValidation.emptyViewports.join(",")}`;
      writeCaptures("failed", { message: finalMessage });
    }
  }
  if (exitCode === 0) {
    const pngValidation = validatePngArtifacts();
    if (!pngValidation.passed) {
      exitCode = 1;
      finalMessage = pngValidation.message;
      writeCaptures("failed", { message: finalMessage });
    }
  }
  try { process.kill(child.pid); } catch {}
  if (exitCode === 0) process.stdout.write(`${finalMessage}\n`);
  else process.stderr.write(`${finalMessage}\n--- tail ---\n${stripAnsi(raw).slice(-6000)}\n`);
  setTimeout(() => process.exit(exitCode), 500);
};

const scheduleWrite = (data, delayMs = 150, label = "input") => setTimeout(() => writeInput(data, label), delayMs).unref?.();
const scheduleCommand = (command, delayMs = 150, onSubmit = () => {}, label = `submit ${command}`) => {
  setTimeout(() => {
    onSubmit();
    writeInput("\x15", `${label}: clear prompt`);
    writeInput(`${command}\r`, `${label}: type and submit command`);
  }, delayMs).unref?.();
};

child.onData(data => {
  recordOutput(data);
  raw += data;
  const text = stripAnsi(raw);
  const recent = text.slice(-5000);

  if (!trusted && /Do you trust the files in this folder/i.test(recent)) {
    trusted = true;
    scheduleWrite("\r", 250, "trust current folder");
    return;
  }
  if (!commandInputStartedAt && restoreDismissCount < 3 && /Restore interrupted sessions/i.test(recent) && Date.now() - lastRestoreDismissAt > 1000) {
    restored = true;
    restoreDismissCount++;
    lastRestoreDismissAt = Date.now();
    scheduleWrite("\x1b", 250, "dismiss restore sessions");
    return;
  }
  if (!approved && /wants elevated permissions/i.test(recent)) {
    approved = true;
    scheduleWrite("\r", 250, "approve elevated permissions");
    return;
  }
  if (!terminalSetupDeclined && /Set up terminal for multi-line input support/i.test(recent)) {
    terminalSetupDeclined = true;
    scheduleWrite("\x1b", 250, "dismiss terminal setup");
    return;
  }
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

  if (!commandSentAt && /Afterburner Black Box Live/i.test(recent)) {
    finish(1, "Black Box modal auto-opened before explicit /black-box-modal command");
    return;
  }

  const runtimeReady = /\[runtime-extension-host\] loaded from/i.test(text) &&
    /activated Afterburner extension 'black-box-uat'/i.test(text);
  const promptReady = /\/ commands|tab next tab|\? help|Tip:\s*\/app|Skipped terminal setup/i.test(recent);
  if (!commandInputStartedAt && runtimeReady && promptReady) {
    commandInputStartedAt = Date.now();
    scheduleCommand("/black-box-modal", 20000, () => { commandSentAt = Date.now(); }, "submit /black-box-modal");
    return;
  }
  if (commandSentAt && !modalSeenAt &&
      !/Afterburner Black Box Live/i.test(text) &&
      /Unknown command:\s*\/black-box-modal/i.test(recent)) {
    if (Date.now() - commandSentAt > 30_000) {
      finish(1, "Copilot never registered /black-box-modal after runtime startup");
    } else if (Date.now() - commandSubmitRetryAt > 5000) {
      commandSubmitRetryAt = Date.now();
      scheduleCommand("/black-box-modal", 250, () => {}, "retry /black-box-modal");
    }
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
  if (escapeCommandSentAt && !escapeModalSeenAt && /Canvas opened:\s*Afterburner Black Box/i.test(stripAnsi(raw.slice(escapeCommandRawLength)))) {
    finish(1, "Copilot opened the Black Box canvas instead of reopening the native modal");
    return;
  }

  if (commandSentAt && !modalSeenAt && /Afterburner Black Box Live/i.test(text)) {
    modalSeenAt = Date.now();
    recordOperatorStep({ name: "open modal", key: "/black-box-modal", startedAt: commandSentAt, completedAt: modalSeenAt, screenTitle: "Modal open screen", screenRaw: raw, assertions: ["native modal title visible", "Copilot backdrop remains present", "keyboard shortcuts advertised"] });
  }
  if (modalSeenAt && !modalCaptureScheduled) {
    modalCaptureScheduled = true;
    setTimeout(() => {
      modalOpenRaw = raw;
      const step = focusSteps[focusIndex];
      focusSentAt[step.name] = Date.now();
      activeFocusRawLength = raw.length;
      writeInput(step.key, step.name);
    }, 500).unref?.();
    return;
  }
  const activeFocusStep = focusSteps[focusIndex];
  if (activeFocusStep && focusSentAt[activeFocusStep.name] && !focusSeenAt[activeFocusStep.name] && activeFocusStep.want.test(stripAnsi(raw.slice(activeFocusRawLength)))) {
    focusSeenAt[activeFocusStep.name] = Date.now();
    focusRaw[activeFocusStep.name] = raw;
    recordOperatorStep({ name: activeFocusStep.name, key: activeFocusStep.key, startedAt: focusSentAt[activeFocusStep.name], completedAt: focusSeenAt[activeFocusStep.name], screenTitle: activeFocusStep.title, screenRaw: raw, assertions: activeFocusStep.assertions });
    focusIndex++;
    const nextFocusStep = focusSteps[focusIndex];
    setTimeout(() => {
      if (nextFocusStep) {
        focusSentAt[nextFocusStep.name] = Date.now();
        activeFocusRawLength = raw.length;
        writeInput(nextFocusStep.key, nextFocusStep.name);
      } else {
        const step = scrollSteps[scrollIndex];
        scrollSentAt[step.name] = Date.now();
        activeScrollRawLength = raw.length;
        writeInput(step.key, step.name);
      }
    }, 1000).unref?.();
    return;
  }
  const activeStep = scrollSteps[scrollIndex];
  if (activeStep && scrollSentAt[activeStep.name] && !scrollSeenAt[activeStep.name] && activeStep.want.test(stripAnsi(raw.slice(activeScrollRawLength)))) {
    scrollSeenAt[activeStep.name] = Date.now();
    scrollRaw[activeStep.name] = raw;
    recordOperatorStep({ name: activeStep.name, key: activeStep.key, startedAt: scrollSentAt[activeStep.name], completedAt: scrollSeenAt[activeStep.name], screenTitle: activeStep.title, screenRaw: raw, assertions: ["scroll position changed as expected", "modal remained focused after navigation key"] });
    scrollIndex++;
    const nextStep = scrollSteps[scrollIndex];
    setTimeout(() => {
      if (nextStep) {
        scrollSentAt[nextStep.name] = Date.now();
        activeScrollRawLength = raw.length;
        writeInput(nextStep.key, nextStep.name);
      } else {
        refreshSentAt = Date.now();
        refreshRawLength = raw.length;
        writeInput("r", "refresh");
      }
    }, 250).unref?.();
    return;
  }
  if (refreshSentAt && !refreshSeenAt && /Afterburner Black Box Live/i.test(stripAnsi(raw.slice(refreshRawLength)))) {
    refreshSeenAt = Date.now();
    refreshRaw = raw;
    recordOperatorStep({ name: "refresh", key: "r", startedAt: refreshSentAt, completedAt: refreshSeenAt, screenTitle: "Refresh action screen", screenRaw: raw, assertions: ["Refresh action re-rendered the live modal", "modal stayed open and focused"] });
    setTimeout(() => writeInput("d", "doctor"), 250).unref?.();
    return;
  }
  if (modalSeenAt && focusIndex >= focusSteps.length && refreshSeenAt && !doctorSeen && /Afterburner Black Box Doctor/i.test(stripAnsi(raw.slice(refreshRawLength)))) {
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
    recordOperatorStep({ name: "doctor", key: "d", startedAt: refreshSeenAt, completedAt: Date.now(), screenTitle: "Doctor action screen", screenRaw: raw, assertions: ["Doctor view title visible", "health diagnostics visible"] });
  }
  if (doctorSeen && !doctorCaptureScheduled) {
    doctorCaptureScheduled = true;
    setTimeout(() => {
      doctorRaw = raw;
      exportSentAt = Date.now();
      exportRawLength = raw.length;
      writeInput("e", "export");
    }, 500).unref?.();
    return;
  }
  if (exportSentAt && !exportSeenAt && /Afterburner Black Box Export/i.test(stripAnsi(raw.slice(exportRawLength)))) {
    exportSeenAt = Date.now();
    exportRaw = raw;
    recordOperatorStep({ name: "export", key: "e", startedAt: exportSentAt, completedAt: exportSeenAt, screenTitle: "Export action screen", screenRaw: raw, assertions: ["Export view title visible", "sanitized export result visible in modal"] });
    setTimeout(() => {
      closeSent = true;
      closeRequestedAt = Date.now();
      closeRawLength = raw.length;
      writeInput("q", "q close");
    }, 500).unref?.();
    return;
  }
  if (closeSent && !closeRestoredAt) {
    const afterCloseText = stripAnsi(raw.slice(closeRawLength));
    if (/\/ commands|tab next tab|\? help/i.test(afterCloseText)) {
      closeRestoredAt = Date.now();
      closeRestoreRaw = raw;
      recordOperatorStep({ name: "q close", key: "q", startedAt: closeRequestedAt, completedAt: closeRestoredAt, screenTitle: "Q close restore screen", screenRaw: raw, assertions: ["q closed the modal", "Copilot prompt restored"] });
      const selfNoise = visibleSelfNoise();
      if (selfNoise.length > 0) {
        finish(1, `Black Box modal displayed self-noise events: ${selfNoise.map(item => `${item.title}:${item.eventType}`).join(", ")}`);
        return;
      }
      escapeCommandInputStartedAt = Date.now();
      scheduleCommand("/black-box-modal", 500, () => {
        escapeCommandSentAt = Date.now();
        escapeCommandRawLength = raw.length;
        setTimeout(() => {
          if (!escapeModalSeenAt) writeInput("\r", "resubmit /black-box-modal for Escape");
        }, 1500).unref?.();
      }, "submit /black-box-modal for Escape");
      return;
    }
  }
  if (escapeCommandSentAt && !escapeModalSeenAt && /Afterburner Black Box Live/i.test(stripAnsi(raw.slice(escapeCommandRawLength)))) {
    escapeModalSeenAt = Date.now();
    escapeModalRaw = raw;
    recordOperatorStep({ name: "reopen modal", key: "/black-box-modal", startedAt: escapeCommandSentAt, completedAt: escapeModalSeenAt, screenTitle: "Escape close modal screen", screenRaw: raw, assertions: ["modal reopened after q close", "live view visible again"] });
    setTimeout(() => {
      escapeSentAt = Date.now();
      escapeRawLength = raw.length;
      writeInput("\x1b[27;1;0;1;0;1_", "escape close");
    }, 500).unref?.();
    return;
  }
  if (escapeSentAt && !escapeRestoredAt) {
    const afterEscapeText = stripAnsi(raw.slice(escapeRawLength));
    if (/\/ commands|tab next tab|\? help/i.test(afterEscapeText)) {
      escapeRestoredAt = Date.now();
      escapeRestoreRaw = raw;
      recordOperatorStep({ name: "escape close", key: "Escape", startedAt: escapeSentAt, completedAt: escapeRestoredAt, screenTitle: "Escape close restore screen", screenRaw: raw, assertions: ["Escape closed the modal", "Copilot prompt restored"] });
      if (failIfVisualInspectionFailed()) return;
      const scrollSummary = scrollSteps.map(step => `${step.name}:${scrollSeenAt[step.name] - scrollSentAt[step.name]}ms`).join(",");
      finish(0, `real-blackbox-modal-tui-ok openLatencyMs=${modalSeenAt - commandSentAt} scroll=${scrollSummary} refreshLatencyMs=${refreshSeenAt - refreshSentAt} exportLatencyMs=${exportSeenAt - exportSentAt} doctorAction=true exportAction=true qCloseLatencyMs=${closeRestoredAt - closeRequestedAt} escapeCloseLatencyMs=${escapeRestoredAt - escapeSentAt} report=${join(captureDirectory, "blackbox-modal-tui-report.png")}`);
    }
  }
});

child.onExit(({ exitCode }) => {
  if (finished) return;
  if (modalSeenAt && doctorSeen && exportSeenAt && closeSent && closeRestoredAt && escapeRestoredAt) {
    if (failIfVisualInspectionFailed()) return;
    const scrollSummary = scrollSteps.map(step => `${step.name}:${scrollSeenAt[step.name] - scrollSentAt[step.name]}ms`).join(",");
    finish(0, `real-blackbox-modal-tui-ok openLatencyMs=${modalSeenAt - commandSentAt} scroll=${scrollSummary} refreshLatencyMs=${refreshSeenAt - refreshSentAt} exportLatencyMs=${exportSeenAt - exportSentAt} doctorAction=true exportAction=true qCloseLatencyMs=${closeRestoredAt - closeRequestedAt} escapeCloseLatencyMs=${escapeRestoredAt - escapeSentAt} report=${join(captureDirectory, "blackbox-modal-tui-report.png")} exitCode=${exitCode}`);
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
  else if (!exportSentAt) finish(1, "timed out before Black Box export key was sent");
  else if (!exportSeenAt) finish(1, "timed out before Black Box export action rendered");
  else if (!closeSent) finish(1, "timed out before Black Box q close key was sent");
  else if (!closeRestoredAt) finish(1, "timed out before Black Box q close restored the Copilot prompt");
  else if (!escapeCommandSentAt) finish(1, "timed out before reopening Black Box modal for Escape close validation");
  else if (!escapeModalSeenAt) finish(1, "timed out before reopened Black Box modal appeared");
  else if (!escapeSentAt) finish(1, "timed out before Black Box Escape close key was sent");
  else finish(1, "timed out before Black Box Escape close restored the Copilot prompt");
}, timeoutMs);
timeout.unref?.();
