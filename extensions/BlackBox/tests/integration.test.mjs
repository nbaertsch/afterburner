import test from "node:test";
import assert from "node:assert/strict";
import { mkdir, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { DEFAULT_MAX_BYTES, DEFAULT_SEGMENT_BYTES, loadBlackBoxConfig, resolveNativeEventsPath } from "../lib/config.mjs";
import { buildSessionRegistration } from "../lib/session-extension.mjs";
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

test("session registration exposes all commands and the Black Box canvas", async () => {
    let canvasDefinition;
    let registration;
    const logs = [];
    const service = {
        status: async () => ({
            storage: { segmentCount: 2, segmentBytes: 100 },
            queue: { droppedRecords: 0 },
            analytics: { anomalyCount: 1 }
        }),
        tail: async () => [],
        exportBundle: async () => ({ path: "bundle", manifest: { recordCount: 0 } }),
        doctor: async () => ({ healthy: true })
    };
    const createCanvas = definition => { canvasDefinition = definition; return definition; };
    const joinSession = async options => {
        registration = options;
        return { log: async message => logs.push(message) };
    };
    await buildSessionRegistration({ service, createCanvas, joinSession });
    assert.equal(canvasDefinition.id, "afterburner-black-box");
    assert.deepEqual(registration.commands.map(command => command.name), [
        "black-box", "black-box-tail", "black-box-tail-stop", "black-box-export", "black-box-doctor"
    ]);
    assert.deepEqual(canvasDefinition.actions.map(action => action.name), ["snapshot", "tail", "export", "doctor"]);
    assert.equal(logs.length, 0, "Black Box must not write timeline entries before explicit user action.");
    await registration.commands[0].handler();
    assert.match(logs[0], /2 segment/);
});

test("runtime activation registers an isolated observer definition", async t => {
    const home = await workDirectory("runtime");
    t.after(() => cleanup(home));
    await mkdir(join(home, "config"), { recursive: true });
    const configPath = join(home, "config", "black-box.json");
    await writeFile(configPath, JSON.stringify(completeConfig({ native: { enabled: false } })), "utf8");
    const previous = {
        home: process.env.AFTERBURNER_HOME,
        config: process.env.AFTERBURNER_BLACK_BOX_CONFIG
    };
    process.env.AFTERBURNER_HOME = home;
    process.env.AFTERBURNER_BLACK_BOX_CONFIG = configPath;
    let observer;
    try {
        const instance = await activate({ registerRuntimeObserver: value => { observer = value; return () => {}; } });
        assert.equal(observer.id, "black-box");
        assert.equal(typeof observer.onEvent, "function");
        await observer.onEvent({
            schemaVersion: 1, sequence: 1, timestamp: "2026-09-02T10:00:00.000Z",
            type: "model.request.completed",
            metadata: { model: "colosseum-prod/gpt-5-5", durationMs: 42, content: "RUNTIME SECRET" }
        });
        await observer.onEvent({
            schemaVersion: 1, sequence: 2, timestamp: "2026-09-02T10:00:01.000Z",
            type: "extension.activated",
            metadata: { extensionId: "black-box", extensionKind: "afterburner", state: "active" }
        });
        const records = await instance.service.tail({ limit: 10 });
        assert.doesNotMatch(JSON.stringify(records), /RUNTIME SECRET/);
        assert.equal(records.find(record => record.eventType === "model.request.completed")?.attributes.model,
            "colosseum-prod/gpt-5-5");
        assert.deepEqual(records.find(record => record.eventType === "extension.activated")?.attributes, {
            extensionId: "black-box", extensionKind: "afterburner", state: "active"
        });
        await instance.dispose();
    } finally {
        if (previous.home === undefined) delete process.env.AFTERBURNER_HOME;
        else process.env.AFTERBURNER_HOME = previous.home;
        if (previous.config === undefined) delete process.env.AFTERBURNER_BLACK_BOX_CONFIG;
        else process.env.AFTERBURNER_BLACK_BOX_CONFIG = previous.config;
    }
});
