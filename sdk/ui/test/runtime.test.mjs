import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { setTimeout as delay } from "node:timers/promises";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";
import {
  applyPatch,
  createExtensionBridge,
  createModalCanvasCompatibility,
  createRuntime,
  createSidecarHost,
  fallbackProjection,
  stack,
  text
} from "../dist/afterburner-ui.mjs";

const here = dirname(fileURLToPath(import.meta.url));

function enterpriseTestService(records = []) {
  return {
    async status() {
      return {
        enabled: true,
        mode: "runtime",
        storage: { maxBytes: 1024, segmentBytes: 128, segmentCount: 1, retentionBlockedBytes: 0 },
        queue: { records: 0, bytes: 0, droppedRecords: 0, writeErrors: 0 },
        analytics: { anomalyCount: 0, milestoneCount: 0, totalRecords: records.length },
        native: { enabled: false, configured: false },
        recentSignals: []
      };
    },
    async tail() { return records; },
    async doctor() { return { healthy: true }; },
    async exportBundle() { return { path: "C:\\private\\black-box.zip", manifest: { ok: true } }; },
    subscribeRecords() { return () => {}; },
    observed: [],
    async observeRuntime(event) { this.observed.push(event); return true; }
  };
}

async function loadBlackBoxManifest() {
  return JSON.parse(await readFile(new URL("../../../extensions/BlackBox/afterburner.json", import.meta.url), "utf8"));
}

function bridgeTestManifest(overrides = {}) {
  const { ui: uiOverrides = {}, ...manifestOverrides } = overrides;
  return {
    schemaVersion: 1,
    id: "bridge-test",
    ...manifestOverrides,
    ui: {
      protocol: "afterburner.ui",
      revision: 1,
      surfaces: [{ id: "bridge-panel", kind: "panel" }],
      capabilities: ["ui.render.components", "ui.surface.panel", "ui.data.read"],
      grantPolicy: {
        schemaVersion: 1,
        protocol: "afterburner.ui",
        revision: 1,
        extensionId: "bridge-test",
        denyByDefault: true,
        grants: [{ id: "bridge-panel", effect: "allow", capabilities: ["ui.render.components", "ui.surface.panel"], resources: ["bridge-panel"] }]
      },
      ...uiOverrides
    }
  };
}

test("runtime falls back deterministically when broker is unavailable", async () => {
  const runtime = createRuntime();
  const surface = runtime.defineSurface({
    id: "fallback-surface",
    kind: "modal",
    title: "Fallback Surface",
    render: () => stack({}, [text("line one"), text("line two")], { id: "fallback-root" })
  });
  const opened = await surface.open();
  assert.equal(opened.fallback, true);
  assert.match(opened.text, /line one/);
  assert.equal(surface.fallback().text, opened.text);
  assert.deepEqual(fallbackProjection(opened.document).text, opened.text);
  assert.equal(runtime.diagnostics().fallbackCount, 1);
});

test("extension bridge without an explicit host reports unavailable fallback", async () => {
  const api = createExtensionBridge({ manifest: bridgeTestManifest() });
  const registered = api.registerSurface({ id: "bridge-panel", kind: "panel", title: "Bridge Panel" });
  const opened = await registered.handle.open({}, { ownerExtensionId: "bridge-test" });
  assert.equal(api.diagnostics().interactive, false);
  assert.equal(opened.ok, false);
  assert.equal(opened.fallback, true);
  assert.match(opened.text, /Bridge Panel/);
});

test("extension bridge delivers through an explicit fake host", async () => {
  const delivered = [];
  const api = createExtensionBridge({
    manifest: bridgeTestManifest(),
    bridge: { interactive: true, render: async (id, document) => { delivered.push({ id, document }); return { ok: true }; } }
  });
  const registered = api.registerSurface({ id: "bridge-panel", kind: "panel", title: "Bridge Panel" });
  const opened = await registered.handle.open({}, { ownerExtensionId: "bridge-test" });
  assert.equal(api.diagnostics().interactive, true);
  assert.equal(opened.ok, true);
  assert.equal(opened.fallback, false);
  assert.equal(delivered.length, 1);
  assert.equal(delivered[0].id, "bridge-panel");
});

