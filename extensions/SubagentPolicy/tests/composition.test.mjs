import assert from "node:assert/strict";
import { existsSync, readFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";

const packageRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..");

test("installed SubagentPolicy built-in aggregates BYOModels session registration", () => {
    const manifest = JSON.parse(readFileSync(join(packageRoot, "afterburner.json"), "utf8"));
    const plugin = JSON.parse(readFileSync(join(packageRoot, "plugin.json"), "utf8"));
    const entrypoint = join(packageRoot, ...manifest.sessionExtension.entrypoint.split("/"));
    const source = readFileSync(entrypoint, "utf8");

    assert.equal(manifest.id, "subagent-policy");
    assert.equal(manifest.visibility, "builtin");
    assert.equal(plugin.name, "afterburner-builtins");
    assert.ok(existsSync(entrypoint));
    assert.match(source, /BYOModels[\\/]extensions[\\/]BYOModels[\\/]extension\.mjs/);
    assert.ok(manifest.capabilities.includes("application-source-transform"));
});
