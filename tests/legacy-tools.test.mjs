import assert from "node:assert/strict";
import test from "node:test";
import { flattenToolSchema, rewriteLegacyTools } from "../extensions/BYOModels/extensions/BYOModels/legacy-tools.mjs";

function nestedSchema() {
  let schema = { type: "string", enum: ["accepted"], description: "Keep this constraint." };
  for (let index = 0; index < 9; index++) {
    schema = { type: "object", properties: { child: schema }, required: ["child"], additionalProperties: false };
  }
  return schema;
}

const depth = value => !value || typeof value !== "object" ? 0 :
  1 + Math.max(0, ...Object.values(value).map(depth));
function resolve(root, schema) {
  while (schema.$ref) {
    schema = schema.$ref.slice(2).split("/").reduce((value, key) =>
      value[key.replace(/~1/g, "/").replace(/~0/g, "~")], root);
  }
  return schema;
}

test("deep schemas use shallow references without losing validation constraints", () => {
  const original = nestedSchema();
  const snapshot = structuredClone(original);
  const result = flattenToolSchema(original);
  assert.deepEqual(original, snapshot);
  assert.ok(depth(result) <= 10);
  let node = result;
  for (let index = 0; index < 9; index++) {
    node = resolve(result, node);
    assert.equal(node.type, "object");
    assert.equal(node.additionalProperties, false);
    assert.deepEqual(node.required, ["child"]);
    node = node.properties.child;
  }
  assert.deepEqual(resolve(result, node), { type: "string", enum: ["accepted"], description: "Keep this constraint." });
});

test("relocation repairs escaped local pointers and avoids existing definition names", () => {
  const original = nestedSchema();
  original.$defs = { afterburner_schema_1: { type: "number" } };
  original.properties["a/b~c"] = { type: "integer", minimum: 7 };
  original.properties.alias = { $ref: "#/properties/a~1b~0c" };
  const result = flattenToolSchema(original);
  assert.deepEqual(resolve(result, result.properties.alias), { type: "integer", minimum: 7 });
  assert.deepEqual(resolve(result, result.$defs.afterburner_schema_1), { type: "number" });
});

test("legacy tools retain callable names, eagerly expose namespaces, and translate history and choices", () => {
  const payload = {
    tools: [
      { type: "function", name: "shell", parameters: nestedSchema() },
      { type: "namespace", name: "mcp", tools: [
        { type: "function", name: "mcp_lookup", defer_loading: true, parameters: { type: "object" } }
      ] },
      { type: "tool_search" }
    ],
    input: [
      { type: "tool_search_call", id: "search" },
      { type: "tool_search_output", call_id: "search" },
      { type: "function_call", namespace: "mcp", name: "mcp_lookup", call_id: "call", arguments: "{}" },
      { type: "function_call_output", call_id: "call", output: "found" }
    ],
    tool_choice: { type: "allowed_tools", mode: "auto", tools: [{ type: "function", name: "mcp_lookup", namespace: "mcp" }] }
  };
  rewriteLegacyTools(payload);
  assert.deepEqual(payload.tools.map(tool => tool.name), ["shell", "mcp_lookup"]);
  assert.equal(payload.tools[1].defer_loading, undefined);
  assert.deepEqual(payload.input.map(item => item.type), ["function_call", "function_call_output"]);
  assert.equal(payload.input[0].namespace, undefined);
  assert.equal(payload.input[0].call_id, "call");
  assert.equal(payload.tool_choice.tools[0].namespace, undefined);
  assert.ok(depth(payload) <= 16);
});

test("unsupported rewrites fail explicitly instead of weakening schemas or misrouting tools", () => {
  assert.throws(() => rewriteLegacyTools({ tools: [
    { type: "function", name: "same" },
    { type: "namespace", tools: [{ type: "function", name: "same" }] }
  ] }), /duplicate function/);
  assert.throws(() => rewriteLegacyTools({ tool_choice: { type: "tool_search" } }), /forcing/);
  const schema = nestedSchema();
  schema.properties.child.$id = "https://example.test/schema";
  assert.throws(() => flattenToolSchema(schema), /own \$id/);
  assert.throws(() => rewriteLegacyTools({ metadata: nestedSchema() }), /depth limit/);
});

test("shallow schemas including defaults and references are unchanged", () => {
  const schema = { type: "object", properties: { value: { $ref: "#/$defs/item" } }, $defs: { item: { type: "string" } } };
  assert.equal(flattenToolSchema(schema), schema);
});
