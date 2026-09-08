import { createHash } from "node:crypto";

export const LIVE_MODAL_ID = "afterburner-black-box-live";

const MODAL_DOCUMENT_CAPABILITIES = Object.freeze([
    "ui.render.components",
    "ui.action.invoke",
    "ui.data.read",
    "ui.localization.read",
    "ui.accessibility.inspect"
]);
const SAFE_ATTRIBUTE_KEYS = new Set([
    "model", "previousModel", "newModel", "toolName", "extensionId", "extensionKind", "observerId",
    "providerId", "agentId", "taskId", "state", "phase", "delivery", "hookType", "mode",
    "previousMode", "changeSource", "contextTier", "reasoningEffort", "previousReasoningEffort",
    "shutdownType", "errorType", "warningType", "infoType", "initiator", "transport", "success",
    "rte", "alreadyInUse", "remoteSteerable", "statusCode", "turn", "toolCount", "eventCount",
    "eventsFileSizeBytes", "version", "totalNanoAiu", "totalPremiumRequests", "conversationTokens",
    "currentTokens", "systemTokens", "toolDefinitionsTokens", "totalApiDurationMs", "modelCallDurationMs",
    "durationMs", "ttftMs", "outputTtftMs", "interTokenLatencyMs", "contextWindowTokens",
    "maxContextWindowTokens", "maxOutputTokens", "queueDepth", "delivered", "dropped", "failures",
    "disposeFailures", "eventTypeCount", "taskCount", "revision", "surfaceId", "instanceId", "hostId",
    "lifecycleState", "recoveryState", "securityDecision", "policyDecision", "reason", "kind"
]);
const DENIED_KEY_PATTERN = /(prompt|content|message|messages|summary|result|arguments|input|output|response|body|path|cwd|directory|file|secret|token|key|credential|password)/i;

export function buildModalFrame(ui, state = {}, options = {}) {
    const status = state.status ?? emptyStatus();
    const records = sortRecords(state.records ?? [], state.sort).slice(0, state.view?.limit ?? 12);
    const selected = records.find(record => record.recordId === state.selectedRecordId) ?? records[0] ?? null;
    const healthTone = healthToneFor(status, state.lifecycle);
    const progressValue = Math.min(100, percent(status.storage?.segmentBytes, status.storage?.maxBytes));
    const title = options.title ?? state.title ?? "Afterburner Black Box Live";
    const frame = {
        title,
        status: `${status.storage?.segmentCount ?? 0} segment(s), ${status.analytics?.totalRecords ?? records.length} record(s), ${status.queue?.droppedRecords ?? 0} dropped · metadata-only`,
        body: modalTextBody({ status, records, selected, state, healthTone, progressValue }),
        footer: "Esc/q closes · r refresh · d doctor · metadata-only · fallback /black-box-tail",
        actions: [
            { name: "refresh", label: "Refresh", key: "r", description: "Refresh status cards, timeline, and details." },
            { name: "doctor", label: "Doctor", key: "d", description: "Run metadata-only diagnostics." },
            { name: "export", label: "Export", key: "e", description: "Create a sanitized evidence bundle." },
            { name: "close", label: "Close", key: "q", description: "Close the Black Box live modal." }
        ]
    };
    const document = buildModalDocument(ui, { status, records, selected, state, healthTone, progressValue, title, frame });
    if (document) frame.document = document;
    return frame;
}

