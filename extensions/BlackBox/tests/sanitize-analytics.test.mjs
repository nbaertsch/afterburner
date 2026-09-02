import test from "node:test";
import assert from "node:assert/strict";
import { TimelineAnalytics } from "../lib/analytics.mjs";
import { sanitizeNativeEvent } from "../lib/sanitize.mjs";
import { validateMetadataRecord } from "../lib/schema.mjs";
import { completeConfig } from "./helpers.mjs";

const fixedId = () => "12345678-1234-1234-1234-123456789abc";

test("sanitizer persists metadata and body references only", () => {
    const record = sanitizeNativeEvent({
        id: "raw-event-id",
        parentId: "raw-parent-id",
        timestamp: "2026-09-02T10:00:00.000Z",
        type: "tool.execution_complete",
        data: {
            interactionId: "interaction-id",
            toolCallId: "tool-call-id",
            turnId: "turn-id",
            model: "gpt-test",
            success: false,
            result: {
                content: "TOP SECRET tool output",
                detailedContent: "TOP SECRET details"
            },
            summary: "TOP SECRET summary",
            arguments: { password: "TOP SECRET argument" },
            reasoningSummary: "TOP SECRET reasoning"
        }
    }, {
        kind: "native-events",
        processId: 42,
        sessionId: "session-id",
        path: "C:\\sensitive\\events.jsonl",
        byteStart: 10,
        byteEnd: 200
    }, { salt: "test-salt", idFactory: fixedId, now: () => new Date("2026-09-02T10:00:01.000Z") });

    assert.equal(validateMetadataRecord(record).valid, true);
    assert.equal(record.attributes.model, "gpt-test");
    assert.equal(record.attributes.success, false);
    assert.equal(record.source.byteStart, 10);
    assert.equal(record.source.byteEnd, 200);
    assert.match(record.source.pathRef, /^ref_[a-f0-9]{24}$/);
    assert.match(record.correlation.toolCallRef, /^ref_[a-f0-9]{24}$/);
    assert.deepEqual(record.bodyReferences.map(reference => reference.field).sort(), [
        "data.arguments", "data.reasoningSummary", "data.result", "data.summary"
    ]);
    const serialized = JSON.stringify(record);
    assert.doesNotMatch(serialized, /TOP SECRET|sensitive|session-id|tool-call-id|raw-event-id/);
});

test("analytics derives correlated durations, milestones, aggregates, and anomalies", () => {
    const config = completeConfig({ analytics: {
        aggregateEveryEvents: 2,
        longToolDurationMs: 1000,
        longTurnDurationMs: 1000,
        unmatchedAfterMs: 10000
    } });
    const analytics = new TimelineAnalytics(config.analytics, { idFactory: fixedId });
    const start = sanitizeNativeEvent({
        id: "start", timestamp: "2026-09-02T10:00:00.000Z", type: "tool.execution_start",
        data: { toolCallId: "same-call", turnId: "turn", toolName: "powershell", arguments: { secret: "never" } }
    }, { kind: "runtime-observer", processId: 1 }, { salt: "salt", idFactory: fixedId });
    const end = sanitizeNativeEvent({
        id: "end", parentId: "start", timestamp: "2026-09-02T10:00:01.500Z", type: "tool.execution_complete",
        data: { toolCallId: "same-call", turnId: "turn", success: false, result: { content: "never" } }
    }, { kind: "runtime-observer", processId: 1 }, { salt: "salt", idFactory: fixedId });

    assert.deepEqual(analytics.ingest(start), []);
    const derived = analytics.ingest(end);
    assert.ok(derived.some(record => record.kind === "milestone" && record.attributes.durationMs === 1500));
    assert.ok(derived.some(record => record.eventType === "anomaly.long-tool"));
    assert.ok(derived.some(record => record.eventType === "anomaly.tool-failed"));
    assert.ok(derived.some(record => record.kind === "aggregate"));
    assert.ok(derived.every(record => validateMetadataRecord(record).valid));
    assert.doesNotMatch(JSON.stringify(derived), /never/);
});
