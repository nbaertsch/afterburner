import assert from "node:assert/strict";
import { EventEmitter } from "node:events";
import { readFile } from "node:fs/promises";
import { resolve } from "node:path";
import { Script, createContext } from "node:vm";
import test from "node:test";

async function loadModalRuntime(transport, brokerConfig = null) {
  const source = await readFile(new URL("../../src/app.js", import.meta.url), "utf8");
  const modalUI = await import(new URL("../../src/runtime/modal-ui.mjs", import.meta.url));
  const start = source.indexOf("function normalizeModalBootstrapSurfaces");
  const end = source.indexOf("function disposeRuntimeObservers");
  assert.ok(start >= 0 && end > start, "modal runtime block not found");
  const events = [];
  const context = createContext({
    console,
    JSON: { parse: JSON.parse, stringify: JSON.stringify },
    modalUI,
    process: { env: {} },
    setTimeout,
    clearTimeout,
    queueMicrotask,
    resolve,
    events,
    __testModalBrokerConfig: brokerConfig,
    createConnection: () => {
      const socket = new EventEmitter();
      socket.setEncoding = () => {};
      socket.destroy = () => {};
      socket.write = async (data) => {
        const message = JSON.parse(String(data).trim());
        try {
          const response = await transport(message);
          socket.emit("data", `${JSON.stringify(response)}\n`);
        } catch (error) {
          socket.emit("error", error);
        }
      };
      queueMicrotask(() => socket.emit("connect"));
      return socket;
    },
    immutableRuntimeCopy: (value) => value,
    emitRuntimeEvent: (type, metadata = {}) => events.push({ type, metadata })
  });
  const prelude = `
const safeJSONParse = JSON.parse.bind(JSON);
const safeJSONStringify = JSON.stringify.bind(JSON);
const modalCanvasLimit = 16;
const modalHostCanvasLimit = 256;
const modalActionLimit = 16;
const modalSubscriptionLimit = 32;
const modalTextLimit = 64 * 1024;
const modalBlackBoxOwnerExtensionId = "black-box";
const modalBlackBoxLiveSurfaceId = "afterburner-black-box-live";
const modalReservedBlackBoxSurfaceIds = new Set(["black-box", modalBlackBoxLiveSurfaceId]);
const trustedBuiltinSourceTypes = new Set(["embedded", "signed-release"]);
const modalCanvases = new Map();
const modalInstances = new Map();
const modalUpdateQueues = new Map();
const modalSubscribers = new Map();
const modalFallbacks = new Map();
const modalNativeSurfaces = new Map();
let modalInstanceSequence = 0;
let modalBrokerConfig = __testModalBrokerConfig;
const modalDiagnostics = {
  registered: 0,
  active: 0,
  opened: 0,
  updated: 0,
  closed: 0,
  actionInvocations: 0,
  subscriptionCount: 0,
  fallbackCount: 0,
  pipeFailures: 0,
  quotaFailures: 0,
  lastFailureKind: null
};`;
  const body = `${prelude}\n${source.slice(start, end)}\nObject.assign(globalThis, { registerModalCanvas, openModalCanvas, updateModalCanvas, closeModalCanvas, invokeModalAction, subscribeModalCanvas, getModalDiagnostics, getModalFallback, __normalizeNativeIdentityAssertions: normalizeNativeIdentityAssertions, __events: events });`;
  new Script(body, { filename: "modal-runtime.js" }).runInContext(context);
  return context;
}

async function waitFor(predicate) {
  for (let attempt = 0; attempt < 50; attempt++) {
    if (predicate()) return;
    await new Promise((resolve) => setTimeout(resolve, 0));
  }
  assert.ok(predicate(), "condition was not met before timeout");
}

function deferred() {
  let resolve;
  const promise = new Promise((innerResolve) => { resolve = innerResolve; });
  return { promise, resolve };
}

