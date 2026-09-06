import { createHash } from "node:crypto";

export const ENTERPRISE_SURFACE_ID = "afterburner-black-box-live";
export const LEGACY_MODAL_ID = ENTERPRISE_SURFACE_ID;
export const OBSERVABILITY_SINK_ID = "black-box.ui.events";
export const BLACK_BOX_EXTENSION_ID = "black-box";
export const BLACK_BOX_OBSERVABILITY_CAPABILITY = "ui.observability.black-box.sink";
export const ENTERPRISE_SURFACE_CAPABILITIES = Object.freeze([
    "ui.render.components",
    "ui.surface.panel",
    "ui.action.invoke",
    "ui.data.read",
    "ui.stream.read",
    "ui.observability.black-box.sink",
    "ui.localization.read",
    "ui.accessibility.inspect"
]);

// Packaged Black Box must not import repository SDK paths; add only UI bundles shipped inside Black Box.
const PACKAGE_RELATIVE_UI_IMPORT_CANDIDATES = Object.freeze([]);
const MAX_RECORDS = 200;
const VISIBLE_RECORDS = 40;
const PATCH_THROTTLE_MS = 250;
const QUEUE_LIMIT = 64;
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
    "sinkId", "envelopeId", "envelopeKind", "uiEventType", "lifecycleState", "recoveryState",
    "securityDecision", "policyDecision", "grantId", "reason", "kind"
]);
const DENIED_KEY_PATTERN = /(prompt|content|message|messages|summary|result|arguments|input|output|response|body|path|cwd|directory|file|secret|token|key|credential|password)/i;

let cachedUI;
let attemptedImport = false;

export async function loadAfterburnerUI() {
    if (attemptedImport) return cachedUI;
    attemptedImport = true;
    for (const specifier of PACKAGE_RELATIVE_UI_IMPORT_CANDIDATES) {
        try {
            cachedUI = await import(specifier);
            return cachedUI;
        } catch {}
    }
    cachedUI = null;
    return null;
}

function hasRuntimeUI(ui) {
    return ui && typeof ui.createRuntime === "function" && typeof ui.createUIDocument === "function";
}

async function resolveAfterburnerUI(api = {}, options = {}) {
    if (options.ui !== undefined) return hasRuntimeUI(options.ui) ? options.ui : null;
    if (hasRuntimeUI(api.ui)) return api.ui;
    const imported = await loadAfterburnerUI();
    if (hasRuntimeUI(imported)) return imported;
    if (apiFunction(api, ["registerSurface"]) || apiFunction(api, ["renderSurface", "render"])) return createEmbeddedRuntimeUI();
    return null;
}

function createEmbeddedRuntimeUI() {
    const component = kind => (props = {}, children = [], options = {}) => ({ kind, props, children, ...options });
    const components = new Proxy({}, { get: (_target, kind) => component(String(kind)) });
    return {
        components,
        createUIDocument(root, options = {}) {
            return {
                schemaVersion: 1,
                protocol: "afterburner.ui",
                revision: options.revision ?? 1,
                surfaceId: options.surfaceId ?? root?.props?.surfaceId ?? ENTERPRISE_SURFACE_ID,
                root,
                locale: options.locale,
                capabilities: options.capabilities
            };
        },
        createRuntime({ bridge } = {}) {
            return {
                defineSurface(definition) {
                    const descriptor = {
                        ...definition,
                        actions: (definition.actions ?? []).map(({ handler, ...action }) => action)
                    };
                    return {
                        id: definition.id,
                        descriptor,
                        async open(input) {
                            const state = await definition.open?.(input);
                            const document = await definition.render?.({ state, previous: null });
                            if (bridge?.render) await bridge.render(definition.id, document, {});
                            return { ok: true, fallback: false, state, document };
                        },
                        async update(state) {
                            const document = await definition.render?.({ state, previous: state?.document });
                            if (bridge?.render) await bridge.render(definition.id, document, {});
                            return { ok: true, document };
                        },
                        async patch(patch, options) { return await bridge?.patch?.(definition.id, patch, options) ?? { ok: true }; },
                        async close(options) {
                            await definition.close?.(options);
                            if (bridge?.close) await bridge.close(definition.id, options);
                            return { ok: true };
                        },
                        async invoke(actionId, parameters) {
                            const action = (definition.actions ?? []).find(candidate => candidate.id === actionId);
                            return action?.handler?.(parameters) ?? { ok: false, error: "unknown-action" };
                        },
                        fallback() { return { ok: false, fallback: true, text: "deterministic metadata-only fallback" }; }
                    };
                }
            };
        }
    };
}

export function blackBoxSurfaceDescriptor() {
    return {
        id: ENTERPRISE_SURFACE_ID,
        kind: "panel",
        title: "Afterburner Black Box",
        ownerExtensionId: BLACK_BOX_EXTENSION_ID,
        supportedComponents: [
            "application", "surface", "viewport", "stack", "row", "grid", "panel", "card", "text",
            "markdown", "code", "badge", "button", "progress", "table", "toolbar", "tabs",
            "commandPalette", "keybindingHint"
        ],
        actions: enterpriseActions().map(action => ({
            id: action.id,
            title: action.title,
            description: action.description,
            effect: action.effect,
            inputSchema: action.inputSchema
        })),
        dataSources: [
            {
                id: "black-box.timeline",
                kind: "query",
                description: "Sanitized metadata timeline with virtualized paging, filtering, and sorting.",
                inputSchema: timelineInputSchema()
            },
            {
                id: "black-box.live-records",
                kind: "stream",
                description: "Coalesced metadata-only live record notifications."
            },
            {
                id: "black-box.status",
                kind: "query",
                description: "Recorder, queue, analytics, and storage health summary."
            }
        ],
        streams: [
            {
                id: "black-box.live-records",
                encoding: "json",
                description: "Backpressured stream of sanitized Black Box records and UI observability observations."
            }
        ],
        metadata: surfaceMetadata()
    };
}

export function enterpriseGrantDeclarations() {
    return ENTERPRISE_SURFACE_CAPABILITIES.map(capability => ({
        id: `black-box.${capability.replace(/^ui\./, "").replace(/\./g, "-")}`,
        capability,
        scope: capability.includes("surface") ? "surface" : "extension",
        resources: capability === BLACK_BOX_OBSERVABILITY_CAPABILITY ? ["afterburner.ui/envelopes/metadata"] : [ENTERPRISE_SURFACE_ID],
        required: capability !== BLACK_BOX_OBSERVABILITY_CAPABILITY,
        description: capability === BLACK_BOX_OBSERVABILITY_CAPABILITY
            ? "Receive metadata-only UI lifecycle, performance, security, and recovery observations."
            : "Render and operate the Black Box enterprise observability surface."
    }));
}

export function blackBoxObservabilitySinkDescriptor() {
    return {
        id: OBSERVABILITY_SINK_ID,
        extensionId: BLACK_BOX_EXTENSION_ID,
        requirement: "optional",
        capability: BLACK_BOX_OBSERVABILITY_CAPABILITY,
        acceptedKinds: ["component.snapshot", "component.patch", "ui.event", "grant.policy", "lifecycle", "audit.event", "observation", "error", "backpressure"],
        redaction: {
            mode: "metadata-only",
            metadataOnly: true,
            deniedFields: ["payload.prompt", "payload.response", "payload.toolArguments", "payload.toolResult", "payload.source", "payload.body", "path", "cwd"],
            allowedFields: [...SAFE_ATTRIBUTE_KEYS].sort()
        },
        failureBehavior: "ignore-when-absent"
    };
}

