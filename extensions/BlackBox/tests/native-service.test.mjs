import test from "node:test";
import assert from "node:assert/strict";
import { appendFile, mkdir, writeFile } from "node:fs/promises";
import { dirname, join } from "node:path";
import { startBlackBoxService } from "../lib/service.mjs";
import { cleanup, completeConfig, workDirectory } from "./helpers.mjs";

function nativeEvent(id, content) {
    return JSON.stringify({
        id,
        parentId: "parent",
        timestamp: "2026-09-02T10:00:00.000Z",
        type: "user.message",
        data: { turnId: `turn-${id}`, interactionId: `interaction-${id}`, content }
    });
}

test("native JSONL tailing persists durable byte offsets and metadata references only", async t => {
    const root = await workDirectory("native");
    t.after(() => cleanup(root));
    const eventsPath = join(root, "native", "events.jsonl");
    await mkdir(dirname(eventsPath), { recursive: true });
    const first = `${nativeEvent("one", "SECRET ONE")}\n`;
    const second = `${nativeEvent("two", "SECRET TWO")}\n`;
    await writeFile(eventsPath, first + second.slice(0, 20), "utf8");
    const config = completeConfig({
        storage: { maxBytes: 100000, segmentBytes: 4096, maxRecordBytes: 2048 },
        queue: { maxRecords: 100, maxBytes: 100000 },
        native: { maxReadBytes: 4096, maxLineBytes: 65536, maxEventsPerPoll: 100, startPosition: "start" }
    });
    const options = {
        root,
        config,
        nativeEventsPath: eventsPath,
        startTailer: false,
        salt: "stable-test-salt",
        env: { COPILOT_AGENT_SESSION_ID: "session-secret" },
        processId: 21
    };
    const firstService = await startBlackBoxService({ ...options, runId: "first" });
    assert.equal(await firstService.pollNative(), 1);
    await appendFile(eventsPath, second.slice(20), "utf8");
    assert.equal(await firstService.pollNative(), 1);
    const firstTail = await firstService.tail({ limit: 20 });
    const serialized = JSON.stringify(firstTail);
    assert.doesNotMatch(serialized, /SECRET ONE|SECRET TWO|session-secret/);
    const nativeRecords = firstTail.filter(record => record.kind === "event" && record.eventType === "user.message");
    assert.equal(nativeRecords.length, 2);
    assert.ok(nativeRecords.every(record => Number.isSafeInteger(record.source.byteStart) && record.source.byteEnd > record.source.byteStart));
    const tailStatePath = firstService.tailer.statePath;
    await firstService.close();

    await appendFile(eventsPath, `${nativeEvent("three", "SECRET THREE")}\n`, "utf8");
    await writeFile(tailStatePath, "{corrupt", "utf8");
    const recoveredService = await startBlackBoxService({ ...options, runId: "second" });
    assert.equal(recoveredService.tailer.status().stateRecoveries, 1);
    assert.equal(await recoveredService.pollNative(), 1);
    const all = await recoveredService.tail({ limit: 50 });
    assert.equal(all.filter(record => record.kind === "event" && record.eventType === "user.message").length, 3);
    assert.doesNotMatch(JSON.stringify(all), /SECRET THREE/);
    await recoveredService.close();
});

test("native tailing ignores Black Box UI self-noise", async t => {
    const root = await workDirectory("native-self-noise");
    t.after(() => cleanup(root));
    const eventsPath = join(root, "native", "events.jsonl");
    await mkdir(dirname(eventsPath), { recursive: true });
    const events = [
        { id: "modal", timestamp: "2026-09-02T10:00:00.000Z", type: "ui.modal_canvas.updated", data: { modalId: "afterburner-black-box-live", revision: 7 } },
        { id: "host", timestamp: "2026-09-02T10:00:01.000Z", type: "ui.host.patch", data: { surfaceId: "afterburner-black-box-live", revision: 8 } },
        { id: "other", timestamp: "2026-09-02T10:00:02.000Z", type: "ui.modal_canvas.updated", data: { modalId: "other-modal", revision: 9 } }
    ];
    await writeFile(eventsPath, `${events.map(event => JSON.stringify(event)).join("\n")}\n`, "utf8");
    const service = await startBlackBoxService({
        root,
        config: completeConfig({
            storage: { maxBytes: 100000, segmentBytes: 4096, maxRecordBytes: 2048 },
            queue: { maxRecords: 100, maxBytes: 100000 },
            native: { maxReadBytes: 4096, maxLineBytes: 65536, maxEventsPerPoll: 100, startPosition: "start" }
        }),
        nativeEventsPath: eventsPath,
        startTailer: false,
        salt: "stable-test-salt",
        env: { COPILOT_AGENT_SESSION_ID: "session" },
        processId: 24,
        runId: "self-noise"
    });
    assert.equal(await service.pollNative(), 3);
    const records = await service.tail({ limit: 20 });
    assert.equal(records.some(record => record.attributes.modalId === "afterburner-black-box-live"), false);
    assert.equal(records.some(record => record.attributes.surfaceId === "afterburner-black-box-live"), false);
    assert.equal(records.filter(record => record.eventType === "ui.modal_canvas.updated").length, 1);
    await service.close();
});