test("native identity assertions reject coerced or malformed identities", async () => {
  const runtime = await loadModalRuntime(async () => ({ ok: true }));
  const validHash = `sha256:${"a".repeat(64)}`;
  const assertions = runtime.__normalizeNativeIdentityAssertions([
    { extensionId: 123, activePath: "C:\\extensions\\123", manifestHash: validHash, treeHash: validHash },
    { activePath: "C:\\extensions\\missing", manifestHash: validHash, treeHash: validHash },
    { extensionId: "valid", activePath: "C:\\extensions\\valid", manifestHash: "sha256:short", treeHash: validHash },
    { extensionId: "valid", activePath: "C:\\extensions\\valid", manifestHash: validHash, treeHash: validHash }
  ]);
  assert.equal(assertions.length, 1);
  assert.equal(assertions[0].extensionId, "valid");
});

function modalSurface(ownerExtensionId, canvasId, surfaceId = canvasId) {
  return { ownerExtensionId, canvasId, surfaceId };
}

function modalBrokerConfigFor(ownerExtensionId, canvasId, surfaceId = canvasId) {
  return { sessionId: "sessionAAAAAAAAAAAAAAAAAAAAAAAAAA", modalSurfaces: [{ ...modalSurface(ownerExtensionId, canvasId, surfaceId), sessionId: "sessionAAAAAAAAAAAAAAAAAAAAAAAAAA", pipe: "pipe" }] };
}

test("fallback is consumable and update before open is rejected", async () => {
  const runtime = await loadModalRuntime(async () => ({ ok: false, fallback: true, error: "no-broker" }));
  const handle = runtime.registerModalCanvas({
    id: "fallback-test",
    displayName: "Fallback Test",
    open: () => ({ body: "open body" })
  });

  await assert.rejects(() => handle.update({ body: "early" }), /not open/);
  assert.equal(runtime.getModalDiagnostics().active, 0);

  const opened = await handle.open();
  assert.equal(opened.fallback, true);
  assert.match(opened.text, /open body/);
  assert.equal(handle.fallback().text, opened.text);
  assert.equal(runtime.getModalFallback("fallback-test").frame.body, "open body");
  assert.equal(opened.frame.document.surfaceId, "fallback-test");
  assert.equal(opened.frame.document.root.kind, "dialog");

  const updated = await handle.update({ body: "updated body" });
  assert.equal(updated.fallback, true);
  assert.match(handle.fallback().text, /updated body/);

  const closed = await handle.close();
  assert.equal(closed.ok, true);
  assert.equal(closed.fallback, true);
  assert.equal(handle.fallback(), null);
  await handle.dispose();
});

test("modal broker surfaces require native session identity", async () => {
  const runtime = await loadModalRuntime(async () => ({ ok: true }), {
    modalSurfaces: [{ ...modalSurface("test-owner", "missing-session"), pipe: "pipe" }]
  });
  const handle = runtime.registerModalCanvas({
    id: "missing-session",
    displayName: "Missing Session",
    open: () => ({ body: "must fall back" })
  }, { ownerExtensionId: "test-owner" });

  const opened = await handle.open();
  assert.equal(opened.fallback, true);
  assert.equal(opened.error, "modal-surface-unavailable");
  await handle.dispose();
});

test("preissued modal capability does not expose mint authority to monkey patches", async () => {
  const messages = [];
  const captured = [];
  const runtime = await loadModalRuntime(async (message) => {
    messages.push(message);
    if (message.operation === "poll") return new Promise(() => {});
    return { ok: true };
  }, modalBrokerConfigFor("test-owner", "preissued-test"));
  const originalStringify = runtime.JSON.stringify;
  runtime.JSON.stringify = (value, ...args) => {
    captured.push(value);
    return originalStringify(value, ...args);
  };
  const handle = runtime.registerModalCanvas({
    id: "preissued-test",
    displayName: "Preissued Test",
    open: () => ({ body: "opened" })
  }, { ownerExtensionId: "test-owner" });

  const opened = await handle.open();
  assert.equal(opened.ok, true);
  assert.equal(messages.some((message) => message.operation === "register"), false);
  assert.equal(captured.some((message) => message?.registrationToken || message?.token), false);
  assert.equal(messages.some((message) => "registrationToken" in message || "token" in message), false);
  await handle.dispose();
});

