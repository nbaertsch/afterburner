import { startBlackBoxService } from "./service.mjs";

function commandText(input) {
    if (typeof input === "string") return input.trim();
    if (typeof input?.arguments === "string") return input.arguments.trim();
    if (typeof input?.text === "string") return input.text.trim();
    return "";
}

function requestedLimit(input) {
    const match = /\b(\d{1,5})\b/.exec(commandText(input));
    return match ? Number(match[1]) : undefined;
}

function statusLine(status) {
    return `Black Box: ${status.storage.segmentCount} segment(s), ${status.storage.segmentBytes} byte(s), ` +
        `${status.queue.droppedRecords} dropped record(s), ${status.analytics.anomalyCount} anomaly/anomalies.`;
}

function tailLine(record) {
    const duration = Number.isFinite(record.attributes.durationMs) ? ` ${record.attributes.durationMs}ms` : "";
    const outcome = typeof record.attributes.success === "boolean" ? ` success=${record.attributes.success}` : "";
    return `${record.timestamp} ${record.kind} ${record.eventType}${duration}${outcome}`;
}

async function safeAction(action) {
    try { return await action(); }
    catch { return { ok: false, error: "black-box-unavailable" }; }
}

export async function buildSessionRegistration({ service, createCanvas, joinSession }) {
    let session;
    let liveTailTimer = null;
    const liveTailSeen = new Set();
    const stopLiveTailTimer = () => {
        if (liveTailTimer) clearInterval(liveTailTimer);
        liveTailTimer = null;
    };
    const startLiveTail = async (limit = 50) => {
        stopLiveTailTimer();
        for (const record of await service.tail({ limit })) liveTailSeen.add(record.recordId);
        liveTailTimer = setInterval(async () => {
            try {
                const records = (await service.tail({ limit })).reverse()
                    .filter(record => !liveTailSeen.has(record.recordId));
                for (const record of records) {
                    liveTailSeen.add(record.recordId);
                    await session?.log(`Black Box live: ${tailLine(record)}`);
                }
                while (liveTailSeen.size > 1000) liveTailSeen.delete(liveTailSeen.values().next().value);
            } catch {}
        }, 2000);
        liveTailTimer.unref?.();
    };
    const canvas = createCanvas({
        id: "afterburner-black-box",
        displayName: "Afterburner Black Box",
        description: "Metadata-only session timeline, anomalies, milestones, storage health, and sanitized exports.",
        actions: [
            {
                name: "snapshot",
                description: "Return the current metadata-only Black Box status.",
                inputSchema: { type: "object", properties: {}, additionalProperties: false },
                handler: async () => safeAction(() => service.status())
            },
            {
                name: "tail",
                description: "Return recent sanitized timeline records.",
                inputSchema: {
                    type: "object",
                    properties: { limit: { type: "integer", minimum: 1, maximum: 500 } },
                    additionalProperties: false
                },
                handler: async input => safeAction(() => service.tail(input ?? {}))
            },
            {
                name: "export",
                description: "Create a sanitized Black Box export bundle.",
                inputSchema: {
                    type: "object",
                    properties: { maxRecords: { type: "integer", minimum: 1 } },
                    additionalProperties: false
                },
                handler: async input => safeAction(async () => {
                    const result = await service.exportBundle(input ?? {});
                    return { ok: true, path: result.path, manifest: result.manifest };
                })
            },
            {
                name: "doctor",
                description: "Check configuration, native tailing, storage integrity, and drop counters.",
                inputSchema: { type: "object", properties: {}, additionalProperties: false },
                handler: async () => safeAction(() => service.doctor())
            }
        ],
        open: async () => {
            const status = await safeAction(() => service.status());
            return {
                title: "Afterburner Black Box",
                status: status?.storage ? statusLine(status) : "Black Box is unavailable."
            };
        }
    });

    const logSafely = async action => {
        try { await action(); }
        catch { await session?.log("Black Box operation failed without affecting the session."); }
    };

    session = await joinSession({
        commands: [
            {
                name: "black-box",
                description: "Show Black Box recorder, retention, queue, and anomaly status.",
                handler: async () => logSafely(async () => session.log(statusLine(await service.status())))
            },
            {
                name: "black-box-tail",
                description: "Start a bounded live metadata tail, or pass 'stop' to stop it.",
                handler: async input => logSafely(async () => {
                    if (/^stop\b/i.test(commandText(input))) {
                        stopLiveTailTimer();
                        await session.log("Black Box live tail stopped.");
                        return;
                    }
                    const limit = requestedLimit(input) ?? 50;
                    const records = await service.tail({ limit });
                    await session.log(records.length ? records.map(tailLine).join("\n") : "Black Box timeline is empty.");
                    await startLiveTail(limit);
                    await session.log("Black Box live tail started; run /black-box-tail stop to stop it.");
                })
            },
            {
                name: "black-box-tail-stop",
                description: "Stop the active Black Box live metadata tail.",
                handler: async () => logSafely(async () => {
                    stopLiveTailTimer();
                    await session.log("Black Box live tail stopped.");
                })
            },
            {
                name: "black-box-export",
                description: "Create a local sanitized Black Box export bundle.",
                handler: async input => logSafely(async () => {
                    const result = await service.exportBundle({ maxRecords: requestedLimit(input) });
                    await session.log(`Black Box export: ${result.path} (${result.manifest.recordCount} record(s)).`);
                })
            },
            {
                name: "black-box-doctor",
                description: "Check Black Box configuration, storage, native tailing, and recovery state.",
                handler: async () => logSafely(async () => session.log(JSON.stringify(await service.doctor(), null, 2)))
            }
        ],
        canvases: [canvas]
    });
    return {
        session,
        canvas,
        stopLiveTail: stopLiveTailTimer
    };
}

export async function startSessionExtension(options = {}) {
    const service = options.service ?? await startBlackBoxService({
        env: options.env,
        mode: "session",
        startTailer: options.startTailer
    });
    try {
        const registration = await buildSessionRegistration({
            service,
            createCanvas: options.createCanvas,
            joinSession: options.joinSession
        });
        return {
            service,
            ...registration,
            async dispose() {
                registration.stopLiveTail();
                await service.close();
            }
        };
    } catch (error) {
        await service.close().catch(() => {});
        throw error;
    }
}