function enterpriseActions() {
    return [
        {
            id: "refresh",
            title: "Refresh",
            description: "Refresh status, storage health, and the latest metadata timeline records.",
            effect: "read",
            inputSchema: { type: "object", properties: {}, additionalProperties: true }
        },
        {
            id: "doctor",
            title: "Doctor",
            description: "Run metadata-only Black Box diagnostics.",
            effect: "read",
            inputSchema: { type: "object", properties: {}, additionalProperties: false }
        },
        {
            id: "export",
            title: "Export",
            description: "Create a sanitized local Black Box export bundle.",
            effect: "execute",
            inputSchema: { type: "object", properties: { maxRecords: { type: "integer", minimum: 1 } }, additionalProperties: false }
        },
        {
            id: "select",
            title: "Select record",
            description: "Persist the focused timeline row and update the details pane.",
            effect: "navigate",
            inputSchema: { type: "object", properties: { recordId: { type: "string" } }, required: ["recordId"], additionalProperties: false }
        },
        {
            id: "filter",
            title: "Filter timeline",
            description: "Apply a metadata-only filter to timeline rows.",
            effect: "read",
            inputSchema: { type: "object", properties: { query: { type: "string" } }, additionalProperties: false }
        },
        {
            id: "sort",
            title: "Sort timeline",
            description: "Sort timeline rows by timestamp, kind, type, severity, duration, or success.",
            effect: "read",
            inputSchema: {
                type: "object",
                properties: {
                    field: { type: "string", enum: ["timestamp", "kind", "eventType", "severity", "durationMs", "success"] },
                    direction: { type: "string", enum: ["asc", "desc"] }
                },
                additionalProperties: false
            }
        },
        {
            id: "close",
            title: "Close",
            description: "Close the Black Box surface without stopping the recorder.",
            effect: "dismiss",
            inputSchema: { type: "object", properties: {}, additionalProperties: false }
        }
    ];
}

function timelineInputSchema() {
    return {
        type: "object",
        properties: {
            filter: { type: "string" },
            sort: { type: "object" },
            offset: { type: "integer", minimum: 0 },
            limit: { type: "integer", minimum: 1, maximum: MAX_RECORDS }
        },
        additionalProperties: false
    };
}

function surfaceMetadata() {
    return {
        enterprise: true,
        version: 1,
        deterministicFallback: true,
        legacyModalCanvasId: LEGACY_MODAL_ID,
        absentHostBehavior: "text-fallback; recorder remains active",
        deniedCapabilityBehavior: "text-fallback plus explicit diagnostic; recorder remains active",
        grants: enterpriseGrantDeclarations(),
        keyboard: {
            navigation: ["Tab", "Shift+Tab", "ArrowUp", "ArrowDown", "PageUp", "PageDown"],
            shortcuts: { refresh: "r", doctor: "d", export: "e", close: "q", commandPalette: "Ctrl+K" }
        },
        mouse: { selectableRows: true, toolbarButtons: true, splitPaneResize: "host-mediated" },
        accessibility: {
            role: "application",
            label: "Afterburner Black Box metadata observability surface",
            liveRegions: ["bb-status", "bb-progress"],
            supportsScreenReaderTables: true
        },
        localization: {
            defaultLocale: "en-US",
            messageBundle: "black-box.enterprise.v1",
            keys: ["title", "timeline", "details", "status", "doctor", "export", "storageHealth"]
        },
        privacy: {
            metadataOnly: true,
            excludes: ["prompts", "assistant bodies", "tool payloads", "secrets", "raw paths", "working directories"]
        }
    };
}

export async function registerEnterpriseSurface(api = {}, service, options = {}) {
    const ui = await resolveAfterburnerUI(api, options);
    const state = createSurfaceState(options.initialState);
    const grantEvaluation = await evaluateGrants(api);
    const denied = grantEvaluation.requiredDenied;
    const bridge = denied ? null : createBridge(api);
    state.fallbackReason = denied ? "required UI capability was denied" : bridge ? null : "UI host bridge is unavailable";
    state.observability = observabilityGrantState(grantEvaluation.optionalDenials);
    const fallbackText = async reason => buildEnterpriseFallbackText(await safeStatus(service), await safeTail(service, { limit: 12 }), reason);

    if (!hasRuntimeUI(ui)) {
        const reason = denied ? "required UI capability was denied" : "UI SDK is unavailable";
        return createFallbackEnterpriseRegistration({ service, state, grantEvaluation, denied, reason, fallbackText });
    }

    state.ui = ui;
    const runtime = ui.createRuntime({ bridge });
    let surface;
    const load = input => loadSurfaceState(service, state, input);
    surface = runtime.defineSurface({
        ...blackBoxSurfaceDescriptor(),
        actions: enterpriseActions().map(action => ({ ...action, handler: parameters => handleAction(action.id, parameters, surface, service, state) })),
        open: async input => {
            state.lifecycle.openedAt = new Date().toISOString();
            return load(input);
        },
        close: async () => { stopStreaming(state); state.lifecycle.closedAt = new Date().toISOString(); },
        render: context => buildEnterpriseSurfaceDocument(ui, context.state ?? state)
    });

    const descriptorResult = await registerDescriptor(api, surface.descriptor, denied);
    let openResult = null;
    const open = async input => {
        if (denied || !bridge) {
            await load(input);
            const reason = denied ? "required UI capability was denied" : "UI host bridge is unavailable";
            return { ok: false, fallback: true, reason, text: await fallbackText(reason) };
        }
        openResult = await surface.open(input);
        if (openResult?.document) state.document = openResult.document;
        startStreaming({ service, surface, state });
        return openResult;
    };
    const recover = async input => {
        state.lifecycle.reconnectCount++;
        state.lifecycle.lastReconnectAt = new Date().toISOString();
        return open(input ?? { preserveFocus: true });
    };
    return {
        id: ENTERPRISE_SURFACE_ID,
        runtime,
        surface,
        state,
        denied,
        grantEvaluation,
        descriptorResult,
        bridge,
        open,
        recover,
        fallbackText: await fallbackText(denied ? "required UI capability was denied" : bridge ? "surface is not open" : "UI host bridge is unavailable"),
        async dispose() {
            stopStreaming(state);
            await surface.close({ reason: "dispose" }).catch(() => {});
        }
    };
}

async function createFallbackEnterpriseRegistration({ service, state, grantEvaluation, denied, reason, fallbackText }) {
    const text = await fallbackText(reason);
    const descriptor = blackBoxSurfaceDescriptor();
    const open = async input => {
        await loadSurfaceState(service, state, input);
        return { ok: false, fallback: true, reason, text: await fallbackText(reason) };
    };
    const surface = {
        id: ENTERPRISE_SURFACE_ID,
        descriptor,
        async open(input) { return open(input); },
        async close() { stopStreaming(state); return { ok: true }; },
        async invoke() { return { ok: false, fallback: true, reason, text: await fallbackText(reason) }; },
        fallback() { return { ok: false, fallback: true, reason, text }; }
    };
    return {
        id: ENTERPRISE_SURFACE_ID,
        runtime: null,
        surface,
        state,
        denied,
        grantEvaluation,
        descriptorResult: { ok: false, denied: Boolean(denied), reason },
        bridge: null,
        open,
        recover: open,
        fallbackText: text,
        async dispose() { stopStreaming(state); }
    };
}

function createSurfaceState(initial = {}) {
    return {
        filter: "",
        sort: { field: "timestamp", direction: "desc" },
        selectedRecordId: null,
        activeTab: "timeline",
        view: { offset: 0, limit: VISIBLE_RECORDS },
        status: null,
        records: [],
        doctor: null,
        exportResult: null,
        document: null,
        revision: 0,
        lifecycle: {
            stream: "closed",
            backpressure: false,
            queueDepth: 0,
            droppedPatches: 0,
            lastPatchAt: null,
            lastReconnectAt: null,
            reconnectCount: 0,
            openedAt: null,
            closedAt: null,
            lastError: null
        },
        observability: observabilityGrantState(),
        ...initial
    };
}

