import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import {
  application,
  button,
  code,
  capabilityCatalog,
  capabilityKinds,
  componentCatalog,
  componentKinds,
  components,
  createNode,
  createUIDocument,
  markdown,
  modalFrameToUIDocument,
  stack,
  surfaceCatalog,
  surfaceKinds,
  text,
  validateSurfaceDescriptor,
  validateUIDocument
} from "../dist/afterburner-ui.mjs";

test("packaged dist stays synced with source runtime SDK", async () => {
  const dist = await readFile(new URL("../dist/afterburner-ui.mjs", import.meta.url), "utf8");
  const source = await readFile(new URL("../../../src/runtime/afterburner-ui.mjs", import.meta.url), "utf8");
  assert.equal(dist, source);
});

test("packaged catalogs expose components, surfaces, and capabilities", async () => {
  const componentSchema = JSON.parse(await readFile(new URL("../../../schemas/ui-component-v1.schema.json", import.meta.url), "utf8"));
  const extensionUISchema = JSON.parse(await readFile(new URL("../../../schemas/extension-ui-v1.schema.json", import.meta.url), "utf8"));
  assert.deepEqual(componentKinds, componentSchema.$defs.kind.enum);
  assert.deepEqual(componentCatalog.map(entry => entry.kind), componentKinds);
  assert.deepEqual(surfaceCatalog.map(entry => entry.kind), surfaceKinds);
  assert.deepEqual(surfaceKinds, extensionUISchema.properties.surfaces.items.properties.kind.enum);
  assert.ok(capabilityKinds.includes("ui.observability.black-box.sink"));
  assert.deepEqual(capabilityCatalog.map(entry => entry.id), capabilityKinds);
  for (const entry of [...componentCatalog, ...surfaceCatalog, ...capabilityCatalog]) {
    assert.equal(entry.stability, "stable");
    assert.equal(typeof entry.description, "string");
    assert.ok(entry.description.length > 0);
  }
});

test("TypeScript declarations cover public SDK catalogs and builders", async () => {
  const declarations = await readFile(new URL("../index.d.ts", import.meta.url), "utf8");
  for (const name of ["componentCatalog", "surfaceCatalog", "capabilityKinds", "capabilityCatalog"]) {
    assert.match(declarations, new RegExp(`export const ${name}:`), `${name} declaration missing`);
  }
  for (const kind of componentKinds) {
    assert.match(declarations, new RegExp(`export const ${kind}:`), `${kind} builder declaration missing`);
  }
});

test("surface descriptors reject unknown supported component kinds and required capabilities", () => {
  assert.throws(() => validateSurfaceDescriptor({ id: "panel", kind: "panel", supportedComponents: ["text", "madeUpWidget"] }), /Unknown supported component kind 'madeUpWidget'/);
  assert.throws(() => validateSurfaceDescriptor({ id: "panel", kind: "panel", requiredCapabilities: ["ui.surface.pnael"] }), /Unknown required capability 'ui.surface.pnael'/);
});

test("builders cover the full W0 component catalog and produce stable IDs", () => {
  for (const kind of componentKinds) assert.equal(typeof components[kind], "function", `${kind} builder missing`);
  const first = stack({ gap: "s" }, [text("Hello"), button({ label: "Go", actionId: "go" })], { key: "body" });
  const second = stack({ gap: "s" }, [text("Hello"), button({ label: "Go", actionId: "go" })], { key: "body" });
  assert.equal(first.id, second.id);
  assert.equal(first.children[0].kind, "text");
  assert.equal(first.children[1].props.actionId, "go");
  assert.throws(() => createNode("bogus"), /Unknown component kind/);
});

test("documents and local schema validation enforce IDs, revisions and trees", () => {
  const root = application({ title: "App" }, [markdown("**ready**"), code({ language: "js", code: "1 + 1" })], { id: "app-root" });
  const doc = createUIDocument(root, { surfaceId: "surface-main", revision: 7, locale: "en-US" });
  assert.equal(validateUIDocument(doc), doc);
  assert.equal(doc.root.children[0].props.markdown, "**ready**");
  assert.throws(() => createUIDocument({ id: "Bad", kind: "text" }, { surfaceId: "surface" }), /stable lowercase id/);
  assert.throws(() => validateUIDocument({ root, revision: -1, surfaceId: "surface-main" }), /revision/);
});

test("ModalFrame compatibility maps deterministically to UIDocument", () => {
  const doc = modalFrameToUIDocument({ id: "black-box-live", displayName: "Black Box", actions: [{ name: "refresh", label: "Refresh", key: "r" }] }, {
    status: "healthy",
    body: { lines: ["one", "two"] },
    footer: "done"
  });
  assert.equal(doc.surfaceId, "black-box-live");
  assert.equal(doc.root.kind, "dialog");
  assert.equal(doc.root.props.title, "Black Box");
  assert.match(JSON.stringify(doc), /Refresh/);
  const again = modalFrameToUIDocument({ id: "black-box-live", displayName: "Black Box", actions: [{ name: "refresh", label: "Refresh", key: "r" }] }, { status: "healthy", body: { lines: ["one", "two"] }, footer: "done" });
  assert.deepEqual(doc, again);
});