function buildModalDocument(ui, context) {
    if (!ui?.createUIDocument) return null;
    const c = ui.components ?? ui;
    if (!c?.dialog || !c?.toolbar || !c?.grid || !c?.panel || !c?.table) return null;
    const { status, records, selected, state, healthTone, progressValue, title, frame } = context;
    const actionBar = c.actionBar ?? c.toolbar;
    const statusGrid = c.statusGrid ?? c.grid;
    const surface = c.surface ?? c.application;
    const viewport = c.viewport ?? c.stack;
    const split = c.split ?? c.row;
    const scroll = c.scroll ?? c.panel;
    try {
        const root = c.dialog({ title, status: frame.status, modal: true }, [
            surface({ mode: "metadata-only", density: "comfortable" }, [
                c.breadcrumb?.({ items: [
                    { id: "afterburner", label: "Afterburner" },
                    { id: "black-box", label: "Black Box" },
                    { id: state.activeTab ?? "timeline", label: activeTabTitle(state) }
                ] }, [], { id: "bb-modal-breadcrumb", accessibility: { role: "navigation", name: "Black Box modal location" } }),
                actionBar({ label: "Black Box action bar" }, frame.actions.map(action =>
                    c.button({ label: action.label, actionId: action.name, keybinding: action.key, description: action.description }, [], {
                        id: `bb-modal-action-${action.name}`,
                        actionBindings: { activate: action.name }
                    })
                ), { id: "bb-modal-action-bar", accessibility: { role: "toolbar", name: "Black Box action bar" } }),
                viewport({}, [
                    statusGrid({ label: "Black Box modal status cards", columns: ["recorder", "storage", "signals", "queue"] }, [
                        metricCard(c, "bb-modal-card-recorder", "Recorder", status.enabled ? "Enabled" : "Disabled", `${status.mode ?? "unknown"} · ${status.analytics?.totalRecords ?? records.length} records`, status.enabled ? "success" : "warning"),
                        metricCard(c, "bb-modal-card-storage", "Storage", `${status.storage?.segmentCount ?? 0} segment(s)`, `${formatBytes(status.storage?.segmentBytes)} used`, healthTone),
                        metricCard(c, "bb-modal-card-signals", "Signals", `${status.analytics?.anomalyCount ?? 0} anomalies`, `${status.analytics?.milestoneCount ?? 0} milestones`, status.analytics?.anomalyCount ? "warning" : "success"),
                        metricCard(c, "bb-modal-card-queue", "Queue", `${status.queue?.records ?? 0} queued`, `${status.queue?.droppedRecords ?? 0} dropped`, status.queue?.droppedRecords ? "warning" : "info")
                    ], { id: "bb-modal-status-cards" }),
                    c.progress({ label: "Storage usage", value: progressValue, max: 100, status: `${formatPercent(progressValue)} used`, tone: healthTone }, [], {
                        id: "bb-modal-storage-progress",
                        accessibility: { role: "progressbar", name: "Black Box modal storage usage", valueText: `${formatPercent(progressValue)} used` }
                    }),
                    ...signalTrendNodes(c, records),
                    ...healthStateNodes(c, status, state.lifecycle),
                    ...activeResultPanels(c, state),
                    split({ responsive: { collapseBelowColumns: 100, orientation: "vertical" } }, [
                        scroll({}, [
                            c.panel({ title: "Metadata timeline", width: "58%" }, [
                                c.table({
                                    label: "Timeline table",
                                    columns: timelineColumns(),
                                    rows: records.map(record => timelineRow(record, selected?.recordId)),
                                    selection: { selectedRowId: selected?.recordId ?? null },
                                    virtualization: { enabled: records.length > 8, rowHeight: 1, overscan: 4, totalRows: records.length, offset: 0, limit: records.length }
                                }, [], {
                                    id: "bb-modal-timeline-table",
                                    accessibility: { role: "table", name: "Sanitized Black Box modal timeline" }
                                })
                            ], { id: "bb-modal-timeline-panel" })
                        ], { id: "bb-modal-timeline-scroll" }),
                        c.panel({ title: "Details", width: "42%" }, [
                            c.markdown({ markdown: selected ? detailMarkdown(selected) : "No metadata record selected." }, [], { id: "bb-modal-detail-summary" }),
                            c.code({ language: "json", code: selected ? JSON.stringify(redactForDisplay(selected), null, 2) : "{}" }, [], { id: "bb-modal-detail-json" })
                        ], { id: "bb-modal-detail-panel", accessibility: { role: "region", name: "Selected metadata details" } })
                    ], { id: "bb-modal-main-split" }),
                    c.text({ value: modalFooter(state), tone: "muted" }, [], { id: "bb-modal-footer", accessibility: { role: "status", name: "Black Box modal status" } })
                ], { id: "bb-modal-viewport", accessibility: { role: "region", name: "Black Box live metadata" } })
            ], { id: "bb-modal-surface", accessibility: { role: "application", name: "Black Box command center" } })
        ].filter(Boolean), {
            id: "bb-modal-root",
            accessibility: { role: "dialog", name: title },
            metadata: {
                privacy: { metadataOnly: true },
                input: { keyboard: true, mouse: false }
            }
        });
        return ui.createUIDocument(root, {
            surfaceId: LIVE_MODAL_ID,
            revision: nextRevision(state),
            locale: "en-US",
            capabilities: MODAL_DOCUMENT_CAPABILITIES
        });
    } catch {
        return null;
    }
}

