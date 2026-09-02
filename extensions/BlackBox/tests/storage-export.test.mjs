import test from "node:test";
import assert from "node:assert/strict";
import { appendFile, readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { exportSanitizedBundle } from "../lib/export.mjs";
import { createBlackBoxRecord } from "../lib/sanitize.mjs";
import { SegmentedJsonlStore } from "../lib/storage.mjs";
import { cleanup, workDirectory } from "./helpers.mjs";

function record(index) {
    return createBlackBoxRecord({
        kind: "event",
        eventType: "test.event",
        source: { kind: "black-box" },
        attributes: { eventCount: index }
    });
}

test("segmented storage rotates, retains by bytes, counts drops, and recovers corrupt state", async t => {
    const root = await workDirectory("storage");
    t.after(() => cleanup(root));
    const options = {
        root,
        storage: { maxBytes: 2500, segmentBytes: 1024, maxRecordBytes: 1024 },
        queue: { maxRecords: 100, maxBytes: 100000 },
        processId: 10,
        runId: "recovery"
    };
    const store = await new SegmentedJsonlStore(options).initialize();
    for (let index = 0; index < 30; index++) assert.equal(store.enqueue(record(index)), true);
    await store.flush();
    const inventory = await store.inventory();
    assert.ok(inventory.segmentCount > 1);
    assert.ok(inventory.segmentBytes <= options.storage.maxBytes);
    await store.close();

    await writeFile(store.statePath, "{corrupt", "utf8");
    const recovered = await new SegmentedJsonlStore(options).initialize();
    const recoveredInventory = await recovered.inventory();
    assert.equal(recoveredInventory.counters.stateRecoveries >= 1, true);
    assert.ok(recovered.sequence >= 1);
    await recovered.close();

    const dropStore = await new SegmentedJsonlStore({
        ...options,
        runId: "drops",
        queue: { maxRecords: 1, maxBytes: 100000 }
    }).initialize();
    assert.equal(dropStore.enqueue(record(1)), true);
    assert.equal(dropStore.enqueue(record(2)), false);
    await dropStore.flush();
    assert.equal((await dropStore.inventory()).counters.droppedQueue >= 1, true);
    await dropStore.close();
});

test("export bundle re-sanitizes stored records", async t => {
    const root = await workDirectory("export");
    t.after(() => cleanup(root));
    const store = await new SegmentedJsonlStore({
        root,
        storage: { maxBytes: 100000, segmentBytes: 4096, maxRecordBytes: 2048 },
        queue: { maxRecords: 100, maxBytes: 100000 },
        processId: 11,
        runId: "export"
    }).initialize();
    const clean = record(1);
    await store.append(clean);
    const segmentPath = join(store.segmentDirectory, store.currentSegment);
    await appendFile(segmentPath, `${JSON.stringify({ ...clean, secret: "MUST NOT EXPORT", attributes: {
        ...clean.attributes,
        content: "MUST NOT EXPORT",
        "metrics.payload": "MUST NOT EXPORT"
    } })}\n`, "utf8");

    const result = await exportSanitizedBundle({ root, store, maxRecords: 100 });
    const timeline = await readFile(join(result.path, "timeline.jsonl"), "utf8");
    const manifest = JSON.parse(await readFile(join(result.path, "manifest.json"), "utf8"));
    assert.doesNotMatch(timeline, /MUST NOT EXPORT|"content"|"secret"/);
    assert.equal(manifest.privacy.bodiesIncluded, false);
    assert.equal(manifest.privacy.metadataOnly, true);
    assert.equal(manifest.recordCount, 2);
    await store.close();
});
