import { createHash, randomUUID } from "node:crypto";
import { sanitizeStoredRecord } from "./schema.mjs";

const BODY_KEYS = new Set([
    "content", "transformedcontent", "encryptedcontent", "reasoningopaque", "reasoningblocks", "reasoningsummary",
    "message", "messages", "summary", "result", "arguments", "input", "output", "response",
    "responsechunk", "requestmessages", "assignmentcontext", "toolrequests", "servertools", "detailedcontent"
]);
const STRING_FIELDS = new Map([
    ["model", "model"], ["modelId", "model"], ["selectedModel", "model"], ["currentModel", "model"],
    ["previousModel", "previousModel"], ["newModel", "newModel"], ["toolName", "toolName"],
    ["extensionId", "extensionId"], ["extensionKind", "extensionKind"], ["observerId", "observerId"],
    ["providerId", "providerId"], ["agentId", "agentId"], ["taskId", "taskId"], ["state", "state"],
    ["phase", "phase"], ["delivery", "delivery"], ["hookType", "hookType"], ["mode", "mode"],
    ["newMode", "mode"], ["previousMode", "previousMode"], ["cause", "cause"], ["source", "changeSource"],
    ["contextTier", "contextTier"], ["reasoningEffort", "reasoningEffort"],
    ["previousReasoningEffort", "previousReasoningEffort"],
    ["shutdownType", "shutdownType"], ["errorType", "errorType"], ["warningType", "warningType"],
    ["infoType", "infoType"], ["initiator", "initiator"], ["transport", "transport"],
    ["surfaceId", "surfaceId"], ["instanceId", "instanceId"], ["hostId", "hostId"],
    ["sinkId", "sinkId"], ["envelopeId", "envelopeId"], ["envelopeKind", "envelopeKind"],
    ["uiEventType", "uiEventType"], ["lifecycleState", "lifecycleState"],
    ["recoveryState", "recoveryState"], ["securityDecision", "securityDecision"],
    ["policyDecision", "policyDecision"], ["grantId", "grantId"], ["reason", "reason"],
    ["kind", "kind"]
]);
const BOOLEAN_FIELDS = new Set(["success", "rte", "alreadyInUse", "remoteSteerable"]);
const NUMBER_FIELDS = new Set([
    "statusCode", "turn", "toolCount", "eventCount", "eventsFileSizeBytes", "version", "totalNanoAiu",
    "totalPremiumRequests", "conversationTokens", "currentTokens", "systemTokens", "toolDefinitionsTokens",
    "totalApiDurationMs", "modelCallDurationMs", "durationMs", "ttftMs", "outputTtftMs", "interTokenLatencyMs",
    "contextWindowTokens", "maxContextWindowTokens", "maxOutputTokens", "queueDepth", "delivered", "dropped",
    "failures", "disposeFailures", "eventTypeCount", "taskCount", "revision"
]);
const CORRELATIONS = [
    ["eventRef", value => value?.id],
    ["parentRef", value => value?.parentId],
    ["turnRef", value => value?.data?.turnId],
    ["interactionRef", value => value?.data?.interactionId],
    ["toolCallRef", value => value?.data?.toolCallId],
    ["modelCallRef", value => value?.data?.callId ?? value?.data?.providerCallId],
    ["requestRef", value => value?.data?.requestId ?? value?.data?.clientRequestId ?? value?.data?.serviceRequestId]
];

function cleanLabel(value) {
    if (typeof value !== "string") return undefined;
    const normalized = value.replace(/[\r\n\0\t]/g, " ").replace(/\s+/g, " ").trim();
    if (!normalized) return undefined;
    return normalized.slice(0, 160);
}

function eventType(value) {
    const normalized = typeof value === "string" ? value.toLowerCase().replace(/[^a-z0-9._-]+/g, "-") : "unknown";
    return (/^[a-z0-9]/.test(normalized) ? normalized : `event-${normalized}`).slice(0, 96) || "unknown";
}

function timestamp(value, fallback) {
    const parsed = Date.parse(value);
    return Number.isFinite(parsed) ? new Date(parsed).toISOString() : fallback;
}

function bodyReference(field, value) {
    const reference = { field: field.slice(0, 160) };
    if (typeof value === "string") reference.byteLength = Buffer.byteLength(value, "utf8");
    else if (Array.isArray(value)) reference.itemCount = value.length;
    else if (value && typeof value === "object") reference.fieldCount = Object.keys(value).length;
    return reference;
}

function findBodyReferences(value, path = "data", output = [], depth = 0) {
    if (!value || typeof value !== "object" || depth > 5 || output.length >= 32) return output;
    for (const [key, child] of Object.entries(value)) {
        const childPath = `${path}.${key}`;
        const normalizedKey = key.toLowerCase();
        if (BODY_KEYS.has(normalizedKey) || /(content|messages?|summary|arguments|result)$/.test(normalizedKey)) {
            output.push(bodyReference(childPath, child));
            if (output.length >= 32) break;
        } else if (child && typeof child === "object") {
            findBodyReferences(child, childPath, output, depth + 1);
        }
    }
    return output;
}

