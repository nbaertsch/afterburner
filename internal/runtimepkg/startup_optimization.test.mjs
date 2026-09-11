import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { Script, createContext } from "node:vm";
import test from "node:test";

async function loadStartupHelpers() {
  const source = await readFile(new URL("../../src/app.js", import.meta.url), "utf8");
  const start = source.indexOf("function runtimePhaseStartedAt");
  const end = source.indexOf("function parseJsonc");
  assert.ok(start >= 0 && end > start, "startup helper block not found");
  const events = [];
  const context = createContext({
    process,
    trustedBuiltinSourceTypes: new Set(["embedded", "signed-release"]),
    emitRuntimeEvent(type, metadata) {
      events.push({ type, metadata });
    },
    appSourceTransforms: []
  });
  new Script(`${source.slice(start, end)}
    Object.assign(globalThis, {
      runTimedRuntimePhase,
      activateRuntimeExtensionLanes,
      activateRuntimeExtensionGroups,
      immutableNativeRuntimeExtension,
      runtimeExtensionBlocksAppImport
    });`, { filename: "startup-helpers.js" }).runInContext(context);
  return { context, events };
}

async function loadOutputCacheHelper(initialSource) {
  const source = await readFile(new URL("../../src/app.js", import.meta.url), "utf8");
  const start = source.indexOf("async function writeRuntimeOutputIfChanged");
  const end = source.indexOf("installRuntimeObserverSeams();", start);
  assert.ok(start >= 0 && end > start, "runtime output cache helper not found");
  let storedSource = initialSource;
  let writes = 0;
  const context = createContext({
    async readFile() {
      if (storedSource === undefined) {
        const error = new Error("missing");
        error.code = "ENOENT";
        throw error;
      }

      return storedSource;
    },
    async writeFile(_path, value) {
      writes++;
      storedSource = value;
    }
  });

  new Script(`${source.slice(start, end)}
    globalThis.writeRuntimeOutputIfChanged = writeRuntimeOutputIfChanged;`,
  { filename: "runtime-output-cache.js" }).runInContext(context);
  return { context, writes: () => writes, source: () => storedSource };
}

async function loadPickerRegistrationHelpers() {
  const source = await readFile(new URL("../../src/app.js", import.meta.url), "utf8");
  const start = source.indexOf("function registerModelPickerAdapter");
  const end = source.indexOf("function registerAppSourceTransform", start);
  assert.ok(start >= 0 && end > start, "picker registration helper not found");
  const context = createContext({
    pickerAdapters: [],
    pickerAdapterOrders: new WeakMap(),
    pickerAdapterRegistrationSequence: 0,
    Number,
    emitRuntimeEvent() {},
    reportInstrumentationFailure() {},
    process: { env: {}, stderr: { write() {} } }
  });
  new Script(`${source.slice(start, end)}
    Object.assign(globalThis, { registerModelPickerAdapter });`,
  { filename: "picker-registration.js" }).runInContext(context);
  return context;
}

test("only trusted immutable non-transform extensions can leave the blocking startup lane", async () => {
  const { context } = await loadStartupHelpers();
  assert.equal(context.immutableNativeRuntimeExtension({
    trustedBuiltin: true,
    sourceType: "signed-release"
  }, { visibility: "builtin" }), true);
  assert.equal(context.immutableNativeRuntimeExtension({
    trustedBuiltin: false,
    sourceType: "signed-release"
  }, { visibility: "builtin" }), false);
  assert.equal(context.immutableNativeRuntimeExtension({
    trustedBuiltin: true,
    sourceType: "path"
  }, { visibility: "builtin" }), false);
  assert.equal(context.runtimeExtensionBlocksAppImport({
    capabilities: ["application-source-transform"]
  }), true);
  assert.equal(context.runtimeExtensionBlocksAppImport({
    capabilities: ["runtime-observer"]
  }), false);
});