function activeTabTitle(state) {
    if (state.activeTab === "doctor") return "Doctor";
    if (state.activeTab === "export") return "Export";
    return "Timeline";
}

function activeResultTextLines(state) {
    if (state.activeTab === "doctor") return ["", "Doctor", JSON.stringify(redactForDisplay(state.doctor ?? { status: "Doctor unavailable" }), null, 2)];
    if (state.activeTab === "export") return ["", "Export", JSON.stringify(redactForDisplay(state.exportResult ?? { status: "Run Export to create a sanitized local bundle." }), null, 2)];
    return [];
}

function activeResultPanels(c, state) {
    if (state.activeTab === "doctor") {
        return [c.panel({ title: "Doctor" }, [
            c.code({ language: "json", code: JSON.stringify(redactForDisplay(state.doctor ?? { status: "Doctor unavailable" }), null, 2) }, [], { id: "bb-modal-doctor-json" })
        ], { id: "bb-modal-doctor-panel" })];
    }
    if (state.activeTab === "export") {
        return [c.panel({ title: "Export" }, [
            c.code({ language: "json", code: JSON.stringify(redactForDisplay(state.exportResult ?? { status: "Run Export to create a sanitized local bundle." }), null, 2) }, [], { id: "bb-modal-export-json" })
        ], { id: "bb-modal-export-panel" })];
    }
    return [];
}

function modalTextBody({ status, records, selected, state, healthTone, progressValue }) {
    return [
        "Shortcuts: r Refresh · d Doctor · e Export · q/Esc Close · ↑/↓ PgUp/PgDn Home/End Scroll",
        "Privacy: metadata-only; payload bodies redacted. Fallback: /black-box-tail",
        "",
        `Storage usage: ${formatPercent(progressValue)} of ${formatBytes(status.storage?.maxBytes)} retained`,
        `Signal trend: ${signalTrend(records)}`,
        "",
        "Status cards",
        `  Recorder  ${status.enabled ? "Enabled " : "Disabled"}  ${fitCell(status.mode ?? "unknown", 8)}  ${status.analytics?.totalRecords ?? records.length} records`,
        `  Storage   ${fitCell(`${status.storage?.segmentCount ?? 0} segment(s)`, 12)}  ${fitCell(formatBytes(status.storage?.segmentBytes), 10)}  ${healthTone}`,
        `  Signals   ${fitCell(`${status.analytics?.anomalyCount ?? 0} anomalies`, 12)}  ${status.analytics?.milestoneCount ?? 0} milestones  ${status.queue?.droppedRecords ?? 0} dropped`,
        `  Queue     ${fitCell(`${status.queue?.records ?? 0} queued`, 12)}  ${fitCell(formatBytes(status.queue?.bytes ?? 0), 10)}  ${status.queue?.writeErrors ?? 0} write errors`,
        ...healthCalloutLines(status, state.lifecycle),
        ...activeResultTextLines(state),
        "",
        "Selected event",
        ...(selected ? selectedSummaryLines(selected) : ["  No metadata event selected."]),
        "",
        "Metadata timeline table",
        ...timelineLines(records),
        "",
        "Details",
        ...(selected ? detailLines(selected) : ["  No metadata events recorded yet."])
    ].join("\n");
}

