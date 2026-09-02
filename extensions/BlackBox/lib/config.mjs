import { readFile } from "node:fs/promises";
import { dirname, join, resolve } from "node:path";

export const DEFAULT_MAX_BYTES = 500 * 1024 * 1024;
export const DEFAULT_SEGMENT_BYTES = 8 * 1024 * 1024;

function integer(value, fallback, { min = 1, max = Number.MAX_SAFE_INTEGER } = {}) {
    return Number.isSafeInteger(value) && value >= min && value <= max ? value : fallback;
}

function defaults() {
    return {
        version: 1,
        enabled: true,
        storage: {
            maxBytes: DEFAULT_MAX_BYTES,
            segmentBytes: DEFAULT_SEGMENT_BYTES,
            maxRecordBytes: 64 * 1024
        },
        queue: {
            maxRecords: 4096,
            maxBytes: 16 * 1024 * 1024
        },
        native: {
            enabled: true,
            pollIntervalMs: 1000,
            maxReadBytes: 1024 * 1024,
            maxLineBytes: 16 * 1024 * 1024,
            maxEventsPerPoll: 2000,
            startPosition: "start"
        },
        analytics: {
            aggregateEveryEvents: 250,
            longToolDurationMs: 120_000,
            longTurnDurationMs: 300_000,
            unmatchedAfterMs: 900_000
        },
        tail: {
            defaultRecords: 50,
            maxRecords: 500
        },
        export: {
            maxRecords: 100_000
        }
    };
}

export function normalizeConfig(input = {}) {
    const base = defaults();
    const diagnostics = [];
    if (!input || typeof input !== "object" || Array.isArray(input)) {
        return { config: base, diagnostics: ["config-not-object"] };
    }
    if (input.version !== undefined && input.version !== 1) diagnostics.push("unsupported-version");

    const storage = input.storage && typeof input.storage === "object" ? input.storage : {};
    const queue = input.queue && typeof input.queue === "object" ? input.queue : {};
    const native = input.native && typeof input.native === "object" ? input.native : {};
    const analytics = input.analytics && typeof input.analytics === "object" ? input.analytics : {};
    const tail = input.tail && typeof input.tail === "object" ? input.tail : {};
    const exportConfig = input.export && typeof input.export === "object" ? input.export : {};

    base.enabled = input.enabled !== false;
    base.storage.maxBytes = integer(storage.maxBytes, base.storage.maxBytes, { min: 1024 });
    base.storage.segmentBytes = integer(storage.segmentBytes, base.storage.segmentBytes, {
        min: 1024,
        max: base.storage.maxBytes
    });
    base.storage.maxRecordBytes = integer(storage.maxRecordBytes, base.storage.maxRecordBytes, {
        min: 256,
        max: base.storage.segmentBytes
    });
    base.queue.maxRecords = integer(queue.maxRecords, base.queue.maxRecords, { min: 1, max: 1_000_000 });
    base.queue.maxBytes = integer(queue.maxBytes, base.queue.maxBytes, {
        min: base.storage.maxRecordBytes,
        max: base.storage.maxBytes
    });
    base.native.enabled = native.enabled !== false;
    base.native.pollIntervalMs = integer(native.pollIntervalMs, base.native.pollIntervalMs, { min: 100, max: 60_000 });
    base.native.maxReadBytes = integer(native.maxReadBytes, base.native.maxReadBytes, { min: 4096, max: 64 * 1024 * 1024 });
    base.native.maxLineBytes = integer(native.maxLineBytes, base.native.maxLineBytes, {
        min: base.native.maxReadBytes,
        max: 256 * 1024 * 1024
    });
    base.native.maxEventsPerPoll = integer(native.maxEventsPerPoll, base.native.maxEventsPerPoll, { min: 1, max: 100_000 });
    base.native.startPosition = native.startPosition === "end" ? "end" : "start";
    base.analytics.aggregateEveryEvents = integer(
        analytics.aggregateEveryEvents,
        base.analytics.aggregateEveryEvents,
        { min: 1, max: 1_000_000 }
    );
    base.analytics.longToolDurationMs = integer(analytics.longToolDurationMs, base.analytics.longToolDurationMs);
    base.analytics.longTurnDurationMs = integer(analytics.longTurnDurationMs, base.analytics.longTurnDurationMs);
    base.analytics.unmatchedAfterMs = integer(analytics.unmatchedAfterMs, base.analytics.unmatchedAfterMs);
    base.tail.maxRecords = integer(tail.maxRecords, base.tail.maxRecords, { min: 1, max: 10_000 });
    base.tail.defaultRecords = integer(tail.defaultRecords, base.tail.defaultRecords, {
        min: 1,
        max: base.tail.maxRecords
    });
    base.export.maxRecords = integer(exportConfig.maxRecords, base.export.maxRecords, { min: 1, max: 10_000_000 });

    return { config: base, diagnostics };
}

export function resolveAfterburnerHome(env = process.env) {
    return resolve(env.AFTERBURNER_HOME || join(env.USERPROFILE || env.HOME || ".", ".afterburner"));
}

export function resolveDataRoot(env = process.env) {
    return join(resolveAfterburnerHome(env), "extension-data", "black-box");
}

export function resolveNativeEventsPath(env = process.env) {
    if (env.AFTERBURNER_BLACK_BOX_EVENTS?.trim()) return resolve(env.AFTERBURNER_BLACK_BOX_EVENTS.trim());
    if (env.COPILOT_SESSION_STATE_DIR?.trim()) return join(resolve(env.COPILOT_SESSION_STATE_DIR.trim()), "events.jsonl");
    const sessionId = env.SESSION_ID?.trim() || env.COPILOT_AGENT_SESSION_ID?.trim();
    if (env.COPILOT_HOME?.trim() && sessionId) {
        return join(resolve(env.COPILOT_HOME.trim()), "session-state", sessionId, "events.jsonl");
    }
    return null;
}

export async function loadBlackBoxConfig(env = process.env) {
    const configPath = resolve(
        env.AFTERBURNER_BLACK_BOX_CONFIG?.trim() ||
        join(resolveAfterburnerHome(env), "config", "black-box.json")
    );
    let parsed = {};
    const diagnostics = [];
    try {
        parsed = JSON.parse(await readFile(configPath, "utf8"));
    } catch (error) {
        if (error?.code === "ENOENT") diagnostics.push("config-missing-defaults-used");
        else diagnostics.push("config-invalid-defaults-used");
    }
    const normalized = normalizeConfig(parsed);
    return {
        config: normalized.config,
        configPath,
        configDirectory: dirname(configPath),
        diagnostics: [...diagnostics, ...normalized.diagnostics]
    };
}
