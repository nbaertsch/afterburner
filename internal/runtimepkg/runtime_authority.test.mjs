import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { createHash } from "node:crypto";
import { resolve } from "node:path";
import { Script, createContext } from "node:vm";
import test from "node:test";
import * as modalUI from "../../src/runtime/modal-ui.mjs";

async function loadRuntimeAuthority(stubs = {}) {
  const source = await readFile(new URL("../../src/app.js", import.meta.url), "utf8");
  const start = source.indexOf("function runtimeHostGrantResolver");
  const end = source.indexOf("async function loadRuntimeExtensions");
  assert.ok(start >= 0 && end > start, "runtime extension API block not found");
  const registrations = { observers: [], modals: [] };
  const context = createContext({
    modalUI,
    createHash,
    resolve,
    modalBlackBoxOwnerExtensionId: "black-box",
    modalBlackBoxLiveSurfaceId: "afterburner-black-box-live",
    modalReservedBlackBoxSurfaceIds: new Set(["black-box", "afterburner-black-box-live"]),
    trustedBuiltinSourceTypes: new Set(["embedded", "signed-release"]),
    runtime: {},
    safeJSONStringify: JSON.stringify.bind(JSON),
    safeJSONParse: JSON.parse.bind(JSON),
    registrations,
    registerModelPickerAdapter() {},
    registerAppSourceTransform() {},
    registerExternalTaskProvider() {},
    registerRuntimeObserver(definition, options) {
      registrations.observers.push({ definition, options });
      return () => {};
    },
    getRuntimeObserverDiagnostics(options) {
      return { options, observers: registrations.observers.map(entry => ({ id: entry.definition.id, ownerExtensionId: entry.options?.ownerExtensionId })) };
    },
    registerModalCanvas(definition, options) {
      registrations.modals.push({ definition, options });
      return { id: definition.id, ownerExtensionId: definition.ownerExtensionId };
    },
    openModalCanvas() {},
    updateModalCanvas() {},
    closeModalCanvas() {},
    invokeModalAction() {},
    subscribeModalCanvas() {},
    getModalDiagnostics() { return {}; },
    getModalFallback() { return null; },
    externalTaskSnapshot() { return []; },
    invokeExternalTask() {},
    contextCapabilityOverride() { return null; },
    ...stubs
  });
  new Script(`${source.slice(start, end)}\nObject.assign(globalThis, { runtimeExtensionApi, registrations });`, { filename: "runtime-authority.js" }).runInContext(context);
  return context;
}

async function installRuntimeGlobal() {
  const source = await readFile(new URL("../../src/app.js", import.meta.url), "utf8");
  const start = source.indexOf("Object.defineProperty(globalThis, \"__copilotRuntimeAddon__\"");
  const end = source.indexOf("await import(pathToFileURL(await transformedAppPath()).href);");
  assert.ok(start >= 0 && end > start, "runtime global block not found");
  const context = createContext({
    runtime: { native: true },
    getRuntimeObserverDiagnostics: () => ({ ok: true }),
    getModalDiagnostics: () => ({ ok: true })
  });
  new Script(source.slice(start, end), { filename: "runtime-global.js" }).runInContext(context);
  return context.__copilotRuntimeAddon__;
}

function extensionManifest(id, capabilities = []) {
  return { schemaVersion: 1, id, capabilities };
}

const blackBoxManifest = {
  schemaVersion: 1,
  id: "black-box",
  visibility: "builtin",
  capabilities: ["runtime-observer", "modal-canvas"]
};

function verifiedBlackBoxOptions(overrides = {}) {
  return {
    extensionId: "black-box",
    manifest: blackBoxManifest,
    manifestHash: "sha256:blackbox-manifest",
    packageTreeHash: "sha256:blackbox-tree",
    nativeIdentityAssertion: {
      extensionId: "black-box",
      activePath: "C:\\extensions\\BlackBox",
      manifestHash: "sha256:blackbox-manifest",
      treeHash: "sha256:blackbox-tree",
      sourceType: "embedded",
      sourceValue: "black-box",
      trustedBuiltin: true
    },
    registrySource: { type: "embedded", value: "black-box" },
    registryIdentity: {
      extensionId: "black-box",
      manifestHash: "sha256:blackbox-manifest",
      treeHash: "sha256:blackbox-tree",
      sourceType: "embedded",
      sourceValue: "black-box",
      signerId: "afterburner-core",
      signerFingerprint: "builtin:black-box",
      builtinSigned: true,
      registryEpoch: 1,
      grantEpoch: 1
    },
    ...overrides
  };
}

test("global runtime addon does not expose forgeable privileged authority", async () => {
  const addon = await installRuntimeGlobal();
  assert.equal(addon.addon.native, true);
  for (const key of [
    "createExtensionApi",
    "registerRuntimeObserver",
    "registerModalCanvas",
    "openModalCanvas",
    "updateModalCanvas",
    "closeModalCanvas",
    "invokeModalAction",
    "subscribeModalCanvas",
    "getModalFallback"
  ]) {
    assert.equal(addon[key], undefined, `${key} must not be public`);
  }
  assert.equal(typeof addon.diagnostics.getRuntimeObserverDiagnostics, "function");
});

