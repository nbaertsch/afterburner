import test from "node:test";
import assert from "node:assert/strict";
import { writeFileSync } from "node:fs";
import { spawnSync } from "node:child_process";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { readFileSync } from "node:fs";
import { installNativeSubagentPolicy } from "../runtime/extension.mjs";

const appPath = process.env.COPILOT_1084_APP_JS ??
    "C:\\Users\\nimbl\\AppData\\Local\\copilot\\pkg\\win32-x64\\1.0.84-4\\app.js";
const config = { policies: { "luna-three": {
    displayName: "Luna Three", description: "Luna routing", maxConcurrency: 3, maxDepth: 1,
    resultExposure: "summary", agents: { explore: { model: "luna" } }
} } };

test("patches exact Copilot 1.0.84-4 native /subagents anchors", () => {
    const transformed = installNativeSubagentPolicy(readFileSync(appPath, "utf8"), config);
    assert.match(transformed, /Policy presets/);
    assert.match(transformed, /"maxConcurrency":3/);
    assert.match(transformed, /"explore":\{"model":"luna"\}/);
    assert.match(transformed, /Configure default and per-agent subagent models/);
});

test("transformed Copilot source remains valid JavaScript", () => {
    const transformed = installNativeSubagentPolicy(readFileSync(appPath, "utf8"), config);
    const path = join(tmpdir(), `afterburn-subagent-policy-${process.pid}.mjs`);
    writeFileSync(path, transformed);
    const result = spawnSync(process.execPath, ["--check", path], { encoding: "utf8" });
    assert.equal(result.status, 0, result.stderr);
});

test("fails closed when either native anchor drifts", () => {
    const source = readFileSync(appPath, "utf8");
    assert.throws(() => installNativeSubagentPolicy(source.replace("if(t)return RD.default.createElement(sdn,{builtInAgents:", "if(t)return X("), config), /expected 1 anchor, found 0/);
    assert.throws(() => installNativeSubagentPolicy(source.replace("onCancel:()=>a(!1)});let se=re=>", "onCancel:()=>a(!1)});let changed=re=>"), config), /expected 1 anchor, found 0/);
});