test("broker wire messages preserve rich modal document", async () => {
  const messages = [];
  const pollResolvers = [];
  let closeGeneration = null;
  const allowed = new Set(["operation", "id", "sessionId", "ownerExtensionId", "canvasId", "surfaceId", "generation", "title", "status", "body", "footer", "actions", "document"]);
  const runtime = await loadModalRuntime(async (message) => {
    messages.push(message);
    const unknown = Object.keys(message).filter((key) => !allowed.has(key));
    if (unknown.length > 0) return { ok: false, error: "modal-invalid-request" };
    if (message.operation === "close") {
      closeGeneration = message.generation;
      for (const pending of pollResolvers) {
        if (!pending.resolved && pending.message.generation === closeGeneration) {
          pending.resolved = true;
          pending.resolve({ ok: true, event: { type: "closed", id: pending.message.id, generation: pending.message.generation } });
        }
      }
      return { ok: true };
    }
    if (message.operation === "poll") {
      if (closeGeneration === message.generation) {
        return { ok: true, event: { type: "closed", id: message.id, generation: message.generation } };
      }
      const item = deferred();
      pollResolvers.push({ message, resolve: item.resolve, resolved: false });
      return item.promise;
    }
    return { ok: true };
  }, modalBrokerConfigFor("test-owner", "wire-document-test"));
  const richDocument = {
    schemaVersion: 1,
    protocol: "afterburner.modal",
    revision: 7,
    surfaceId: "wire-document-test",
    root: {
      kind: "dialog",
      props: { title: "Wire Document Test", modal: true },
      children: [
        { kind: "table", props: { label: "Rich local table", columns: [{ id: "status", title: "Status" }], rows: [{ id: "row-1", cells: { status: "metadata" } }] }, children: [], id: "rich-local-table" }
      ],
      id: "rich-local-root"
    }
  };
  const handle = runtime.registerModalCanvas({
    id: "wire-document-test",
    displayName: "Wire Document Test",
    actions: [{ name: "refresh", label: "Refresh", key: "r", description: "Refresh document" }],
    open: () => ({ status: "opening", body: { markdown: "**rich** body" }, footer: "footer", document: richDocument })
  }, { ownerExtensionId: "test-owner" });

  const opened = await handle.open();
  assert.equal(opened.ok, true);
  assert.equal(opened.fallback, undefined);
  assert.equal(opened.frame.document.surfaceId, "wire-document-test");
  assert.equal(opened.frame.document.root.children[0].kind, "table");
  assert.equal(messages.at(-1).operation, "poll");
  const openMessage = messages.find((message) => message.operation === "open");
  assert.deepEqual(Object.keys(openMessage).sort(), ["actions", "body", "canvasId", "document", "footer", "generation", "id", "operation", "ownerExtensionId", "sessionId", "status", "surfaceId", "title"]);
  assert.equal(openMessage.token, undefined);
  assert.equal(openMessage.sessionId, "sessionAAAAAAAAAAAAAAAAAAAAAAAAAA");
  assert.equal(openMessage.ownerExtensionId, "test-owner");
  assert.equal(openMessage.canvasId, "wire-document-test");
  assert.equal(openMessage.surfaceId, "wire-document-test");
  assert.equal(openMessage.document.root.children[0].kind, "table");
  assert.equal(openMessage.body, "**rich** body");
  assert.deepEqual(openMessage.actions, [{ name: "refresh", label: "Refresh", key: "r", description: "Refresh document" }]);

  const updated = await handle.update({ body: "updated body" });
  assert.equal(updated.ok, true);
  assert.equal(updated.frame.document.surfaceId, "wire-document-test");
  const updateMessage = messages.find((message) => message.operation === "update");
  assert.equal(updateMessage.document.surfaceId, "wire-document-test");
  assert.equal(updateMessage.body, "updated body");

  await handle.close();
  const closeMessage = messages.find((message) => message.operation === "close");
  assert.deepEqual(Object.keys(closeMessage).sort(), ["canvasId", "generation", "id", "operation", "ownerExtensionId", "sessionId", "surfaceId"]);
  await handle.dispose();
});

test("legacy scoped Black Box handle uses explicit native identity", async () => {
  const messages = [];
  const runtime = await loadModalRuntime(async (message) => {
    messages.push(message);
    return { ok: true };
  }, modalBrokerConfigFor("black-box", "black-box"));
  const handle = runtime.registerModalCanvas({
    id: "black-box",
    displayName: "Legacy Black Box",
    open: () => ({ body: "legacy" })
  }, { ownerExtensionId: "black-box" });

  const opened = await handle.open();
  assert.equal(opened.ok, true);
  const openMessage = messages.find((message) => message.operation === "open");
  assert.equal(openMessage.sessionId, "sessionAAAAAAAAAAAAAAAAAAAAAAAAAA");
  assert.equal(openMessage.ownerExtensionId, "black-box");
  assert.equal(openMessage.canvasId, "black-box");
  assert.equal(openMessage.surfaceId, "black-box");
  assert.equal(openMessage.token, undefined);
  await handle.dispose();
});

