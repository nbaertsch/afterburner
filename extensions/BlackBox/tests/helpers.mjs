import { randomUUID } from "node:crypto";
import { mkdir, rm } from "node:fs/promises";
import { resolve } from "node:path";

export function completeConfig(overrides = {}) {
    const config = {
        version: 1,
        enabled: true,
        storage: { maxBytes: 500 * 1024 * 1024, segmentBytes: 8 * 1024 * 1024, maxRecordBytes: 64 * 1024 },
        queue: { maxRecords: 4096, maxBytes: 16 * 1024 * 1024 },
        native: {
            enabled: true,
            pollIntervalMs: 1000,
            maxReadBytes: 4096,
            maxLineBytes: 64 * 1024,
            maxEventsPerPoll: 2000,
            startPosition: "start"
        },
        analytics: {
            aggregateEveryEvents: 1000,
            longToolDurationMs: 120000,
            longTurnDurationMs: 300000,
            unmatchedAfterMs: 900000
        },
        tail: { defaultRecords: 50, maxRecords: 500 },
        export: { maxRecords: 100000 }
    };
    for (const [section, values] of Object.entries(overrides)) {
        if (values && typeof values === "object" && !Array.isArray(values)) config[section] = { ...config[section], ...values };
        else config[section] = values;
    }
    return config;
}

export async function workDirectory(name) {
    const path = resolve(".test-work", `${name}-${randomUUID()}`);
    await mkdir(path, { recursive: true });
    return path;
}

export async function cleanup(path) {
    await rm(path, { recursive: true, force: true, maxRetries: 5, retryDelay: 100 });
}