async function loadSurfaceState(service, state, input = {}) {
    const next = { ...state };
    if (typeof input?.filter === "string") next.filter = input.filter.slice(0, 160);
    if (input?.sort && typeof input.sort === "object") next.sort = normalizeSort(input.sort, next.sort);
    if (Number.isSafeInteger(Number(input?.offset)) && Number(input.offset) >= 0) next.view = { ...next.view, offset: Number(input.offset) };
    if (Number.isSafeInteger(Number(input?.limit)) && Number(input.limit) > 0) next.view = { ...next.view, limit: Math.min(Number(input.limit), MAX_RECORDS) };
    next.status = await safeStatus(service);
    next.records = await safeTail(service, { limit: Math.max(MAX_RECORDS, next.view.limit) });
    if (!next.selectedRecordId || !next.records.some(record => record.recordId === next.selectedRecordId)) {
        next.selectedRecordId = next.records[0]?.recordId ?? null;
    }
    Object.assign(state, next);
    return state;
}

async function handleAction(actionId, parameters = {}, surface, service, state) {
    try {
        if (actionId === "close") return surface.close({ reason: "user" });
        if (actionId === "doctor") {
            state.doctor = await service.doctor();
            state.activeTab = "doctor";
            await loadSurfaceState(service, state, parameters);
            return surface.update(state);
        }
        if (actionId === "export") {
            const result = await service.exportBundle(parameters ?? {});
            state.exportResult = { ok: true, pathRef: hashDisplayPath(result.path), manifest: result.manifest };
            state.activeTab = "export";
            await loadSurfaceState(service, state, parameters);
            return surface.update(state);
        }
        if (actionId === "select") {
            if (typeof parameters?.recordId === "string") state.selectedRecordId = parameters.recordId;
            return surface.update(state);
        }
        if (actionId === "filter") {
            state.filter = String(parameters?.query ?? "").slice(0, 160);
            state.view = { ...state.view, offset: 0 };
            return surface.update(await loadSurfaceState(service, state, parameters));
        }
        if (actionId === "sort") {
            state.sort = normalizeSort(parameters, state.sort);
            return surface.update(state);
        }
        return surface.update(await loadSurfaceState(service, state, parameters));
    } catch (error) {
        state.lifecycle.lastError = safeErrorCode(error);
        return { ok: false, error: state.lifecycle.lastError, fallback: true, text: buildEnterpriseFallbackText(state.status, state.records, state.lifecycle.lastError) };
    }
}

function startStreaming({ service, surface, state }) {
    if (state.unsubscribe) return;
    if (typeof service.subscribeRecords !== "function") return;
    state.lifecycle.stream = "open";
    let pending = false;
    let flushing = false;
    let timer = null;
    let queued = 0;
    const clear = () => { if (timer) clearTimeout(timer); timer = null; };
    const flush = async () => {
        timer = null;
        if (flushing) { pending = true; return; }
        flushing = true;
        try {
            await loadSurfaceState(service, state, { preserveFocus: true });
            const previous = state.document;
            const next = buildEnterpriseSurfaceDocument(state.ui, state);
            if (previous?.revision) {
                const patch = {
                    surfaceId: ENTERPRISE_SURFACE_ID,
                    baseRevision: previous.revision,
                    nextRevision: previous.revision + 1,
                    operations: [{ op: "replace", path: "/root", value: next.root, reason: "black-box-live-coalesced-refresh" }]
                };
                const result = await surface.patch(patch, { allowDuringBackpressure: false });
                state.document = result.document ?? { ...next, revision: previous.revision + 1 };
            } else {
                const result = await surface.update(state);
                state.document = result.document ?? null;
            }
            state.lifecycle.backpressure = false;
            state.lifecycle.queueDepth = 0;
            state.lifecycle.lastPatchAt = new Date().toISOString();
        } catch (error) {
            state.lifecycle.lastError = safeErrorCode(error);
            if (state.lifecycle.lastError.includes("backpressure") || queued >= QUEUE_LIMIT) state.lifecycle.backpressure = true;
            if (queued >= QUEUE_LIMIT) state.lifecycle.droppedPatches++;
        } finally {
            queued = 0;
            flushing = false;
            if (pending) {
                pending = false;
                schedule();
            }
        }
    };
    const schedule = () => {
        queued++;
        state.lifecycle.queueDepth = queued;
        if (queued > QUEUE_LIMIT) {
            state.lifecycle.backpressure = true;
            state.lifecycle.droppedPatches++;
            queued = QUEUE_LIMIT;
        }
        if (timer) return;
        timer = setTimeout(flush, PATCH_THROTTLE_MS);
        timer.unref?.();
    };
    const unsubscribe = service.subscribeRecords(schedule);
    state.unsubscribe = () => {
        clear();
        state.lifecycle.stream = "closed";
        try { unsubscribe?.(); } catch {}
    };
}

function stopStreaming(state) {
    try { state.unsubscribe?.(); } catch {}
    delete state.unsubscribe;
    state.lifecycle.stream = "closed";
}

function apiFunction(api = {}, candidates = []) {
    for (const [target, names] of [
        [api.ui, candidates],
        [api, candidates],
        [api.uiBridge, candidates],
        [api.bridge, candidates]
    ]) {
        for (const name of names) {
            if (typeof target?.[name] === "function") return { target, fn: target[name] };
        }
    }
    return null;
}

function createBridge(api = {}) {
    const render = apiFunction(api, ["renderSurface", "render"]);
    const patch = apiFunction(api, ["patchSurface", "patch"]);
    const close = apiFunction(api, ["closeSurface", "close"]);
    if (!render && !patch && !close) return null;
    return {
        interactive: true,
        render: render ? (id, document, options) => render.fn.call(render.target, id, document, options) : undefined,
        patch: patch ? (id, patchDoc, options) => patch.fn.call(patch.target, id, patchDoc, options) : undefined,
        close: close ? (id, options) => close.fn.call(close.target, id, options) : undefined
    };
}

async function registerDescriptor(api = {}, descriptor, denied) {
    if (denied) return { ok: false, denied: true, reason: denied };
    const register = apiFunction(api, ["registerSurface"]);
    if (!register) return { ok: false, reason: "surface-registration-unavailable" };
    try { return await register.fn.call(register.target, descriptor); }
    catch (error) { return { ok: false, denied: isDenied(error), reason: safeErrorCode(error) }; }
}

async function evaluateGrants(api = {}) {
    const declarations = enterpriseGrantDeclarations();
    const required = declarations.filter(item => item.required);
    const optional = declarations.filter(item => !item.required);
    const evaluation = { requiredDenied: null, optionalDenials: [] };
    if (typeof api.requestCapabilities === "function") {
        try {
            const requiredDenied = capabilityDeniedReason(await api.requestCapabilities(required), required, "capability-denied");
            if (requiredDenied) return { ...evaluation, requiredDenied };
        } catch (error) {
            if (isDenied(error)) return { ...evaluation, requiredDenied: safeErrorCode(error) };
            throw error;
        }
        try {
            const optionalDenied = capabilityDenials(await api.requestCapabilities(optional), optional, "optional-capability-denied");
            evaluation.optionalDenials.push(...optionalDenied);
        } catch (error) {
            if (!isDenied(error)) throw error;
            evaluation.optionalDenials.push({ capability: BLACK_BOX_OBSERVABILITY_CAPABILITY, reason: safeErrorCode(error) });
        }
    }
    if (typeof api.hasCapability === "function") {
        for (const grant of required) {
            try {
                if (api.hasCapability(grant.capability) === false) return { ...evaluation, requiredDenied: `capability-denied:${grant.capability}` };
            } catch (error) {
                if (isDenied(error)) return { ...evaluation, requiredDenied: safeErrorCode(error) };
                throw error;
            }
        }
        for (const grant of optional) {
            try {
                if (api.hasCapability(grant.capability) === false) {
                    evaluation.optionalDenials.push({ capability: grant.capability, reason: `capability-denied:${grant.capability}` });
                }
            } catch (error) {
                if (!isDenied(error)) throw error;
                evaluation.optionalDenials.push({ capability: grant.capability, reason: safeErrorCode(error) });
            }
        }
    }
    evaluation.optionalDenials = uniqueDenials(evaluation.optionalDenials);
    return evaluation;
}

