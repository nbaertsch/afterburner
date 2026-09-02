const RECORD_KINDS = new Set(["event", "milestone", "anomaly", "aggregate", "drop"]);
const SEVERITIES = new Set(["info", "warning", "error"]);
const SOURCE_KINDS = new Set(["runtime-observer", "native-events", "black-box"]);
const CORRELATION_KEYS = new Set([
    "eventRef", "parentRef", "turnRef", "interactionRef", "toolCallRef", "modelCallRef", "requestRef"
]);
const ATTRIBUTE_KEYS = new Set([
    "model", "previousModel", "newModel", "toolName", "extensionId", "extensionKind", "observerId",
    "providerId", "agentId", "taskId", "state", "phase", "success", "rte", "delivery",
    "hookType", "mode", "previousMode", "cause", "changeSource", "contextTier", "reasoningEffort",
    "previousReasoningEffort", "shutdownType", "errorType", "warningType",
    "infoType", "statusCode", "turn", "toolCount", "eventCount", "eventsFileSizeBytes", "version",
    "alreadyInUse", "remoteSteerable", "totalNanoAiu", "totalPremiumRequests", "conversationTokens",
    "currentTokens", "systemTokens", "toolDefinitionsTokens", "totalApiDurationMs", "modelCallDurationMs",
    "ttftMs", "outputTtftMs", "interTokenLatencyMs", "durationMs", "contextWindowTokens",
    "maxContextWindowTokens", "maxOutputTokens", "queueDepth", "delivered", "dropped", "failures",
    "disposeFailures", "eventTypeCount", "taskCount", "revision", "averageToolDurationMs",
    "maxToolDurationMs", "totalRecords", "toolStartCount", "toolCompleteCount", "toolFailureCount",
    "assistantTurnCount", "modelCallCount", "anomalyCount", "milestoneCount", "dropCount", "droppedBytes",
    "segmentCount", "segmentBytes", "queueRecords", "queueBytes", "anomalyCode", "milestoneCode",
    "relatedEventType", "detailCode", "fileCount", "linesAdded", "linesRemoved", "initiator", "transport"
]);
const STRING_ATTRIBUTE_KEYS = new Set([
    "model", "previousModel", "newModel", "toolName", "extensionId", "extensionKind", "observerId",
    "providerId", "agentId", "taskId", "state", "phase", "delivery", "hookType", "mode",
    "previousMode", "cause", "changeSource", "contextTier", "reasoningEffort", "previousReasoningEffort",
    "shutdownType", "errorType", "warningType", "infoType", "anomalyCode", "milestoneCode",
    "relatedEventType", "detailCode", "initiator", "transport"
]);
const REF_PATTERN = /^ref_[a-f0-9]{24}$/;
const RECORD_PATTERN = /^rec_[a-f0-9]{24}$/;
const EVENT_PATTERN = /^[a-z0-9][a-z0-9._-]{0,95}$/;

function plainObject(value) {
    return Boolean(value) && typeof value === "object" && !Array.isArray(value);
}

function validTimestamp(value) {
    return typeof value === "string" && Number.isFinite(Date.parse(value));
}

function validAttributeKey(key) {
    return ATTRIBUTE_KEYS.has(key) || /^(usage|quota|metrics|codeChanges)\.[A-Za-z0-9_.-]{1,80}$/.test(key);
}

function safeScalar(key, value) {
    return value === null || typeof value === "boolean" ||
        (typeof value === "number" && Number.isFinite(value)) ||
        (STRING_ATTRIBUTE_KEYS.has(key) && typeof value === "string" && value.length <= 160 && !/[\r\n\0]/.test(value));
}