function timelineLines(records) {
    if (!records.length) return ["  No metadata events recorded yet."];
    const widths = { time: 20, kind: 9, event: 30, severity: 8, duration: 8, success: 7 };
    const header = [
        fitCell("Time", widths.time), fitCell("Kind", widths.kind), fitCell("Event", widths.event),
        fitCell("Severity", widths.severity), fitCell("Duration", widths.duration), fitCell("Success", widths.success)
    ].join(" │ ");
    const divider = Object.values(widths).map(width => "─".repeat(width)).join("─┼─");
    return [`  ${header}`, `  ${divider}`, ...records.map(record => `  ${timelineTextRow(record, widths)}`)];
}

function timelineTextRow(record, widths) {
    const attributes = sanitizeAttributes(record.attributes);
    return [
        fitCell(shortTimestamp(record.timestamp), widths.time),
        fitCell(record.kind ?? "event", widths.kind),
        fitCell(record.eventType ?? "unknown", widths.event),
        fitCell(record.severity ?? "info", widths.severity),
        fitCell(Number.isFinite(attributes.durationMs) ? `${attributes.durationMs}ms` : "", widths.duration),
        fitCell(typeof attributes.success === "boolean" ? String(attributes.success) : "", widths.success)
    ].join(" │ ");
}

function selectedSummaryLines(record) {
    const attributes = sanitizeAttributes(record.attributes);
    const latency = Number.isFinite(attributes.durationMs) ? `${attributes.durationMs}ms` : "duration n/a";
    const outcome = typeof attributes.success === "boolean" ? (attributes.success ? "success" : "failed") : "outcome n/a";
    return [`  ${fitCell(shortTimestamp(record.timestamp), 18)}  ${fitCell(record.kind ?? "event", 9)}  ${fitCell(record.eventType ?? "unknown", 34)}  ${fitCell(record.severity ?? "info", 8)}  ${latency}  ${outcome}`];
}

function healthIssues(status, lifecycle = {}) {
    const issues = [];
    if (lifecycle.backpressure) issues.push("live stream backpressure");
    if (status.queue?.writeErrors) issues.push(`${status.queue.writeErrors} queue write error(s)`);
    if (status.queue?.droppedRecords) issues.push(`${status.queue.droppedRecords} dropped record(s)`);
    if (status.analytics?.anomalyCount) issues.push(`${status.analytics.anomalyCount} anomaly/anomalies`);
    return issues;
}

function healthCalloutLines(status, lifecycle) {
    const issues = healthIssues(status, lifecycle);
    return issues.length ? ["", `Needs attention: ${issues.join(" · ")}`] : [];
}

function healthStateNodes(c, status, lifecycle) {
    const issues = healthIssues(status, lifecycle);
    if (issues.length && typeof c.alert === "function") {
        return [c.alert({ severity: "warning", message: `Needs attention: ${issues.join(" · ")}` }, [], {
            id: "bb-modal-health-alert",
            accessibility: { role: "alert", name: "Black Box health warnings" }
        })];
    }
    if (typeof c.badge === "function") {
        return [c.badge({ label: "Health: no active issues", tone: "success" }, [], {
            id: "bb-modal-health-ok",
            accessibility: { role: "status", name: "Black Box health" }
        })];
    }
    return [];
}

function signalTrend(records) {
    const values = records.slice(0, 12).reverse().map(record => record.kind === "anomaly" ? 3 : record.kind === "milestone" || record.severity === "warning" ? 2 : 1);
    const bars = ["▁", "▃", "▆", "█"];
    return values.length ? values.map(value => bars[value]).join("") : "▁▁▁▁ no recent events";
}

function signalTrendNodes(c, records) {
    if (typeof c.sparkline !== "function") return [];
    const values = records.slice(0, 12).reverse().map(record => record.kind === "anomaly" ? 3 : record.kind === "milestone" || record.severity === "warning" ? 2 : 1);
    return [c.sparkline({ label: "Signal trend", values: values.length ? values : [0], tone: values.some(value => value >= 3) ? "warning" : "info" }, [], {
        id: "bb-modal-signal-trend",
        accessibility: { role: "img", name: "Black Box recent signal trend" }
    })];
}

