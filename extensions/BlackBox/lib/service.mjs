import { randomBytes } from "node:crypto";
import { watch } from "node:fs";
import { mkdir, open, readFile, realpath, stat, unlink, writeFile } from "node:fs/promises";
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

async function waitForFileChange(directory, file, timeoutMs) {
    let watchDirectory = directory;
    try { watchDirectory = await realpath(directory); } catch {}
    await new Promise(resolve => {
        let settled = false;
        let watcher = null;
        const finish = () => {
            if (settled) return;
            settled = true;
            clearTimeout(timer);
            try { watcher?.close?.(); } catch {}
            resolve();
        };
        const timer = setTimeout(finish, Math.max(1, timeoutMs));
        timer.unref?.();
        try {
            watcher = watch(watchDirectory, { persistent: false }, (_event, filename) => {
                if (!filename || String(filename) === file) finish();
            });
            watcher.unref?.();
        } catch {
            finish();
        }
    });
}

function shouldIgnoreRuntimeObservation(event) {
    const type = typeof event?.type === "string" ? event.type : "";
    const data = event?.metadata ?? event?.data ?? {};
    const modalId = data.modalId;
    const surfaceId = data.surfaceId;
    if (modalId === "afterburner-black-box-live" && type.startsWith("ui.modal_canvas.")) return true;
    if (surfaceId === "afterburner-black-box-live" && type.startsWith("ui.host.")) return true;
    return false;
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
    const recordListeners = new Set();
    const internal = {
        observerErrors: 0,
        diagnosticRecords: 0,
        disabledDrops: 0
    };
    const publish = (listeners, record) => {
        for (const listener of listeners) {
            try { Promise.resolve(listener(record)).catch(() => {}); }
            catch {}
        }
    };
    const publishSignal = record => publish(signalListeners, record);
    const publishRecord = record => publish(recordListeners, record);

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
            if (store.enqueue(record)) {
                publishRecord(record);
                publishSignal(record);
            }
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
            if (!accepted) return false;
            publishRecord(record);
            for (const derived of analytics.ingest(record)) {
                if (store.enqueue(derived)) {
                    publishRecord(derived);
                    if (derived.kind === "anomaly" || derived.kind === "milestone") publishSignal(derived);
                }
            }
            return true;
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

    const activationStateDirectory = join(root, "state");
    const activationPath = join(activationStateDirectory, "modal-activation.jsonl");
    const activationAckDirectory = join(activationStateDirectory, "modal-activation-acks");
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
            if (shouldIgnoreRuntimeObservation(normalized)) return false;
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
        subscribeRecords(listener) {
            if (typeof listener !== "function") throw new Error("Black Box record listener must be a function.");
            recordListeners.add(listener);
            return () => recordListeners.delete(listener);
        },
        async tail(input = {}) {
            await store.flush();
            const limit = safeLimit(input.limit, config.tail.defaultRecords, config.tail.maxRecords);
            const kinds = Array.isArray(input.kinds) ? new Set(input.kinds) : null;
            return store.readRecent(limit, record => !kinds || kinds.has(record.kind));
        },
        async modalActivationWatch() {
            await mkdir(activationStateDirectory, { recursive: true });
            let directory = activationStateDirectory;
            try { directory = await realpath(activationStateDirectory); } catch {}
            return { directory, file: "modal-activation.jsonl" };
        },
        async requestModalOpen(input = {}) {
            await mkdir(activationAckDirectory, { recursive: true });
            const request = {
                schemaVersion: 1,
                requestId: randomBytes(12).toString("hex"),
                surfaceId: String(input.surfaceId ?? "afterburner-black-box-live"),
                createdAt: new Date().toISOString(),
                input: input.input && typeof input.input === "object" ? input.input : {}
            };
            await writeFile(activationPath, `${JSON.stringify(request)}\n`, { flag: "a" });
            const ackFile = `${request.requestId}.json`;
            const ackPath = join(activationAckDirectory, ackFile);
            const deadline = Date.now() + safeLimit(input.timeoutMs, 3000, 10000);
            while (Date.now() < deadline) {
                try {
                    const ack = JSON.parse(await readFile(ackPath, "utf8"));
                    try { await unlink(ackPath); } catch {}
                    return { ok: ack.ok === true, requestId: request.requestId, surfaceId: request.surfaceId, error: ack.error };
                } catch (error) {
                    if (error?.code !== "ENOENT") return { ok: false, requestId: request.requestId, surfaceId: request.surfaceId, error: "modal-activation-ack-invalid" };
                }
                await waitForFileChange(activationAckDirectory, ackFile, Math.min(100, Math.max(1, deadline - Date.now())));
            }
            return { ok: false, requestId: request.requestId, surfaceId: request.surfaceId, error: "modal-activation-timeout" };
        },
        async consumeModalOpenRequests() {
            let body = "";
            try { body = await readFile(activationPath, "utf8"); }
            catch (error) {
                if (error?.code === "ENOENT") return [];
                throw error;
            }
            try { await unlink(activationPath); } catch {}
            return body.split(/\r?\n/).filter(Boolean).map(line => {
                try { return JSON.parse(line); }
                catch { return null; }
            }).filter(request => request?.schemaVersion === 1 && typeof request.surfaceId === "string" && typeof request.requestId === "string");
        },
        async completeModalOpenRequest(request, result = {}) {
            if (!request?.requestId) return false;
            await mkdir(activationAckDirectory, { recursive: true });
            const ack = {
                schemaVersion: 1,
                requestId: request.requestId,
                ok: result.ok === true,
                error: result.error ? String(result.error).slice(0, 160) : undefined,
                completedAt: new Date().toISOString()
            };
            await writeFile(join(activationAckDirectory, `${request.requestId}.json`), JSON.stringify(ack), "utf8");
            return true;
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
            recordListeners.clear();
            await tailer?.stop();
            await store.close();
        },
        store,
        tailer
    };
    return service;
}