test("action event updates current generation and Escape close does not send host close", async () => {
  const messages = [];
  const pollQueue = [
    (message) => ({ ok: true, event: { type: "action", id: message.id, generation: message.generation, actionName: "refresh", key: "r" } }),
    (message) => ({ ok: true, event: { type: "close", id: message.id, generation: message.generation, key: "escape" } })
  ];
  const runtime = await loadModalRuntime(async (message) => {
    messages.push(message);
    if (message.operation === "poll") {
      const next = pollQueue.shift();
      return next ? next(message) : new Promise(() => {});
    }
    return { ok: true };
  }, modalBrokerConfigFor("test-owner", "broker-test"));
  let actionCount = 0;
  const subscriberEvents = [];
  const handle = runtime.registerModalCanvas({
    id: "broker-test",
    displayName: "Broker Test",
    actions: [{
      name: "refresh",
      label: "Refresh",
      key: "r",
      handler: async (_input, controls) => {
        actionCount++;
        await controls.update({ body: "from action" });
        return { ok: true };
      }
    }],
    open: () => ({ body: "opened" })
  }, { ownerExtensionId: "test-owner" });
  handle.subscribe((event) => subscriberEvents.push(event));

  const opened = await handle.open();
  assert.equal(opened.ok, true);
  await waitFor(() => actionCount === 1 && runtime.getModalDiagnostics().active === 0);

  const pollMessages = messages.filter((message) => message.operation === "poll");
  assert.ok(pollMessages.length >= 2);
  assert.ok(pollMessages.every((message) => Number.isSafeInteger(message.generation)));
  assert.ok(messages.some((message) => message.operation === "update" && message.body === "from action" && message.generation === pollMessages[0].generation));
  assert.equal(messages.filter((message) => message.operation === "close").length, 0);
  assert.equal(runtime.getModalDiagnostics().closed, 1);
  assert.equal(runtime.getModalDiagnostics().lastFailureKind, null);
  assert.ok(subscriberEvents.some((event) => event.type === "opened" && event.frame.body === "opened" && Number.isSafeInteger(event.generation)));
  assert.ok(subscriberEvents.some((event) => event.type === "closed" && event.reason === "escape" && Number.isSafeInteger(event.generation)));
  await handle.dispose();
});

test("semantic control events reach the owning extension", async () => {
  const received = [];
  const messages = [];
  let pollCount = 0;
  const runtime = await loadModalRuntime(async (message) => {
    messages.push(message);
    if (message.operation === "poll") {
      pollCount++;
      if (pollCount === 1) {
        return {
          ok: true,
          event: {
            type: "change",
            id: message.id,
            generation: message.generation,
            sequence: 7,
            targetId: "display-name",
            documentRevision: 1,
            value: "Afterburner"
          }
        };
      }
      return new Promise(() => {});
    }
    return { ok: true };
  }, modalBrokerConfigFor("test-owner", "semantic-events"));
  const handle = runtime.registerModalCanvas({
    id: "semantic-events",
    onEvent: async (event, controls) => {
      received.push(event);
      await controls.update({ body: "acknowledged" });
    },
    open: () => ({
      document: {
        schemaVersion: 1,
        protocol: "afterburner.ui",
        revision: 1,
        surfaceId: "semantic-events",
        root: {
          id: "root",
          kind: "dialog",
          children: [{ id: "display-name", kind: "textInput", props: { label: "Display name" } }]
        }
      }
    })
  }, { ownerExtensionId: "test-owner" });

  await handle.open();
  await waitFor(() => received.length === 1);
  assert.deepEqual(JSON.parse(JSON.stringify(received[0])), {
    type: "change",
    targetId: "display-name",
    documentRevision: 1,
    generation: 1,
    sequence: 7,
    value: "Afterburner"
  });
  await waitFor(() => messages.some((message) => message.operation === "update"));
  assert.equal(messages.find((message) => message.operation === "update").ackEventSequence, 7);
  await handle.dispose();
});