test("runtimeExtensionApi exposes only modal document builders", async () => {
  const context = await loadRuntimeAuthority();
  const api = context.runtimeExtensionApi("C:\\extensions\\sample", { extensionId: "sample", manifest: extensionManifest("sample", ["modal-canvas"]) });
  assert.equal(typeof api.ui.createUIDocument, "function");
  assert.equal(typeof api.ui.components.dialog, "function");
  assert.equal(typeof api.ui.registerModalCanvas, "function");
  assert.equal(api.registerSurface, undefined);
  assert.equal(api.registerObservabilitySink, undefined);
});

test("runtimeExtensionApi binds modal ownership and blocks forged black-box owner", async () => {
  const context = await loadRuntimeAuthority();
  const api = context.runtimeExtensionApi("C:\\extensions\\attacker", {
    extensionId: "attacker",
    manifest: extensionManifest("attacker", ["modal-canvas"]),
    nativeIdentityAssertion: { extensionId: "attacker", activePath: "C:\\extensions\\attacker" }
  });
  const handle = api.registerModalCanvas({ id: "forged", ownerExtensionId: "black-box", open: () => ({ body: "bad" }) });
  assert.equal(handle.ownerExtensionId, "attacker");
  assert.equal(context.registrations.modals[0].definition.ownerExtensionId, "attacker");
  assert.equal(context.registrations.modals[0].options.ownerExtensionId, "attacker");
});

test("runtimeExtensionApi denies direct Black Box surface impersonation", async () => {
  const context = await loadRuntimeAuthority();
  const api = context.runtimeExtensionApi("C:\\extensions\\attacker", { extensionId: "attacker", manifest: extensionManifest("attacker", ["modal-canvas"]) });
  assert.throws(() => api.registerModalCanvas({ id: "afterburner-black-box-live", open: () => ({ body: "bad" }) }), /reserved for Black Box|authorization/i);
  assert.throws(() => api.registerModalCanvas({ id: "black-box", open: () => ({ body: "bad" }) }), /reserved for Black Box|authorization/i);
});

test("runtimeExtensionApi denies fake Black Box string identity", async () => {
  const context = await loadRuntimeAuthority();
  const api = context.runtimeExtensionApi("C:\\extensions\\BlackBox", { extensionId: "black-box", manifest: blackBoxManifest });
  assert.throws(() => api.registerModalCanvas({ id: "afterburner-black-box-live", open: () => ({ body: "bad" }) }), /reserved for Black Box|authorization|denied/i);
  assert.equal(api.registerRuntimeObserver, undefined);
});

test("runtimeExtensionApi allows verified embedded Black Box modal surfaces", async () => {
  const context = await loadRuntimeAuthority();
  const api = context.runtimeExtensionApi("C:\\extensions\\BlackBox", verifiedBlackBoxOptions());
  const live = api.registerModalCanvas({ id: "afterburner-black-box-live", open: () => ({ body: "ok" }) });
  const legacy = api.registerModalCanvas({ id: "black-box", open: () => ({ body: "legacy" }) });
  assert.equal(live.ownerExtensionId, "black-box");
  assert.equal(legacy.ownerExtensionId, "black-box");
});

test("runtimeExtensionApi cannot be reached through the public global", async () => {
  const addon = await installRuntimeGlobal();
  assert.equal(typeof addon.createExtensionApi, "undefined");
});

test("extensions without runtime-observer capability cannot subscribe", async () => {
  const context = await loadRuntimeAuthority();
  const api = context.runtimeExtensionApi("C:\\extensions\\no-observer", { extensionId: "no-observer", manifest: extensionManifest("no-observer", ["modal-canvas"]) });
  assert.equal(api.registerRuntimeObserver, undefined);
  assert.equal(api.getRuntimeObserverDiagnostics, undefined);
});

test("authorized Black Box receives owner-scoped runtime observer and modal APIs", async () => {
  const context = await loadRuntimeAuthority();
  const api = context.runtimeExtensionApi("C:\\extensions\\BlackBox", verifiedBlackBoxOptions());
  assert.equal(typeof api.registerRuntimeObserver, "function");
  const dispose = api.registerRuntimeObserver({ id: "black-box", onEvent() {} });
  assert.equal(typeof dispose, "function");
  assert.equal(context.registrations.observers[0].definition.id, "black-box");
  assert.equal(context.registrations.observers[0].options.ownerExtensionId, "black-box");
  assert.equal(typeof api.registerModalCanvas, "function");
  assert.equal(api.registerObservabilitySink, undefined);
});

test("host grant denial suppresses runtime observer API despite declaration", async () => {
  const context = await loadRuntimeAuthority({
    __afterburnerRuntimeGrantResolver: request => request.operation === "runtimeObserver" ? { allowed: false, reason: "host-denied" } : null
  });
  const api = context.runtimeExtensionApi("C:\\extensions\\blocked", { extensionId: "blocked", manifest: extensionManifest("blocked", ["runtime-observer"]) });
  assert.equal(api.registerRuntimeObserver, undefined);
});
