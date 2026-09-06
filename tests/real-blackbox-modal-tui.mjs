import { spawnSync } from "node:child_process";
import { existsSync, mkdirSync, readFileSync, statSync, writeFileSync } from "node:fs";
import { homedir } from "node:os";
import { inflateSync } from "node:zlib";
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
const hasFlag = name => process.argv.includes(name);

if (hasFlag("--help") || hasFlag("-h")) {
  process.stdout.write(`Usage: node tests\\real-blackbox-modal-tui.mjs [afterburn.exe] [capture-dir] [timeout-ms] [options]\n\nOptions:\n  --afterburn <path>          Afterburner executable to launch.\n  --capture-dir <path>       Directory for raw/text/json/png visual artifacts.\n  --timeout-ms <ms>          End-to-end UAT timeout.\n  --max-open-ms <ms>         Native modal first-open latency budget.\n  --max-reopen-ms <ms>       Native modal reopen latency budget.\n  --max-scroll-ms <ms>       Per-key scroll response budget.\n  --max-refresh-ms <ms>      Refresh action response budget.\n  --max-close-ms <ms>        q close response budget.\n  --max-escape-close-ms <ms> Escape close response budget.\n\nEnvironment overrides use AFTERBURNER_REAL_TUI_* names matching each option.\n`);
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
  closeMs: Number(option("--max-close-ms") ?? process.env.AFTERBURNER_REAL_TUI_MAX_CLOSE_MS ?? 500),
  escapeCloseMs: Number(option("--max-escape-close-ms") ?? process.env.AFTERBURNER_REAL_TUI_MAX_ESCAPE_CLOSE_MS ?? 500)
};
const scriptStartedAt = Date.now();
mkdirSync(captureDirectory, { recursive: true });

const env = { ...process.env, COPILOT_RUNTIME_EXTENSION_DEBUG: "1" };
delete env.COPILOT_AGENT_SESSION_ID;
delete env.COPILOT_LOADER_PID;
delete env.COPILOT_SUPERVISED;

const terminalColumns = 140;
const terminalRows = 40;

const child = pty.spawn(afterburn, [], {
  name: "xterm-256color",
  cols: terminalColumns,
  rows: terminalRows,
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
    latencyBudget: validateLatencyBudgets(),
    visualInspection: visualInspectionChecks(),
    visualGeometry: {
      modalOverlay: modalGeometry(capturedScreenMap()["Modal overlay full screen"] ?? ""),
      doctorOverlay: modalGeometry(capturedScreenMap()["Doctor overlay full screen"] ?? "")
    },
    visualEvidence: visualEvidenceManifest(),
    visualEvidenceValidation: validateVisualEvidenceManifest(),
    visualArtifacts: {
      raw: artifactPath("blackbox-modal-tui.raw"),
      text: artifactPath("blackbox-modal-tui.txt"),
      result: artifactPath("blackbox-modal-tui-result.json"),
      pngReport: artifactPath("blackbox-modal-tui-report.png"),
      screenPngs
    },
    ...extra
  };
};

const renderTerminalScreen = (value, columns = terminalColumns, rows = terminalRows) => {
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
  const before = text.lastIndexOf("\n", Math.max(0, titleIndex - 1200));
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
  ...scrollSteps.map(step => [step.title, visualScreen(scrollRaw[step.name], /Afterburner Black Box Live/gi)]),
  ["Refresh action screen", visualScreen(refreshRaw, /Afterburner Black Box Live/gi)],
  ["Doctor overlay full screen", extractOverlayEvidence(doctorRaw, /Afterburner Black Box Doctor/gi)],
  ["Doctor action screen", visualScreen(doctorRaw, /Afterburner Black Box Doctor/gi)],
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
  const left = Math.min(...frameLines.map(item => item.left));
  const right = Math.max(...frameLines.map(item => item.right));
  const top = Math.min(...frameLines.map(item => item.index));
  const bottom = Math.max(...frameLines.map(item => item.index));
  return { present: true, left, right, top, bottom, width: right - left + 1, height: bottom - top + 1 };
};

const geometryLooksOverlay = geometry => geometry.present && geometry.left >= 2 && geometry.right < terminalColumns - 2 && geometry.top >= 1 && geometry.bottom < terminalRows - 1 && geometry.width >= 80 && geometry.width < terminalColumns && geometry.height >= 10 && geometry.height < terminalRows;