test("concurrent updates serialize document revisions", async () => {
  const messages = [];
  const runtime = await loadModalRuntime(async (message) => {
    messages.push(message);
    return { ok: true };
  }, modalBrokerConfigFor("test-owner", "serialized-updates"));
  const handle = runtime.registerModalCanvas({
    id: "serialized-updates",
    open: () => ({ body: "initial" })
  }, { ownerExtensionId: "test-owner" });

  await handle.open();
  await Promise.all([
    handle.update({ body: "first" }),
    handle.update({ body: "second" })
  ]);
  const updates = messages.filter((message) => message.operation === "update");
  assert.equal(updates.length, 2);
  assert.deepEqual(updates.map((message) => message.document.revision), [2, 3]);
  assert.deepEqual(updates.map((message) => message.body), ["first", "second"]);
  await handle.dispose();
});

test("duplicate open is rejected without orphaning the active host generation", async () => {
  const messages = [];
  const runtime = await loadModalRuntime(async (message) => {
    messages.push(message);
    if (message.operation === "poll") return new Promise(() => {});
    return { ok: true };
  }, modalBrokerConfigFor("test-owner", "single-open"));
  const handle = runtime.registerModalCanvas({
    id: "single-open",
    open: () => ({ body: "open" })
  }, { ownerExtensionId: "test-owner" });

  await handle.open();
  await assert.rejects(() => handle.open(), error => error?.code === "ui.surfaceAlreadyOpen");
  await handle.dispose();
  assert.equal(messages.filter(message => message.operation === "open").length, 1);
  assert.equal(messages.filter(message => message.operation === "close").length, 1);
});

test("failed update still closes a possibly active native generation", async () => {
  const messages = [];
  const runtime = await loadModalRuntime(async (message) => {
    messages.push(message);
    if (message.operation === "poll") return new Promise(() => {});
    if (message.operation === "update") return { ok: false, error: "modal-pipe-timeout" };
    return { ok: true };
  }, modalBrokerConfigFor("test-owner", "close-after-update-failure"));
  const handle = runtime.registerModalCanvas({
    id: "close-after-update-failure",
    open: () => ({ body: "open" })
  }, { ownerExtensionId: "test-owner" });

  await handle.open();
  const update = await handle.update({ body: "changed" });
  assert.equal(update.fallback, true);
  await handle.dispose();
  assert.equal(messages.filter(message => message.operation === "close").length, 1);
});

test("Escape close can immediately reopen without stale close poisoning", async () => {
  const messages = [];
  const pollResolvers = [];
  let pollCount = 0;
  let closeGeneration = null;
  const runtime = await loadModalRuntime(async (message) => {
    messages.push(message);
    if (message.operation === "close") {
      closeGeneration = message.generation;
      for (const pending of pollResolvers) {
        if (!pending.resolved && pending.message.generation === closeGeneration) {
          pending.resolved = true;
          pending.resolve({ ok: true, event: { type: "closed", id: pending.message.id, generation: pending.message.generation } });
        }
      }
      return { ok: true };
    }
    if (message.operation === "poll") {
      if (closeGeneration === message.generation) {
        return { ok: true, event: { type: "closed", id: message.id, generation: message.generation } };
      }
      pollCount++;
      if (pollCount === 1) {
        return { ok: true, event: { type: "close", id: message.id, generation: message.generation, key: "escape" } };
      }
      const item = deferred();
      pollResolvers.push({ message, resolve: item.resolve, resolved: false });
      return item.promise;
    }
    return { ok: true };
  }, modalBrokerConfigFor("test-owner", "reopen-test"));
  const subscriberEvents = [];
  let reopened = false;
  const handle = runtime.registerModalCanvas({
    id: "reopen-test",
    displayName: "Reopen Test",
    open: (input = {}) => ({ body: input.body ?? "initial" })
  }, { ownerExtensionId: "test-owner" });
  handle.subscribe((event) => {
    subscriberEvents.push(event);
    if (event.type === "closed" && !reopened) {
      reopened = true;
      void handle.open({ body: "reopened" });
    }
  });

  await handle.open({ body: "initial" });
  await waitFor(() => runtime.getModalDiagnostics().active === 1 && messages.filter((message) => message.operation === "open").length === 2);

  const openMessages = messages.filter((message) => message.operation === "open");
  assert.equal(openMessages[0].body, "initial");
  assert.equal(openMessages[1].body, "reopened");
  assert.ok(openMessages[1].generation > openMessages[0].generation);
  assert.equal(messages.filter((message) => message.operation === "close").length, 0);
  assert.ok(subscriberEvents.some((event) => event.type === "closed" && event.reason === "escape"));

  await handle.dispose();
  assert.equal(runtime.getModalDiagnostics().active, 0);
});

