import assert from "node:assert/strict";
import test from "node:test";
import {
    catalogModelIDs,
    loadPolicyConfig,
    sdkSettings,
    validatePolicyModelAvailability
} from "../extensions/SubagentPolicy/policy.mjs";

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
        maxDepth: 1,
        resultExposure: "status-and-final"
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

test("accepts native and provider-qualified model identities", () => {
    const policy = {
        displayName: "Custom",
        description: "Custom",
        maxConcurrency: 2,
        maxDepth: 1,
        resultExposure: "final-only",
        agents: { explore: { model: "gpt-5.6-luna", modelPolicy: "required" } }
    };
    const native = loadPolicyConfig({ policies: { custom: policy } });
    assert.equal(native.policies.custom.agents.explore.model, "gpt-5.6-luna");
    const providerQualified = loadPolicyConfig({ policies: { custom: {
        ...policy,
        agents: { explore: { model: "doi/qwen38-turbo-fable", modelPolicy: "required" } }
    } } });
    assert.equal(providerQualified.policies.custom.agents.explore.model, "doi/qwen38-turbo-fable");
    assert.throws(() => loadPolicyConfig({ policies: { custom: {
        ...policy,
        agents: { explore: { model: "bad model", modelPolicy: "required" } }
    } } }), /Copilot model identity/);
});

test("derives authoritative catalog IDs and fails closed for unavailable models", () => {
    const models = [
        { id: "gpt-5.6-sol" },
        { id: "doi/qwen38-turbo-fable", selectionId: "doi/qwen38-turbo-fable" },
        { provider: "provider", providerModelId: "model" }
    ];
    assert.deepEqual([...catalogModelIDs(models)].sort(), [
        "doi/qwen38-turbo-fable",
        "gpt-5.6-sol",
        "provider/model"
    ]);
    const available = {
        agents: {
            explore: { model: "gpt-5.6-sol" },
            task: { model: "doi/qwen38-turbo-fable" }
        }
    };
    assert.deepEqual(validatePolicyModelAvailability(available, models), [
        "gpt-5.6-sol",
        "doi/qwen38-turbo-fable"
    ]);
    assert.throws(
        () => validatePolicyModelAvailability({
            agents: { explore: { model: "colosseum-prod/gpt-5-6-luna" } }
        }, models),
        /unavailable Copilot model ID.*colosseum-prod\/gpt-5-6-luna/
    );
    assert.throws(() => validatePolicyModelAvailability(available, null), /catalog is unavailable/);
});