function detailLines(record) {
    const lines = [
        `  Record   │ ${cleanLabel(record.recordId ?? "unknown")}`,
        `  Event    │ ${cleanLabel(record.eventType ?? "unknown")}`,
        `  Kind     │ ${cleanLabel(record.kind ?? "event")} / ${cleanLabel(record.severity ?? "info")}`,
        `  Time     │ ${cleanLabel(record.timestamp ?? "unknown")}`
    ];
    for (const [key, value] of Object.entries(sanitizeAttributes(record.attributes)).slice(0, 12)) {
        lines.push(`  ${fitCell(key, 8)} │ ${String(value)}`);
    }
    if (record.bodyReferences?.length) lines.push(`  Redacted │ ${record.bodyReferences.length} body reference(s) omitted`);
    return lines;
}

function nextRevision(state) {
    state.revision = Number(state.revision ?? 0) + 1;
    return state.revision;
}

function metricCard(c, id, title, value, detail, tone) {
    return c.card({ title, tone }, [
        c.text({ value, tone }, [], { id: `${id}-value` }),
        c.text({ value: detail, tone: "muted" }, [], { id: `${id}-detail` })
    ], { id, accessibility: { role: "status", name: title } });
}

function timelineColumns() {
    return [
        { id: "timestamp", title: "Timestamp", width: 26 },
        { id: "kind", title: "Kind", width: 10 },
        { id: "eventType", title: "Event", width: 32 },
        { id: "severity", title: "Severity", width: 10 },
        { id: "durationMs", title: "Duration", align: "right", width: 10 },
        { id: "success", title: "Success", width: 8 }
    ];
}

function timelineRow(record, selectedId) {
    const attributes = sanitizeAttributes(record.attributes);
    return {
        id: record.recordId,
        selected: record.recordId === selectedId,
        cells: {
            timestamp: record.timestamp,
            kind: record.kind,
            eventType: record.eventType,
            severity: record.severity,
            durationMs: Number.isFinite(attributes.durationMs) ? attributes.durationMs : "",
            success: typeof attributes.success === "boolean" ? String(attributes.success) : ""
        }
    };
}

function detailMarkdown(record) {
    const attributes = sanitizeAttributes(record.attributes);
    const lines = [
        `**${cleanLabel(record.eventType ?? "unknown")}**`,
        `- Timestamp: ${cleanLabel(record.timestamp ?? "unknown")}`,
        `- Kind: ${cleanLabel(record.kind ?? "event")}`,
        `- Severity: ${cleanLabel(record.severity ?? "info")}`
    ];
    if (Number.isFinite(attributes.durationMs)) lines.push(`- Duration: ${attributes.durationMs} ms`);
    if (typeof attributes.success === "boolean") lines.push(`- Success: ${attributes.success}`);
    if (record.bodyReferences?.length) lines.push(`- Body references redacted: ${record.bodyReferences.length}`);
    return lines.join("\n");
}

function sortRecords(records, sort = {}) {
    const field = ["timestamp", "kind", "eventType", "severity", "durationMs", "success"].includes(sort.field) ? sort.field : "timestamp";
    const direction = sort.direction === "asc" ? 1 : -1;
    return [...records].sort((left, right) => {
        const l = field === "durationMs" || field === "success" ? left.attributes?.[field] : left[field];
        const r = field === "durationMs" || field === "success" ? right.attributes?.[field] : right[field];
        if (l === r) return 0;
        if (l === undefined || l === null || l === "") return direction;
        if (r === undefined || r === null || r === "") return -direction;
        return (l > r ? 1 : -1) * direction;
    });
}

function emptyStatus() {
    return {
        enabled: false,
        mode: "unknown",
        storage: { maxBytes: 0, segmentBytes: 0, segmentCount: 0, retentionBlockedBytes: 0 },
        queue: { records: 0, bytes: 0, droppedRecords: 0, writeErrors: 0 },
        analytics: { anomalyCount: 0, milestoneCount: 0, totalRecords: 0 },
        native: { enabled: false, configured: false },
        recentSignals: []
    };
}