test("delayed stale closed event is ignored after reopen", async () => {
  const messages = [];
  const pollResolvers = [];
  let closeGeneration = null;
  const runtime = await loadModalRuntime(async (message) => {
    messages.push(message);
    if (message.operation === "close") {
      closeGeneration = message.generation;
      for (const pending of pollResolvers) {
        if (!pending.resolved && pending.message.generation === closeGeneration) {
          pending.resolved = true;
          pending.resolve({ ok: true, event: { type: "closed", id: pending.message.id, generation: pending.message.generation } });
        }
      }
      return { ok: true };
    }
    if (message.operation === "poll") {
      if (closeGeneration === message.generation) {
        return { ok: true, event: { type: "closed", id: message.id, generation: message.generation } };
      }
      const item = deferred();
      pollResolvers.push({ message, resolve: item.resolve, resolved: false });
      return item.promise;
    }
    return { ok: true };
  }, modalBrokerConfigFor("test-owner", "stale-closed-test"));
  const handle = runtime.registerModalCanvas({
    id: "stale-closed-test",
    displayName: "Stale Closed Test",
    open: (input = {}) => ({ body: input.body ?? "opened" })
  }, { ownerExtensionId: "test-owner" });

  await handle.open({ body: "first" });
  await waitFor(() => pollResolvers.length === 1);
  const firstGeneration = pollResolvers[0].message.generation;
  pollResolvers[0].resolve({ ok: true, event: { type: "close", id: "stale-closed-test", generation: firstGeneration, key: "escape" } });
  await waitFor(() => runtime.getModalDiagnostics().active === 0);

  await handle.open({ body: "second" });
  await waitFor(() => pollResolvers.length === 2);
  const secondGeneration = pollResolvers[1].message.generation;
  assert.ok(secondGeneration > firstGeneration);
  pollResolvers[1].resolve({ ok: true, event: { type: "closed", id: "stale-closed-test", generation: firstGeneration } });
  await waitFor(() => runtime.getModalDiagnostics().active === 1 && pollResolvers.length === 3);

  assert.equal(runtime.getModalDiagnostics().canvases[0].generation, secondGeneration);
  assert.equal(messages.filter((message) => message.operation === "close").length, 0);

  await handle.dispose();
  assert.equal(runtime.getModalDiagnostics().active, 0);
});

test("owner-scoped modal APIs isolate identical local surface IDs", async () => {
  const runtime = await loadModalRuntime(async () => ({ ok: false, fallback: true, error: "no-broker" }));
  const ownerA = runtime.registerModalCanvas({
    id: "owned-modal",
    displayName: "Owned Modal",
    actions: [{ name: "refresh", label: "Refresh", handler: async () => ({ ok: true }) }],
    open: () => ({ body: "owned" })
  }, { ownerExtensionId: "owner-a" });
  const ownerB = runtime.registerModalCanvas({
    id: "owned-modal",
    displayName: "Other Modal",
    open: () => ({ body: "other" })
  }, { ownerExtensionId: "owner-b" });

  await ownerA.open();
  await ownerB.open();
  await ownerB.update({ body: "other-updated" });
  assert.equal(ownerA.fallback().frame.body, "owned");
  assert.equal(ownerB.fallback().frame.body, "other-updated");
  assert.equal(ownerA.diagnostics().canvases.length, 1);
  assert.equal(ownerB.diagnostics().canvases.length, 1);
  await assert.rejects(() => runtime.updateModalCanvas("owned-modal", { body: "unscoped" }), /Unknown modal canvas/);

  await ownerA.dispose();
  await ownerB.dispose();
});

