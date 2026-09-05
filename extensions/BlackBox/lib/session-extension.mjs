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

function formatBytes(value) {
    const bytes = Number(value);
    if (!Number.isFinite(bytes) || bytes < 0) return "unknown";
    const units = ["B", "KiB", "MiB", "GiB"];
    let scaled = bytes;
    let unit = 0;
    while (scaled >= 1024 && unit < units.length - 1) {
        scaled /= 1024;
        unit++;
    }
    const digits = scaled >= 10 || unit === 0 ? 0 : 1;
    return `${scaled.toFixed(digits)} ${units[unit]}`;
}

function formatEnabled(value) {
    return value ? "enabled" : "disabled";
}

function formatPanel(status) {
    const signals = Array.isArray(status.recentSignals) && status.recentSignals.length > 0
        ? status.recentSignals.slice(0, 5).map(record => `  - ${tailLine(record)}`).join("\n")
        : "  - none";
    return [
        "Afterburner Black Box",
        "======================",
        `Recorder: ${formatEnabled(status.enabled)} (${status.mode ?? "unknown"})`,
        `Storage : ${status.storage.segmentCount} segment(s), ${formatBytes(status.storage.segmentBytes)} used, ` +
            `${formatBytes(status.storage.retentionBlockedBytes)} retention-blocked`,
        `Queue   : ${status.queue.records ?? 0} queued, ${formatBytes(status.queue.bytes ?? 0)}, ` +
            `${status.queue.droppedRecords} dropped, ${status.queue.writeErrors} write error(s)`,
        `Signals : ${status.analytics.anomalyCount} anomalie(s), ${status.analytics.milestoneCount} milestone(s), ` +
            `${status.analytics.totalRecords} total record(s)`,
        `Native  : ${formatEnabled(status.native?.enabled)}, ` +
            `${status.native?.configured ? "configured" : "not configured"}`,
        "Recent signals:",
        signals,
        "Commands: /black-box-tail [N] | /black-box-export [N] | /black-box-doctor"
    ].join("\n");
}

function tailLine(record) {
    const attributes = record.attributes ?? {};
    const duration = Number.isFinite(attributes.durationMs) ? ` ${attributes.durationMs}ms` : "";
    const outcome = typeof attributes.success === "boolean" ? ` success=${attributes.success}` : "";
    return `${record.timestamp} ${record.kind} ${record.eventType}${duration}${outcome}`;
}

function formatTimeline(records) {
    if (!records.length) return "Black Box timeline is empty.";
    return [
        `Afterburner Black Box timeline (${records.length} record(s))`,
        "========================================",
        ...records.map(tailLine)
    ].join("\n");
}

function modalOpenSucceeded(result) {
    return result === undefined || typeof result === "string" || result?.ok === true || result?.opened === true ||
        result?.frame || (typeof result?.canvasId === "string" && typeof result?.instanceId === "string");
}

function renderReturnedFrame(frame) {
    if (typeof frame === "string") return frame.trim();
    if (!frame || typeof frame !== "object") return "";
    return [frame.title, frame.status, frame.body, frame.footer]
        .filter(value => typeof value === "string" && value.trim().length > 0)
        .join("\n");
}

function returnedFallbackText(result) {
    if (typeof result === "string") return result.trim();
    if (!result || typeof result !== "object") return "";
    return renderReturnedFrame(result.frame ?? result.fallbackFrame) ||
        [result.fallbackText, result.text, result.message, result.content, result.body]
            .find(value => typeof value === "string" && value.trim().length > 0)?.trim() || "";
}

function modalFallbackText(reason, status) {
    return [
        `Black Box modal unavailable: ${reason}. Showing text fallback.`,
        "",
        formatPanel(status)
    ].join("\n");
}

async function safeAction(action) {
    try { return await action(); }
    catch { return { ok: false, error: "black-box-unavailable" }; }
}

export async function buildSessionRegistration({ service, createCanvas, joinSession, openModalCanvas }) {
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
    const canvas = typeof createCanvas === "function" ? createCanvas({
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
    }) : null;

    const logSafely = async action => {
        try { await action(); }
        catch { await session?.log("Black Box operation failed without affecting the session."); }
    };
    const openRuntimeModal = async () => {
        if (typeof openModalCanvas !== "function") {
            return { opened: false, reason: "modal open API is unavailable in this session" };
        }
        try {
            const result = await openModalCanvas("afterburner-black-box", {});
            if (modalOpenSucceeded(result)) {
                return {
                    opened: true,
                    fallback: typeof result === "string" || result?.fallback === true,
                    fallbackText: returnedFallbackText(result)
                };
            }
            return { opened: false, reason: "the registered runtime modal did not open" };
        } catch (error) {
            const detail = String(error?.message ?? error ?? "unknown error").replace(/[\r\n\t]+/g, " ").slice(0, 500);
            return { opened: false, reason: `the registered runtime modal is unavailable (${detail})` };
        }
    };

    session = await joinSession({
        commands: [
            {
                name: "black-box",
                description: "Show Black Box recorder, retention, queue, and anomaly status.",
                handler: async () => logSafely(async () => session.log(formatPanel(await service.status())))
            },
            {
                name: "black-box-modal",
                description: "Open the registered Black Box live modal, or show an explicit text fallback.",
                handler: async () => logSafely(async () => {
                    const result = await openRuntimeModal();
                    if (result.opened) {
                        if (result.fallback) {
                            await session.log(result.fallbackText ||
                                modalFallbackText("the host opened a text fallback because no modal broker is attached", await service.status()));
                            return;
                        }
                        await session.log("Black Box live modal opened.");
                        return;
                    }
                    await session.log(modalFallbackText(result.reason, await service.status()));
                })
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
                    await session.log(formatTimeline(records));
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
        canvases: canvas ? [canvas] : []
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
            joinSession: options.joinSession,
            openModalCanvas: options.openModalCanvas
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