function healthToneFor(status, lifecycle = {}) {
    if (lifecycle.backpressure || status.queue?.writeErrors || status.queue?.droppedRecords || status.analytics?.anomalyCount) return "warning";
    return status.enabled ? "success" : "muted";
}

function percent(value, total) {
    const numerator = Number(value);
    const denominator = Number(total);
    if (!Number.isFinite(numerator) || !Number.isFinite(denominator) || denominator <= 0) return 0;
    return numerator / denominator * 100;
}

function formatPercent(value) {
    return `${Math.round(Number(value) || 0)}%`;
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
    return `${scaled.toFixed(scaled >= 10 || unit === 0 ? 0 : 1)} ${units[unit]}`;
}

function modalFooter(state) {
    const lifecycle = state.lifecycle ?? {};
    const error = lifecycle.lastError ? ` · last error ${cleanLabel(lifecycle.lastError)}` : "";
    return `Metadata only · live stream ${lifecycle.stream ?? "closed"} · coalesced queue ${lifecycle.queueDepth ?? 0} · reconnects ${lifecycle.reconnectCount ?? 0}${error}`;
}

export function sanitizeAttributes(input = {}) {
    const output = {};
    for (const [key, value] of Object.entries(input ?? {})) {
        if (DENIED_KEY_PATTERN.test(key) && !SAFE_ATTRIBUTE_KEYS.has(key)) continue;
        const sanitized = sanitizeScalar(key, decodeJSONScalar(value));
        if (sanitized !== undefined) output[key] = sanitized;
    }
    return output;
}

function decodeJSONScalar(value) {
    if (typeof value !== "string") return value;
    const trimmed = value.trim();
    if (!/^(?:"|\{|\[|true$|false$|null$|-?\d)/.test(trimmed)) return value;
    try { return JSON.parse(trimmed); }
    catch { return value; }
}

function sanitizeScalar(key, value) {
    if (DENIED_KEY_PATTERN.test(key) && !SAFE_ATTRIBUTE_KEYS.has(key)) return undefined;
    if (typeof value === "string") return looksSensitiveString(value) ? "[redacted]" : cleanLabel(value);
    if (typeof value === "number" && Number.isFinite(value)) return value;
    if (typeof value === "boolean") return value;
    if (value && typeof value === "object") return Array.isArray(value) ? value.length : Object.keys(value).length;
    return undefined;
}

function cleanLabel(value) {
    if (typeof value !== "string") return undefined;
    const normalized = value.replace(/[\r\n\0\t]/g, " ").replace(/\s+/g, " ").trim();
    return normalized ? normalized.slice(0, 160) : undefined;
}

function redactForDisplay(value, depth = 0) {
    if (value === null || value === undefined || depth > 6) return value;
    if (typeof value === "string") return looksSensitiveString(value) ? "[redacted]" : value;
    if (Array.isArray(value)) return value.slice(0, 64).map(item => redactForDisplay(item, depth + 1));
    if (typeof value !== "object") return value;
    const output = {};
    for (const [key, child] of Object.entries(value)) {
        output[key] = DENIED_KEY_PATTERN.test(key) && !["pathRef", "bodyReferences"].includes(key)
            ? "[redacted]"
            : redactForDisplay(child, depth + 1);
    }
    return output;
}

function looksSensitiveString(value) {
    return /\b[A-Za-z]:[\\/]|\\\\|(?:^|\s)\/(?:users|home|tmp|var|mnt|workspace)\/|secret|password|credential|api[_-]?key|access[_-]?token/i.test(value);
}

function fitCell(value, width) {
    const text = cleanLabel(String(value ?? "")) ?? "";
    return text.length > width ? `${text.slice(0, Math.max(0, width - 1))}…` : text.padEnd(width, " ");
}

function shortTimestamp(value) {
    return (cleanLabel(value ?? "unknown") ?? "unknown").replace(/^\d{4}-/, "").replace("T", " ").replace(/\.\d{3}Z$/, "Z");
}

export function hashDisplayPath(path) {
    if (typeof path !== "string" || !path) return null;
    return `path_${createHash("sha256").update(path).digest("hex").slice(0, 16)}`;
}