function collectScalarMetrics(value, prefix, output, depth = 0) {
    if (!value || typeof value !== "object" || depth > 4 || Object.keys(output).length >= 90) return;
    for (const [key, child] of Object.entries(value)) {
        const metricKey = `${prefix}.${key}`.slice(0, 86);
        if (typeof child === "number" && Number.isFinite(child)) output[metricKey] = child;
        else if (typeof child === "boolean") output[metricKey] = child;
        else if (child && typeof child === "object" && !Array.isArray(child)) {
            collectScalarMetrics(child, metricKey, output, depth + 1);
        }
        if (Object.keys(output).length >= 90) break;
    }
}

function referenceId(salt, scope, value) {
    if (value === undefined || value === null || value === "") return undefined;
    return `ref_${createHash("sha256").update(String(salt)).update("\0").update(scope).update("\0")
        .update(String(value)).digest("hex").slice(0, 24)}`;
}

function recordId(idFactory) {
    return `rec_${idFactory().replace(/-/g, "").slice(0, 24).toLowerCase()}`;
}

function severityFor(type, data) {
    if (type === "session.error" || data?.success === false) return "error";
    if (type.includes("warning")) return "warning";
    return "info";
}

export function createReferenceFactory(salt) {
    return (scope, value) => referenceId(salt, scope, value);
}

export function sanitizeNativeEvent(rawEvent, source = {}, options = {}) {
    const now = options.now?.() ?? new Date();
    const observedAt = now instanceof Date ? now.toISOString() : new Date(now).toISOString();
    const idFactory = options.idFactory ?? randomUUID;
    const salt = options.salt ?? "black-box-ephemeral";
    const raw = rawEvent && typeof rawEvent === "object" ? rawEvent : {};
    const data = raw.data && typeof raw.data === "object" ? raw.data : {};
    const type = eventType(raw.type);
    const attributes = {};

    for (const [sourceKey, targetKey] of STRING_FIELDS) {
        const label = cleanLabel(data[sourceKey]);
        if (label !== undefined) attributes[targetKey] = label;
    }
    for (const key of BOOLEAN_FIELDS) if (typeof data[key] === "boolean") attributes[key] = data[key];
    for (const key of NUMBER_FIELDS) if (Number.isFinite(data[key])) attributes[key] = data[key];
    const modelCall = data.modelCall && typeof data.modelCall === "object" ? data.modelCall : {};
    for (const key of ["model", "initiator", "transport"]) {
        const label = cleanLabel(modelCall[key]);
        if (label !== undefined) attributes[key] = label;
    }
    if (typeof modelCall.rte === "boolean") attributes.rte = modelCall.rte;
    for (const [key, prefix] of [
        ["responseUsage", "usage"], ["copilotUsage", "usage.copilot"], ["quotaSnapshots", "quota"],
        ["agentMetrics", "metrics.agent"], ["modelMetrics", "metrics.model"], ["codeChanges", "codeChanges"]
    ]) collectScalarMetrics(data[key], prefix, attributes);
    if (Array.isArray(data.codeChanges?.filesModified)) attributes.fileCount = data.codeChanges.filesModified.length;
    if (Number.isFinite(data.codeChanges?.linesAdded)) attributes.linesAdded = data.codeChanges.linesAdded;
    if (Number.isFinite(data.codeChanges?.linesRemoved)) attributes.linesRemoved = data.codeChanges.linesRemoved;

    const correlation = {};
    for (const [key, getter] of CORRELATIONS) {
        const reference = referenceId(salt, key, getter(raw));
        if (reference) correlation[key] = reference;
    }
    const sessionId = source.sessionId ?? data.sessionId;
    const cleanSource = { kind: ["runtime-observer", "native-events", "black-box"].includes(source.kind) ? source.kind : "black-box" };
    for (const [key, scope, value] of [
        ["processRef", "process", source.processId],
        ["sessionRef", "session", sessionId],
        ["pathRef", "path", source.path]
    ]) {
        const reference = referenceId(salt, scope, value);
        if (reference) cleanSource[key] = reference;
    }
    if (Number.isSafeInteger(source.byteStart) && source.byteStart >= 0) cleanSource.byteStart = source.byteStart;
    if (Number.isSafeInteger(source.byteEnd) && source.byteEnd >= 0) cleanSource.byteEnd = source.byteEnd;

    return sanitizeStoredRecord({
        schemaVersion: 1,
        recordId: recordId(idFactory),
        kind: "event",
        timestamp: timestamp(raw.timestamp, observedAt),
        observedAt,
        eventType: type,
        severity: severityFor(type, data),
        source: cleanSource,
        correlation,
        attributes,
        bodyReferences: findBodyReferences(data)
    });
}

export function createBlackBoxRecord({
    kind, eventType: type, severity = "info", source = { kind: "black-box" }, correlation = {},
    attributes = {}, bodyReferences = [], timestamp: eventTimestamp
}, options = {}) {
    const now = options.now?.() ?? new Date();
    const observedAt = now instanceof Date ? now.toISOString() : new Date(now).toISOString();
    return sanitizeStoredRecord({
        schemaVersion: 1,
        recordId: recordId(options.idFactory ?? randomUUID),
        kind,
        timestamp: timestamp(eventTimestamp, observedAt),
        observedAt,
        eventType: eventType(type),
        severity,
        source,
        correlation,
        attributes,
        bodyReferences
    });
}
