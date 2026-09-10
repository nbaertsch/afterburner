import { spawnSync } from "node:child_process";
import { createHash } from "node:crypto";
import { cpSync, existsSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, writeFileSync } from "node:fs";
import { readdir, readFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import process from "node:process";
import pty from "node-pty";

const stripAnsi = value => value
  .replace(/\x1b\][^\x07]*(?:\x07|\x1b\\)/g, "")
  .replace(/\x1b\[[0-?]*[ -/]*[@-~]/g, "")
  .replace(/\x1b[()][A-Za-z0-9]/g, "");

const afterburn = resolve(process.argv[2] ?? process.env.AFTERBURNER_EXE ?? "artifacts\\afterburn.exe");
const maybeFake = process.argv[3] && /(?:^|[\\/])[^\\/]+\.exe$/i.test(process.argv[3]) ? resolve(process.argv[3]) : undefined;
const captureArg = maybeFake ? process.argv[4] : process.argv[3];
const captureDirectory = resolve(captureArg ?? process.env.AFTERBURNER_MULTI_SESSION_CAPTURE ?? join(process.cwd(), "artifacts", "multi-session-conpty"));
const timeoutMs = Number(process.env.AFTERBURNER_MULTI_SESSION_TIMEOUT_MS ?? 120_000);
const root = mkdtempSync(join(tmpdir(), "afterburner-multisession-"));
const home = join(root, "afterburner");
const normal = join(root, "normal");
mkdirSync(captureDirectory, { recursive: true });
mkdirSync(normal, { recursive: true });
writeFileSync(join(normal, "settings.json"), `${JSON.stringify({
  experimental: true
}, null, 2)}\n`, "utf8");
const packages = join(root, "packages");
const auditDirectory = join(captureDirectory, "route-ipc-audit");
mkdirSync(packages, { recursive: true });
rmSync(auditDirectory, { recursive: true, force: true });
mkdirSync(auditDirectory, { recursive: true });
process.once("exit", () => { try { rmSync(root, { recursive: true, force: true }); } catch {} });

function run(args, message) {
  const result = spawnSync(afterburn, args, { cwd: process.cwd(), encoding: "utf8", env: { ...process.env, AFTERBURNER_HOME: home, AFTERBURNER_DISABLE_BUILTIN_RELEASE_FETCH: "1" } });
  if (result.status !== 0) throw new Error(`${message}: ${result.status}\nstdout=${result.stdout}\nstderr=${result.stderr}`);
}

function sha256File(path) {
  return createHash("sha256").update(readFileSync(path)).digest("hex");
}

let packageEvidence = [];
if (maybeFake) {
  for (const id of ["black-box", "openai-server"]) run(["install", id], `install ${id}`);
  run(["extension", "enable", "black-box"], "enable black-box");
  run(["extension", "enable", "openai-server"], "enable openai-server");
  packageEvidence = [{ mode: "builtin-catalog-with-fakecopilot" }];
} else {
  const releasePackages = [["black-box", join(process.cwd(), "artifacts", "black-box.zip")], ["openai-server", join(process.cwd(), "artifacts", "openai-server.zip")]];
  const releasePackagesAvailable = releasePackages.every(([, archive]) => existsSync(archive));
  if (!releasePackagesAvailable && process.env.AFTERBURNER_REQUIRE_RELEASE_PACKAGES === "1") {
    throw new Error("required release gate must install canonical built extension packages from artifacts/*.zip");
  }
  if (releasePackagesAvailable) {
    for (const [id, archive] of releasePackages) {
      const uatId = `${id}-uat`;
      const packageSource = join(packages, uatId);
      const expanded = spawnSync("powershell", ["-NoProfile", "-Command", "Expand-Archive", "-LiteralPath", archive, "-DestinationPath", packageSource], { cwd: process.cwd(), encoding: "utf8" });
      if (expanded.status !== 0) throw new Error(`expand release package ${id}: ${expanded.status}\nstdout=${expanded.stdout}\nstderr=${expanded.stderr}`);
      const manifestPath = join(packageSource, "afterburner.json");
      const manifest = JSON.parse(readFileSync(manifestPath, "utf8"));
      if (manifest.id !== id || manifest.visibility !== "builtin") throw new Error(`release package ${id} manifest was not canonical built-in metadata`);
      manifest.id = uatId;
      manifest.visibility = "private";
      writeFileSync(manifestPath, `${JSON.stringify(manifest, null, 2)}\n`, "utf8");
      const transformedArchive = join(packages, `${uatId}.zip`);
      run(["extension", "pack", packageSource, transformedArchive], `pack transformed release package ${id}`);
      run(["extension", "install", transformedArchive], `install transformed release package ${id}`);
      run(["extension", "enable", uatId], `enable ${uatId}`);
      packageEvidence.push({ id: uatId, sourceArchive: archive.replace(process.cwd(), "%REPO%"), sourceSha256: sha256File(archive), transformedSha256: sha256File(transformedArchive), transformation: "afterburner.json id -> *-uat and visibility -> private only", mode: "release-zip-manifest-id-transform" });
    }
  } else {
    for (const [id, source] of [["black-box", join(process.cwd(), "extensions", "BlackBox")], ["openai-server", join(process.cwd(), "extensions", "OpenAIServer")]]) {
      const uatId = `${id}-uat`;
      const packageSource = join(packages, uatId);
      cpSync(source, packageSource, { recursive: true });
      const manifestPath = join(packageSource, "afterburner.json");
      const manifest = JSON.parse(readFileSync(manifestPath, "utf8"));
      manifest.id = uatId;
      manifest.visibility = "private";
      writeFileSync(manifestPath, `${JSON.stringify(manifest, null, 2)}\n`, "utf8");
      const archive = join(packages, `${uatId}.zip`);
      run(["extension", "pack", packageSource, archive], `pack ${uatId}`);
      run(["extension", "install", archive], `install ${uatId}`);
      run(["extension", "enable", uatId], `enable ${uatId}`);
      packageEvidence.push({ id: uatId, archive: archive.replace(root, "%TEMP%"), sha256: sha256File(archive), transformation: "afterburner.json id -> *-uat and visibility -> private only", mode: "source-manifest-id-transform" });
    }
  }
}

const baseEnv = {
  ...process.env,
  COPILOT_RUNTIME_EXTENSION_DEBUG: "1",
  AFTERBURNER_HOME: home,
  AFTERBURNER_NORMAL_COPILOT_HOME: normal,
  AFTERBURNER_ISOLATE_SESSION_STATE: "1",
  AFTERBURNER_SKIP_PREFLIGHT: "1",
  AFTERBURNER_TERMINAL_BROKER: "1",
  ...(maybeFake ? { AFTERBURNER_COPILOT_EXECUTABLE: maybeFake } : {})
};
for (const key of [
  "COPILOT_AGENT_SESSION_ID",
  "COPILOT_CLI",
  "COPILOT_CLI_BINARY_VERSION",
  "COPILOT_CLI_RESOLVED_DIST_DIR",
  "COPILOT_HOME",
  "COPILOT_LOADER_PID",
  "COPILOT_SUPERVISED",
  "AFTERBURNER_SESSION_ROUTE"
]) delete baseEnv[key];

function launch(label, extraEnv = {}) {
  const child = pty.spawn(afterburn, ["--name", `afterburn-${label}-${process.pid}-${Date.now()}`, "--no-remote"], {
    name: "xterm-256color",
    cols: 140,
    rows: 40,
    cwd: process.cwd(),
    env: { ...baseEnv, AFTERBURNER_ROUTE_IPC_AUDIT: auditDirectory, AFTERBURNER_TEST_SESSION_LABEL: label, ...extraEnv }
  });
  const state = {
    label,
    child,
    raw: "",
    io: [],
    closed: false,
    approvedTrust: false,
    approvedElevation: false,
    dismissedRestore: false,
    dismissedTerminalSetup: false,
    nativeAppSelectionMovedAt: 0,
    declinedNativeApp: false
  };
  child.onData(data => {
    state.raw += data;
    state.io.push({ t: Date.now(), type: "output", bytes: Buffer.byteLength(data), dataBase64: Buffer.from(data).toString("base64") });
  });
  child.onExit(event => { state.closed = true; state.exit = event; });
  return state;
}

const currentSessions = [];

function send(session, data, label) {
  session.io.push({ t: Date.now(), type: "input", label, display: data.replace(/\r/g, "<Enter>").replace(/\x1b/g, "<Esc>"), dataBase64: Buffer.from(data).toString("base64") });
  session.child.write(data);
}

async function waitFor(predicate, description, ms = timeoutMs) {
  const deadline = Date.now() + ms;
  while (Date.now() < deadline) {
    for (const session of currentSessions) {
      const text = stripAnsi(session.raw);
      const recent = text.slice(-4000);
      if (!session.dismissedRestore && /Restore interrupted sessions/i.test(recent)) {
        session.dismissedRestore = true;
        send(session, "\x1b", "dismiss startup dialog");
      }
      if (!session.dismissedTerminalSetup && /Set up terminal for multi-line input support|Would you like to add this key binding/i.test(recent)) {
        session.dismissedTerminalSetup = true;
        send(session, "\x1b", "dismiss terminal setup");
      }
      if (!session.nativeAppSelectionMovedAt && /Yes, install[\s\S]{0,200}No, thanks/i.test(recent)) {
        session.nativeAppSelectionMovedAt = Date.now();
        send(session, "\x1b[C", "select no native desktop app");
      } else if (!session.declinedNativeApp && session.nativeAppSelectionMovedAt && Date.now() - session.nativeAppSelectionMovedAt >= 500) {
        session.declinedNativeApp = true;
        send(session, "\r", "decline native desktop app");
      }
      if (!session.approvedTrust && /Confirm folder trust|Do you trust the files in this folder/i.test(recent)) {
        session.approvedTrust = true;
        send(session, "\r", "approve folder trust");
      }
      if (!session.approvedElevation && /wants elevated permissions/i.test(recent)) {
        session.approvedElevation = true;
        send(session, "\r", "approve extension permissions");
      }
    }
    if (await predicate()) return;
    await new Promise(resolve => setTimeout(resolve, 100));
  }
  const sessionState = currentSessions.map(session => {
    const tail = stripAnsi(session.raw).replace(/\s+/g, " ").trim().slice(-1200);
    return `${session.label} closed=${session.closed} tail=${JSON.stringify(tail)}`;
  }).join("; ");
  throw new Error(`timed out waiting for ${description}${sessionState ? `; ${sessionState}` : ""}`);
}

async function waitForOutputSettled(session, quietMs = 500, maxWaitMs = 3_000) {
  const deadline = Date.now() + maxWaitMs;
  while (Date.now() < deadline) {
    const latestOutput = session.io.findLast(event => event.type === "output");
    if (latestOutput && Date.now() - latestOutput.t >= quietMs) return;
    await new Promise(resolve => setTimeout(resolve, 50));
  }
}

async function snapshotState() {
  const files = [];
  async function visit(dir) {
    let entries;
    try { entries = await readdir(dir, { withFileTypes: true }); } catch { return; }
    for (const entry of entries) {
      const path = join(dir, entry.name);
      if (entry.isDirectory()) await visit(path);
      else if (/modal-|bridge-state|diagnostics/.test(path)) {
        let content = "";
        try { content = (await readFile(path, "utf8")).slice(0, 4096); } catch {}
        content = content
          .replace(/("routeId"\s*:\s*")[^"]+(")/g, "$1%ROUTE%$2")
          .replace(/("routeProof"\s*:\s*")[^"]+(")/g, "$1%ROUTE_PROOF%$2");
        files.push({ path: path.replace(home, "%AFTERBURNER_HOME%"), content });
      }
    }
  }
  await visit(join(home, "state"));
  await visit(join(home, "extension-data"));
  return files;
}

function analyzeEvidenceState(files) {
  const joined = files.map(file => `${file.path}\n${file.content}`).join("\n---\n");
  return {
    hasRouteOwnedRecords: /%AFTERBURNER_HOME%.*routes/i.test(joined),
    hasBridgeOwnershipProof: /routeProof|bridge-state|openai-server/i.test(joined),
    hasBridgeStateMutation: /status refreshed|interactive menu open|auto-started|bridge ready/i.test(joined),
    hasDiagnosticsOrNoMalformedQueueLoss: !/schemaVersion"\s*:\s*1[^\n]+requestId/i.test(joined)
  };
}

function assertNoSessionPersistenceErrors(sessions) {
  const pattern = /Failed to persist session events|Cannot create a file when that file already exists/i;
  const offenders = sessions.filter(session => pattern.test(stripAnsi(session.raw))).map(session => session.label);
  if (offenders.length > 0) throw new Error(`session event persistence failure in ${offenders.join(", ")}`);
}

async function readAuditEntries() {
  const entries = [];
  for (const file of await readdir(auditDirectory).catch(() => [])) {
    if (!file.endsWith(".jsonl")) continue;
    const body = await readFile(join(auditDirectory, file), "utf8").catch(() => "");
    entries.push(...body.trim().split(/\r?\n/).filter(Boolean).map(line => JSON.parse(line)));
  }
  return entries;
}

function assertRouteAudit(entries, surfaceId, predicate = () => true) {
  const appends = entries.filter(entry => entry.operation === "queue-append" && entry.surfaceId === surfaceId && predicate(entry));
  if (appends.length < 1) throw new Error(`missing ${surfaceId} route IPC append audit`);
  for (const append of appends) {
    const requestEntries = entries.filter(entry => entry.requestId === append.requestId);
    const active = requestEntries.filter(entry => /-A$/.test(entry.ownerLabel ?? ""));
    const passive = requestEntries.filter(entry => /-B$/.test(entry.ownerLabel ?? ""));
    const operations = new Set(active.map(entry => entry.operation));
    for (const required of ["queue-append", "claim-new", "ack-write"]) {
      if (!operations.has(required)) throw new Error(`missing ${required} audit for ${surfaceId} request ${append.requestId}`);
    }
    if (passive.length !== 0) throw new Error(`passive session owned ${surfaceId} request ${append.requestId}`);
    if (requestEntries.some(entry => !/^[A-Za-z0-9_-]{16}$/.test(entry.routeHash ?? ""))) throw new Error(`invalid route hash audit for ${surfaceId}`);
  }
  return appends.map(entry => entry.requestId);
}

function analyzeAudit(entries) {
  const serialized = JSON.stringify(entries);
  if (/route[A-Z]{4,}/.test(serialized) || /AFTERBURNER_SESSION_ROUTE/.test(serialized)) throw new Error("route IPC audit leaked raw route material");
  const activeRoutes = new Set(entries.filter(entry => /-A$/.test(entry.ownerLabel ?? "")).map(entry => entry.routeHash));
  const passiveRoutes = new Set(entries.filter(entry => /-B$/.test(entry.ownerLabel ?? "")).map(entry => entry.routeHash));
  if (activeRoutes.size < 1 || passiveRoutes.size < 1) throw new Error("missing active/passive route binding audit");
  for (const route of activeRoutes) if (passiveRoutes.has(route)) throw new Error("active and passive sessions shared a route hash");
  const blackboxRequests = assertRouteAudit(entries, "afterburner-black-box-live");
  const openaiModalRequests = assertRouteAudit(entries, "openai-server", entry => !entry.action);
  const openaiActionRequests = assertRouteAudit(entries, "openai-server", entry => entry.action === "status");
  if (!entries.some(entry => entry.operation === "state-write" && /-A$/.test(entry.ownerLabel ?? ""))) throw new Error("missing active bridge state write audit");
  if (!entries.some(entry => entry.operation === "state-read" && /-A$/.test(entry.ownerLabel ?? ""))) throw new Error("missing active bridge state read audit");
  return { activeRoutes: activeRoutes.size, passiveRoutes: passiveRoutes.size, blackboxRequests, openaiModalRequests, openaiActionRequests };
}

function isReady(text) {
  return /\/ commands|\? help|tab next tab/i.test(text);
}

async function terminateSessions(list) {
  for (const session of list) {
    if (session.closed) continue;
    try { send(session, "\x1b", "close active modal before exit"); } catch {}
  }
  await new Promise(resolve => setTimeout(resolve, 250));
  for (const session of list) {
    if (session.closed) continue;
    try { send(session, "\x15/exit\r", "exit Copilot session"); } catch {}
  }
  let deadline = Date.now() + 5000;
  while (Date.now() < deadline && list.some(session => !session.closed)) {
    await new Promise(resolve => setTimeout(resolve, 50));
  }
  for (const session of list.filter(candidate => !candidate.closed)) {
    try { process.kill(session.child.pid); } catch {}
  }
  deadline = Date.now() + 1000;
  while (Date.now() < deadline && list.some(session => !session.closed)) {
    await new Promise(resolve => setTimeout(resolve, 50));
  }
}

async function bootstrapExperimentalProfile() {
  const session = launch("bootstrap");
  currentSessions.splice(0, currentSessions.length, session);
  try {
    await waitFor(() => {
      const text = stripAnsi(session.raw);
      return isReady(text) && (/Staff mode activated/i.test(text) || /activated.*black-box/i.test(text));
    }, "experimental profile bootstrap");
    await waitForOutputSettled(session, 1_000, 30_000);
    const launchHomes = join(home, "launch-homes");
    const launchHome = readdirSync(launchHomes, { withFileTypes: true })
      .filter(entry => entry.isDirectory())
      .map(entry => join(launchHomes, entry.name))
      .find(path => existsSync(join(path, "config.json")));
    if (!launchHome) throw new Error("experimental profile bootstrap did not produce config.json");
    mkdirSync(join(home, "copilot-home"), { recursive: true });
    cpSync(join(launchHome, "config.json"), join(home, "copilot-home", "config.json"));
  } finally {
    await terminateSessions([session]);
  }
}

async function runSimultaneousFakeModals() {
  const aTitle = "Afterburner Session A Modal";
  const bTitle = "Afterburner Session B Modal";
  const a = launch("simultaneous-A", { AFTERBURNER_TEST_MODAL: "1", AFTERBURNER_TEST_MODAL_ACTIONS: "1", AFTERBURNER_TEST_MODAL_TITLE: aTitle });
  const b = launch("simultaneous-B", { AFTERBURNER_TEST_MODAL: "1", AFTERBURNER_TEST_MODAL_ACTIONS: "1", AFTERBURNER_TEST_MODAL_TITLE: bTitle });
  currentSessions.splice(0, currentSessions.length, a, b);
  try {
    await waitFor(() => new RegExp(aTitle, "i").test(stripAnsi(a.raw)) && new RegExp(bTitle, "i").test(stripAnsi(b.raw)), "simultaneous modal open in both sessions", 20_000);
    if (new RegExp(bTitle, "i").test(stripAnsi(a.raw)) || new RegExp(aTitle, "i").test(stripAnsi(b.raw))) throw new Error("simultaneous modal titles crossed session boundaries");
    send(a, "r", "refresh action in simultaneous A");
    await waitFor(() => /refresh action observed/i.test(stripAnsi(a.raw)), "refresh action in simultaneous A", 10_000);
    if (new RegExp(bTitle, "i").test(stripAnsi(a.raw)) || new RegExp(aTitle, "i").test(stripAnsi(b.raw))) throw new Error("refresh action crossed session modal ownership");
    send(a, "d", "doctor action in simultaneous A");
    await waitFor(() => /Afterburner Black Box Doctor/i.test(stripAnsi(a.raw)), "doctor action in simultaneous A", 10_000);
    send(b, "r", "refresh action in simultaneous B");
    await waitFor(() => /refresh action observed/i.test(stripAnsi(b.raw)), "refresh action in simultaneous B", 10_000);
    send(b, "d", "doctor action in simultaneous B");
    await waitFor(() => /Afterburner Black Box Doctor/i.test(stripAnsi(b.raw)), "doctor action in simultaneous B", 10_000);
    if (new RegExp(bTitle, "i").test(stripAnsi(a.raw)) || new RegExp(aTitle, "i").test(stripAnsi(b.raw))) throw new Error("doctor action crossed session modal ownership");
    send(a, "q", "close simultaneous A action modal");
    await waitFor(() => /Afterburner Black Box Escape/i.test(stripAnsi(a.raw)), "simultaneous A escape-reopen", 10_000);
    send(a, "\x1b", "close simultaneous A escape modal");
    await waitFor(() => /copilot-after-modal/i.test(stripAnsi(a.raw)), "simultaneous A close", 10_000);
    if (!new RegExp(bTitle, "i").test(stripAnsi(b.raw)) && !/Afterburner Black Box Doctor/i.test(stripAnsi(b.raw))) throw new Error("closing simultaneous A disrupted simultaneous B modal");
    send(b, "q", "close simultaneous B action modal");
    await waitFor(() => /Afterburner Black Box Escape/i.test(stripAnsi(b.raw)), "simultaneous B escape-reopen", 10_000);
    send(b, "\x1b", "close simultaneous B escape modal");
    await waitFor(() => /copilot-after-modal/i.test(stripAnsi(b.raw)), "simultaneous B close", 10_000);
    return { a, b };
  } catch (error) {
    error.sessions = [a, b];
    throw error;
  }
}

async function runPair(name, activeExtraEnv, activePattern, passiveForbiddenPattern, action, actionPattern) {
  const a = launch(`${name}-A`, activeExtraEnv);
  const b = launch(`${name}-B`);
  currentSessions.splice(0, currentSessions.length, a, b);
  try {
    if (maybeFake) {
      await waitFor(() => activePattern.test(stripAnsi(a.raw)), `${name} modal in A`, 20_000);
    } else {
      await waitFor(() => /activated.*black-box/i.test(stripAnsi(a.raw)) && /activated.*black-box/i.test(stripAnsi(b.raw)), "both runtimes loaded extensions");
      await waitFor(() => isReady(stripAnsi(a.raw)) && isReady(stripAnsi(b.raw)), "both terminals ready after startup dialogs", 30_000);
      await Promise.all([
        waitForOutputSettled(a, 1_000, 30_000),
        waitForOutputSettled(b, 1_000, 30_000)
      ]);
      send(b, `\x15passive-${name}-before`, "passive negative control before");
      await waitFor(() => stripAnsi(b.raw).includes(`passive-${name}-before`), `${name} passive responsiveness before`, 10_000);
      send(b, "\x15", "clear passive input before");
      const command = `/${name === "blackbox" ? "black-box-modal" : "openai-server"}`;
      const commandDescription = name === "blackbox"
        ? /Open the registered Black Box live modal/i
        : /Open the interactive OpenAI Server management menu/i;
      let lastCommandProbe = 0;
      await waitFor(() => {
        if (commandDescription.test(stripAnsi(a.raw))) return true;
        if (Date.now() - lastCommandProbe >= 2_000) {
          send(a, `\x15${command}`, `probe ${name} command registration in A`);
          lastCommandProbe = Date.now();
        }
        return false;
      }, `${name} command registration`, 60_000);
      send(a, "\r", `open ${name} modal in A`);
      await waitFor(() => activePattern.test(stripAnsi(a.raw)), `${name} modal in A`, 45_000);
      await waitFor(async () => {
        const entries = await readAuditEntries();
        return entries.some(entry =>
          entry.operation === "ack-write" &&
          entry.surfaceId === (name === "blackbox" ? "afterburner-black-box-live" : "openai-server") &&
          new RegExp(`^${name}-A$`).test(entry.ownerLabel ?? "") &&
          !entry.action);
      }, `${name} modal acknowledgement in A`, 10_000);
    }
    let actionInputs = [];
    if (action) {
      actionInputs = Array.isArray(action) ? action : [action];
      if (!maybeFake) await waitForOutputSettled(a);
      for (const [index, input] of actionInputs.entries()) {
        const outputLength = a.raw.length;
        send(a, input, `${name} action ${index + 1}/${actionInputs.length} in A`);
        if (!maybeFake && index < actionInputs.length - 1) {
          await waitFor(() => a.raw.length > outputLength, `${name} action ${index + 1} repaint in A`, 3_000);
          await waitForOutputSettled(a);
        }
      }
    }
    if (actionPattern && !maybeFake) {
      await waitFor(() => actionPattern.test(stripAnsi(a.raw)), `${name} action output in A`, 20_000);
    }
    await new Promise(resolve => setTimeout(resolve, 1500));
    if (passiveForbiddenPattern.test(stripAnsi(b.raw))) throw new Error(`${name} modal rendered in passive session B`);
    if (!maybeFake) {
      send(b, `\x15passive-${name}-after`, "passive negative control after");
      await waitFor(() => stripAnsi(b.raw).includes(`passive-${name}-after`), `${name} passive responsiveness after`, 10_000);
      send(b, "\x15", "clear passive input after");
    }
    return { a, b };
  } catch (error) {
    error.sessions = [a, b];
    throw error;
  }
}

let sessions = [];
const allSessions = [];
const startedAt = Date.now();
let evidence;
try {
  if (!maybeFake) await bootstrapExperimentalProfile();
  if (maybeFake) {
    const simultaneous = await runSimultaneousFakeModals();
    sessions.push(simultaneous.a, simultaneous.b);
    allSessions.push(...sessions);
    await terminateSessions(sessions);
    sessions = [];
  }
  const blackbox = await runPair("blackbox", maybeFake ? { AFTERBURNER_TEST_MODAL: "1", AFTERBURNER_TEST_MODAL_TITLE: "Afterburner Black Box Live" } : {}, /Afterburner Black Box Live/i, /Afterburner Black Box Live/i, "\x1b");
  sessions.push(blackbox.a, blackbox.b);
  allSessions.push(...sessions);
  await terminateSessions(sessions);
  sessions = [];
  const openai = await runPair("openai", maybeFake ? { AFTERBURNER_TEST_MODAL: "1", AFTERBURNER_TEST_MODAL_TITLE: "OpenAI Server", AFTERBURNER_TEST_MODAL_ACTIONS: "1" } : {}, /OpenAI Server/i, /OpenAI Server/i, maybeFake ? "r" : ["\t", "\t", " "], /status refreshed|OpenAI server listening|OpenAI server is not running|requests=/i);
  sessions.push(openai.a, openai.b);
  allSessions.push(...sessions);
  assertNoSessionPersistenceErrors(allSessions);
  const state = await snapshotState();
  const machine = analyzeEvidenceState(state);
  const mode = maybeFake ? "native-fake-copilot" : "live-extension-commands";
  if (process.env.AFTERBURNER_REQUIRE_LIVE_EXTENSION_COMMANDS === "1" && mode !== "live-extension-commands") throw new Error("required release gate must use live-extension-commands mode");
  let audit = null;
  if (!maybeFake) {
    if (!machine.hasRouteOwnedRecords) throw new Error("missing route-owned IPC evidence");
    if (!machine.hasBridgeOwnershipProof) throw new Error("missing OpenAI bridge ownership evidence");
    if (!machine.hasBridgeStateMutation) throw new Error("missing OpenAI bridge state mutation evidence");
    audit = analyzeAudit(await readAuditEntries());
  }
  evidence = { ok: true, elapsedMs: Date.now() - startedAt, mode, assertions: ["live-extension-command-mode", "release-package-bytes-installed", "simultaneous-modal-session-isolation", "simultaneous-action-session-isolation", "black-box-active-only-request-ack", "openai-active-only-modal-action-ack", "openai-bridge-ownership-proof", "openai-bridge-state-mutation", "openai-action-output", "passive-responsive-before-after", "no-session-event-persistence-errors", "sanitized-route-ipc-audit"], machine, audit, packages: packageEvidence, state };
} catch (error) {
  if (Array.isArray(error.sessions)) {
    sessions.push(...error.sessions);
    allSessions.push(...error.sessions.filter(session => !allSessions.includes(session)));
  }
  evidence = { ok: false, elapsedMs: Date.now() - startedAt, mode: maybeFake ? "native-fake-copilot" : "live-extension-commands", error: String(error?.message ?? error), state: await snapshotState() };
} finally {
  await terminateSessions(sessions);
  for (const session of allSessions) {
    writeFileSync(join(captureDirectory, `${session.label}.ansi`), session.raw);
    writeFileSync(join(captureDirectory, `${session.label}.txt`), stripAnsi(session.raw));
    writeFileSync(join(captureDirectory, `${session.label}.io.jsonl`), session.io.map(event => JSON.stringify(event)).join("\n") + "\n");
  }
  writeFileSync(join(captureDirectory, "evidence.json"), JSON.stringify({ ...evidence, afterburn, fakeCopilot: maybeFake, home: "%temp%", capturedAt: new Date().toISOString() }, null, 2));
}

if (!evidence.ok) throw new Error(`multi-session ConPTY isolation failed; artifacts in ${captureDirectory}: ${evidence.error}`);
console.log(`multi-session ConPTY isolation passed; artifacts in ${captureDirectory}`);
setTimeout(() => process.exit(0), 500);
