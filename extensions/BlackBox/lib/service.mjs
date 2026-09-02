import { randomBytes } from "node:crypto";
import { mkdir, open, readFile, stat, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { TimelineAnalytics } from "./analytics.mjs";
import { loadBlackBoxConfig, resolveDataRoot, resolveNativeEventsPath } from "./config.mjs";
import { exportSanitizedBundle } from "./export.mjs";
import { NativeEventsTailer } from "./native-tail.mjs";
import { createBlackBoxRecord, createReferenceFactory, sanitizeNativeEvent } from "./sanitize.mjs";
import { SegmentedJsonlStore } from "./storage.mjs";

async function loadOrCreateSalt(root) {
    const stateDirectory = join(root, "state");
    const path = join(stateDirectory, "identity-salt");
    await mkdir(stateDirectory, { recursive: true });
    try {
        const existing = (await readFile(path, "utf8")).trim();
        if (/^[a-f0-9]{64}$/.test(existing)) return existing;
    } catch (error) {
        if (error?.code !== "ENOENT") throw error;
    }
    const value = randomBytes(32).toString("hex");
    try {
        const handle = await open(path, "wx");
        try { await handle.writeFile(`${value}\n`, "utf8"); await handle.sync(); }
        finally { await handle.close(); }
        return value;
    } catch (error) {
        if (error?.code !== "EEXIST") {
            await writeFile(path, `${value}\n`, "utf8");
            return value;
        }
        const concurrent = (await readFile(path, "utf8")).trim();
        if (/^[a-f0-9]{64}$/.test(concurrent)) return concurrent;
        await writeFile(path, `${value}\n`, "utf8");
        return value;
    }
}

function safeLimit(value, fallback, maximum) {
    const number = Number(value);
    return Number.isSafeInteger(number) && number > 0 ? Math.min(number, maximum) : fallback;
}

export async function startBlackBoxService(options = {}) {
    const env = options.env ?? process.env;
    const loaded = options.config
        ? { config: options.config, diagnostics: options.configDiagnostics ?? [], configPath: null }
        : await loadBlackBoxConfig(env);
    const config = loaded.config;
    const root = options.root ?? resolveDataRoot(env);
    const mode = options.mode ?? "session";
    const processId = options.processId ?? process.pid;
    const runId = options.runId ?? randomBytes(6).toString("hex");
    const salt = options.salt ?? await loadOrCreateSalt(root);
    const reference = createReferenceFactory(salt);
    const store = options.store ?? new SegmentedJsonlStore({
        root,
        storage: config.storage,
        queue: config.queue,
        processId,
        runId: `${mode}-${runId}`
    });
    await store.initialize();
    const analytics = new TimelineAnalytics(config.analytics, {
        now: options.now,
        idFactory: options.idFactory
    });
    const signalListeners = new Set();
    const internal = {
        observerErrors: 0,
        diagnosticRecords: 0,
        disabledDrops: 0
    };
    const publishSignal = record => {
        for (const listener of signalListeners) {
            try { Promise.resolve(listener(record)).catch(() => {}); }
            catch {}
        }
    };

    const recordDiagnostic = async (code, source = {}) => {
        const record = createBlackBoxRecord({
            kind: "anomaly",
            eventType: `anomaly.${code}`,
            severity: code.includes("failed") || code.includes("corrupt") ? "error" : "warning",
            source: {
                kind: source.pathRef ? "native-events" : "black-box",
                processRef: reference("process", processId),
                ...(source.pathRef ? { pathRef: source.pathRef } : {}),
                ...(Number.isSafeInteger(source.byteStart) ? { byteStart: source.byteStart } : {}),
                ...(Number.isSafeInteger(source.byteEnd) ? { byteEnd: source.byteEnd } : {})
            },
            attributes: { anomalyCode: code, detailCode: code }
        }, { now: options.now, idFactory: options.idFactory });
        if (record) {
            internal.diagnosticRecords++;
            store.enqueue(record);
            publishSignal(record);
        }
    };

    const observe = async (rawEvent, source = {}) => {
        if (!config.enabled) {
            internal.disabledDrops++;
            return false;
        }
        try {
            const record = sanitizeNativeEvent(rawEvent, {
                kind: source.kind ?? (mode === "runtime" ? "runtime-observer" : "black-box"),
                processId,
                sessionId: source.sessionId ?? env.SESSION_ID ?? env.COPILOT_AGENT_SESSION_ID,
                path: source.path,
                byteStart: source.byteStart,
                byteEnd: source.byteEnd
            }, { salt, now: options.now, idFactory: options.idFactory });
            if (!record) return false;
            const accepted = store.enqueue(record);
            for (const derived of analytics.ingest(record)) {
                store.enqueue(derived);
                if (derived.kind === "anomaly" || derived.kind === "milestone") publishSignal(derived);
            }
            return accepted;
        } catch {
            internal.observerErrors++;
            await recordDiagnostic("observer-processing-failed");
            return false;
        }
    };

    const nativePath = options.nativeEventsPath === undefined
        ? resolveNativeEventsPath(env)
        : options.nativeEventsPath;
    let tailer = null;
    if (mode === "session" && config.enabled && config.native.enabled && nativePath) {
        tailer = new NativeEventsTailer({
            root,
            path: nativePath,
            sessionId: env.SESSION_ID ?? env.COPILOT_AGENT_SESSION_ID,
            processId,
            config: config.native,
            reference,
            recoverOffset: pathRef => store.recoverOffset(pathRef),
            onEvent: (event, source) => observe(event, source),
            onDiagnostic: recordDiagnostic
        });
        await tailer.initialize();
        if (options.startTailer !== false) tailer.start();
    }

    const service = {
        root,
        config,
        configDiagnostics: loaded.diagnostics,
        nativePathConfigured: Boolean(nativePath),
        async observeRuntime(rawEvent, context = {}) {
            const event = rawEvent?.event && typeof rawEvent.event === "object" ? rawEvent.event : rawEvent;
            const envelope = rawEvent?.event ? rawEvent : context;
            const normalized = event?.metadata && event.data === undefined
                ? { ...event, data: event.metadata }
                : event;
            return observe(normalized, {
                kind: "runtime-observer",
                sessionId: envelope?.sessionId ?? envelope?.session?.id ?? env.SESSION_ID ?? env.COPILOT_AGENT_SESSION_ID
            });
        },
        async pollNative() {
            return tailer ? tailer.poll() : 0;
        },
        async status() {
            await store.flush();
            const inventory = await store.inventory();
            const recentSignals = await store.readRecent(10, record => record.kind === "anomaly" || record.kind === "milestone");
            return {
                schemaVersion: 1,
                enabled: config.enabled,
                mode,
                storage: {
                    maxBytes: config.storage.maxBytes,
                    segmentBytes: config.storage.segmentBytes,
                    segmentCount: inventory.segmentCount,
                    segmentBytes: inventory.segmentBytes,
                    writerCount: inventory.writerCount,
                    retentionBlockedBytes: inventory.retention.blockedBytes
                },
                queue: {
                    records: inventory.queueRecords,
                    bytes: inventory.queueBytes,
                    droppedRecords: inventory.counters.droppedQueue + inventory.counters.droppedOversize +
                        inventory.counters.droppedInvalid + inventory.counters.droppedWrite,
                    droppedBytes: inventory.counters.droppedBytes,
                    writeErrors: inventory.counters.writeErrors
                },
                analytics: analytics.status(),
                native: tailer ? tailer.status() : { enabled: false, configured: Boolean(nativePath) },
                internal: { ...internal },
                recentSignals
            };
        },
        subscribeSignals(listener) {
            if (typeof listener !== "function") throw new Error("Black Box signal listener must be a function.");
            signalListeners.add(listener);
            return () => signalListeners.delete(listener);
        },
        async tail(input = {}) {
            await store.flush();
            const limit = safeLimit(input.limit, config.tail.defaultRecords, config.tail.maxRecords);
            const kinds = Array.isArray(input.kinds) ? new Set(input.kinds) : null;
            return store.readRecent(limit, record => !kinds || kinds.has(record.kind));
        },
        async exportBundle(input = {}) {
            await store.flush();
            return exportSanitizedBundle({
                root,
                store,
                maxRecords: safeLimit(input.maxRecords, config.export.maxRecords, config.export.maxRecords),
                now: options.now
            });
        },
        async doctor() {
            await store.flush();
            const inventory = await store.inventory();
            const integrity = await store.inspectIntegrity();
            let nativeFile = { configured: Boolean(nativePath), exists: false, sizeBytes: null };
            if (nativePath) {
                try {
                    const metadata = await stat(nativePath);
                    nativeFile = { configured: true, exists: true, sizeBytes: metadata.size };
                } catch {}
            }
            const configHealthy = loaded.diagnostics.every(code => code === "config-missing-defaults-used");
            const nativeStatus = tailer ? tailer.status() : {};
            const nativeHealthy = !config.native.enabled ||
                (nativeFile.configured && (nativeFile.exists || nativeStatus.waitingForFile === true));
            return {
                schemaVersion: 1,
                healthy: configHealthy && nativeHealthy && integrity.writable &&
                    integrity.invalidLines === 0 && inventory.counters.writeErrors === 0,
                configDiagnostics: [...loaded.diagnostics],
                storage: {
                    writable: integrity.writable,
                    segmentCount: inventory.segmentCount,
                    segmentBytes: inventory.segmentBytes,
                    corruptStateCount: inventory.corruptStateCount,
                    checkedFiles: integrity.checkedFiles,
                    checkedLines: integrity.checkedLines,
                    invalidLines: integrity.invalidLines,
                    writeErrors: inventory.counters.writeErrors,
                    stateRecoveries: inventory.counters.stateRecoveries
                },
                native: { ...nativeFile, ...(tailer ? tailer.status() : {}) },
                failureIsolation: {
                    observerErrors: internal.observerErrors,
                    disabledDrops: internal.disabledDrops
                }
            };
        },
        async close() {
            signalListeners.clear();
            await tailer?.stop();
            await store.close();
        },
        store,
        tailer
    };
    return service;
}