function capabilityDeniedReason(result, declarations, fallback) {
    const denials = capabilityDenials(result, declarations, fallback);
    return denials[0]?.reason ?? null;
}

function capabilityDenials(result, declarations, fallback) {
    if (!declarations.length) return [];
    if (result === true || result?.granted === true && !hasExplicitDenials(result)) return [];
    const explicit = explicitCapabilityDenials(result);
    if (result === false || result?.granted === false || explicit.length > 0) {
        if (explicit.length === 0) return declarations.map(grant => ({ capability: grant.capability, reason: fallback }));
        const requested = new Set(declarations.map(grant => grant.capability));
        return explicit
            .filter(denial => !denial.capability || requested.has(denial.capability))
            .map(denial => ({
                capability: denial.capability ?? declarations[0]?.capability,
                reason: safeGrantReason(denial.reason ?? denial.code ?? denial.capability, fallback)
            }));
    }
    return [];
}

function hasExplicitDenials(result) {
    return explicitCapabilityDenials(result).length > 0;
}

function explicitCapabilityDenials(result) {
    const raw = Array.isArray(result?.denied) ? result.denied
        : Array.isArray(result?.denials) ? result.denials
            : Array.isArray(result?.capabilities) ? result.capabilities.filter(item => item?.granted === false || item?.allowed === false)
                : [];
    return raw.map(item => typeof item === "string" ? { capability: item, reason: `capability-denied:${item}` } : {
        capability: typeof item?.capability === "string" ? item.capability : undefined,
        reason: item?.reason ?? item?.code
    });
}

function uniqueDenials(denials = []) {
    const seen = new Set();
    return denials.filter(denial => {
        const key = `${denial.capability ?? "unknown"}:${denial.reason ?? "denied"}`;
        if (seen.has(key)) return false;
        seen.add(key);
        return true;
    });
}

function observabilityGrantState(optionalDenials = []) {
    const denials = uniqueDenials(optionalDenials).map(denial => ({
        capability: denial.capability ?? BLACK_BOX_OBSERVABILITY_CAPABILITY,
        reason: safeGrantReason(denial.reason, "optional-capability-denied")
    }));
    const enabled = denials.length === 0;
    return {
        sinkId: OBSERVABILITY_SINK_ID,
        capability: BLACK_BOX_OBSERVABILITY_CAPABILITY,
        enabled,
        diagnostic: enabled ? null : "optional-observability-sink-denied",
        denials
    };
}

async function evaluateObservabilityGrant(api = {}) {
    const grant = enterpriseGrantDeclarations().find(item => item.capability === BLACK_BOX_OBSERVABILITY_CAPABILITY);
    try {
        if (typeof api.requestCapabilities === "function") {
            const denied = capabilityDenials(await api.requestCapabilities([grant]), [grant], "optional-capability-denied");
            if (denied.length > 0) return observabilityGrantState(denied);
        }
        if (typeof api.hasCapability === "function" && api.hasCapability(BLACK_BOX_OBSERVABILITY_CAPABILITY) === false) {
            return observabilityGrantState([{ capability: BLACK_BOX_OBSERVABILITY_CAPABILITY, reason: `capability-denied:${BLACK_BOX_OBSERVABILITY_CAPABILITY}` }]);
        }
    } catch (error) {
        if (isDenied(error)) return observabilityGrantState([{ capability: BLACK_BOX_OBSERVABILITY_CAPABILITY, reason: safeErrorCode(error) }]);
        throw error;
    }
    return observabilityGrantState();
}

export function buildEnterpriseModalFrame(ui, state = {}, options = {}) {
    const status = state.status ?? emptyStatus();
    const records = filterAndSortRecords(state.records ?? [], state.filter, state.sort).slice(0, state.view?.limit ?? 12);
    const selected = records.find(record => record.recordId === state.selectedRecordId) ?? records[0] ?? null;
    const healthTone = healthToneFor(status, state.lifecycle);
    const storagePercent = percent(status.storage?.segmentBytes, status.storage?.maxBytes);
    const progressValue = Number.isFinite(storagePercent) ? Math.min(100, storagePercent) : 0;
    const title = options.title ?? state.title ?? "Afterburner Black Box Live";
    const frame = {
        title,
        status: `${status.storage?.segmentCount ?? 0} segment(s), ${status.analytics?.totalRecords ?? records.length} record(s), ${status.queue?.droppedRecords ?? 0} dropped · metadata-only`,
        body: modalTextBody({ status, records, selected, state, healthTone, progressValue }),
        footer: "Esc/q closes · r refresh · d doctor · metadata-only · fallback /black-box-tail",
        actions: [
            { name: "refresh", label: "Refresh", key: "r", description: "Refresh status cards, timeline, and details." },
            { name: "doctor", label: "Doctor", key: "d", description: "Run metadata-only diagnostics." },
            { name: "close", label: "Close", key: "q", description: "Close the Black Box live modal." }
        ]
    };
    const document = buildEnterpriseModalDocument(ui, { status, records, selected, state, healthTone, progressValue, title, frame });
    if (document) frame.document = document;
    return frame;
}

function buildEnterpriseModalDocument(ui, { status, records, selected, state, healthTone, progressValue, title, frame }) {
    if (!ui?.createUIDocument) return null;
    const c = ui.components ?? ui;
    if (!c?.dialog || !c?.toolbar || !c?.grid || !c?.panel || !c?.table) return null;
    const actionBar = typeof c.actionBar === "function" ? c.actionBar : c.toolbar;
    const statusGrid = typeof c.statusGrid === "function" ? c.statusGrid : c.grid;
    try {
        const root = c.dialog({ title, status: frame.status, modal: true }, [
            actionBar({ label: "Black Box action bar" }, frame.actions.map(action =>
                c.button({ label: action.label, actionId: action.name, keybinding: action.key, description: action.description }, [], {
                    id: `bb-modal-action-${action.name}`,
                    actionBindings: { activate: action.name }
                })
            ), { id: "bb-modal-action-bar", accessibility: { role: "toolbar", name: "Black Box action bar" } }),
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
            ...modalSignalTrendNodes(c, records),
            ...modalHealthAlertNodes(c, status, state.lifecycle),
            c.row({ label: "Timeline and detail split", responsive: { collapseBelowColumns: 100, orientation: "vertical" } }, [
                c.panel({ title: "Metadata timeline", width: "58%" }, [
                    c.table({
                        label: "Timeline table",
                        columns: timelineColumns(),
                        rows: records.map(record => timelineRow(record, selected?.recordId)),
                        selection: { selectedRowId: selected?.recordId ?? null, persistKey: "black-box.modal.timeline.selection" },
                        virtualization: { enabled: records.length > 8, rowHeight: 1, overscan: 4, totalRows: records.length, offset: 0, limit: records.length }
                    }, [], {
                        id: "bb-modal-timeline-table",
                        actionBindings: { select: "select" },
                        accessibility: { role: "table", name: "Sanitized Black Box modal timeline" },
                        localization: { key: "timeline" }
                    })
                ], { id: "bb-modal-timeline-panel" }),
                c.panel({ title: "Details", width: "42%" }, [
                    c.markdown({ markdown: selected ? detailMarkdown(selected) : "No metadata record selected." }, [], { id: "bb-modal-detail-summary" }),
                    c.code({ language: "json", code: selected ? JSON.stringify(redactForDisplay(selected), null, 2) : "{}" }, [], { id: "bb-modal-detail-json" })
                ], { id: "bb-modal-detail-panel", accessibility: { role: "region", name: "Selected metadata details" }, localization: { key: "details" } })
            ], { id: "bb-modal-main-split" }),
            ...(state.activeTab === "doctor" ? [c.panel({ title: "Doctor" }, [
                c.code({ language: "json", code: JSON.stringify(redactForDisplay(state.doctor ?? { status: "Doctor unavailable" }), null, 2) }, [], { id: "bb-modal-doctor-json" })
            ], { id: "bb-modal-doctor-panel" })] : []),
            c.text({ value: fallbackFooter(state), tone: "muted" }, [], { id: "bb-modal-footer", accessibility: { role: "status", name: "Black Box modal status" } })
        ], { id: "bb-modal-root", accessibility: { role: "dialog", name: title }, metadata: surfaceMetadata() });
        return ui.createUIDocument(root, {
            surfaceId: ENTERPRISE_SURFACE_ID,
            revision: nextRevision(state),
            locale: "en-US",
            capabilities: ENTERPRISE_SURFACE_CAPABILITIES
        });
    } catch {
        return null;
    }
}