test("patch operations update local snapshots before delivery", async () => {
  const delivered = [];
  const runtime = createRuntime({ bridge: { interactive: true, render: async (_id, doc) => { delivered.push(doc); return { ok: true }; }, patch: async (_id, patch) => { delivered.push(patch); return { ok: true }; } } });
  const surface = runtime.defineSurface({ id: "patch-surface", kind: "panel", render: () => stack({}, [text("old", { id: "patch-text" })], { id: "patch-root" }) });
  const opened = await surface.open();
  const patched = await surface.patch({ surfaceId: "patch-surface", baseRevision: opened.document.revision, nextRevision: 2, operations: [{ op: "replace", path: "/root/children/0/props/value", value: "new" }] });
  assert.equal(patched.document.root.children[0].props.value, "new");
  assert.equal(delivered.at(-1).operations[0].op, "replace");
  assert.throws(() => applyPatch(opened.document, { surfaceId: "patch-surface", baseRevision: 99, nextRevision: 100, operations: [] }), /baseRevision/);
});

test("compatibility adapter preserves registerModalCanvas shape over UIDocument", async () => {
  const compatibility = createModalCanvasCompatibility();
  const handle = compatibility.registerModalCanvas({
    id: "compat-modal",
    displayName: "Compat Modal",
    actions: [{ name: "refresh", label: "Refresh", handler: async () => ({ ok: true }) }],
    open: async () => ({ body: "opened" })
  });
  const opened = await handle.open();
  assert.equal(opened.fallback, true);
  assert.equal(opened.document.surfaceId, "compat-modal");
  assert.match(opened.frame.body, /opened/);
  assert.deepEqual(await handle.invoke("refresh", {}), { ok: true });
  await handle.close();
  await handle.dispose();
});

test("authorized Black Box enterprise bridge registers and renders through real runtime API", async () => {
  const { registerEnterpriseSurface, subscribeObservability } = await import("../../../extensions/BlackBox/lib/ui-surface.mjs");
  const api = createExtensionBridge({
    ownerExtensionId: "black-box",
    manifest: await loadBlackBoxManifest(),
    bridge: { interactive: true, render: async () => ({ ok: true }), patch: async () => ({ ok: true }), close: async () => ({ ok: true }) }
  });
  const service = enterpriseTestService();
  const disposeObservability = await subscribeObservability(api, service);
  const enterprise = await registerEnterpriseSurface(api, service);
  assert.equal(enterprise.descriptorResult.ok, true);
  assert.equal(api.diagnostics().surfaces.includes("afterburner-black-box-live"), true);
  assert.equal(api.diagnostics().observabilitySinks.includes("black-box.ui.events"), true);

  const opened = await enterprise.open({});
  await new Promise(resolve => setTimeout(resolve, 0));
  assert.equal(opened.ok, true);
  assert.equal(opened.fallback, false);
  assert.equal(opened.document.surfaceId, "afterburner-black-box-live");
  assert.equal(service.observed.length > 0, true);
  assert.doesNotMatch(JSON.stringify(service.observed), /document|payload|SECRET/i);
  disposeObservability?.();
  await enterprise.dispose();
});

test("ungranted extension is denied by default for enterprise surfaces", () => {
  const api = createExtensionBridge({
    ownerExtensionId: "ungranted-extension",
    manifest: {
      schemaVersion: 1,
      id: "ungranted-extension",
      ui: {
        protocol: "afterburner.ui",
        revision: 1,
        surfaces: [{ id: "blocked-panel", kind: "panel" }],
        capabilities: ["ui.render.components", "ui.surface.panel"]
      }
    }
  });
  assert.throws(() => api.registerSurface({ id: "blocked-panel", kind: "panel", title: "Blocked" }), /denied|grant/i);
});

test("host grant resolver can deny an otherwise manifest-authorized surface", async () => {
  const manifest = await loadBlackBoxManifest();
  const api = createExtensionBridge({ ownerExtensionId: "black-box", manifest, grantResolver: () => ({ allowed: false, reason: "host-policy-denied" }) });
  assert.throws(() => api.registerSurface({ id: "afterburner-black-box-live", kind: "panel", title: "Denied" }), /denied/i);
});

