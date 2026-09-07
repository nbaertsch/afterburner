import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import * as ui from "./runtime/afterburner-ui.mjs";

const publicKinds = [
  "application", "window", "surface", "viewport", "stack", "column", "row", "grid", "box", "section", "split", "scroll", "disclosure", "statusGrid", "panel", "card", "separator", "spacer", "empty", "text", "markdown", "code", "icon", "badge", "keyValue", "detail", "alert", "button", "link", "textInput", "passwordInput", "searchInput", "numberInput", "textArea", "select", "checkbox", "radioGroup", "toggle", "slider", "dateInput", "fileInput", "progress", "meter", "bar", "sparkline", "spinner", "loading", "list", "table", "tree", "timeline", "log", "form", "toolbar", "actionBar", "contextMenu", "tabs", "breadcrumb", "pagination", "help", "dialog", "toast", "errorBoundary", "confirmation", "prompt", "terminal", "canvas", "image", "video", "chart", "commandPalette", "keybindingHint", "extensionOutlet"
];

const publicSurfaceKinds = ["terminal", "modal", "panel", "inline", "statusLine", "commandPalette", "overlay"];

const publicCapabilityKinds = [
  "ui.render.components", "ui.render.terminal", "ui.surface.terminal", "ui.surface.modal", "ui.surface.panel", "ui.surface.inline", "ui.surface.statusLine", "ui.surface.commandPalette", "ui.surface.overlay", "ui.action.invoke", "ui.data.read", "ui.data.write", "ui.stream.read", "ui.stream.write", "ui.theme.read", "ui.theme.write", "ui.localization.read", "ui.accessibility.inspect", "ui.policy.evaluate", "ui.audit.write", "ui.observability.sink", "ui.observability.black-box.sink"
];

test("runtime SDK bundled copy stays byte-for-byte synced with source", async () => {
  const bundled = await readFile(new URL("./runtime/afterburner-ui.mjs", import.meta.url), "utf8");
  const source = await readFile(new URL("../../src/runtime/afterburner-ui.mjs", import.meta.url), "utf8");
  assert.equal(bundled, source);
});

test("runtime SDK exposes every public component kind as a composable builder", async () => {
  assert.deepEqual(ui.componentKinds, publicKinds);
  assert.deepEqual(ui.componentCatalog.map(entry => entry.kind), publicKinds);
  assert.ok(Object.isFrozen(ui.componentCatalog));
  for (const entry of ui.componentCatalog) {
    assert.equal(entry.stability, "stable", `catalog stability for ${entry.kind}`);
    assert.equal(typeof entry.description, "string", `catalog description for ${entry.kind}`);
    assert.ok(entry.description.length > 0, `catalog description for ${entry.kind}`);
  }
  for (const kind of publicKinds) {
    assert.equal(typeof ui.components[kind], "function", `components.${kind}`);
    assert.equal(typeof ui[kind], "function", `named export ${kind}`);
    const id = `${kind.replace(/[A-Z]/g, letter => `-${letter.toLowerCase()}`)}-fixture`;
    const node = ui[kind]({ label: kind }, [], { id });
    assert.equal(node.kind, kind);
    assert.equal(node.id, id);
  }
});

test("runtime SDK component kinds stay aligned with JSON schema", async () => {
  const schema = JSON.parse(await readFile(new URL("../../schemas/ui-component-v1.schema.json", import.meta.url), "utf8"));
  assert.deepEqual(schema.$defs.kind.enum, publicKinds);
});

test("runtime SDK rejects unknown UI capabilities at descriptor and bridge boundaries", async () => {
  assert.throws(() => ui.validateSurfaceDescriptor({ id: "panel", kind: "pnael" }), /Unknown surface kind 'pnael'\. Did you mean 'panel'\?/);
  assert.throws(() => ui.validateSurfaceDescriptor({ id: "panel", kind: "panel", supportedComponents: ["text", "markdwon"] }), /Unknown supported component kind 'markdwon'\. Did you mean 'markdown'\?/);
  assert.throws(() => ui.validateSurfaceDescriptor({ id: "panel", kind: "panel", requiredCapabilities: ["ui.surface.pnael"] }), /Unknown required capability 'ui.surface.pnael'\. Did you mean 'ui.surface.panel'\?/);
  const runtime = ui.createRuntime();
  assert.throws(() => runtime.registerObservabilitySink({ id: "sink", capability: "ui.observability.balck-box.sink" }, () => {}), /Unknown observability capability 'ui.observability.balck-box.sink'\. Did you mean 'ui.observability.black-box.sink'\?/);
  const bridge = ui.createExtensionBridge({ ownerExtensionId: "author", denyByDefault: false });
  await assert.rejects(() => bridge.requestCapabilities([{ capability: "ui.surface.pnael" }]), /Unknown requested capability 'ui.surface.pnael'\. Did you mean 'ui.surface.panel'\?/);
  assert.throws(() => bridge.hasCapability("ui.surface.pnael"), /Unknown requested capability 'ui.surface.pnael'\. Did you mean 'ui.surface.panel'\?/);
});

test("runtime SDK surface catalog stays aligned with extension UI schema", async () => {
  const schema = JSON.parse(await readFile(new URL("../../schemas/extension-ui-v1.schema.json", import.meta.url), "utf8"));
  const schemaKinds = schema.properties.surfaces.items.properties.kind.enum;
  assert.deepEqual(ui.surfaceKinds, publicSurfaceKinds);
  assert.deepEqual(ui.surfaceCatalog.map(entry => entry.kind), publicSurfaceKinds);
  assert.deepEqual(schemaKinds, publicSurfaceKinds);
  assert.ok(Object.isFrozen(ui.surfaceCatalog));
  for (const entry of ui.surfaceCatalog) {
    assert.equal(entry.stability, "stable", `surface stability for ${entry.kind}`);
    assert.equal(typeof entry.description, "string", `surface description for ${entry.kind}`);
    assert.ok(entry.description.length > 0, `surface description for ${entry.kind}`);
  }
});

test("runtime SDK exposes public UI capability catalog metadata", async () => {
  assert.deepEqual(ui.capabilityKinds, publicCapabilityKinds);
  assert.deepEqual(ui.capabilityCatalog.map(entry => entry.id), publicCapabilityKinds);
  assert.ok(Object.isFrozen(ui.capabilityCatalog));
  for (const entry of ui.capabilityCatalog) {
    assert.equal(entry.stability, "stable", `capability stability for ${entry.id}`);
    assert.equal(typeof entry.description, "string", `capability description for ${entry.id}`);
    assert.ok(entry.description.length > 0, `capability description for ${entry.id}`);
  }
});