function modalTextBody({ status, records, selected, state, healthTone, progressValue }) {
    const lines = [
        "Shortcuts: r Refresh · d Doctor · q/Esc Close · ↑/↓ PgUp/PgDn Home/End Scroll",
        "Privacy: metadata-only; payload bodies redacted. Fallback: /black-box-tail",
        "",
        `Storage usage: ${formatPercent(progressValue)} of ${formatBytes(status.storage?.maxBytes)} retained`,
        `Signal trend: ${modalSignalTrend(records)}`,
        "",
        "Status cards",
        `  Recorder  ${status.enabled ? "Enabled " : "Disabled"}  ${fitCell(status.mode ?? "unknown", 8)}  ${status.analytics?.totalRecords ?? records.length} records`,
        `  Storage   ${fitCell(`${status.storage?.segmentCount ?? 0} segment(s)`, 12)}  ${fitCell(formatBytes(status.storage?.segmentBytes), 10)}  ${healthTone}`,
        `  Signals   ${fitCell(`${status.analytics?.anomalyCount ?? 0} anomalies`, 12)}  ${status.analytics?.milestoneCount ?? 0} milestones  ${status.queue?.droppedRecords ?? 0} dropped`,
        `  Queue     ${fitCell(`${status.queue?.records ?? 0} queued`, 12)}  ${fitCell(formatBytes(status.queue?.bytes ?? 0), 10)}  ${status.queue?.writeErrors ?? 0} write errors`,
        ...modalHealthCalloutLines(status, state.lifecycle),
        "",
        "Selected event",
        ...(selected ? modalSelectedSummaryLines(selected) : ["  No metadata event selected."]),
        "",
        "Metadata timeline table",
        ...modalTimelineLines(records),
        "",
        "Details",
        ...(selected ? modalDetailLines(selected) : ["  No metadata events recorded yet."])
    ];
    if (state.activeTab === "doctor") {
        lines.push("", "Doctor", JSON.stringify(redactForDisplay(state.doctor ?? { status: "Doctor unavailable" }), null, 2));
    }
    return lines.join("\n");
}

function modalTimelineLines(records) {
    if (!records.length) return ["  No metadata events recorded yet."];
    const widths = { time: 20, kind: 9, event: 30, severity: 8, duration: 8, success: 7 };
    const header = [
        fitCell("Time", widths.time), fitCell("Kind", widths.kind), fitCell("Event", widths.event),
        fitCell("Severity", widths.severity), fitCell("Duration", widths.duration), fitCell("Success", widths.success)
    ].join(" │ ");
    const divider = [widths.time, widths.kind, widths.event, widths.severity, widths.duration, widths.success]
        .map(width => "─".repeat(width)).join("─┼─");
    return [
        `  ${header}`,
        `  ${divider}`,
        ...records.map(record => `  ${modalTimelineRow(record, widths)}`)
    ];
}

function modalTimelineRow(record, widths) {
    const attributes = sanitizeAttributes(record.attributes ?? {});
    return [
        fitCell(shortTimestamp(record.timestamp), widths.time),
        fitCell(record.kind ?? "event", widths.kind),
        fitCell(record.eventType ?? "unknown", widths.event),
        fitCell(record.severity ?? "info", widths.severity),
        fitCell(Number.isFinite(attributes.durationMs) ? `${attributes.durationMs}ms` : "", widths.duration),
        fitCell(typeof attributes.success === "boolean" ? String(attributes.success) : "", widths.success)
    ].join(" │ ");
}

function modalSelectedSummaryLines(record) {
    const attributes = sanitizeAttributes(record.attributes ?? {});
    const latency = Number.isFinite(attributes.durationMs) ? `${attributes.durationMs}ms` : "duration n/a";
    const outcome = typeof attributes.success === "boolean" ? (attributes.success ? "success" : "failed") : "outcome n/a";
    return [
        `  ${fitCell(shortTimestamp(record.timestamp), 18)}  ${fitCell(record.kind ?? "event", 9)}  ${fitCell(record.eventType ?? "unknown", 34)}  ${fitCell(record.severity ?? "info", 8)}  ${latency}  ${outcome}`
    ];
}

function modalHealthIssues(status, lifecycle = {}) {
    const issues = [];
    if (lifecycle.backpressure) issues.push("live stream backpressure");
    if (status.queue?.writeErrors) issues.push(`${status.queue.writeErrors} queue write error(s)`);
    if (status.queue?.droppedRecords) issues.push(`${status.queue.droppedRecords} dropped record(s)`);
    if (status.analytics?.anomalyCount) issues.push(`${status.analytics.anomalyCount} anomaly/anomalies`);
    return issues;
}

function modalHealthCalloutLines(status, lifecycle = {}) {
    const issues = modalHealthIssues(status, lifecycle);
    return issues.length ? ["", `Needs attention: ${issues.join(" · ")}`] : [];
}

function modalHealthAlertNodes(c, status, lifecycle = {}) {
    const issues = modalHealthIssues(status, lifecycle);
    if (!issues.length || typeof c.alert !== "function") return [];
    return [c.alert({ severity: "warning", message: `Needs attention: ${issues.join(" · ")}` }, [], {
        id: "bb-modal-health-alert",
        accessibility: { role: "alert", name: "Black Box health warnings" }
    })];
}

function modalSignalTrend(records = []) {
    const values = records.slice(0, 12).reverse().map(record => record.kind === "anomaly" ? 3 : record.kind === "milestone" ? 2 : record.severity === "warning" ? 2 : 1);
    const bars = ["▁", "▃", "▆", "█"];
    return values.length ? values.map(value => bars[Math.max(0, Math.min(bars.length - 1, value))]).join("") : "▁▁▁▁ no recent events";
}