const visualInspectionChecks = () => {
  const screens = Object.fromEntries(capturedScreens());
  const modalOverlayScreen = screens["Modal overlay full screen"] ?? "";
  const doctorOverlayScreen = screens["Doctor overlay full screen"] ?? "";
  const modalScreen = screens["Modal open screen"] ?? "";
  const doctorScreen = screens["Doctor action screen"] ?? "";
  const modalOverlayGeometry = modalGeometry(modalOverlayScreen);
  const doctorOverlayGeometry = modalGeometry(doctorOverlayScreen);
  const checks = {
    modalPreservesBackdrop: /Afterburner Black Box Live/i.test(modalOverlayScreen) && /(?:Copilot v|\/ commands|open sidebar)/i.test(modalOverlayScreen),
    doctorPreservesBackdrop: /Afterburner Black Box Doctor/i.test(doctorOverlayScreen) && /(?:Copilot v|\/ commands|open sidebar)/i.test(doctorOverlayScreen),
    modalUsesBoundedOverlayGeometry: geometryLooksOverlay(modalOverlayGeometry),
    doctorUsesBoundedOverlayGeometry: geometryLooksOverlay(doctorOverlayGeometry),
    modalHasBoxChrome: /╭/.test(modalScreen) && /╰/.test(modalScreen),
    modalShowsTitle: /Afterburner Black Box Live/i.test(modalScreen),
    modalShowsSecureCanvasSubtitle: /Host-rendered secure canvas/i.test(modalScreen),
    modalShowsActionBar: /\[r\] Refresh\s+\[d\] Doctor\s+\[q\] Close/i.test(modalScreen),
    modalAdvertisesCloseKeys: /Esc\/q closes/i.test(modalScreen),
    modalAdvertisesMetadataOnlyFallback: /metadata-only fallback remains \/black-box-tail/i.test(modalScreen),
    modalAdvertisesAllScrollKeys: /↑\/↓ PgUp\/PgDn Home\/End/.test(modalScreen),
    modalShowsStatusCards: /Status cards/i.test(modalScreen) && /Recorder/i.test(modalScreen) && /Storage/i.test(modalScreen) && /Signals/i.test(modalScreen) && /Queue/i.test(modalScreen),
    modalShowsTimelineTable: /Metadata timeline table/i.test(modalScreen) && /Time\s+│\s+Kind\s+│\s+Event\s+│\s+Severity\s+│\s+Duration\s+│\s+Success/i.test(modalScreen),
    modalShowsScrollPosition: /lines \d+-\d+ of \d+/i.test(modalScreen),
    everyScrollScreenCaptured: scrollSteps.every(step => Boolean(screens[step.title])),
    refreshScreenCaptured: /Afterburner Black Box Live/i.test(screens["Refresh action screen"] ?? ""),
    doctorScreenCaptured: /Afterburner Black Box Doctor/i.test(doctorScreen),
    doctorShowsRecorderHealth: /Recorder|Storage|Queue/i.test(doctorScreen),
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
    proves: ["title", "secure-canvas subtitle", "action bar", "all keyboard hints", "status cards", "timeline table", "scroll position"]
  },
  scrollControls: Object.fromEntries(scrollSteps.map(step => [step.name, {
    screen: `${slugTitle(step.title)}.png`,
    key: step.key,
    proves: [`${step.title} responds`],
    latencyMs: scrollSentAt[step.name] && scrollSeenAt[step.name] ? scrollSeenAt[step.name] - scrollSentAt[step.name] : null
  }])),
  refresh: { screen: "refresh-action-screen.png", key: "r", proves: ["Refresh action re-renders the live modal"], latencyMs: refreshSentAt && refreshSeenAt ? refreshSeenAt - refreshSentAt : null },
  doctor: { screen: "doctor-action-screen.png", key: "d", proves: ["Doctor view opens", "recorder/storage/queue health is visible"] },
  doctorOverlay: { screen: "doctor-overlay-full-screen.png", proves: ["Doctor view preserves Copilot backdrop", "Doctor view uses bounded centered overlay geometry"] },
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
  closeMs: closeRequestedAt && closeRestoredAt ? closeRestoredAt - closeRequestedAt : null,
  escapeCloseMs: escapeSentAt && escapeRestoredAt ? escapeRestoredAt - escapeSentAt : null
});

const validateLatencyBudgets = () => {
  const measured = measuredLatencies();
  const failures = [];
  for (const name of ["openMs", "reopenMs", "refreshMs", "closeMs", "escapeCloseMs"]) {
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

const writeCaptures = (status = "running", extra = {}) => {
  const capture = result(status, extra);
  writeFileSync(join(captureDirectory, "blackbox-modal-tui.raw"), raw, "utf8");
  writeFileSync(join(captureDirectory, "blackbox-modal-tui.txt"), stripAnsi(raw), "utf8");
  writeFileSync(join(captureDirectory, "blackbox-modal-tui-result.json"), `${JSON.stringify(capture, null, 2)}\n`, "utf8");
  writePngReport(capture);
  capture.visualEvidenceValidation = validateVisualEvidenceManifest();
  capture.visualArtifacts.pngValidation = validatePngArtifacts();
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
  setTimeout(() => {
    child.write("\x15");
    child.write(`\x1b[200~${command}\x1b[201~`);
  }, delayMs).unref?.();
  setTimeout(() => {
    onSubmit();
    child.write("\r");
  }, delayMs + 900).unref?.();
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
  if (escapeCommandSentAt && !escapeModalSeenAt && /Canvas opened:\s*Afterburner Black Box/i.test(stripAnsi(raw.slice(escapeCommandRawLength)))) {
    finish(1, "Copilot opened the Black Box canvas instead of reopening the native modal");
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
