import assert from "node:assert/strict";
import test from "node:test";
import { activateModelRegistration } from "../extensions/BYOModels/extensions/BYOModels/activation.mjs";
import {
    capabilityCache,
    cachedRegistrations,
    immediateRegistrations,
    withDeadline
} from "../extensions/BYOModels/extensions/BYOModels/model-metadata.mjs";

const configured = [{
    provider: "provider",
    id: "model",
    name: "Model",
    modelId: "gpt-5.6-sol",
    wireModel: "deployment"
}];
const registration = {
    ...configured[0],
    maxPromptTokens: 128_000,
    maxContextWindowTokens: 256_000,
    maxOutputTokens: 32_000,
    capabilities: {
        supports: { vision: true, reasoningEffort: true }
    }
};
const runtime = [{
    selectionId: "provider/model",
    upstreamModelId: "gpt-5.6-sol",
    maxContextWindowTokens: 256_000,
    maxOutputTokens: 32_000,
    supportedReasoningEfforts: ["low", "high"],
    defaultReasoningEffort: "high"
}];

test("validated capability cache registers immediately", () => {
    const cache = capabilityCache(configured, [{ ...registration, unexpected: "discarded" }], runtime);
    assert.deepEqual(immediateRegistrations(configured, cache), [registration]);
    assert.deepEqual(cache.models, runtime);
});

test("cache is rejected after model configuration changes", () => {
    const cache = capabilityCache(configured, [registration], runtime);
    const changed = [{ ...configured[0], modelId: "gpt-5.6-luna" }];
    assert.deepEqual(cachedRegistrations(cache, changed), []);
    assert.deepEqual(immediateRegistrations(changed, cache), []);
});

test("complete configured metadata takes precedence over cache", () => {
    const cache = capabilityCache(configured, [registration], runtime);
    const explicit = {
        ...configured[0],
        maxPromptTokens: 64_000,
        maxContextWindowTokens: 128_000,
        maxOutputTokens: 16_000,
        capabilities: {
            supports: { vision: false, reasoningEffort: false }
        }
    };
    assert.deepEqual(immediateRegistrations([explicit], cache), [explicit]);
});

test("RPC deadline reports the timed out operation", async () => {
    await assert.rejects(
        withDeadline(new Promise(() => {}), 10, "capability discovery"),
        /capability discovery timed out after 10ms/
    );
});

test("cold-cache activation waits for live discovery and returns registered models", async () => {
    const registered = [];
    const reports = [];

    const activated = await activateModelRegistration({
        models: configured,
        cache: null,
        register: async (models) => registered.push(models),
        report: async (message) => reports.push(message),
        refresh: async (immediate) => {
            assert.deepEqual(immediate, []);
            await registered.push([registration]);
            return [registration];
        }
    });

    assert.deepEqual(activated, [registration]);
    assert.deepEqual(registered, [[registration]]);
    assert.match(reports[0], /Registered 0 BYOModels model\(s\) immediately/);
    assert.match(reports[0], /before extension activation returns/);
});

test("cached activation retains immediate models when live refresh fails", async () => {
    const cache = capabilityCache(configured, [registration], runtime);
    const registered = [];
    const reports = [];

    const activated = await activateModelRegistration({
        models: configured,
        cache,
        register: async (models) => registered.push(models),
        report: async (message) => reports.push(message),
        refresh: async (immediate) => {
            assert.deepEqual(immediate, [registration]);
            return immediate;
        }
    });

    assert.deepEqual(activated, [registration]);
    assert.deepEqual(registered, [[registration]]);
    assert.match(reports[0], /Registered 1 BYOModels model\(s\) immediately/);
});

test("activation does not resolve while live registration is still pending", async () => {
    let finishRefresh;
    const refreshGate = new Promise(resolve => {
        finishRefresh = resolve;
    });
    let resolved = false;
    const activation = activateModelRegistration({
        models: configured,
        cache: null,
        register: async () => assert.fail("cold cache has no immediate registrations"),
        report: async () => {},
        refresh: async () => {
            await refreshGate;
            return [registration];
        }
    }).then((value) => {
        resolved = true;
        return value;
    });

    await new Promise(resolve => setImmediate(resolve));
    assert.equal(resolved, false);
    finishRefresh();
    assert.deepEqual(await activation, [registration]);
    assert.equal(resolved, true);
});