function modalSignalTrendNodes(c, records = []) {
    if (typeof c.sparkline !== "function") return [];
    const values = records.slice(0, 12).reverse().map(record => record.kind === "anomaly" ? 3 : record.kind === "milestone" ? 2 : record.severity === "warning" ? 2 : 1);
    return [c.sparkline({ label: "Signal trend", values: values.length ? values : [0], tone: values.some(value => value >= 3) ? "warning" : "info" }, [], {
        id: "bb-modal-signal-trend",
        accessibility: { role: "img", name: "Black Box recent signal trend" }
    })];
}

function modalDetailLines(record) {
    const attributes = sanitizeAttributes(record.attributes ?? {});
    const lines = [
        `  Record   │ ${cleanLabel(record.recordId ?? "unknown")}`,
        `  Event    │ ${cleanLabel(record.eventType ?? "unknown")}`,
        `  Kind     │ ${cleanLabel(record.kind ?? "event")} / ${cleanLabel(record.severity ?? "info")}`,
        `  Time     │ ${cleanLabel(record.timestamp ?? "unknown")}`
    ];
    for (const [key, value] of Object.entries(attributes).slice(0, 12)) {
        lines.push(`  ${fitCell(key, 8)} │ ${formatAttribute(value)}`);
    }
    if (record.bodyReferences?.length) lines.push(`  Redacted │ ${record.bodyReferences.length} body reference(s) omitted`);
    return lines;
}

function fitCell(value, width) {
    const text = cleanLabel(String(value ?? "")) ?? "";
    if (text.length > width) return `${text.slice(0, Math.max(0, width - 1))}…`;
    return text.padEnd(width, " ");
}

function shortTimestamp(value) {
    const text = cleanLabel(value ?? "unknown") ?? "unknown";
    return text.replace(/^\d{4}-/, "").replace("T", " ").replace(/\.\d{3}Z$/, "Z");
}

function formatAttribute(value) {
    if (typeof value === "number" || typeof value === "boolean") return String(value);
    return cleanLabel(String(value ?? "")) ?? "";
}