test("manifest grants prefer matching deny over allow and keep surface scopes narrow", () => {
  const manifest = bridgeTestManifest({
    ui: {
      surfaces: [{ id: "allowed-panel", kind: "panel" }, { id: "blocked-panel", kind: "panel" }],
      capabilities: ["ui.render.components", "ui.surface.panel"],
      grantPolicy: {
        schemaVersion: 1,
        protocol: "afterburner.ui",
        revision: 1,
        extensionId: "bridge-test",
        denyByDefault: true,
        grants: [
          { id: "allow-both", effect: "allow", capabilities: ["ui.render.components", "ui.surface.panel"], resources: ["allowed-panel", "blocked-panel"] },
          { id: "deny-blocked-panel", effect: "deny", capabilities: ["ui.surface.panel"], resources: ["blocked-panel"] }
        ]
      }
    }
  });
  const api = createExtensionBridge({ manifest });
  assert.equal(api.registerSurface({ id: "allowed-panel", kind: "panel", title: "Allowed" }).ok, true);
  assert.throws(() => api.registerSurface({ id: "blocked-panel", kind: "panel", title: "Blocked" }), /denied/i);
  assert.equal(api.hasCapability("ui.surface.panel", "allowed-panel"), true);
  assert.equal(api.hasCapability("ui.surface.panel", "blocked-panel"), false);
  assert.equal(api.hasCapability("ui.surface.panel", "unlisted-panel"), false);
  assert.equal(api.hasCapability("ui.surface.panel"), false);
});

test("optional capability requests do not fail required grants", async () => {
  const api = createExtensionBridge({ manifest: bridgeTestManifest() });
  const optional = await api.requestCapabilities([
    { capability: "ui.render.components", resource: "bridge-panel" },
    { capability: "ui.data.read", resource: "bridge-panel", requirement: "optional" }
  ]);
  assert.equal(optional.granted, true);
  assert.deepEqual(optional.optionalDenied, [{ capability: "ui.data.read", resource: "bridge-panel", reason: "grant-denied" }]);
  const required = await api.requestCapabilities([{ capability: "ui.data.read", resource: "bridge-panel" }]);
  assert.equal(required.granted, false);
});

test("manifest denyByDefault modes control unmentioned grants", () => {
  const permissive = bridgeTestManifest({
    ui: { grantPolicy: { schemaVersion: 1, protocol: "afterburner.ui", revision: 1, extensionId: "bridge-test", denyByDefault: false, grants: [] } }
  });
  assert.equal(createExtensionBridge({ manifest: permissive }).registerSurface({ id: "bridge-panel", kind: "panel", title: "Allowed" }).ok, true);

  const restrictive = bridgeTestManifest({
    ui: { grantPolicy: { schemaVersion: 1, protocol: "afterburner.ui", revision: 1, extensionId: "bridge-test", denyByDefault: true, grants: [] } }
  });
  assert.throws(() => createExtensionBridge({ manifest: restrictive }).registerSurface({ id: "bridge-panel", kind: "panel", title: "Denied" }), /denied/i);
});

test("surface owner isolation denies cross-extension update close action and subscription", async () => {
  const runtime = createRuntime({ bridge: { interactive: true, render: async () => ({ ok: true }), close: async () => ({ ok: true }) } });
  const surface = runtime.defineSurface({
    id: "owned-panel",
    kind: "panel",
    ownerExtensionId: "owner-a",
    actions: [{ id: "refresh", title: "Refresh", handler: async () => ({ ok: true }) }],
    render: () => stack({}, [text("owned")], { id: "owned-root" })
  });
  await surface.open();

  await assert.rejects(() => runtime.update("owned-panel", stack({}, [text("bad")], { id: "bad-root" }), { ownerExtensionId: "owner-b" }), /owned by another extension|authorization/i);
  await assert.rejects(() => runtime.close("owned-panel", { ownerExtensionId: "owner-b" }), /owned by another extension|authorization/i);
  await assert.rejects(() => runtime.invoke("owned-panel", "refresh", {}, { ownerExtensionId: "owner-b" }), /owned by another extension|authorization/i);
  assert.throws(() => runtime.subscribe("owned-panel", () => {}, { ownerExtensionId: "owner-b" }), /owned by another extension|authorization/i);

  await surface.close();
});

