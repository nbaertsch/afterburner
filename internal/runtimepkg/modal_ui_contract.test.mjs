import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import {
  UI_PROTOCOL,
  componentKinds,
  components,
  createDocument,
  modalFrameToDocument,
  validateDocument
} from "../../src/runtime/modal-ui.mjs";

test("native UI exposes a finite component catalog", () => {
  assert.equal(UI_PROTOCOL, "afterburner.ui");
  assert.ok(componentKinds.includes("dialog"));
  assert.ok(componentKinds.includes("textInput"));
  assert.equal(components.notAComponent, undefined);
});

test("published document schema matches the runtime component catalog", async () => {
  const schema = JSON.parse(await readFile(new URL("../../schemas/ui-document-v1.schema.json", import.meta.url), "utf8"));
  assert.deepEqual(schema.$defs.node.properties.kind.enum, componentKinds);
  assert.equal(schema.properties.protocol.const, UI_PROTOCOL);
  assert.equal(schema.properties.schemaVersion.const, 1);
});

test("native UI validates stable interactive node identities", () => {
  assert.throws(() => createDocument(components.dialog({}, [
    components.button({ label: "Save" })
  ], { id: "root" }), { surfaceId: "settings" }), /stable id/);
  const document = createDocument(components.dialog({}, [
    components.button({ label: "Save" }, [], { id: "save" })
  ], { id: "root" }), { surfaceId: "settings" });
  assert.equal(document.protocol, "afterburner.ui");
  assert.equal(document.root.children[0].id, "save");
});

test("native UI rejects unknown kinds, duplicate ids, and terminal escapes", () => {
  const base = {
    schemaVersion: 1,
    protocol: "afterburner.ui",
    revision: 1,
    surfaceId: "settings",
    root: { id: "root", kind: "dialog", props: {}, children: [] }
  };
  assert.throws(() => validateDocument({
    ...base,
    root: {
      ...base.root,
      children: [{ id: "browser", kind: "browser", props: {}, children: [] }]
    }
  }), /unsupported component/);
  assert.throws(() => validateDocument({
    ...base,
    root: {
      ...base.root,
      children: [
        { id: "same", kind: "text", props: { value: "one" }, children: [] },
        { id: "same", kind: "text", props: { value: "two" }, children: [] }
      ]
    }
  }), /duplicate component id/);
  assert.throws(() => validateDocument({
    ...base,
    root: { ...base.root, props: { title: "\u001b[31munsafe" } }
  }), /escape sequences/);
});

test("legacy modal protocol normalizes to the native UI protocol", () => {
  const document = validateDocument({
    schemaVersion: 1,
    protocol: "afterburner.modal",
    revision: 2,
    surfaceId: "legacy",
    root: { id: "legacy-root", kind: "dialog", props: {}, children: [] }
  });
  assert.equal(document.protocol, "afterburner.ui");
  assert.equal(document.revision, 2);
});

test("component builders omit undefined optional properties", () => {
  const node = components.button({ label: "Save", keybinding: undefined }, [], {
    id: "save",
    actionBindings: { activate: "save", change: undefined }
  });
  assert.deepEqual(node.props, { label: "Save" });
  assert.deepEqual(node.actionBindings, { activate: "save" });
});

test("fallback documents support maximum-length surface ids", () => {
  const surfaceId = "a".repeat(64);
  const document = modalFrameToDocument(
    { id: surfaceId, displayName: "Maximum surface" },
    { body: "content" }
  );
  assert.equal(document.surfaceId, surfaceId);
  assert.equal(document.root.id, "modal-root");
});