export function validateMetadataRecord(record) {
    const errors = [];
    if (!plainObject(record)) return { valid: false, errors: ["record-not-object"] };
    if (record.schemaVersion !== 1) errors.push("schema-version");
    if (!RECORD_PATTERN.test(record.recordId ?? "")) errors.push("record-id");
    if (!RECORD_KINDS.has(record.kind)) errors.push("kind");
    if (!validTimestamp(record.timestamp)) errors.push("timestamp");
    if (!validTimestamp(record.observedAt)) errors.push("observed-at");
    if (!EVENT_PATTERN.test(record.eventType ?? "")) errors.push("event-type");
    if (!SEVERITIES.has(record.severity)) errors.push("severity");

    if (!plainObject(record.source) || !SOURCE_KINDS.has(record.source?.kind)) errors.push("source");
    else {
        for (const key of Object.keys(record.source)) {
            if (!["kind", "processRef", "sessionRef", "pathRef", "byteStart", "byteEnd"].includes(key)) errors.push("source-key");
        }
        for (const key of ["processRef", "sessionRef", "pathRef"]) {
            if (record.source[key] !== undefined && !REF_PATTERN.test(record.source[key])) errors.push(`source-${key}`);
        }
        for (const key of ["byteStart", "byteEnd"]) {
            if (record.source[key] !== undefined && (!Number.isSafeInteger(record.source[key]) || record.source[key] < 0)) {
                errors.push(`source-${key}`);
            }
        }
        if (record.source.byteStart !== undefined && record.source.byteEnd !== undefined &&
            record.source.byteEnd < record.source.byteStart) errors.push("source-byte-order");
    }

    if (!plainObject(record.correlation)) errors.push("correlation");
    else for (const [key, value] of Object.entries(record.correlation)) {
        if (!CORRELATION_KEYS.has(key) || !REF_PATTERN.test(value)) errors.push("correlation-entry");
    }

    if (!plainObject(record.attributes) || Object.keys(record.attributes).length > 96) errors.push("attributes");
    else for (const [key, value] of Object.entries(record.attributes)) {
        if (!validAttributeKey(key) || !safeScalar(key, value)) errors.push("attribute-entry");
    }

    if (!Array.isArray(record.bodyReferences) || record.bodyReferences.length > 32) errors.push("body-references");
    else for (const reference of record.bodyReferences) {
        if (!plainObject(reference) || typeof reference.field !== "string" || reference.field.length > 160 ||
            !/^[A-Za-z0-9_.\[\]-]+$/.test(reference.field)) {
            errors.push("body-reference-field");
            continue;
        }
        for (const key of Object.keys(reference)) {
            if (!["field", "byteLength", "itemCount", "fieldCount"].includes(key)) errors.push("body-reference-key");
        }
        for (const key of ["byteLength", "itemCount", "fieldCount"]) {
            if (reference[key] !== undefined && (!Number.isSafeInteger(reference[key]) || reference[key] < 0)) {
                errors.push("body-reference-count");
            }
        }
    }

    const allowedTop = new Set([
        "schemaVersion", "recordId", "kind", "timestamp", "observedAt", "eventType", "severity",
        "source", "correlation", "attributes", "bodyReferences"
    ]);
    for (const key of Object.keys(record)) if (!allowedTop.has(key)) errors.push("top-level-key");
    return { valid: errors.length === 0, errors: [...new Set(errors)] };
}

export function isMetadataRecord(record) {
    return validateMetadataRecord(record).valid;
}

export function sanitizeStoredRecord(record) {
    if (!plainObject(record)) return null;
    const clean = {
        schemaVersion: 1,
        recordId: RECORD_PATTERN.test(record.recordId ?? "") ? record.recordId : "",
        kind: RECORD_KINDS.has(record.kind) ? record.kind : "event",
        timestamp: validTimestamp(record.timestamp) ? new Date(record.timestamp).toISOString() : "",
        observedAt: validTimestamp(record.observedAt) ? new Date(record.observedAt).toISOString() : "",
        eventType: EVENT_PATTERN.test(record.eventType ?? "") ? record.eventType : "",
        severity: SEVERITIES.has(record.severity) ? record.severity : "info",
        source: { kind: SOURCE_KINDS.has(record.source?.kind) ? record.source.kind : "black-box" },
        correlation: {},
        attributes: {},
        bodyReferences: []
    };
    for (const key of ["processRef", "sessionRef", "pathRef"]) {
        if (REF_PATTERN.test(record.source?.[key] ?? "")) clean.source[key] = record.source[key];
    }
    for (const key of ["byteStart", "byteEnd"]) {
        if (Number.isSafeInteger(record.source?.[key]) && record.source[key] >= 0) clean.source[key] = record.source[key];
    }
    for (const [key, value] of Object.entries(record.correlation ?? {})) {
        if (CORRELATION_KEYS.has(key) && REF_PATTERN.test(value)) clean.correlation[key] = value;
    }
    for (const [key, value] of Object.entries(record.attributes ?? {}).slice(0, 96)) {
        if (validAttributeKey(key) && safeScalar(key, value)) clean.attributes[key] = value;
    }
    for (const reference of (Array.isArray(record.bodyReferences) ? record.bodyReferences : []).slice(0, 32)) {
        if (!plainObject(reference) || typeof reference.field !== "string" || reference.field.length > 160 ||
            !/^[A-Za-z0-9_.\[\]-]+$/.test(reference.field)) continue;
        const cleanReference = { field: reference.field };
        for (const key of ["byteLength", "itemCount", "fieldCount"]) {
            if (Number.isSafeInteger(reference[key]) && reference[key] >= 0) cleanReference[key] = reference[key];
        }
        clean.bodyReferences.push(cleanReference);
    }
    return isMetadataRecord(clean) ? clean : null;
}
