import assert from "node:assert/strict";
import test from "node:test";
import { menuActions, modalFrame } from "../extensions/SubagentPolicy/menu.mjs";

test("native menu exposes documented actions", () => {
    assert.deepEqual(menuActions.map(action => action.key), ["1", "2", "3", "4", "r", "x", "q"]);
});

test("modal frame exposes active policy and limits", () => {
    const frame = modalFrame({
        activePolicy: "balanced",
        defaultPolicy: "conservative",
        configPath: "C:\\policy.json",
        policies: {
            balanced: {
                displayName: "Balanced",
                maxConcurrency: 4,
                maxDepth: 1,
                resultExposure: "status-and-final"
            }
        }
    }, "applied");
    assert.match(frame.status, /Active: balanced/);
    assert.match(frame.body, /concurrency 4, depth 1/);
    assert.match(frame.body, /C:\\policy\.json/);
});