test("runtime observer accepts only scoped current-session events", async t => {
    const root = await workDirectory("runtime-scope");
    t.after(() => cleanup(root));
    const service = await startBlackBoxService({
        root,
        config: completeConfig({ native: { enabled: false } }),
        salt: "stable-test-salt",
        env: { SESSION_ID: "current-session" },
        processId: 25,
        runId: "runtime-scope"
    });
    assert.equal(await service.observeRuntime({ type: "extension.discovery.started", metadata: {} }), false);
    assert.equal(await service.observeRuntime({ type: "session.info", metadata: { sessionId: "other-session" } }), false);
    assert.equal(await service.observeRuntime({ type: "session.info", metadata: { sessionId: "current-session", content: "SECRET" } }), true);
    const records = await service.tail({ limit: 20 });
    assert.equal(records.filter(record => record.eventType === "session.info").length, 1);
    assert.doesNotMatch(JSON.stringify(records), /SECRET|current-session|other-session/);
    await service.close();
});

test("runtime observer adopts first scoped session when no environment session is present", async t => {
    const root = await workDirectory("runtime-scope-adopt");
    t.after(() => cleanup(root));
    const service = await startBlackBoxService({
        root,
        config: completeConfig({ native: { enabled: false } }),
        salt: "stable-test-salt",
        env: {},
        processId: 26,
        runId: "runtime-scope-adopt"
    });
    assert.equal(await service.observeRuntime({ type: "session.info", metadata: { sessionId: "first-session" } }), true);
    assert.equal(await service.observeRuntime({ type: "session.info", metadata: { sessionId: "second-session" } }), false);
    const records = await service.tail({ limit: 20 });
    assert.equal(records.filter(record => record.eventType === "session.info").length, 1);
    await service.close();
});

test("doctor treats a configured not-yet-created native event file as waiting", async t => {
    const root = await workDirectory("native-waiting");
    t.after(() => cleanup(root));
    const service = await startBlackBoxService({
        root,
        config: completeConfig(),
        nativeEventsPath: join(root, "pending-session", "events.jsonl"),
        startTailer: false,
        salt: "stable-test-salt",
        env: { SESSION_ID: "pending-session" },
        processId: 23,
        runId: "waiting"
    });
    await service.pollNative();
    const doctor = await service.doctor();
    assert.equal(doctor.healthy, true);
    assert.equal(doctor.native.exists, false);
    assert.equal(doctor.native.waitingForFile, true);
    await service.close();
});

test("native tailing skips oversized and corrupt lines without stalling", async t => {
    const root = await workDirectory("native-corrupt");
    t.after(() => cleanup(root));
    const eventsPath = join(root, "native", "events.jsonl");
    await mkdir(dirname(eventsPath), { recursive: true });
    const valid = JSON.stringify({
        id: "valid",
        timestamp: "2026-09-02T10:00:00.000Z",
        type: "session.warning",
        data: { warningType: "test-warning", message: "SECRET WARNING" }
    });
    await writeFile(eventsPath, `${"x".repeat(300)}\n{invalid-json}\n${valid}\n`, "utf8");
    const config = completeConfig({
        storage: { maxBytes: 100000, segmentBytes: 4096, maxRecordBytes: 2048 },
        queue: { maxRecords: 100, maxBytes: 100000 },
        native: { maxReadBytes: 64, maxLineBytes: 256, maxEventsPerPoll: 100, startPosition: "start" }
    });
    const service = await startBlackBoxService({
        root,
        config,
        nativeEventsPath: eventsPath,
        startTailer: false,
        salt: "stable-test-salt",
        env: { COPILOT_AGENT_SESSION_ID: "session" },
        processId: 22,
        runId: "corrupt"
    });
    const signals = [];
    const unsubscribe = service.subscribeSignals(record => signals.push(record.eventType));
    assert.equal(await service.pollNative(), 3);
    unsubscribe();
    const records = await service.tail({ limit: 20 });
    assert.ok(records.some(record => record.eventType === "anomaly.native-line-oversize"));
    assert.ok(records.some(record => record.eventType === "anomaly.native-line-corrupt"));
    assert.ok(signals.includes("anomaly.native-line-oversize"));
    assert.ok(signals.includes("anomaly.native-line-corrupt"));
    assert.ok(records.some(record => record.eventType === "session.warning"));
    assert.doesNotMatch(JSON.stringify(records), /SECRET WARNING|x{20}/);
    await service.close();
});
