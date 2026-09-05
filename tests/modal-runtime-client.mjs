import assert from "node:assert/strict";
import { EventEmitter } from "node:events";
import { open, readFile, writeFile } from "node:fs/promises";
import { dirname, join } from "node:path";
import { pathToFileURL } from "node:url";
import { Script, createContext } from "node:vm";

function createPipeConnection(pipe) {
  const socket = new EventEmitter();
  let closed = false;
  let file = null;
  socket.setEncoding = () => {};
  socket.destroy = () => {
    if (closed) return;
    closed = true;
    file?.close().catch(() => {});
    queueMicrotask(() => socket.emit("close"));
  };
  socket.write = (data) => {
    (async () => {
      try {
        file = await open(pipe, "r+");
        await file.writeFile(data);
        const buffer = Buffer.alloc(4096);
        let response = "";
        while (!closed) {
          const { bytesRead } = await file.read(buffer, 0, buffer.length, null);
          if (bytesRead <= 0) break;
          response += buffer.toString("utf8", 0, bytesRead);
          if (response.includes("\n")) break;
        }
        if (!closed) socket.emit("data", response);
      } catch (error) {
        if (!closed) socket.emit("error", error);
      }
    })();
    return true;
  };
  queueMicrotask(() => socket.emit("connect"));
  return socket;
}

async function loadModalRuntime(appPath, brokerConfig) {
  const source = await readFile(appPath, "utf8");
  const afterburnerUI = await import(pathToFileURL(join(dirname(appPath), "runtime", "afterburner-ui.mjs")).href);
  const start = source.indexOf("function normalizeModalBootstrapSurfaces");
  const end = source.indexOf("function disposeRuntimeObservers");
  assert.ok(start >= 0 && end > start, "modal runtime block not found");
  const events = [];
  const context = createContext({
    console,
    afterburnerUI,
    process: { env: { ...process.env } },
    setTimeout,
    clearTimeout,
    queueMicrotask,
    createConnection: createPipeConnection,
    EventEmitter,
    events,
    __testModalBrokerConfig: brokerConfig,
    immutableRuntimeCopy: (value) => value,
    emitRuntimeEvent: (type, metadata = {}) => events.push({ type, metadata })
  });
  const prelude = `
const safeJSONParse = JSON.parse.bind(JSON);
const safeJSONStringify = JSON.stringify.bind(JSON);
const modalCanvasLimit = 16;
const modalActionLimit = 16;
const modalSubscriptionLimit = 32;
const modalTextLimit = 64 * 1024;
const modalBlackBoxOwnerExtensionId = "black-box";
const modalBlackBoxLiveSurfaceId = "afterburner-black-box-live";
const modalReservedBlackBoxSurfaceIds = new Set(["black-box", modalBlackBoxLiveSurfaceId]);
const trustedBuiltinSourceTypes = new Set(["embedded", "signed-release"]);
const modalCanvases = new Map();
const modalInstances = new Map();
const modalSubscribers = new Map();
const modalFallbacks = new Map();
const modalNativeSurfaces = new Map();
let modalInstanceSequence = 0;
let modalBrokerConfig = __testModalBrokerConfig;
const modalDiagnostics = {
  registered: 0,
  active: 0,
  opened: 0,
  updated: 0,
  closed: 0,
  actionInvocations: 0,
  subscriptionCount: 0,
  fallbackCount: 0,
  pipeFailures: 0,
  quotaFailures: 0,
  lastFailureKind: null
};`;
  const body = `${prelude}\n${source.slice(start, end)}\nObject.assign(globalThis, { registerModalCanvas, getModalDiagnostics });`;
  new Script(body, { filename: "prepared-modal-runtime.js" }).runInContext(context);
  return context;
}

async function main() {
  const appPath = process.env.AFTERBURNER_TEST_MODAL_RUNTIME_APP;
  const resultPath = process.env.AFTERBURNER_TEST_MODAL_JS_RESULT;
  assert.ok(appPath, "AFTERBURNER_TEST_MODAL_RUNTIME_APP is required");
  assert.ok(resultPath, "AFTERBURNER_TEST_MODAL_JS_RESULT is required");
  assert.ok(process.env.AFTERBURNER_MODAL_BOOTSTRAP, "AFTERBURNER_MODAL_BOOTSTRAP is required");
  assert.equal(process.env.AFTERBURNER_MODAL_PIPE, undefined, "AFTERBURNER_MODAL_PIPE must not be exposed");
  assert.equal(process.env.AFTERBURNER_MODAL_SECRET, undefined, "AFTERBURNER_MODAL_SECRET must not be exposed");
  const brokerConfig = JSON.parse(await readFile(process.env.AFTERBURNER_MODAL_BOOTSTRAP, "utf8"));
  assert.equal(brokerConfig.pipe, undefined, "bootstrap must not expose a reusable global modal pipe");
  assert.ok(brokerConfig.modalSurfaces?.some(surface => surface.ownerExtensionId === "black-box" && surface.surfaceId === "afterburner-black-box-live" && typeof surface.pipe === "string"));

  const runtime = await loadModalRuntime(appPath, brokerConfig);
  const handle = runtime.registerModalCanvas({
    id: "afterburner-black-box-live",
    displayName: "Afterburner Black Box Live",
    actions: [{ name: "close", label: "Close", key: "q", description: "Close modal" }],
    open: () => ({
      status: "runtime modal open",
      body: { markdown: "live metadata frame" },
      footer: "waiting for runtime close"
    })
  }, { ownerExtensionId: "black-box" });

  const opened = await handle.open();
  assert.notEqual(opened.error, "modal-invalid-request", `open sent an invalid modal request: ${JSON.stringify(opened)}`);
  assert.equal(opened.frame.document.surfaceId, "afterburner-black-box-live");
  const generation = handle.diagnostics().canvases.find((canvas) => canvas.id === "afterburner-black-box-live")?.generation;

  const updated = await handle.update({ body: { lines: ["live activity update frame"] }, footer: "updated footer" });
  assert.notEqual(updated.error, "modal-invalid-request", `update sent an invalid modal request: ${JSON.stringify(updated)}`);
  assert.equal(updated.frame.document.surfaceId, "afterburner-black-box-live");

  await handle.dispose();

  await writeFile(resultPath, JSON.stringify({
    runtimeClient: true,
    invalidRequest: false,
    opened: true,
    updated: true,
    event: { type: "closed", id: "afterburner-black-box-live", generation }
  }), "utf8");
  process.stdout.write("modal-js-through-broker-ok\n");
}

main().catch(async (error) => {
  const resultPath = process.env.AFTERBURNER_TEST_MODAL_JS_RESULT;
  if (resultPath) {
    await writeFile(resultPath, JSON.stringify({
      runtimeClient: true,
      invalidRequest: String(error?.message ?? error).includes("modal-invalid-request"),
      error: error?.stack ?? String(error)
    }), "utf8").catch(() => {});
  }
  console.error(error?.stack ?? String(error));
  process.exit(43);
});