test("observability sinks receive metadata-only UI observations", async () => {
  const observations = [];
  const runtime = createRuntime({ bridge: { interactive: true, render: async () => ({ ok: true }) } });
  runtime.registerObservabilitySink({
    descriptor: { id: "metadata-sink", extensionId: "observer-a", capability: "ui.observability.black-box.sink" },
    publish: async observation => observations.push(observation)
  });
  const surface = runtime.defineSurface({
    id: "observed-panel",
    kind: "panel",
    ownerExtensionId: "observer-a",
    render: () => stack({}, [text("SECRET BODY")], { id: "observed-root" })
  });

  await surface.open({ prompt: "PROMPT SECRET" });
  await new Promise(resolve => setTimeout(resolve, 0));
  assert.equal(observations.length > 0, true);
  assert.equal(observations[0].surfaceId, "observed-panel");
  assert.doesNotMatch(JSON.stringify(observations), /SECRET BODY|PROMPT SECRET|document|payload/i);
});

test("compatibility modal handles remain isolated by owner", async () => {
  const runtime = createRuntime({ bridge: { interactive: true, render: async () => ({ ok: true }), close: async () => ({ ok: true }) } });
  const ownerA = createModalCanvasCompatibility({ runtime, ownerExtensionId: "owner-a" });
  const ownerB = createModalCanvasCompatibility({ runtime, ownerExtensionId: "owner-b" });
  const handle = ownerA.registerModalCanvas({ id: "compat-owned", displayName: "Owned", open: () => ({ body: "owned" }) });
  await handle.open();

  assert.throws(() => ownerB.registerModalCanvas({ id: "compat-owned", displayName: "Other", open: () => ({ body: "bad" }) }), /owned by another extension|authorization/i);
  await assert.rejects(() => runtime.update("compat-owned", { body: "bad" }, { ownerExtensionId: "owner-b" }), /owned by another extension|authorization/i);
  await handle.dispose();
});

test("sidecar host restricts env, handles calls, bounds queues and reports crash", async () => {
  process.env.AFTERBURNER_UI_ALLOWED_TEST = "visible";
  process.env.AFTERBURNER_UI_BLOCKED_TEST = "hidden";
  const sidecar = createSidecarHost({ entrypoint: join(here, "sidecar-fixture.mjs"), envAllowList: ["AFTERBURNER_UI_ALLOWED_TEST"], queueLimit: 1, heartbeatMs: 50, heartbeatTimeoutMs: 500 });
  const response = await sidecar.call("echo", { ok: true });
  assert.deepEqual(response.payload, { ok: true });
  assert.ok(response.envKeys.includes("AFTERBURNER_UI_ALLOWED_TEST"));
  assert.equal(response.envKeys.includes("AFTERBURNER_UI_BLOCKED_TEST"), false);
  assert.equal(sidecar.policyInterface().restrictEnvironment, true);
  assert.equal(sidecar.policyInterface().enforceJobObject, false);
  await assert.rejects(() => Promise.all([sidecar.call("never", {}), sidecar.call("never2", {})]), /queue limit|Unknown sidecar method/);
  const crashed = new Promise((resolve) => sidecar.once("exit", resolve));
  await assert.rejects(() => sidecar.call("crash", {}), /sidecar exited|transport/);
  await crashed;
});

function isProcessRunning(pid) {
  try { process.kill(pid, 0); return true; } catch { return false; }
}

test("sidecar host fails closed when requested sandbox capabilities are unavailable", () => {
  const sidecar = createSidecarHost({ entrypoint: join(here, "sidecar-fixture.mjs"), isolationPolicy: { filesystem: { mode: "read", readRoots: [here] }, network: { mode: "deny" } } });
  assert.throws(() => sidecar.start(), /Sidecar isolation unavailable:.*filesystem\.read.*network\.deny/);
});

test("sidecar host stops its process tree", async (t) => {
  const sidecar = createSidecarHost({ entrypoint: join(here, "sidecar-fixture.mjs"), heartbeatMs: 50, heartbeatTimeoutMs: 500 });
  t.after(() => sidecar.stop("test-cleanup"));
  const { pid } = await sidecar.call("spawnChild", {});
  assert.equal(typeof pid, "number");
  assert.equal(isProcessRunning(pid), true);
  const exited = new Promise((resolve) => sidecar.once("exit", resolve));
  sidecar.stop("cleanup");
  await Promise.race([exited, delay(1000)]);
  for (let attempt = 0; attempt < 20 && isProcessRunning(pid); attempt++) await delay(100);
  assert.equal(isProcessRunning(pid), false);
});
