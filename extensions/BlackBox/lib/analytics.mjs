import { createBlackBoxRecord } from "./sanitize.mjs";

function milliseconds(timestamp) {
    const value = Date.parse(timestamp);
    return Number.isFinite(value) ? value : null;
}

function correlationKey(record, preferred) {
    for (const key of preferred) if (record.correlation[key]) return record.correlation[key];
    return null;
}

function matchingStart(map, record, preferred) {
    for (const key of preferred) {
        const reference = record.correlation[key];
        if (reference && map.has(reference)) return [reference, map.get(reference)];
    }
    return [null, null];
}

export class TimelineAnalytics {
    constructor(config, options = {}) {
        this.config = config;
        this.now = options.now;
        this.idFactory = options.idFactory;
        this.toolStarts = new Map();
        this.turnStarts = new Map();
        this.modelStarts = new Map();
        this.counts = {
            totalRecords: 0,
            toolStartCount: 0,
            toolCompleteCount: 0,
            toolFailureCount: 0,
            assistantTurnCount: 0,
            modelCallCount: 0,
            anomalyCount: 0,
            milestoneCount: 0
        };
        this.toolDurationTotal = 0;
        this.toolDurationCount = 0;
        this.maxToolDuration = 0;
    }

    #derived(base, values) {
        return createBlackBoxRecord({
            source: base.source,
            correlation: base.correlation,
            timestamp: base.timestamp,
            bodyReferences: [],
            ...values
        }, { now: this.now, idFactory: this.idFactory });
    }

    #milestone(base, code, attributes = {}) {
        this.counts.milestoneCount++;
        return this.#derived(base, {
            kind: "milestone",
            eventType: `milestone.${code}`,
            attributes: { milestoneCode: code, relatedEventType: base.eventType, ...attributes }
        });
    }

    #anomaly(base, code, attributes = {}, severity = "warning") {
        this.counts.anomalyCount++;
        return this.#derived(base, {
            kind: "anomaly",
            eventType: `anomaly.${code}`,
            severity,
            attributes: { anomalyCode: code, relatedEventType: base.eventType, ...attributes }
        });
    }

    #duration(start, end) {
        const startedAt = milliseconds(start.timestamp);
        const endedAt = milliseconds(end.timestamp);
        if (startedAt === null || endedAt === null) return null;
        return endedAt - startedAt;
    }

    ingest(record) {
        if (!record) return [];
        const derived = [];
        this.counts.totalRecords++;

        if (record.eventType === "tool.execution_start") {
            this.counts.toolStartCount++;
            const key = correlationKey(record, ["toolCallRef", "eventRef"]);
            if (key) this.toolStarts.set(key, record);
        } else if (record.eventType === "tool.execution_complete") {
            this.counts.toolCompleteCount++;
            const [key, start] = matchingStart(this.toolStarts, record, ["toolCallRef", "parentRef"]);
            if (key) this.toolStarts.delete(key);
            if (!start) {
                derived.push(this.#anomaly(record, "unmatched-tool-complete"));
            } else {
                const durationMs = this.#duration(start, record);
                if (durationMs === null || durationMs < 0) {
                    derived.push(this.#anomaly(record, "invalid-tool-duration"));
                } else {
                    this.toolDurationTotal += durationMs;
                    this.toolDurationCount++;
                    this.maxToolDuration = Math.max(this.maxToolDuration, durationMs);
                    derived.push(this.#milestone(record, "tool-complete", {
                        durationMs,
                        success: record.attributes.success ?? null,
                        toolName: start.attributes.toolName ?? record.attributes.toolName ?? null
                    }));
                    if (durationMs >= this.config.longToolDurationMs) {
                        derived.push(this.#anomaly(record, "long-tool", { durationMs }));
                    }
                }
            }
            if (record.attributes.success === false) {
                this.counts.toolFailureCount++;
                derived.push(this.#anomaly(record, "tool-failed", {}, "error"));
            }
        } else if (record.eventType === "assistant.turn_start") {
            const key = correlationKey(record, ["turnRef", "eventRef"]);
            if (key) this.turnStarts.set(key, record);
        } else if (record.eventType === "assistant.turn_end") {
            this.counts.assistantTurnCount++;
            const [key, start] = matchingStart(this.turnStarts, record, ["turnRef", "parentRef"]);
            if (key) this.turnStarts.delete(key);
            if (!start) {
                derived.push(this.#anomaly(record, "unmatched-turn-end"));
            } else {
                const durationMs = this.#duration(start, record);
                if (durationMs === null || durationMs < 0) derived.push(this.#anomaly(record, "invalid-turn-duration"));
                else {
                    derived.push(this.#milestone(record, "assistant-turn-complete", { durationMs }));
                    if (durationMs >= this.config.longTurnDurationMs) {
                        derived.push(this.#anomaly(record, "long-assistant-turn", { durationMs }));
                    }
                }
            }
        } else if (record.eventType === "model.model_call_started") {
            const key = correlationKey(record, ["modelCallRef", "eventRef"]);
            if (key) this.modelStarts.set(key, record);
        } else if (record.eventType === "model.model_call_success") {
            this.counts.modelCallCount++;
            const [key, start] = matchingStart(this.modelStarts, record, ["modelCallRef", "parentRef"]);
            if (key) this.modelStarts.delete(key);
            const reported = record.attributes.modelCallDurationMs;
            const measured = start ? this.#duration(start, record) : null;
            const durationMs = Number.isFinite(reported) ? reported : measured;
            derived.push(this.#milestone(record, "model-call-complete", {
                ...(Number.isFinite(durationMs) && durationMs >= 0 ? { durationMs } : {}),
                model: record.attributes.model ?? start?.attributes.model ?? null
            }));
        } else if ([
            "session.start", "session.resume", "session.shutdown", "session.task_complete", "session.model_change",
            "user.message"
        ].includes(record.eventType)) {
            derived.push(this.#milestone(record, record.eventType.replaceAll(".", "-")));
        }

        if (record.eventType === "session.error") {
            derived.push(this.#anomaly(record, "session-error", {
                errorType: record.attributes.errorType ?? null,
                statusCode: record.attributes.statusCode ?? null
            }, "error"));
        }

        derived.push(...this.sweep(record));
        if (this.counts.totalRecords % this.config.aggregateEveryEvents === 0) {
            derived.push(this.snapshot(record));
        }
        return derived.filter(Boolean);
    }

    sweep(baseRecord) {
        const current = milliseconds(baseRecord.timestamp);
        if (current === null) return [];
        const derived = [];
        for (const [key, start] of this.toolStarts) {
            const age = current - milliseconds(start.timestamp);
            if (Number.isFinite(age) && age >= this.config.unmatchedAfterMs) {
                this.toolStarts.delete(key);
                derived.push(this.#anomaly(baseRecord, "stale-tool-start", { durationMs: age }));
            }
        }
        for (const [key, start] of this.turnStarts) {
            const age = current - milliseconds(start.timestamp);
            if (Number.isFinite(age) && age >= this.config.unmatchedAfterMs) {
                this.turnStarts.delete(key);
                derived.push(this.#anomaly(baseRecord, "stale-turn-start", { durationMs: age }));
            }
        }
        for (const [key, start] of this.modelStarts) {
            const age = current - milliseconds(start.timestamp);
            if (Number.isFinite(age) && age >= this.config.unmatchedAfterMs) {
                this.modelStarts.delete(key);
                derived.push(this.#anomaly(baseRecord, "stale-model-call", { durationMs: age }));
            }
        }
        return derived;
    }

    snapshot(baseRecord) {
        const averageToolDurationMs = this.toolDurationCount > 0
            ? Math.round(this.toolDurationTotal / this.toolDurationCount)
            : 0;
        return this.#derived(baseRecord, {
            kind: "aggregate",
            eventType: "black-box.aggregate",
            attributes: {
                ...this.counts,
                averageToolDurationMs,
                maxToolDurationMs: this.maxToolDuration
            }
        });
    }

    status() {
        return {
            ...this.counts,
            averageToolDurationMs: this.toolDurationCount > 0
                ? Math.round(this.toolDurationTotal / this.toolDurationCount)
                : 0,
            maxToolDurationMs: this.maxToolDuration,
            pendingTools: this.toolStarts.size,
            pendingTurns: this.turnStarts.size,
            pendingModelCalls: this.modelStarts.size
        };
    }
}
