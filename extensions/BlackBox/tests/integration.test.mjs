import test from "node:test";
import assert from "node:assert/strict";
import { cp, mkdir, mkdtemp, readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { fileURLToPath, pathToFileURL } from "node:url";
import { setTimeout as delay } from "node:timers/promises";
import { DEFAULT_MAX_BYTES, DEFAULT_SEGMENT_BYTES, loadBlackBoxConfig, resolveNativeEventsPath } from "../lib/config.mjs";
import { buildSessionRegistration } from "../lib/session-extension.mjs";
import {
    BLACK_BOX_OBSERVABILITY_CAPABILITY,
    blackBoxObservabilitySinkDescriptor,
    registerEnterpriseSurface,
    subscribeObservability
} from "../lib/ui-surface.mjs";
import { activate } from "../runtime/extension.mjs";
import { cleanup, completeConfig, workDirectory } from "./helpers.mjs";

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
        openOnStart: process.env.AFTERBURNER_BLACK_BOX_OPEN_MODAL_ON_START
    };
    process.env.AFTERBURNER_HOME = home;
    process.env.AFTERBURNER_BLACK_BOX_CONFIG = configPath;
    delete process.env.AFTERBURNER_BLACK_BOX_OPEN_MODAL_ON_START;
    t.after(async () => {
        if (previous.home === undefined) delete process.env.AFTERBURNER_HOME;
        else process.env.AFTERBURNER_HOME = previous.home;
        if (previous.config === undefined) delete process.env.AFTERBURNER_BLACK_BOX_CONFIG;
        else process.env.AFTERBURNER_BLACK_BOX_CONFIG = previous.config;
        if (previous.openOnStart === undefined) delete process.env.AFTERBURNER_BLACK_BOX_OPEN_MODAL_ON_START;
        else process.env.AFTERBURNER_BLACK_BOX_OPEN_MODAL_ON_START = previous.openOnStart;
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
        protocol: "afterburner.ui",
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

test("session registration preserves command panels and the existing Black Box canvas", async () => {
    const { registration, canvasDefinition, logs } = await captureSessionRegistration();
    assert.equal(canvasDefinition.id, "afterburner-black-box");
    assert.equal(canvasDefinition.presentation, undefined, "the legacy canvas presentation must not be changed into a modal");
    assert.deepEqual(registration.commands.map(item => item.name), [
        "black-box", "black-box-modal", "black-box-tail", "black-box-tail-stop", "black-box-export", "black-box-doctor"
    ]);
    assert.deepEqual(canvasDefinition.actions.map(action => action.name), ["snapshot", "tail", "export", "doctor"]);
    assert.equal(logs.length, 0, "Black Box must not write timeline entries before explicit user action.");
    await command(registration, "black-box").handler();
    assert.match(logs[0], /Afterburner Black Box/);
    assert.match(logs[0], /Storage/);
    assert.match(logs[0], /Queue/);
    assert.match(logs[0], /1 anomalie/);
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

test("session extension uses the real session canvas RPC to open Black Box", async () => {
    const wrapper = await readFile(new URL("../com.github.copilot/extensions/BlackBox/extension.mjs", import.meta.url), "utf8");
    assert.match(wrapper, /extensions\/BlackBox\/extension\.mjs/);
    const source = await readFile(new URL("../extensions/BlackBox/extension.mjs", import.meta.url), "utf8");
    assert.match(source, /joinedSession\?\.rpc\?\.canvas/);
    assert.match(source, /canvasRpc\.list/);
    assert.match(source, /canvasRpc\.open/);
    assert.doesNotMatch(source, /copilotSdk\.openModalCanvas/);
    assert.match(source, /candidate\.extensionId/);
    assert.match(source, /candidate\.canvasId/);
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
    assert.deepEqual(logs, ["Black Box live modal opened."]);
});

test("black-box-modal command opens the registered runtime modal when queue is unavailable", async () => {
    const opens = [];
    const { registration, logs } = await captureSessionRegistration({
        service: { ...fakePanelService(), requestModalOpen: async () => { throw new Error("queue unavailable"); } },
        openModalCanvas: async (id, input) => {
            opens.push({ id, input });
            return { ok: true, fallback: false };
        }
    });
    await command(registration, "black-box-modal").handler();
    assert.deepEqual(opens, [{ id: "afterburner-black-box", input: {} }]);
    assert.deepEqual(logs, ["Black Box live modal opened."]);
});

test("black-box-modal treats Copilot canvas open snapshots as success", async () => {
    const { registration, logs } = await captureSessionRegistration({
        service: { ...fakePanelService(), requestModalOpen: async () => { throw new Error("queue unavailable"); } },
        openModalCanvas: async () => ({
            instanceId: "afterburner-black-box-session",
            canvasId: "afterburner-black-box"
        })
    });
    await command(registration, "black-box-modal").handler();
    assert.deepEqual(logs, ["Black Box live modal opened."]);
});

test("black-box-modal command displays returned brokerless fallback frame", async () => {
    const { registration, logs } = await captureSessionRegistration({
        service: { ...fakePanelService(), requestModalOpen: async () => { throw new Error("queue unavailable"); } },
        openModalCanvas: async () => ({
            ok: true,
            fallback: true,
            frame: {
                title: "Returned fallback title",
                status: "Returned fallback status",
                body: "Returned fallback body",
                footer: "Returned fallback footer"
            }
        })
    });
    await command(registration, "black-box-modal").handler();
    assert.deepEqual(logs, [[
        "Returned fallback title",
        "Returned fallback status",
        "Returned fallback body",
        "Returned fallback footer"
    ].join("\n")]);
    assert.doesNotMatch(logs[0], /opened using the host text fallback|live modal opened/i);

    const textFallback = await captureSessionRegistration({
        service: { ...fakePanelService(), requestModalOpen: async () => { throw new Error("queue unavailable"); } },
        openModalCanvas: async () => "Returned brokerless fallback text"
    });
    await command(textFallback.registration, "black-box-modal").handler();
    assert.deepEqual(textFallback.logs, ["Returned brokerless fallback text"]);
});

test("black-box-modal command provides explicit text fallback without overclaiming", async () => {
    const unavailable = await captureSessionRegistration({
        withoutCanvas: true,
        service: { ...fakePanelService(), requestModalOpen: async () => { throw new Error("queue unavailable"); } }
    });
    await command(unavailable.registration, "black-box-modal").handler();
    assert.match(unavailable.logs[0], /modal activation queue and canvas API are unavailable/);
    assert.match(unavailable.logs[0], /Showing text fallback/);
    assert.match(unavailable.logs[0], /Afterburner Black Box/);
    assert.doesNotMatch(unavailable.logs[0], /registered by the runtime extension/);

    const failing = await captureSessionRegistration({
        withoutCanvas: true,
        service: { ...fakePanelService(), requestModalOpen: async () => { throw new Error("queue unavailable"); } },
        openModalCanvas: async () => { throw new Error("Unknown modal canvas"); }
    });
    await command(failing.registration, "black-box-modal").handler();
    assert.match(failing.logs[0], /registered runtime modal is unavailable/);
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
        metadata: { model: "colosseum-prod/gpt-5-5", durationMs: 42, content: "RUNTIME SECRET" }
    });
    const records = await instance.service.tail({ limit: 10 });
    assert.doesNotMatch(JSON.stringify(records), /RUNTIME SECRET/);
    assert.equal(records.find(record => record.eventType === "model.request.completed")?.attributes.model,
        "colosseum-prod/gpt-5-5");
});

function enterpriseTestService(initialRecords = []) {
    const listeners = new Set();
    const records = [...initialRecords];
    return {
        async status() {
            return {
                schemaVersion: 1,
                enabled: true,
                mode: "runtime",
                storage: { maxBytes: 1024 * 1024, segmentBytes: 4096, segmentCount: 1, retentionBlockedBytes: 0 },
                queue: { records: 0, bytes: 0, droppedRecords: 0, writeErrors: 0 },
                analytics: { anomalyCount: records.filter(record => record.kind === "anomaly").length, milestoneCount: records.filter(record => record.kind === "milestone").length, totalRecords: records.length },
                native: { enabled: false, configured: false },
                recentSignals: records.filter(record => record.kind === "anomaly" || record.kind === "milestone").slice(0, 10)
            };
        },
        async tail() { return [...records].reverse(); },
        async doctor() { return { healthy: true, checked: "metadata-only" }; },
        async exportBundle() { return { path: "C:\\private\\black-box-export.zip", manifest: { recordCount: records.length } }; },
        subscribeRecords(listener) { listeners.add(listener); return () => listeners.delete(listener); },
        emit(record) {
            records.push(record);
            for (const listener of listeners) listener(record);
        },
        async observeRuntime(event) {
            this.observed = event;
            return true;
        },
        observed: null
    };
}

function enterpriseRecord(id, overrides = {}) {
    return {
        schemaVersion: 1,
        recordId: id,
        kind: overrides.kind ?? "event",
        timestamp: overrides.timestamp ?? "2026-09-02T10:00:00.000Z",
        observedAt: overrides.observedAt ?? "2026-09-02T10:00:00.000Z",
        eventType: overrides.eventType ?? "model.request.completed",
        severity: overrides.severity ?? "info",
        source: { kind: "runtime-observer", processRef: "ref_process" },
        correlation: { eventRef: "ref_event" },
        attributes: { model: "colosseum-prod/gpt-5-5", durationMs: 42, success: true, content: "SECRET BODY", ...overrides.attributes },
        bodyReferences: [{ field: "data.content", byteLength: 11 }]
    };
}

test("enterprise surface defines schema, accessible tree, actions, fallback, patches, and reconnect", async () => {
    const service = enterpriseTestService([
        enterpriseRecord("rec_a"),
        enterpriseRecord("rec_b", { kind: "anomaly", eventType: "ui.host.recovery", severity: "warning", attributes: { recoveryState: "reconnected" } })
    ]);
    const registered = [];
    const rendered = [];
    const patches = [];
    const enterprise = await registerEnterpriseSurface({
        ui: createFakeRuntimeUI(),
        registerSurface: async descriptor => { registered.push(descriptor); return { ok: true }; },
        renderSurface: async (id, document) => { rendered.push({ id, document }); return { ok: true }; },
        patchSurface: async (id, patch) => { patches.push({ id, patch }); return { ok: true }; },
        closeSurface: async () => ({ ok: true })
    }, service);

    assert.equal(enterprise.surface.id, "afterburner-black-box-live");
    assert.equal(registered[0].kind, "panel");
    assert.deepEqual(registered[0].actions.map(action => action.id), ["refresh", "doctor", "export", "select", "filter", "sort", "close"]);
    assert.equal(registered[0].metadata.privacy.metadataOnly, true);
    assert.ok(registered[0].metadata.grants.some(grant => grant.capability === "ui.observability.black-box.sink"));

    const opened = await enterprise.open({ filter: "model", sort: { field: "timestamp", direction: "desc" } });
    assert.equal(opened.ok, true);
    const tree = JSON.stringify(opened.document);
    assert.match(tree, /commandPalette/);
    assert.match(tree, /Timeline table/);
    assert.match(tree, /virtualization/);
    assert.match(tree, /progressbar/);
    assert.match(tree, /tablist/);
    assert.match(tree, /Black Box command palette/);
    assert.doesNotMatch(tree, /SECRET BODY|private\\black-box/);
    assert.equal(rendered[0].id, "afterburner-black-box-live");

    await enterprise.surface.invoke("select", { recordId: "rec_a" });
    assert.equal(enterprise.state.selectedRecordId, "rec_a");
    service.emit(enterpriseRecord("rec_c", { timestamp: "2026-09-02T10:00:01.000Z", attributes: { durationMs: 7, success: false, prompt: "PROMPT SECRET" } }));
    await waitFor(() => patches.length > 0);
    assert.equal(patches[0].id, "afterburner-black-box-live");
    assert.equal(patches[0].patch.operations[0].op, "replace");
    assert.equal(patches[0].patch.operations[0].path, "/root");
    assert.doesNotMatch(JSON.stringify(patches), /PROMPT SECRET|SECRET BODY|private\\black-box/);

    const recovered = await enterprise.recover();
    assert.equal(recovered.ok, true);
    assert.equal(enterprise.state.lifecycle.reconnectCount, 1);
    assert.equal(enterprise.state.selectedRecordId, "rec_a");
    await enterprise.dispose();
});

test("enterprise surface degrades deterministically when host or capabilities are absent", async () => {
    const noHost = await registerEnterpriseSurface({}, enterpriseTestService([enterpriseRecord("rec_fallback")]));
    const opened = await noHost.open();
    assert.equal(opened.fallback, true);
    assert.match(opened.text, /Afterburner Black Box Enterprise/);
    assert.match(opened.text, /deterministic metadata-only fallback/);
    assert.equal(noHost.surface.fallback().text, opened.text);

    const denied = await registerEnterpriseSurface({ hasCapability: capability => capability !== "ui.surface.panel" }, enterpriseTestService());
    assert.match(denied.fallbackText, /required UI capability was denied/);
    const deniedOpen = await denied.open();
    assert.equal(deniedOpen.fallback, true);
    assert.match(deniedOpen.text, /deterministic metadata-only fallback/);
});

test("required UI grant denial blocks the enterprise surface without leaking denial details", async () => {
    const registered = [];
    const rendered = [];
    const enterprise = await registerEnterpriseSurface({
        requestCapabilities: async declarations => {
            const denied = declarations
                .filter(grant => grant.capability === "ui.surface.panel")
                .map(grant => ({ capability: grant.capability, reason: "enterprise-policy-denied", detail: "PROMPT SECRET" }));
            return denied.length ? { granted: false, denied } : { granted: true };
        },
        registerSurface: async descriptor => { registered.push(descriptor); return { ok: true }; },
        renderSurface: async (id, document) => { rendered.push({ id, document }); return { ok: true }; }
    }, enterpriseTestService([enterpriseRecord("rec_required_deny", { attributes: { prompt: "PROMPT SECRET" } })]));

    assert.equal(enterprise.denied, "enterprise-policy-denied");
    assert.equal(enterprise.descriptorResult.denied, true);
    assert.equal(registered.length, 0);
    const opened = await enterprise.open();
    assert.equal(opened.fallback, true);
    assert.match(opened.text, /required UI capability was denied/);
    assert.equal(rendered.length, 0);
    assert.doesNotMatch(JSON.stringify(opened), /PROMPT SECRET/);
});

test("optional observability grant denial keeps the enterprise surface usable", async () => {
    const requested = [];
    const registered = [];
    const rendered = [];
    const service = enterpriseTestService([enterpriseRecord("rec_optional_deny", { attributes: { prompt: "PROMPT SECRET" } })]);
    const enterprise = await registerEnterpriseSurface({
        ui: createFakeRuntimeUI(),
        requestCapabilities: async declarations => {
            requested.push(declarations.map(grant => grant.capability));
            const denied = declarations
                .filter(grant => grant.capability === BLACK_BOX_OBSERVABILITY_CAPABILITY)
                .map(grant => ({ capability: grant.capability, reason: "optional-policy-denied", detail: "TOOL SECRET" }));
            return denied.length ? { granted: false, denied } : { granted: true };
        },
        registerSurface: async descriptor => { registered.push(descriptor); return { ok: true }; },
        renderSurface: async (id, document) => { rendered.push({ id, document }); return { ok: true }; },
        patchSurface: async () => ({ ok: true }),
        closeSurface: async () => ({ ok: true })
    }, service);

    assert.equal(enterprise.denied, null);
    assert.equal(enterprise.state.observability.enabled, false);
    assert.equal(enterprise.state.observability.denials[0].reason, "optional-policy-denied");
    assert.ok(requested.some(capabilities => capabilities.includes(BLACK_BOX_OBSERVABILITY_CAPABILITY)));
    assert.equal(registered.length, 1);
    const opened = await enterprise.open();
    assert.equal(opened.ok, true);
    assert.equal(opened.fallback, false);
    const tree = JSON.stringify(opened.document);
    assert.match(tree, /Optional observability subscription disabled/);
    assert.match(tree, /observability sink disabled/);
    assert.doesNotMatch(tree, /PROMPT SECRET|TOOL SECRET/);
    assert.equal(rendered.length, 1);
    await enterprise.dispose();
});

test("UI observability sink accepts metadata only and redacts payload-shaped fields", async () => {
    const service = enterpriseTestService();
    const requested = [];
    let registeredSink;
    const dispose = await subscribeObservability({
        requestCapabilities: async declarations => { requested.push(declarations.map(grant => grant.capability)); return { granted: true }; },
        registerObservabilitySink: sink => { registeredSink = sink; return () => { registeredSink = null; }; }
    }, service);
    assert.deepEqual(requested, [[BLACK_BOX_OBSERVABILITY_CAPABILITY]]);
    assert.equal(registeredSink.descriptor.id, blackBoxObservabilitySinkDescriptor().id);
    await registeredSink.publish({
        schemaVersion: 1,
        protocol: "afterburner.ui",
        revision: 1,
        type: "ui.host.lifecycle",
        sinkId: "black-box.ui.events",
        envelopeId: "env-1",
        envelopeKind: "component.snapshot",
        surfaceId: "afterburner-black-box-live",
        at: "2026-09-02T10:00:00.000Z",
        attributes: {
            state: JSON.stringify("rendered"),
            durationMs: JSON.stringify(42),
            prompt: JSON.stringify("PROMPT SECRET"),
            toolArguments: JSON.stringify({ secret: "TOOL SECRET" }),
            rawPath: JSON.stringify("C:\\private\\workspace")
        }
    });
    assert.equal(service.observed.type, "ui.host.lifecycle");
    assert.equal(service.observed.data.state, "rendered");
    assert.equal(service.observed.data.durationMs, 42);
    assert.equal(service.observed.data.surfaceId, "afterburner-black-box-live");
    assert.doesNotMatch(JSON.stringify(service.observed), /PROMPT SECRET|TOOL SECRET|private\\workspace/);
    dispose();
});

test("UI observability optional grant denial disables only the sink and records metadata diagnostic", async () => {
    const service = enterpriseTestService();
    let registerCalled = false;
    const result = await subscribeObservability({
        requestCapabilities: async declarations => {
            assert.deepEqual(declarations.map(grant => grant.capability), [BLACK_BOX_OBSERVABILITY_CAPABILITY]);
            return {
                granted: false,
                denied: [{ capability: BLACK_BOX_OBSERVABILITY_CAPABILITY, reason: "denied C:\\private\\SECRET", detail: "C:\\private\\SECRET" }]
            };
        },
        registerObservabilitySink: () => { registerCalled = true; }
    }, service);

    assert.equal(registerCalled, false);
    assert.equal(result.denied, true);
    assert.equal(result.disabled, true);
    assert.equal(result.reason, "optional-capability-denied");
    assert.equal(result.diagnostic, "optional-observability-sink-denied");
    assert.equal(service.observed.type, "ui.observability.subscription");
    assert.equal(service.observed.data.state, "disabled");
    assert.equal(service.observed.data.sinkId, "black-box.ui.events");
    assert.equal(service.observed.data.securityDecision, "deny");
    assert.doesNotMatch(JSON.stringify(service.observed), /SECRET|private/);
});

test("UI observability host absence preserves legacy no-op behavior", async () => {
    const service = enterpriseTestService();
    const result = await subscribeObservability({}, service);
    assert.equal(result, null);
    assert.equal(service.observed, null);
});

test("installed-like activation uses runtime api.ui without repository UI imports", async t => {
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
    const copiedSurfaceSource = await readFile(join(installedRoot, "lib", "ui-surface.mjs"), "utf8");
    assert.doesNotMatch(copiedSurfaceSource, /(?:sdk[\\/]ui|src[\\/]runtime)/, "packaged Black Box must not reference repository UI SDK paths");

    const registered = [];
    const rendered = [];
    const modalDefinitions = [];
    const brokerHandle = { id: "afterburner-black-box-live", broker: "mock-runtime" };
    const ui = {
        ...createFakeRuntimeUI(),
        registerSurface: async descriptor => { registered.push(descriptor); return { ok: true, handle: brokerHandle, descriptor }; },
        renderSurface: async (id, document) => { rendered.push({ id, document }); return { ok: true, handle: brokerHandle }; },
        patchSurface: async () => ({ ok: true, handle: brokerHandle }),
        closeSurface: async () => ({ ok: true, handle: brokerHandle }),
        registerModalCanvas: definition => {
            modalDefinitions.push(definition);
            return { open: async () => definition.open(), close: async () => ({ ok: true }), dispose() {} };
        },
        registerObservabilitySink: () => ({ dispose() {} })
    };
    let observer;
    const moduleUrl = pathToFileURL(join(installedRoot, "runtime", "extension.mjs")).href + `?installed=${Date.now()}`;
    const { activate: activateInstalled } = await import(moduleUrl);
    const instance = await activateInstalled({
        ui,
        requestCapabilities: async () => ({ granted: true }),
        registerRuntimeObserver: value => { observer = value; return () => {}; }
    });
    t.after(() => instance?.dispose());

    assert.equal(observer.id, "black-box");
    assert.equal(registered[0].id, "afterburner-black-box-live");
    assert.equal(registered[0].ownerExtensionId, "black-box");
    assert.equal(instance.enterprise.descriptorResult.handle, brokerHandle);
    const opened = await instance.enterprise.open({});
    assert.equal(opened.ok, true);
    assert.equal(opened.document.surfaceId, "afterburner-black-box-live");
    assert.equal(rendered[0].id, "afterburner-black-box-live");
    assert.equal(rendered[0].document.surfaceId, "afterburner-black-box-live");
    assert.equal(modalDefinitions[0].id, "afterburner-black-box-live");
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
    assert.deepEqual(modalDefinition.actions.map(action => action.name), ["refresh", "doctor", "close"]);

    const openFrame = await modalDefinition.open();
    assert.equal(openFrame.title, "Afterburner Black Box Live");
    assert.match(openFrame.body, /Status cards/);
    assert.match(openFrame.body, /Metadata timeline table/);
    assert.match(openFrame.body, /Details/);
    assert.match(openFrame.body, /No metadata events recorded yet/);
    assert.equal(openFrame.document.surfaceId, "afterburner-black-box-live");
    assert.equal(openFrame.document.root.kind, "dialog");
    assert.match(JSON.stringify(openFrame.document), /bb-modal-status-cards/);
    assert.match(JSON.stringify(openFrame.document), /bb-modal-timeline-table/);
    assert.match(JSON.stringify(openFrame.document), /bb-modal-detail-panel/);

    const updates = [];
    let closeCount = 0;
    const controls = {
        update: async frame => { updates.push(frame); return { ok: true, frame }; },
        close: async () => { closeCount++; return { ok: true }; }
    };
    const [refresh, doctor, close] = modalDefinition.actions;
    await refresh.handler({}, controls);
    assert.equal(updates.at(-1).title, "Afterburner Black Box Live");
    assert.equal(updates.at(-1).document.root.kind, "dialog");
    await doctor.handler({}, controls);
    assert.equal(updates.at(-1).title, "Afterburner Black Box Doctor");
    assert.match(updates.at(-1).body, /Doctor/);
    assert.match(updates.at(-1).body, /"healthy"/);
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
    await observer.onEvent({
        schemaVersion: 1,
        sequence: 3,
        timestamp: "2026-09-02T10:00:02.000Z",
        type: "extension.activated",
        metadata: { extensionId: "black-box", extensionKind: "afterburner", state: "active" }
    });
    await delay(350);
    assert.equal(updates.length, updateCount, "unsubscribed modal must not continue receiving live updates");

    const records = await instance.service.tail({ limit: 10 });
    assert.doesNotMatch(JSON.stringify(records), /PROMPT SECRET|RUNTIME SECRET|TOOL SECRET/);
    assert.deepEqual(records.find(record => record.eventType === "extension.activated")?.attributes, {
        extensionId: "black-box", extensionKind: "afterburner", state: "active"
    });

    await instance.dispose();
    assert.equal(handleCloseCount, 1);
    assert.equal(handleDisposeCount, 1);
});
