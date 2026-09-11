import test from "node:test";
import assert from "node:assert/strict";
import { cp, mkdir, mkdtemp, readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { fileURLToPath, pathToFileURL } from "node:url";
import { setTimeout as delay } from "node:timers/promises";
import { DEFAULT_MAX_BYTES, DEFAULT_SEGMENT_BYTES, loadBlackBoxConfig, resolveNativeEventsPath } from "../lib/config.mjs";
import { buildSessionRegistration, startSessionExtension } from "../lib/session-extension.mjs";
import { startBlackBoxService } from "../lib/service.mjs";
import { buildModalFrame } from "../lib/modal-surface.mjs";
import { MODAL_ACTIVATION_POLL_MS, activate } from "../runtime/extension.mjs";
import { cleanup, completeConfig, workDirectory } from "./helpers.mjs";
import { atomicWriteFile, routeAckDirectory, waitForRouteAck } from "../shared/route-ipc.mjs";

const ROUTE_A = "routeAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA";
const ROUTE_B = "routeBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB";

test("configuration defaults use 500 MiB retention and 8 MiB segments", async t => {
    const home = await workDirectory("config");
    t.after(() => cleanup(home));
    const loaded = await loadBlackBoxConfig({ AFTERBURNER_HOME: home });
    assert.equal(loaded.config.storage.maxBytes, DEFAULT_MAX_BYTES);
    assert.equal(loaded.config.storage.segmentBytes, DEFAULT_SEGMENT_BYTES);
    assert.deepEqual(loaded.diagnostics, ["config-missing-defaults-used"]);
});

test("native event path resolves the Copilot extension SESSION_ID", () => {
    assert.equal(resolveNativeEventsPath({
        COPILOT_HOME: "C:\\managed-home",
        SESSION_ID: "session-123"
    }), "C:\\managed-home\\session-state\\session-123\\events.jsonl");
});

test("modal activation requests wait for runtime acknowledgement", async t => {
    const home = await workDirectory("modal-activation-ack");
    t.after(() => cleanup(home));
    const configPath = join(home, "config", "black-box.json");
    await mkdir(join(home, "config"), { recursive: true });
    await writeFile(configPath, JSON.stringify(completeConfig({ native: { enabled: false } })), "utf8");
    const service = await startBlackBoxService({
        mode: "session",
        env: { AFTERBURNER_HOME: home, AFTERBURNER_BLACK_BOX_CONFIG: configPath, AFTERBURNER_SESSION_ROUTE: ROUTE_A }
    });
    t.after(() => service.close());
    const pending = service.requestModalOpen({ timeoutMs: 2000 });
    await delay(100);
    const requests = await service.consumeModalOpenRequests();
    assert.equal(requests.length, 1);
    await service.completeModalOpenRequest(requests[0], { ok: true });
    assert.equal((await pending).ok, true);
});

test("modal activation preserves other routes until the owning route polls", async t => {
    const home = await workDirectory("modal-activation-other-route");
    t.after(() => cleanup(home));
    const configPath = join(home, "config", "black-box.json");
    await mkdir(join(home, "config"), { recursive: true });
    await writeFile(configPath, JSON.stringify(completeConfig({ native: { enabled: false } })), "utf8");
    const sessionA = await startBlackBoxService({
        mode: "session",
        env: { AFTERBURNER_HOME: home, AFTERBURNER_BLACK_BOX_CONFIG: configPath, SESSION_ID: "session-a", AFTERBURNER_SESSION_ROUTE: ROUTE_A }
    });
    const runtimeB = await startBlackBoxService({
        mode: "runtime",
        env: { AFTERBURNER_HOME: home, AFTERBURNER_BLACK_BOX_CONFIG: configPath, SESSION_ID: "session-b", AFTERBURNER_SESSION_ROUTE: ROUTE_B }
    });
    const runtimeA = await startBlackBoxService({
        mode: "runtime",
        env: { AFTERBURNER_HOME: home, AFTERBURNER_BLACK_BOX_CONFIG: configPath, SESSION_ID: "session-a", AFTERBURNER_SESSION_ROUTE: ROUTE_A }
    });
    t.after(() => Promise.all([sessionA.close(), runtimeA.close(), runtimeB.close()]));
    const pending = sessionA.requestModalOpen({ timeoutMs: 2000 });
    await delay(50);
    assert.deepEqual(await runtimeB.consumeModalOpenRequests(), []);
    const requests = await runtimeA.consumeModalOpenRequests();
    assert.equal(requests.length, 1);
    assert.equal(requests[0].routeId, ROUTE_A);
    assert.equal(await runtimeB.completeModalOpenRequest(requests[0], { ok: true }), false);
    await runtimeA.completeModalOpenRequest(requests[0], { ok: true });
    assert.equal((await pending).ok, true);
});

test("modal activation survives old legacy consumers and does not steal live claims", async t => {
    const home = await workDirectory("modal-activation-recovery");
    t.after(() => cleanup(home));
    const configPath = join(home, "config", "black-box.json");
    await mkdir(join(home, "config"), { recursive: true });
    await writeFile(configPath, JSON.stringify(completeConfig({ native: { enabled: false } })), "utf8");
    const env = { AFTERBURNER_HOME: home, AFTERBURNER_BLACK_BOX_CONFIG: configPath, AFTERBURNER_SESSION_ROUTE: ROUTE_A };
    const session = await startBlackBoxService({ mode: "session", env });
    const runtime = await startBlackBoxService({ mode: "runtime", env });
    t.after(() => Promise.all([session.close(), runtime.close()]));
    const stateDirectory = join(home, "extension-data", "black-box", "state");
    await mkdir(stateDirectory, { recursive: true });
    await writeFile(join(stateDirectory, "modal-activation.jsonl"), "", "utf8");
    const pending = session.requestModalOpen({ timeoutMs: 5000 });
    await delay(50);
    assert.equal(await readFile(join(stateDirectory, "modal-activation.jsonl"), "utf8"), "");
    const firstClaim = await runtime.consumeModalOpenRequests();
    assert.equal(firstClaim.length, 1);
    assert.deepEqual(await runtime.consumeModalOpenRequests(), []);
    await delay(2100);
    assert.deepEqual(await runtime.consumeModalOpenRequests(), []);
    await runtime.completeModalOpenRequest(firstClaim[0], { ok: true });
    assert.equal((await pending).ok, true);
});

test("modal activation retries partial acks and rejects stale acks", async t => {
    const home = await workDirectory("modal-activation-ack-freshness");
    t.after(() => cleanup(home));
    const configPath = join(home, "config", "black-box.json");
    await mkdir(join(home, "config"), { recursive: true });
    await writeFile(configPath, JSON.stringify(completeConfig({ native: { enabled: false } })), "utf8");
    const stateDirectory = join(home, "extension-data", "black-box", "state");
    const ackDirectory = join(stateDirectory, "modal-activation-acks");
    const request = { schemaVersion: 2, routeId: ROUTE_A, requestId: "ackrequest", surfaceId: "afterburner-black-box-live", createdAt: new Date().toISOString() };
    const routeAckDir = routeAckDirectory(ackDirectory, ROUTE_A);
    await mkdir(routeAckDir, { recursive: true });
    await writeFile(join(routeAckDir, "ackrequest.json"), "{", "utf8");
    const pending = waitForRouteAck(ackDirectory, request, { env: { AFTERBURNER_SESSION_ROUTE: ROUTE_A }, timeoutMs: 2000 });
    await delay(50);
    await atomicWriteFile(join(routeAckDir, "ackrequest.json"), JSON.stringify({ schemaVersion: 2, routeId: ROUTE_A, requestId: "ackrequest", surfaceId: "afterburner-black-box-live", ok: true, completedAt: "2000-01-01T00:00:00.000Z" }) + "\n");
    await delay(50);
    await atomicWriteFile(join(routeAckDir, "ackrequest.json"), JSON.stringify({ schemaVersion: 2, routeId: ROUTE_A, requestId: "ackrequest", surfaceId: "afterburner-black-box-live", ok: true, completedAt: new Date().toISOString() }) + "\n");
    assert.equal((await pending).ok, true);
});

test("sessionless legacy modal activation requests are ignored with a trusted route", async t => {
    const home = await workDirectory("modal-activation-stale");
    t.after(() => cleanup(home));
    const configPath = join(home, "config", "black-box.json");
    await mkdir(join(home, "config"), { recursive: true });
    await writeFile(configPath, JSON.stringify(completeConfig({ native: { enabled: false } })), "utf8");
    const stateDirectory = join(home, "extension-data", "black-box", "state");
    await mkdir(stateDirectory, { recursive: true });
    await writeFile(join(stateDirectory, "modal-activation.jsonl"), JSON.stringify({
        schemaVersion: 1,
        requestId: "preexisting-request",
        surfaceId: "afterburner-black-box-live",
        createdAt: new Date().toISOString(),
        input: {}
    }) + "\n", "utf8");
    await delay(5);
    const service = await startBlackBoxService({
        mode: "runtime",
        env: { AFTERBURNER_HOME: home, AFTERBURNER_BLACK_BOX_CONFIG: configPath, AFTERBURNER_SESSION_ROUTE: ROUTE_A }
    });
    t.after(() => service.close());
    assert.deepEqual(await service.consumeModalOpenRequests(), []);
});

test("modal activation fails closed without trusted route", async t => {
    const home = await workDirectory("modal-activation-no-route");
    t.after(() => cleanup(home));
    const configPath = join(home, "config", "black-box.json");
    await mkdir(join(home, "config"), { recursive: true });
    await writeFile(configPath, JSON.stringify(completeConfig({ native: { enabled: false } })), "utf8");
    const service = await startBlackBoxService({
        mode: "session",
        env: { AFTERBURNER_HOME: home, AFTERBURNER_BLACK_BOX_CONFIG: configPath }
    });
    t.after(() => service.close());
    const result = await service.requestModalOpen({ timeoutMs: 25 });
    assert.equal(result.ok, false);
    assert.equal(result.error, "trusted-route-unavailable");
    const stateDirectory = join(home, "extension-data", "black-box", "state");
    await assert.rejects(readFile(join(stateDirectory, "modal-activation.jsonl"), "utf8"), /ENOENT/);
});

test("modal activation acknowledgement wakes without waiting for polling fallback", async t => {
    const home = await workDirectory("modal-activation-ack-watch");
    t.after(() => cleanup(home));
    const configPath = join(home, "config", "black-box.json");
    await mkdir(join(home, "config"), { recursive: true });
    await writeFile(configPath, JSON.stringify(completeConfig({ native: { enabled: false } })), "utf8");
    const service = await startBlackBoxService({
        mode: "session",
        env: { AFTERBURNER_HOME: home, AFTERBURNER_BLACK_BOX_CONFIG: configPath, AFTERBURNER_SESSION_ROUTE: ROUTE_A }
    });
    t.after(() => service.close());
    const originalSetTimeout = globalThis.setTimeout;
    globalThis.setTimeout = (handler, delayMs, ...args) => originalSetTimeout(handler, delayMs === 100 ? 1000 : delayMs, ...args);
    t.after(() => { globalThis.setTimeout = originalSetTimeout; });

    const pending = service.requestModalOpen({ timeoutMs: 2000 });
    const requestDeadline = Date.now() + 1000;
    let requests = [];
    while (requests.length === 0 && Date.now() < requestDeadline) {
        requests = await service.consumeModalOpenRequests();
        if (requests.length === 0) await delay(10);
    }
    assert.equal(requests.length, 1);
    const acknowledgedAt = Date.now();
    await service.completeModalOpenRequest(requests[0], { ok: true });
    assert.equal((await pending).ok, true);
    assert.ok(Date.now() - acknowledgedAt < 500, "acknowledgement should resolve before the inflated polling fallback");
});

test("runtime observer ignores Black Box modal self-noise", async t => {
    const home = await workDirectory("modal-self-noise");
    t.after(() => cleanup(home));
    const configPath = join(home, "config", "black-box.json");
    await mkdir(join(home, "config"), { recursive: true });
    await writeFile(configPath, JSON.stringify(completeConfig({ native: { enabled: false } })), "utf8");
    const service = await startBlackBoxService({
        mode: "runtime",
        env: { AFTERBURNER_HOME: home, AFTERBURNER_BLACK_BOX_CONFIG: configPath, SESSION_ID: "session-one" }
    });
    t.after(() => service.close());
    for (const type of ["ui.modal_canvas.opened", "ui.modal_canvas.updated", "ui.modal_canvas.action_started", "ui.modal_canvas.action_completed", "ui.modal_canvas.closed"]) {
        assert.equal(await service.observeRuntime({ type, metadata: { modalId: "afterburner-black-box-live" } }), false, type);
    }
    for (const type of ["ui.host.lifecycle", "ui.host.patch", "ui.host.recovery", "ui.host.quota"]) {
        assert.equal(await service.observeRuntime({ type, metadata: { surfaceId: "afterburner-black-box-live" } }), false, type);
    }
    assert.equal(await service.observeRuntime({ type: "ui.modal_canvas.opened", metadata: { modalId: "other-modal" } }), false);
    assert.equal(await service.observeRuntime({ type: "ui.host.lifecycle", metadata: { surfaceId: "other-surface" } }), false);
    assert.equal(await service.observeRuntime({ type: "ui.modal_canvas.opened", metadata: { sessionId: "session-one", modalId: "other-modal" } }), true);
    assert.equal(await service.observeRuntime({ type: "ui.host.lifecycle", metadata: { sessionId: "session-one", surfaceId: "other-surface" } }), true);
    assert.equal(await service.observeRuntime({ type: "ui.host.lifecycle", metadata: { sessionId: "other-session", surfaceId: "other-surface" } }), false);
    const status = await service.status();
    assert.equal(status.analytics.totalRecords, 2);
});

function fakePanelService(records = []) {
    return {
        status: async () => ({
            schemaVersion: 1,
            enabled: true,
            mode: "session",
            storage: { segmentCount: 2, segmentBytes: 100, retentionBlockedBytes: 0 },
            queue: { records: 0, bytes: 0, droppedRecords: 0, writeErrors: 0 },
            analytics: { anomalyCount: 1, milestoneCount: 3, totalRecords: 12 },
            native: { enabled: true, configured: true },
            recentSignals: records
        }),
        tail: async () => records,
        requestModalOpen: async () => ({ ok: true, requestId: "queued", surfaceId: "afterburner-black-box-live" }),
        exportBundle: async () => ({ path: "bundle", manifest: { recordCount: 0 } }),
        doctor: async () => ({ healthy: true })
    };
}

async function captureSessionRegistration(options = {}) {
    let registration;
    let canvasDefinition;
    const logs = [];
    const createCanvas = options.createCanvas ?? (definition => { canvasDefinition = definition; return definition; });
    const joinSession = async value => {
        registration = value;
        return { log: async message => logs.push(message) };
    };
    await buildSessionRegistration({
        service: options.service ?? fakePanelService(options.records),
        createCanvas: options.withoutCanvas ? undefined : createCanvas,
        joinSession,
        openModalCanvas: options.openModalCanvas
    });
    return { registration, canvasDefinition, logs };
}

async function configureRuntimeEnvironment(t, name) {
    const home = await workDirectory(name);
    await mkdir(join(home, "config"), { recursive: true });
    const configPath = join(home, "config", "black-box.json");
    await writeFile(configPath, JSON.stringify(completeConfig({ native: { enabled: false } })), "utf8");
    const previous = {
        home: process.env.AFTERBURNER_HOME,
        config: process.env.AFTERBURNER_BLACK_BOX_CONFIG,
        openOnStart: process.env.AFTERBURNER_BLACK_BOX_OPEN_MODAL_ON_START,
        openSurfaceOnStart: process.env.AFTERBURNER_BLACK_BOX_OPEN_SURFACE_ON_START,
        sessionId: process.env.SESSION_ID,
        copilotSessionId: process.env.COPILOT_AGENT_SESSION_ID,
        route: process.env.AFTERBURNER_SESSION_ROUTE
    };
    process.env.AFTERBURNER_HOME = home;
    process.env.AFTERBURNER_BLACK_BOX_CONFIG = configPath;
    process.env.AFTERBURNER_SESSION_ROUTE = ROUTE_A;
    process.env.SESSION_ID = "session-one";
    delete process.env.COPILOT_AGENT_SESSION_ID;
    delete process.env.AFTERBURNER_BLACK_BOX_OPEN_MODAL_ON_START;
    t.after(async () => {
        if (previous.home === undefined) delete process.env.AFTERBURNER_HOME;
        else process.env.AFTERBURNER_HOME = previous.home;
        if (previous.config === undefined) delete process.env.AFTERBURNER_BLACK_BOX_CONFIG;
        else process.env.AFTERBURNER_BLACK_BOX_CONFIG = previous.config;
        if (previous.openOnStart === undefined) delete process.env.AFTERBURNER_BLACK_BOX_OPEN_MODAL_ON_START;
        else process.env.AFTERBURNER_BLACK_BOX_OPEN_MODAL_ON_START = previous.openOnStart;
        if (previous.openSurfaceOnStart === undefined) delete process.env.AFTERBURNER_BLACK_BOX_OPEN_SURFACE_ON_START;
        else process.env.AFTERBURNER_BLACK_BOX_OPEN_SURFACE_ON_START = previous.openSurfaceOnStart;
        if (previous.sessionId === undefined) delete process.env.SESSION_ID;
        else process.env.SESSION_ID = previous.sessionId;
        if (previous.copilotSessionId === undefined) delete process.env.COPILOT_AGENT_SESSION_ID;
        else process.env.COPILOT_AGENT_SESSION_ID = previous.copilotSessionId;
        if (previous.route === undefined) delete process.env.AFTERBURNER_SESSION_ROUTE;
        else process.env.AFTERBURNER_SESSION_ROUTE = previous.route;
        await cleanup(home);
    });
}

async function waitFor(predicate, timeoutMs = 1500) {
    const deadline = Date.now() + timeoutMs;
    while (Date.now() < deadline) {
        if (predicate()) return;
        await delay(25);
    }
    assert.fail("timed out waiting for expected modal update");
}

function command(registration, name) {
    return registration.commands.find(candidate => candidate.name === name);
}

function createFakeRuntimeUI() {
    const component = kind => (props = {}, children = [], options = {}) => ({ kind, props, children, ...options });
    const components = new Proxy({}, { get: (_target, kind) => component(String(kind)) });
    const createUIDocument = (root, options = {}) => ({
        schemaVersion: 1,
        protocol: "afterburner.modal",
        revision: options.revision ?? 1,
        surfaceId: options.surfaceId ?? root?.props?.surfaceId,
        root,
        locale: options.locale,
        capabilities: options.capabilities
    });
    const createRuntime = ({ bridge } = {}) => ({
        defineSurface(definition) {
            const descriptor = {
                ...definition,
                actions: (definition.actions ?? []).map(({ handler, ...action }) => action)
            };
            return {
                id: definition.id,
                descriptor,
                async open(input) {
                    const state = await definition.open?.(input);
                    const document = await definition.render?.({ state, previous: null });
                    if (bridge?.render) await bridge.render(definition.id, document, {});
                    return { ok: true, fallback: false, state, document };
                },
                async update(state) {
                    const document = await definition.render?.({ state, previous: state?.document });
                    if (bridge?.render) await bridge.render(definition.id, document, {});
                    return { ok: true, document };
                },
                async patch(patch, options) {
                    return await bridge?.patch?.(definition.id, patch, options) ?? { ok: true };
                },
                async close(options) {
                    await definition.close?.(options);
                    if (bridge?.close) await bridge.close(definition.id, options);
                    return { ok: true };
                },
                async invoke(actionId, parameters) {
                    const action = (definition.actions ?? []).find(candidate => candidate.id === actionId);
                    return action?.handler?.(parameters);
                },
                fallback() { return { ok: false, fallback: true, text: "deterministic metadata-only fallback" }; }
            };
        }
    });
    return { components, createUIDocument, createRuntime };
}

test("session registration preserves command panels without registering a generic Black Box canvas", async () => {
    const { registration, canvasDefinition, logs } = await captureSessionRegistration();
    assert.equal(canvasDefinition, undefined);
    assert.deepEqual(registration.canvases, []);
    assert.deepEqual(registration.commands.map(item => item.name), [
        "black-box", "black-box-modal", "black-box-tail", "black-box-tail-stop", "black-box-export", "black-box-doctor"
    ]);
    assert.equal(logs.length, 0, "Black Box must not write timeline entries before explicit user action.");
    await command(registration, "black-box").handler();
    assert.match(logs[0], /Afterburner Black Box/);
    assert.match(logs[0], /Storage/);
    assert.match(logs[0], /Queue/);
    assert.match(logs[0], /1 anomalie/);
});

test("session commands register while optional Black Box startup is blocked", async () => {
    let releaseStartup;
    const blockedStartup = new Promise(resolve => { releaseStartup = resolve; });
    let registration;
    const logs = [];
    const activation = startSessionExtension({
        startService: () => blockedStartup,
        joinSession: async value => {
            registration = value;
            return { log: async message => logs.push(message) };
        }
    });
    const instance = await Promise.race([
        activation,
        delay(250).then(() => assert.fail("Black Box activation waited for optional service startup"))
    ]);
    assert.ok(command(registration, "black-box"));

    const pendingCommand = command(registration, "black-box").handler();
    releaseStartup(Promise.reject(new Error("native recovery blocked")));
    await pendingCommand;
    await waitFor(() => logs.length > 0);
    assert.ok(logs.some(message => /Black Box startup failed: native recovery blocked/.test(message)));
    await instance.dispose();
});

test("disposing before deferred Black Box startup closes the eventual service", async () => {
    let releaseStartup;
    const blockedStartup = new Promise(resolve => { releaseStartup = resolve; });
    let closeCount = 0;
    const instance = await startSessionExtension({
        startService: () => blockedStartup,
        joinSession: async () => ({ log: async () => {} })
    });
    await instance.dispose();
    releaseStartup({ ...fakePanelService(), close: async () => { closeCount++; } });
    await instance.ready;
    assert.equal(closeCount, 1);
});

test("session registration renders the visible panel without canvas support", async () => {
    const { registration, logs } = await captureSessionRegistration({ withoutCanvas: true });
    assert.deepEqual(registration.canvases, []);
    assert.equal(logs.length, 0, "Black Box must stay silent before explicit user action.");
    await command(registration, "black-box").handler();
    assert.match(logs[0], /Afterburner Black Box/);
    assert.match(logs[0], /Storage\s*:/);
    assert.match(logs[0], /Queue\s*:/);
    assert.match(logs[0], /Signals\s*:/);
});

test("Black Box manifest declares only native modal UI surfaces", async () => {
    const manifest = JSON.parse(await readFile(new URL("../afterburner.json", import.meta.url), "utf8"));
    assert.equal(manifest.id, "black-box");
    assert.ok(manifest.capabilities.includes("modal-canvas"));
    assert.equal(manifest.capabilities.includes("canvas"), false);
    assert.equal(manifest.capabilities.includes("enterprise-surface"), false);
    assert.equal(manifest.ui.protocol, "afterburner.ui");
    assert.equal(manifest.ui.revision, 1);
    assert.deepEqual(manifest.ui.surfaces.map(surface => [surface.id, surface.kind]), [
        ["afterburner-black-box-live", "modal"],
        ["black-box", "modal"]
    ]);
});

test("session extension wrapper does not open the generic Copilot canvas", async () => {
    const wrapper = await readFile(new URL("../com.github.copilot/extensions/BlackBox/extension.mjs", import.meta.url), "utf8");
    assert.match(wrapper, /extensions\/BlackBox\/extension\.mjs/);
    const source = await readFile(new URL("../extensions/BlackBox/extension.mjs", import.meta.url), "utf8");
    assert.doesNotMatch(source, /joinedSession\?\.rpc\?\.canvas/);
    assert.doesNotMatch(source, /canvasRpc\.open/);
    assert.doesNotMatch(source, /copilotSdk\.openModalCanvas/);
});

test("black-box-modal queues a runtime-owned native modal activation", async () => {
    const requests = [];
    const service = {
        ...fakePanelService(),
        requestModalOpen: async request => {
            requests.push(request);
            return { ok: true, requestId: "queued", surfaceId: request.surfaceId };
        }
    };
    const { registration, logs } = await captureSessionRegistration({ service });
    await command(registration, "black-box-modal").handler();
    assert.deepEqual(requests, [{ surfaceId: "afterburner-black-box-live", input: {} }]);
    assert.deepEqual(logs, []);
});

test("black-box-modal never falls back to the generic Copilot canvas", async () => {
    const opens = [];
    const { registration, logs } = await captureSessionRegistration({
        service: { ...fakePanelService(), requestModalOpen: async () => { throw new Error("queue unavailable"); } },
        openModalCanvas: async (id, input) => {
            opens.push({ id, input });
            return { ok: true, fallback: false };
        }
    });
    await command(registration, "black-box-modal").handler();
    assert.deepEqual(opens, []);
    assert.match(logs[0], /runtime modal activation queue is unavailable/);
    assert.match(logs[0], /Showing text fallback/);
});

test("black-box-modal command provides explicit text fallback without overclaiming", async () => {
    const unavailable = await captureSessionRegistration({
        withoutCanvas: true,
        service: { ...fakePanelService(), requestModalOpen: async () => { throw new Error("queue unavailable"); } }
    });
    await command(unavailable.registration, "black-box-modal").handler();
    assert.match(unavailable.logs[0], /runtime modal activation queue is unavailable/);
    assert.match(unavailable.logs[0], /Showing text fallback/);
    assert.match(unavailable.logs[0], /Afterburner Black Box/);
    assert.doesNotMatch(unavailable.logs[0], /registered by the runtime extension/);

    const failing = await captureSessionRegistration({
        withoutCanvas: true,
        service: { ...fakePanelService(), requestModalOpen: async () => { throw new Error("queue unavailable"); } },
        openModalCanvas: async () => { throw new Error("Unknown modal canvas"); }
    });
    await command(failing.registration, "black-box-modal").handler();
    assert.match(failing.logs[0], /runtime modal activation queue is unavailable/);
    assert.match(failing.logs[0], /Showing text fallback/);
});

test("Black Box tail command renders a sanitized timeline panel", async () => {
    const records = [{
        timestamp: "2026-09-02T10:00:00.000Z",
        kind: "milestone",
        eventType: "milestone.tool-complete",
        attributes: { durationMs: 42, success: true, body: "MUST NOT RENDER" }
    }];
    const { registration, logs } = await captureSessionRegistration({ records, withoutCanvas: true });
    await command(registration, "black-box-tail").handler("5");
    assert.match(logs[0], /Afterburner Black Box timeline/);
    assert.match(logs[0], /milestone\.tool-complete 42ms success=true/);
    assert.doesNotMatch(logs[0], /MUST NOT RENDER/);
});

test("runtime activation registers an isolated observer without modal support", async t => {
    await configureRuntimeEnvironment(t, "runtime-no-modal");
    let observer;
    const instance = await activate({ registerRuntimeObserver: value => { observer = value; return () => {}; } });
    t.after(() => instance?.dispose());
    assert.equal(observer.id, "black-box");
    assert.equal(instance.modal, null);
    assert.equal(typeof observer.onEvent, "function");
    await observer.onEvent({
        schemaVersion: 1,
        sequence: 1,
        timestamp: "2026-09-02T10:00:00.000Z",
        type: "model.request.completed",
        metadata: { sessionId: "session-one", model: "colosseum-prod/gpt-5-5", durationMs: 42, content: "RUNTIME SECRET" }
    });
    const records = await instance.service.tail({ limit: 10 });
    assert.doesNotMatch(JSON.stringify(records), /RUNTIME SECRET/);
    assert.equal(records.find(record => record.eventType === "model.request.completed")?.attributes.model,
        "colosseum-prod/gpt-5-5");
});

test("installed-like activation uses only the native modal runtime API", async t => {
    await configureRuntimeEnvironment(t, "runtime-installed-copy");
    const sourceRoot = fileURLToPath(new URL("..", import.meta.url));
    const installedRoot = await mkdtemp(join(tmpdir(), "black-box-installed-"));
    t.after(() => cleanup(installedRoot));
    await cp(sourceRoot, installedRoot, {
        recursive: true,
        filter: source => {
            const normalized = source.toLowerCase();
            const root = sourceRoot.toLowerCase();
            return !normalized.includes(`${root}.test-work`) && !normalized.includes(`${root}node_modules`);
        }
    });
    const copiedSurfaceSource = await readFile(join(installedRoot, "lib", "modal-surface.mjs"), "utf8");
    assert.doesNotMatch(copiedSurfaceSource, /(?:sdk[\\/]ui|src[\\/]runtime)/, "packaged Black Box must not reference repository UI SDK paths");

    const modalDefinitions = [];
    const ui = {
        ...createFakeRuntimeUI(),
        registerModalCanvas: definition => {
            modalDefinitions.push(definition);
            return { open: async () => definition.open(), close: async () => ({ ok: true }), dispose() {} };
        }
    };
    let observer;
    const moduleUrl = pathToFileURL(join(installedRoot, "runtime", "extension.mjs")).href + `?installed=${Date.now()}`;
    const { activate: activateInstalled } = await import(moduleUrl);
    const instance = await activateInstalled({
        ui,
        registerRuntimeObserver: value => { observer = value; return () => {}; }
    });
    t.after(() => instance?.dispose());

    assert.equal(observer.id, "black-box");
    assert.equal(modalDefinitions[0].id, "afterburner-black-box-live");
    assert.equal(instance.enterprise, undefined);
    assert.equal(instance.observability, undefined);
    assert.equal((await instance.modal.open()).document.surfaceId, "afterburner-black-box-live");
    await instance.dispose();
});

test("runtime modal activation uses a low-frequency reconciliation poll", async t => {
    await configureRuntimeEnvironment(t, "runtime-modal-activation-latency");
    const intervals = [];
    const originalSetInterval = globalThis.setInterval;
    globalThis.setInterval = (handler, delayMs, ...args) => {
        intervals.push(delayMs);
        return originalSetInterval(handler, delayMs, ...args);
    };
    t.after(() => { globalThis.setInterval = originalSetInterval; });

    const instance = await activate({
        ui: createFakeRuntimeUI(),
        registerRuntimeObserver: () => () => {},
        registerModalCanvas: definition => ({ open: async () => definition.open(), close: async () => ({ ok: true }), dispose() {} })
    });
    t.after(() => instance?.dispose());

    assert.ok(MODAL_ACTIVATION_POLL_MS >= 1000, `modal activation poll ${MODAL_ACTIVATION_POLL_MS}ms is unexpectedly frequent`);
    assert.ok(intervals.includes(MODAL_ACTIVATION_POLL_MS));
    const opened = await instance.service.requestModalOpen({ timeoutMs: 2000 });
    assert.equal(opened.ok, true);
    await instance.dispose();
});

test("runtime modal activation is woken by filesystem queue changes", async t => {
    await configureRuntimeEnvironment(t, "runtime-modal-activation-watch");
    const originalSetInterval = globalThis.setInterval;
    globalThis.setInterval = (handler, _delayMs, ...args) => originalSetInterval(handler, 10_000, ...args);
    t.after(() => { globalThis.setInterval = originalSetInterval; });

    let openedAt = 0;
    const startedAt = Date.now();
    const instance = await activate({
        ui: createFakeRuntimeUI(),
        registerRuntimeObserver: () => () => {},
        registerModalCanvas: definition => ({
            open: async () => { openedAt = Date.now(); return definition.open(); },
            close: async () => ({ ok: true }),
            dispose() {}
        })
    });
    t.after(() => instance?.dispose());

    const pending = instance.service.requestModalOpen({ timeoutMs: 2000 });
    await waitFor(() => openedAt > 0, 1000);
    assert.ok(openedAt - startedAt < 1000, `watched modal activation took ${openedAt - startedAt}ms`);
    assert.equal((await pending).ok, true);
    await instance.dispose();
});

test("runtime modal prefers the top-level native registrar when available", async t => {
    await configureRuntimeEnvironment(t, "runtime-modal-native-preferred");
    const nativeDefinitions = [];
    const uiDefinitions = [];
    const instance = await activate({
        ui: { ...createFakeRuntimeUI(), registerModalCanvas: definition => { uiDefinitions.push(definition); return { dispose() {} }; } },
        registerRuntimeObserver: () => () => {},
        registerModalCanvas: definition => {
            nativeDefinitions.push(definition);
            return { open: async () => definition.open(), close: async () => ({ ok: true }), dispose() {} };
        }
    });
    t.after(() => instance?.dispose());

    assert.equal(nativeDefinitions[0].id, "afterburner-black-box-live");
    assert.equal(uiDefinitions.length, 0);
    assert.ok(instance.modal);
    await instance.dispose();
});

test("runtime activation never auto-opens the modal", async t => {
    await configureRuntimeEnvironment(t, "runtime-modal-no-auto-open");
    process.env.AFTERBURNER_BLACK_BOX_OPEN_MODAL_ON_START = "1";
    process.env.AFTERBURNER_BLACK_BOX_OPEN_SURFACE_ON_START = "1";
    let openCount = 0;
    const instance = await activate({
        ui: { ...createFakeRuntimeUI(), registerModalCanvas: definition => ({ open: async () => { openCount++; return definition.open(); }, close: async () => ({ ok: true }), dispose() {} }) },
        registerRuntimeObserver: () => () => {},
        registerModalCanvas: definition => ({ open: async () => { openCount++; return definition.open(); }, close: async () => ({ ok: true }), dispose() {} })
    });
    t.after(() => instance?.dispose());
    await delay(250);
    assert.equal(openCount, 0);
    await instance.dispose();
});

test("runtime modal opens, refreshes from accepted metadata events, and handles actions defensively", async t => {
    await configureRuntimeEnvironment(t, "runtime-modal");
    let observer;
    let modalDefinition;
    let handleCloseCount = 0;
    let handleDisposeCount = 0;
    const instance = await activate({
        ui: createFakeRuntimeUI(),
        registerRuntimeObserver: value => { observer = value; return () => {}; },
        registerModalCanvas: definition => {
            modalDefinition = definition;
            return {
                open: async () => modalDefinition.open(),
                close: async () => { handleCloseCount++; return { ok: true }; },
                dispose: () => { handleDisposeCount++; }
            };
        }
    });
    t.after(() => instance?.dispose());

    assert.equal(observer.id, "black-box");
    assert.equal(modalDefinition.id, "afterburner-black-box-live");
    assert.deepEqual(modalDefinition.actions.map(action => action.name), ["refresh", "doctor", "export", "close"]);

    const openFrame = await modalDefinition.open();
    assert.equal(openFrame.title, "Afterburner Black Box Live");
    assert.match(openFrame.body, /Storage usage: 0% of/);
    assert.match(openFrame.body, /Signal trend: ▁▁▁▁ no recent events/);
    assert.match(openFrame.body, /Status cards/);
    assert.doesNotMatch(openFrame.body, /Needs attention:/);
    assert.match(openFrame.body, /Selected event/);
    assert.match(openFrame.body, /No metadata event selected/);
    assert.match(openFrame.body, /Metadata timeline table/);
    assert.match(openFrame.body, /Details/);
    assert.match(openFrame.body, /No metadata events recorded yet/);
    assert.equal(openFrame.document.surfaceId, "afterburner-black-box-live");
    assert.equal(openFrame.document.root.kind, "dialog");
    assert.match(JSON.stringify(openFrame.document), /bb-modal-breadcrumb/);
    assert.match(JSON.stringify(openFrame.document), /bb-modal-surface/);
    assert.match(JSON.stringify(openFrame.document), /bb-modal-viewport/);
    assert.match(JSON.stringify(openFrame.document), /"kind":"split"/);
    assert.match(JSON.stringify(openFrame.document), /"kind":"scroll"/);
    assert.match(JSON.stringify(openFrame.document), /bb-modal-status-cards/);
    assert.match(JSON.stringify(openFrame.document), /"kind":"statusGrid"/);
    assert.match(JSON.stringify(openFrame.document), /bb-modal-action-bar/);
    assert.match(JSON.stringify(openFrame.document), /"kind":"actionBar"/);
    assert.match(JSON.stringify(openFrame.document), /bb-modal-storage-progress/);
    assert.match(JSON.stringify(openFrame.document), /bb-modal-signal-trend/);
    assert.match(JSON.stringify(openFrame.document), /"kind":"sparkline"/);
    assert.match(JSON.stringify(openFrame.document), /bb-modal-timeline-table/);
    assert.match(JSON.stringify(openFrame.document), /bb-modal-detail-panel/);

    const updates = [];
    let closeCount = 0;
    const controls = {
        update: async frame => { updates.push(frame); return { ok: true, frame }; },
        close: async () => { closeCount++; return { ok: true }; }
    };
    const refresh = modalDefinition.actions.find(action => action.name === "refresh");
    const doctor = modalDefinition.actions.find(action => action.name === "doctor");
    const exportAction = modalDefinition.actions.find(action => action.name === "export");
    const close = modalDefinition.actions.find(action => action.name === "close");
    await refresh.handler({}, controls);
    assert.equal(updates.at(-1).title, "Afterburner Black Box Live");
    assert.equal(updates.at(-1).document.root.kind, "dialog");
    await doctor.handler({}, controls);
    assert.equal(updates.at(-1).title, "Afterburner Black Box Doctor");
    assert.match(updates.at(-1).body, /Doctor/);
    assert.match(updates.at(-1).body, /"healthy"/);
    await exportAction.handler({}, controls);
    assert.equal(updates.at(-1).title, "Afterburner Black Box Export");
    assert.match(updates.at(-1).body, /Export/);
    assert.match(JSON.stringify(updates.at(-1).document), /bb-modal-export-panel/);
    await close.handler({}, controls);
    assert.equal(closeCount, 1);
    await refresh.handler({}, {});
    await doctor.handler({}, {});
    await close.handler({}, {});

    const unsubscribe = await modalDefinition.subscribe(controls);
    const accepted = await observer.onEvent({
        schemaVersion: 1,
        sequence: 2,
        timestamp: "2026-09-02T10:00:01.000Z",
        type: "model.request.completed",
        metadata: {
            sessionId: "session-one",
            model: "colosseum-prod/gpt-5-5",
            durationMs: 42,
            success: true,
            prompt: "PROMPT SECRET",
            content: "RUNTIME SECRET",
            toolRequests: [{ arguments: "TOOL SECRET" }]
        }
    });
    assert.equal(accepted, true);
    await waitFor(() => updates.some(frame => /model\.request\.completed[\s\S]*42ms[\s\S]*true/.test(frame.body)));
    assert.ok(updates.some(frame => JSON.stringify(frame.document ?? {}).includes("bb-modal-timeline-table")));
    assert.doesNotMatch(JSON.stringify(updates), /PROMPT SECRET|RUNTIME SECRET|TOOL SECRET/);
    const updateCount = updates.length;
    unsubscribe();
    assert.equal(await observer.onEvent({
        schemaVersion: 1,
        sequence: 3,
        timestamp: "2026-09-02T10:00:02.000Z",
        type: "extension.activated",
        metadata: { extensionId: "black-box", extensionKind: "afterburner", state: "active" }
    }), false);
    await delay(350);
    assert.equal(updates.length, updateCount, "unsubscribed modal must not continue receiving live updates");

    const records = await instance.service.tail({ limit: 10 });
    assert.doesNotMatch(JSON.stringify(records), /PROMPT SECRET|RUNTIME SECRET|TOOL SECRET/);
    assert.equal(records.find(record => record.eventType === "extension.activated"), undefined);

    await instance.dispose();
    assert.equal(handleCloseCount, 1);
    assert.equal(handleDisposeCount, 1);
});

test("runtime modal surfaces actionable health warnings", () => {
    const frame = buildModalFrame(createFakeRuntimeUI(), {
        status: {
            schemaVersion: 1,
            enabled: true,
            mode: "runtime",
            storage: { segmentCount: 3, segmentBytes: 128, retentionBlockedBytes: 0 },
            queue: { records: 2, bytes: 64, droppedRecords: 7, writeErrors: 1 },
            analytics: { anomalyCount: 2, milestoneCount: 4, totalRecords: 13 },
            native: { enabled: true, configured: true },
            recentSignals: []
        },
        records: [{ recordId: "rec_warn", timestamp: "2026-01-01T00:00:00.000Z", kind: "anomaly", eventType: "ui.latency", severity: "warning", attributes: { durationMs: 321, success: false } }]
    });
    assert.match(frame.body, /Storage usage: 0% of/);
    assert.match(frame.body, /Signal trend: █/);
    assert.match(frame.body, /Needs attention: 1 queue write error\(s\) · 7 dropped record\(s\) · 2 anomaly\/anomalies/);
    assert.match(frame.body, /Selected event/);
    assert.match(frame.body, /ui\.latency/);
    const document = JSON.stringify(frame.document);
    assert.match(document, /bb-modal-health-alert/);
    assert.match(document, /\"kind\":\"alert\"/);
    assert.match(document, /Black Box health warnings/);
});
