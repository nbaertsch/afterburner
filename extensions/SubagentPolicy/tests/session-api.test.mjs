import assert from "node:assert/strict";
import test from "node:test";
import { availableModelIDs, updateSubagentSettings } from "../extensions/SubagentPolicy/session-api.mjs";

test("updates subagent settings through the current session RPC API", async () => {
    const calls = [];
    await updateSubagentSettings({
        rpc: {
            tools: {
                updateSubagentSettings(input) {
                    calls.push(input);
                }
            }
        }
    }, { maxConcurrency: 6 });
    assert.deepEqual(calls, [{ settings: { maxConcurrency: 6 } }]);
});

test("updates subagent settings through nested session tools", async () => {
    const calls = [];
    await updateSubagentSettings({
        tools: {
            updateSubagentSettings(input) {
                calls.push(input);
            }
        }
    }, { maxConcurrency: 4 });
    assert.deepEqual(calls, [{ settings: { maxConcurrency: 4 } }]);
});

test("updates subagent settings through the direct Copilot session API", async () => {
    const calls = [];
    await updateSubagentSettings({
        updateSubagentSettings(input) {
            calls.push(input);
        }
    }, null);
    assert.deepEqual(calls, [{ settings: null }]);
});

test("fails explicitly when Copilot exposes neither settings API", () => {
    assert.throws(() => updateSubagentSettings({}, null), /does not expose updateSubagentSettings/);
});

test("reads canonical IDs from the Copilot model catalog", async () => {
    const ids = await availableModelIDs({
        models: {
            async list() {
                return [{ provider: "colosseum-prod", id: "gpt-5-6-luna" }];
            }
        }
    });
    assert.equal(ids.has("colosseum-prod/gpt-5-6-luna"), true);
});

test("fails when model catalog validation is unavailable", async () => {
    await assert.rejects(availableModelIDs({}), /does not expose models\.list/);
});