export function buildEnterpriseSurfaceDocument(ui, state = {}) {
    if (!ui?.createUIDocument) throw new Error("ui-sdk-unavailable");
    const records = filterAndSortRecords(state.records ?? [], state.filter, state.sort);
    const visible = records.slice(state.view?.offset ?? 0, (state.view?.offset ?? 0) + (state.view?.limit ?? VISIBLE_RECORDS));
    const selected = records.find(record => record.recordId === state.selectedRecordId) ?? visible[0] ?? records[0] ?? null;
    const status = state.status ?? emptyStatus();
    const healthTone = healthToneFor(status, state.lifecycle);
    const storagePercent = percent(status.storage?.segmentBytes, status.storage?.maxBytes);
    const progressValue = Number.isFinite(storagePercent) ? Math.min(100, storagePercent) : 0;
    const c = ui.components ?? ui;
    const doc = ui.createUIDocument(c.application({ title: "Afterburner Black Box Enterprise", surfaceId: ENTERPRISE_SURFACE_ID }, [
        c.commandPalette({
            label: "Black Box command palette",
            placeholder: "Run Black Box action…",
            commands: [
                { id: "refresh", title: "Refresh", keybinding: "r" },
                { id: "doctor", title: "Doctor", keybinding: "d" },
                { id: "export", title: "Export", keybinding: "e" },
                { id: "close", title: "Close", keybinding: "q" }
            ]
        }, [], { id: "bb-command-palette", actionBindings: { run: "refresh" }, accessibility: { role: "searchbox", name: "Black Box command palette" } }),
        c.toolbar({ label: "Black Box actions" }, [
            c.button({ label: "Refresh", actionId: "refresh", keybinding: "r" }, [], { id: "bb-action-refresh", actionBindings: { activate: "refresh" } }),
            c.button({ label: "Doctor", actionId: "doctor", keybinding: "d" }, [], { id: "bb-action-doctor", actionBindings: { activate: "doctor" } }),
            c.button({ label: "Export", actionId: "export", keybinding: "e" }, [], { id: "bb-action-export", actionBindings: { activate: "export" } }),
            c.button({ label: "Close", actionId: "close", keybinding: "q" }, [], { id: "bb-action-close", actionBindings: { activate: "close" } })
        ], { id: "bb-toolbar", accessibility: { role: "toolbar", name: "Black Box actions" } }),
        c.grid({
            label: "Black Box summary",
            columns: ["status", "storage", "signals", "stream"],
            responsive: { collapseBelowColumns: 100, stackBelowColumns: 80 }
        }, [
            metricCard(c, "bb-card-status", "Recorder", status.enabled ? "Enabled" : "Disabled", `${status.mode ?? "unknown"} · ${status.analytics?.totalRecords ?? 0} records`, status.enabled ? "success" : "warning"),
            metricCard(c, "bb-card-storage", "Storage health", `${status.storage?.segmentCount ?? 0} segment(s)`, `${formatBytes(status.storage?.segmentBytes)} / ${formatBytes(status.storage?.maxBytes)}`, healthTone),
            metricCard(c, "bb-card-signals", "Anomalies / milestones", `${status.analytics?.anomalyCount ?? 0} / ${status.analytics?.milestoneCount ?? 0}`, `${status.queue?.droppedRecords ?? 0} dropped`, status.analytics?.anomalyCount ? "warning" : "success"),
            metricCard(c, "bb-card-stream", "Live stream", state.lifecycle?.stream ?? "closed", state.lifecycle?.backpressure ? "Backpressured" : "Coalesced", state.lifecycle?.backpressure ? "warning" : "info")
        ], { id: "bb-summary-grid" }),
        c.progress({ label: "Storage usage", value: progressValue, max: 100, status: `${formatPercent(progressValue)} used`, tone: healthTone }, [], {
            id: "bb-progress",
            accessibility: { role: "progressbar", name: "Black Box storage usage", valueText: `${formatPercent(progressValue)} used` },
            localization: { key: "storageHealth" }
        }),
        c.tabs({
            label: "Black Box sections",
            activeTab: state.activeTab ?? "timeline",
            tabs: [
                { id: "timeline", title: "Timeline" },
                { id: "signals", title: "Anomalies & milestones" },
                { id: "storage", title: "Storage health" },
                { id: "doctor", title: "Doctor" },
                { id: "export", title: "Export" }
            ]
        }, [
            c.row({ label: "Timeline and detail split", responsive: { collapseBelowColumns: 100, orientation: "vertical" } }, [
                c.panel({ title: "Metadata timeline", width: "60%" }, [
                    c.text({ value: `${records.length} filtered record(s); showing ${visible.length}. Sort ${state.sort?.field ?? "timestamp"} ${state.sort?.direction ?? "desc"}.`, tone: "muted" }, [], { id: "bb-timeline-status", accessibility: { role: "status", name: "Timeline status" } }),
                    c.table({
                        label: "Timeline table",
                        columns: timelineColumns(),
                        rows: visible.map(record => timelineRow(record, selected?.recordId)),
                        virtualization: { enabled: true, rowHeight: 1, overscan: 6, totalRows: records.length, offset: state.view?.offset ?? 0, limit: state.view?.limit ?? VISIBLE_RECORDS },
                        filter: { query: state.filter ?? "", fields: ["kind", "eventType", "severity", "attributes"] },
                        sort: state.sort ?? { field: "timestamp", direction: "desc" },
                        selection: { selectedRowId: selected?.recordId ?? null, persistKey: "black-box.timeline.selection" },
                        mouse: { rowActivation: "select", hoverPreview: true }
                    }, [], {
                        id: "bb-timeline-table",
                        dataBindings: ["black-box.timeline", "black-box.live-records"],
                        actionBindings: { select: "select", sort: "sort", filter: "filter" },
                        accessibility: { role: "table", name: "Sanitized Black Box metadata timeline", describedBy: "bb-timeline-status" },
                        localization: { key: "timeline" }
                    })
                ], { id: "bb-timeline-panel" }),
                c.panel({ title: "Details", width: "40%" }, [
                    c.markdown({ markdown: selected ? detailMarkdown(selected) : "No metadata record selected." }, [], { id: "bb-detail-summary" }),
                    c.code({ language: "json", code: selected ? JSON.stringify(redactForDisplay(selected), null, 2) : "{}" }, [], { id: "bb-detail-json" })
                ], { id: "bb-detail-panel", accessibility: { role: "region", name: "Selected metadata detail" }, localization: { key: "details" } })
            ], { id: "bb-main-split" }),
            c.grid({ label: "Recent signals", columns: ["timestamp", "kind", "type", "severity"] }, signalCards(c, status.recentSignals ?? []), { id: "bb-signals-grid" }),
            c.panel({ title: "Storage health" }, [
                c.code({ language: "json", code: JSON.stringify(redactForDisplay(status.storage ?? {}), null, 2) }, [], { id: "bb-storage-json" })
            ], { id: "bb-storage-panel" }),
            c.panel({ title: "Doctor" }, [
                c.code({ language: "json", code: JSON.stringify(redactForDisplay(state.doctor ?? { status: "Run Doctor to collect diagnostics." }), null, 2) }, [], { id: "bb-doctor-json" })
            ], { id: "bb-doctor-panel" }),
            c.panel({ title: "Export" }, [
                c.code({ language: "json", code: JSON.stringify(redactForDisplay(state.exportResult ?? { status: "Run Export to create a sanitized local bundle." }), null, 2) }, [], { id: "bb-export-json" })
            ], { id: "bb-export-panel" })
        ], { id: "bb-tabs", accessibility: { role: "tablist", name: "Black Box sections" } }),
        ...(state.fallbackReason ? [c.text({ value: `Surface fallback: ${state.fallbackReason}. Showing deterministic metadata-only fallback.`, tone: "warning" }, [], { id: "bb-fallback-reason", accessibility: { role: "status", name: "Black Box fallback reason" } })] : []),
        ...(state.observability?.enabled === false ? [c.text({ value: "Optional observability subscription disabled by grant policy; live surface remains available with metadata-only diagnostics.", tone: "warning" }, [], { id: "bb-observability-diagnostic", accessibility: { role: "status", name: "Black Box observability diagnostic" } })] : []),
        c.text({ value: fallbackFooter(state), tone: "muted" }, [], {
            id: "bb-footer",
            accessibility: { role: "status", name: "Black Box status" },
            localization: { key: "status" }
        })
    ], {
        id: "bb-root",
        accessibility: { role: "application", name: "Afterburner Black Box enterprise observability" },
        localization: { locale: "en-US", key: "title" },
        metadata: surfaceMetadata()
    }), {
        surfaceId: ENTERPRISE_SURFACE_ID,
        revision: nextRevision(state),
        locale: "en-US",
        capabilities: ENTERPRISE_SURFACE_CAPABILITIES
    });
    state.document = doc;
    return doc;
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

function signalCards(c, records) {
    const signals = records.slice(0, 6);
    if (signals.length === 0) return [c.text({ value: "No anomalies or milestones recorded.", tone: "muted" }, [], { id: "bb-signal-empty" })];
    return signals.map((record, index) => c.card({ title: record.eventType, tone: record.kind === "anomaly" ? "warning" : "info" }, [
        c.text({ value: `${record.timestamp} · ${record.kind} · ${record.severity}` }, [], { id: `bb-signal-${index}-line` })
    ], { id: `bb-signal-${index}` }));
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
    const attributes = record.attributes ?? {};
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
    const attributes = record.attributes ?? {};
    const summary = [
        `**${record.eventType}**`,
        `- Timestamp: ${record.timestamp}`,
        `- Kind: ${record.kind}`,
        `- Severity: ${record.severity}`
    ];
    if (Number.isFinite(attributes.durationMs)) summary.push(`- Duration: ${attributes.durationMs} ms`);
    if (typeof attributes.success === "boolean") summary.push(`- Success: ${attributes.success}`);
    if (record.bodyReferences?.length) summary.push(`- Body references redacted: ${record.bodyReferences.length}`);
    return summary.join("\n");
}

function filterAndSortRecords(records, filter, sort = {}) {
    const query = String(filter ?? "").trim().toLowerCase();
    const filtered = query
        ? records.filter(record => JSON.stringify(redactForDisplay(record)).toLowerCase().includes(query))
        : [...records];
    const field = ["timestamp", "kind", "eventType", "severity", "durationMs", "success"].includes(sort.field) ? sort.field : "timestamp";
    const direction = sort.direction === "asc" ? 1 : -1;
    return filtered.sort((left, right) => compareField(left, right, field) * direction);
}

function compareField(left, right, field) {
    const l = field === "durationMs" || field === "success" ? left.attributes?.[field] : left[field];
    const r = field === "durationMs" || field === "success" ? right.attributes?.[field] : right[field];
    if (l === r) return 0;
    if (l === undefined || l === null || l === "") return 1;
    if (r === undefined || r === null || r === "") return -1;
    return l > r ? 1 : -1;
}

function normalizeSort(input, fallback = { field: "timestamp", direction: "desc" }) {
    const field = ["timestamp", "kind", "eventType", "severity", "durationMs", "success"].includes(input?.field) ? input.field : fallback.field;
    const direction = input?.direction === "asc" || input?.direction === "desc" ? input.direction : fallback.direction;
    return { field, direction };
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

async function safeStatus(service) {
    try { return await service.status(); }
    catch { return emptyStatus(); }
}

async function safeTail(service, input) {
    try { return await service.tail(input); }
    catch { return []; }
}

function healthToneFor(status, lifecycle = {}) {
    if (lifecycle.backpressure || status.queue?.writeErrors || status.queue?.droppedRecords) return "warning";
    if (status.analytics?.anomalyCount) return "warning";
    if (status.enabled) return "success";
    return "muted";
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
    const digits = scaled >= 10 || unit === 0 ? 0 : 1;
    return `${scaled.toFixed(digits)} ${units[unit]}`;
}

function fallbackFooter(state) {
    const stream = state.lifecycle?.stream ?? "closed";
    const queue = state.lifecycle?.queueDepth ?? 0;
    const reconnect = state.lifecycle?.reconnectCount ?? 0;
    const error = state.lifecycle?.lastError ? ` · last error ${state.lifecycle.lastError}` : "";
    const observability = state.observability?.enabled === false ? " · observability sink disabled" : "";
    return `Metadata only · live stream ${stream} · coalesced queue ${queue} · reconnects ${reconnect}${observability}${error}`;
}

export function buildEnterpriseFallbackText(status = emptyStatus(), records = [], reason = "UI host bridge is unavailable") {
    const timeline = records.slice(0, 12).map(record => {
        const attrs = record.attributes ?? {};
        const duration = Number.isFinite(attrs.durationMs) ? ` ${attrs.durationMs}ms` : "";
        const success = typeof attrs.success === "boolean" ? ` success=${attrs.success}` : "";
        return `  - ${record.timestamp} ${record.kind} ${record.eventType}${duration}${success}`;
    });
    return [
        "Afterburner Black Box Enterprise",
        "=================================",
        `Surface unavailable: ${reason}. Showing deterministic metadata-only fallback.`,
        `Recorder: ${status.enabled ? "enabled" : "disabled"} (${status.mode ?? "unknown"})`,
        `Storage : ${status.storage?.segmentCount ?? 0} segment(s), ${formatBytes(status.storage?.segmentBytes)} used, ${formatBytes(status.storage?.retentionBlockedBytes)} retention-blocked`,
        `Queue   : ${status.queue?.records ?? 0} queued, ${formatBytes(status.queue?.bytes)}, ${status.queue?.droppedRecords ?? 0} dropped`,
        `Signals : ${status.analytics?.anomalyCount ?? 0} anomalie(s), ${status.analytics?.milestoneCount ?? 0} milestone(s), ${status.analytics?.totalRecords ?? 0} total`,
        "Actions : refresh | doctor | export | close",
        "Timeline:",
        timeline.length ? timeline.join("\n") : "  - none"
    ].join("\n");
}

export function normalizeUIObservation(observation = {}) {
    const attrs = { ...sanitizeAttributes(observation.attributes ?? {}) };
    for (const [key, source] of [
        ["sinkId", observation.sinkId], ["envelopeId", observation.envelopeId], ["envelopeKind", observation.envelopeKind],
        ["surfaceId", observation.surfaceId], ["extensionId", observation.extensionId], ["hostId", observation.hostId]
    ]) {
        const safe = sanitizeScalar(key, source);
        if (safe !== undefined) attrs[key] = safe;
    }
    const type = sanitizeEventType(observation.type ?? "afterburner.ui.observation");
    return {
        schemaVersion: 1,
        timestamp: observation.at ?? observation.timestamp ?? new Date().toISOString(),
        type,
        data: attrs
    };
}

export function sanitizeAttributes(input = {}) {
    const output = {};
    for (const [key, value] of Object.entries(input ?? {})) {
        if (DENIED_KEY_PATTERN.test(key) && !SAFE_ATTRIBUTE_KEYS.has(key)) continue;
        const sanitized = sanitizeScalar(key, maybeDecodeRawJson(value));
        if (sanitized !== undefined) output[key] = sanitized;
    }
    return output;
}

function maybeDecodeRawJson(value) {
    if (typeof value !== "string") return value;
    const trimmed = value.trim();
    if (!/^(?:"|\{|\[|true$|false$|null$|-?\d)/.test(trimmed)) return value;
    try { return JSON.parse(trimmed); }
    catch { return value; }
}

function sanitizeScalar(key, value) {
    if (DENIED_KEY_PATTERN.test(key) && !SAFE_ATTRIBUTE_KEYS.has(key)) return undefined;
    if (typeof value === "string") {
        if (looksSensitiveString(value)) return "[redacted]";
        return cleanLabel(value);
    }
    if (typeof value === "number" && Number.isFinite(value)) return value;
    if (typeof value === "boolean") return value;
    if (value && typeof value === "object") {
        if (Array.isArray(value)) return value.length;
        return Object.keys(value).length;
    }
    return undefined;
}

function sanitizeEventType(value) {
    const normalized = typeof value === "string" ? value.toLowerCase().replace(/[^a-z0-9._-]+/g, "-") : "afterburner.ui.observation";
    return (/^[a-z0-9]/.test(normalized) ? normalized : `event-${normalized}`).slice(0, 96) || "afterburner.ui.observation";
}

function cleanLabel(value) {
    if (typeof value !== "string") return undefined;
    const normalized = value.replace(/[\r\n\0\t]/g, " ").replace(/\s+/g, " ").trim();
    if (!normalized) return undefined;
    return normalized.slice(0, 160);
}

function redactForDisplay(value, depth = 0) {
    if (value === null || value === undefined || depth > 6) return value;
    if (typeof value === "string") return looksSensitiveString(value) ? "[redacted]" : value;
    if (Array.isArray(value)) return value.slice(0, 64).map(item => redactForDisplay(item, depth + 1));
    if (typeof value !== "object") return value;
    const output = {};
    for (const [key, child] of Object.entries(value)) {
        if (DENIED_KEY_PATTERN.test(key) && !["pathRef", "bodyReferences"].includes(key)) {
            output[key] = "[redacted]";
            continue;
        }
        output[key] = redactForDisplay(child, depth + 1);
    }
    return output;
}

function looksSensitiveString(value) {
    return /\b[A-Za-z]:[\\/]|\\\\|(?:^|\s)\/(?:users|home|tmp|var|mnt|workspace)\/|secret|password|credential|api[_-]?key|access[_-]?token/i.test(value);
}

function hashDisplayPath(path) {
    if (typeof path !== "string" || !path) return null;
    return `path_${createHash("sha256").update(path).digest("hex").slice(0, 16)}`;
}

function safeErrorCode(error) {
    if (typeof error?.code === "string") return safeDiagnosticCode(error.code);
    if (typeof error?.name === "string" && error.name !== "Error") return safeDiagnosticCode(error.name);
    return isDenied(error) ? "authorization-denied" : "unknown-error";
}

function safeGrantReason(value, fallback) {
    const text = String(value ?? "").toLowerCase();
    if (!/^[a-z0-9._:-]{1,96}$/.test(text)) return safeDiagnosticCode(fallback);
    return safeDiagnosticCode(text);
}

function safeDiagnosticCode(value) {
    const normalized = String(value ?? "unknown-error").toLowerCase().replace(/[^a-z0-9._:-]+/g, "-").replace(/^-+|-+$/g, "");
    return normalized.slice(0, 96) || "unknown-error";
}

function isDenied(error) {
    const code = String(error?.code ?? error?.message ?? error ?? "").toLowerCase();
    return code.includes("denied") || code.includes("forbidden") || code.includes("unauthorized") || code.includes("policy");
}

export async function subscribeObservability(api = {}, service) {
    const descriptor = blackBoxObservabilitySinkDescriptor();
    const sink = {
        id: descriptor.id,
        descriptor,
        publish: async observation => service.observeRuntime(normalizeUIObservation(observation), { kind: "runtime-observer" }),
        observe: async observation => service.observeRuntime(normalizeUIObservation(observation), { kind: "runtime-observer" })
    };
    const register = apiFunction(api, ["registerObservabilitySink"]);
    const subscribe = apiFunction(api, ["subscribeObservability"]);
    if (!register && !subscribe) return null;
    try {
        const grantState = await evaluateObservabilityGrant(api);
        if (grantState.enabled === false) {
            await recordObservabilityDiagnostic(service, grantState);
            return { denied: true, disabled: true, reason: grantState.denials[0]?.reason ?? "optional-observability-sink-denied", diagnostic: grantState.diagnostic, dispose() {} };
        }
        if (register) return await register.fn.call(register.target, sink) ?? (() => {});
        return await subscribe.fn.call(subscribe.target, descriptor, sink.publish) ?? (() => {});
    } catch (error) {
        if (!isDenied(error)) throw error;
        const grantState = observabilityGrantState([{ capability: BLACK_BOX_OBSERVABILITY_CAPABILITY, reason: safeErrorCode(error) }]);
        await recordObservabilityDiagnostic(service, grantState);
        return { denied: true, disabled: true, reason: grantState.denials[0]?.reason, diagnostic: grantState.diagnostic, dispose() {} };
    }
}

async function recordObservabilityDiagnostic(service, grantState) {
    try {
        await service?.observeRuntime?.(normalizeUIObservation({
            type: "ui.observability.subscription",
            sinkId: OBSERVABILITY_SINK_ID,
            surfaceId: ENTERPRISE_SURFACE_ID,
            envelopeKind: "grant.policy",
            attributes: {
                sinkId: OBSERVABILITY_SINK_ID,
                surfaceId: ENTERPRISE_SURFACE_ID,
                envelopeKind: "grant.policy",
                state: "disabled",
                grantId: "black-box.observability-black-box-sink",
                reason: grantState?.denials?.[0]?.reason ?? "optional-observability-sink-denied",
                securityDecision: "deny",
                policyDecision: "deny"
            }
        }), { kind: "runtime-observer" });
    } catch {}
}