test("programmatic close sends one generated host close and dispose is idempotent", async () => {
  const messages = [];
  const pollResolvers = [];
  let closeGeneration = null;
  const runtime = await loadModalRuntime(async (message) => {
    messages.push(message);
    if (message.operation === "close") {
      closeGeneration = message.generation;
      for (const pending of pollResolvers) {
        if (!pending.resolved && pending.message.generation === closeGeneration) {
          pending.resolved = true;
          pending.resolve({ ok: true, event: { type: "closed", id: pending.message.id, generation: pending.message.generation } });
        }
      }
      return { ok: true };
    }
    if (message.operation === "poll") {
      if (closeGeneration === message.generation) {
        return { ok: true, event: { type: "closed", id: message.id, generation: message.generation } };
      }
      const item = deferred();
      pollResolvers.push({ message, resolve: item.resolve, resolved: false });
      return item.promise;
    }
    return { ok: true };
  }, modalBrokerConfigFor("test-owner", "programmatic-close-test"));
  const handle = runtime.registerModalCanvas({
    id: "programmatic-close-test",
    displayName: "Programmatic Close Test",
    open: () => ({ body: "opened" })
  }, { ownerExtensionId: "test-owner" });

  await handle.open();
  await waitFor(() => pollResolvers.length === 1);
  const generation = pollResolvers[0].message.generation;
  const closed = await handle.close();
  assert.equal(closed.ok, true);
  assert.equal(runtime.getModalDiagnostics().active, 0);
  const closeMessages = messages.filter((message) => message.operation === "close");
  assert.equal(closeMessages.length, 1);
  assert.equal(closeMessages[0].generation, generation);

  await handle.dispose();
  assert.equal(messages.filter((message) => message.operation === "close").length, 1);
  assert.equal(runtime.getModalDiagnostics().registered, 0);
});

test("close then reopen advances generation and dispose closes active host modal", async () => {
  const messages = [];
  const pollResolvers = [];
  let closeGeneration = null;
  const runtime = await loadModalRuntime(async (message) => {
    messages.push(message);
    if (message.operation === "close") {
      closeGeneration = message.generation;
      for (const pending of pollResolvers) {
        if (!pending.resolved && pending.message.generation === closeGeneration) {
          pending.resolved = true;
          pending.resolve({ ok: true, event: { type: "closed", id: pending.message.id, generation: pending.message.generation } });
        }
      }
      return { ok: true };
    }
    if (message.operation === "poll") {
      if (closeGeneration === message.generation) {
        return { ok: true, event: { type: "closed", id: message.id, generation: message.generation } };
      }
      const item = deferred();
      pollResolvers.push({ message, resolve: item.resolve, resolved: false });
      return item.promise;
    }
    return { ok: true };
  }, modalBrokerConfigFor("test-owner", "generation-test"));
  let actionCount = 0;
  const handle = runtime.registerModalCanvas({
    id: "generation-test",
    displayName: "Generation Test",
    actions: [{ name: "refresh", label: "Refresh", handler: () => { actionCount++; } }],
    open: () => ({ body: "opened" })
  }, { ownerExtensionId: "test-owner" });

  await handle.open();
  await waitFor(() => pollResolvers.length === 1);
  const firstGeneration = runtime.getModalDiagnostics().canvases[0].generation;
  await handle.close();
  await handle.open();
  await waitFor(() => pollResolvers.length === 2);
  const secondGeneration = runtime.getModalDiagnostics().canvases[0].generation;
  assert.ok(secondGeneration > firstGeneration);
  assert.equal(pollResolvers[0].message.generation, firstGeneration);
  assert.equal(pollResolvers[1].message.generation, secondGeneration);

  assert.equal(actionCount, 0);

  const closePromise = handle.dispose();
  await waitFor(() => messages.filter((message) => message.operation === "close").length === 2);
  const closeMessages = messages.filter((message) => message.operation === "close");
  assert.equal(closeMessages.length, 2);
  assert.equal(closeMessages[0].generation, firstGeneration);
  assert.equal(closeMessages[1].generation, secondGeneration);
  await closePromise;
  assert.equal(runtime.getModalDiagnostics().registered, 0);
  assert.equal(runtime.getModalDiagnostics().active, 0);
});
