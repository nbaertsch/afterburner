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

test("activation resolves before a blocked live model refresh starts", async () => {
    const cache = capabilityCache(configured, [registration], runtime);
    const scheduled = [];
    const registered = [];
    const reports = [];
    let modelListCalls = 0;
    const blockedModelList = new Promise(() => {});

    const activated = activateModelRegistration({
        models: configured,
        cache,
        register: async (models) => registered.push(models),
        report: async (message) => reports.push(message),
        schedule: (callback) => scheduled.push(callback),
        refresh: async () => {
            modelListCalls++;
            await blockedModelList;
        }
    });

    assert.deepEqual(await activated, [registration]);
    assert.deepEqual(registered, [[registration]]);
    assert.match(reports[0], /Registered 1 BYOModels model\(s\) immediately/);
    assert.equal(modelListCalls, 0);
    assert.equal(scheduled.length, 1);

    scheduled[0]();
    await new Promise(resolve => setImmediate(resolve));
    assert.equal(modelListCalls, 1);
});

test("default detached scheduler runs refresh after activation resolution", async () => {
    let activationResolved = false;
    let refreshObservedResolution = false;
    let refreshStarted;
    const started = new Promise(resolve => {
        refreshStarted = resolve;
    });

    await activateModelRegistration({
        models: [],
        cache: null,
        register: async () => assert.fail("no models should be registered"),
        report: async () => {},
        refresh: async () => {
            refreshObservedResolution = activationResolved;
            refreshStarted();
            await new Promise(() => {});
        }
    });
    activationResolved = true;

    const keepAlive = setTimeout(() => assert.fail("detached refresh did not start"), 1_000);
    await started.finally(() => clearTimeout(keepAlive));
    assert.equal(refreshObservedResolution, true);
});