test("runtime activation lanes overlap but remain serial within each extension kind", async () => {
  const { context } = await loadStartupHelpers();
  const order = [];
  let releaseCopilot;
  let releaseAfterburner;
  const copilotGate = new Promise(resolve => { releaseCopilot = resolve; });
  const afterburnerGate = new Promise(resolve => { releaseAfterburner = resolve; });
  const run = context.activateRuntimeExtensionLanes([
    async () => { order.push("copilot-1-start"); await copilotGate; order.push("copilot-1-end"); },
    async () => { order.push("copilot-2"); }
  ], [
    async () => { order.push("afterburner-1-start"); await afterburnerGate; order.push("afterburner-1-end"); },
    async () => { order.push("afterburner-2"); }
  ]);

  await new Promise(resolve => setImmediate(resolve));
  assert.deepEqual(order, ["copilot-1-start", "afterburner-1-start"]);
  releaseAfterburner();
  await new Promise(resolve => setImmediate(resolve));
  assert.deepEqual(order, ["copilot-1-start", "afterburner-1-start", "afterburner-1-end", "afterburner-2"]);
  releaseCopilot();
  await run;
  assert.deepEqual(order, [
    "copilot-1-start", "afterburner-1-start", "afterburner-1-end",
    "afterburner-2", "copilot-1-end", "copilot-2"
  ]);
});

test("trusted deferred activation does not block startup-critical activation", async () => {
  const { context } = await loadStartupHelpers();
  const order = [];
  let releaseDeferred;
  const deferredGate = new Promise(resolve => { releaseDeferred = resolve; });
  const { deferredCompletion } = await context.activateRuntimeExtensionGroups([], [
    async () => { order.push("blocking"); }
  ], [
    async () => { order.push("deferred-start"); await deferredGate; order.push("deferred-end"); }
  ]);
  assert.deepEqual(order, ["deferred-start", "blocking"]);
  releaseDeferred();
  await deferredCompletion;
  assert.deepEqual(order, ["deferred-start", "blocking", "deferred-end"]);
});

test("overlapping picker adapters retain declared precedence despite activation timing", async () => {
  const context = await loadPickerRegistrationHelpers();
  const firstDeclared = {
    matches: id => id === "overlap",
    upstreamModelId: () => "first"
  };
  const secondDeclared = {
    matches: id => id === "overlap",
    upstreamModelId: () => "second"
  };
  let releaseFirst;
  const firstGate = new Promise(resolve => { releaseFirst = resolve; });
  const firstActivation = (async () => {
    await firstGate;
    context.registerModelPickerAdapter(firstDeclared, 0);
  })();
  context.registerModelPickerAdapter(secondDeclared, 1);
  releaseFirst();
  await firstActivation;

  assert.equal(context.pickerAdapters[0].upstreamModelId("overlap"), "first");
  assert.equal(context.pickerAdapters[1].upstreamModelId("overlap"), "second");
});

test("timed runtime phases report completion and failure durations", async () => {
  const { context, events } = await loadStartupHelpers();
  assert.equal((await context.runTimedRuntimePhase("extension.import", { extensionId: "one" }, async () => 42)).result, 42);
  await assert.rejects(
    context.runTimedRuntimePhase("extension.activation", { extensionId: "two" }, async () => {
      throw new TypeError("failure");
    }),
    /failure/
  );
  assert.deepEqual(events.map(event => event.type), [
    "extension.import.started",
    "extension.import.completed",
    "extension.activation.started",
    "extension.activation.failed"
  ]);
  assert.ok(events.every(event => event.type.endsWith(".started") || Number.isFinite(event.metadata.durationMs)));
  assert.equal(events.at(-1).metadata.failureKind, "TypeError");
});

test("transformed app output is not rewritten when its bytes are unchanged", async () => {
  const cached = await loadOutputCacheHelper("same");
  assert.equal(await cached.context.writeRuntimeOutputIfChanged("output.mjs", "same"), false);
  assert.equal(cached.writes(), 0);

  assert.equal(await cached.context.writeRuntimeOutputIfChanged("output.mjs", "changed"), true);
  assert.equal(cached.writes(), 1);
  assert.equal(cached.source(), "changed");

  const missing = await loadOutputCacheHelper(undefined);
  assert.equal(await missing.context.writeRuntimeOutputIfChanged("output.mjs", "created"), true);
  assert.equal(missing.writes(), 1);
});
