import assert from "node:assert/strict";
import test from "node:test";
import { loadPolicyConfig, sdkSettings } from "../extensions/SubagentPolicy/policy.mjs";

test("loads documented built-in policies", () => {
    const config = loadPolicyConfig();
    assert.equal(config.policies.balanced.maxConcurrency, 4);
    assert.equal(config.policies.burst.maxConcurrency, 6);
    assert.equal(config.defaultPolicy, null);
});

test("validates and projects per-agent settings", () => {
    const config = loadPolicyConfig({
        version: 1,
        defaultPolicy: "workers",
        policies: {
            workers: {
                displayName: "Workers",
                description: "Dense workers",
                maxConcurrency: 4,
                maxDepth: 1,
                resultExposure: "status-and-final",
                disabledSubagents: ["research"],
                agentFactories: {
                    maxConcurrentSubagents: 4,
                    maxTotalSubagents: 12
                },
                agents: {
                    explore: {
                        model: "doi/qwen38-blackfrost",
                        modelPolicy: "required",
                        effortLevel: "low",
                        contextTier: "default",
                        autoInvoke: true
                    }
                }
            }
        }
    });
    assert.deepEqual(sdkSettings(config.policies.workers), {
        agents: {
            explore: {
                model: "doi/qwen38-blackfrost",
                modelPolicy: "required",
                effortLevel: "low",
                contextTier: "default",
                autoInvoke: true
            }
        },
        disabledSubagents: ["research"],
        maxConcurrency: 4,
        maxDepth: 1
    });
    assert.deepEqual(config.policies.workers.agentFactories, {
        maxConcurrentSubagents: 4,
        maxTotalSubagents: 12
    });
});

test("rejects invalid policy atomically", () => {
    assert.throws(() => loadPolicyConfig({
        policies: {
            broken: {
                displayName: "Broken",
                description: "Broken",
                maxConcurrency: 0,
                maxDepth: 1,
                resultExposure: "detailed"
            }
        }
    }), /maxConcurrency/);
});

test("rejects duplicate disabled agents and unknown fields", () => {
    const base = {
        displayName: "Custom",
        description: "Custom",
        maxConcurrency: 2,
        maxDepth: 1,
        resultExposure: "final-only"
    };
    assert.throws(() => loadPolicyConfig({ policies: { custom: { ...base, disabledSubagents: ["task", "task"] } } }), /duplicates/);
    assert.throws(() => loadPolicyConfig({ policies: { custom: { ...base, mystery: true } } }), /not supported/);
});
