import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import * as ui from "./runtime/afterburner-ui.mjs";

const publicKinds = [
  "application", "window", "surface", "viewport", "stack", "column", "row", "grid", "box", "section", "split", "scroll", "disclosure", "statusGrid", "panel", "card", "separator", "spacer", "empty", "text", "markdown", "code", "icon", "badge", "keyValue", "detail", "alert", "button", "link", "textInput", "passwordInput", "searchInput", "numberInput", "textArea", "select", "checkbox", "radioGroup", "toggle", "slider", "dateInput", "fileInput", "progress", "meter", "bar", "sparkline", "spinner", "loading", "list", "table", "tree", "timeline", "log", "form", "toolbar", "actionBar", "contextMenu", "tabs", "breadcrumb", "pagination", "help", "dialog", "toast", "errorBoundary", "confirmation", "prompt", "terminal", "canvas", "image", "video", "chart", "commandPalette", "keybindingHint", "extensionOutlet"
];

test("runtime SDK exposes every public component kind as a composable builder", async () => {
  assert.deepEqual(ui.componentKinds, publicKinds);
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
