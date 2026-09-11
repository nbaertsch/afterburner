import { createRequire } from "node:module";
import { access, readFile, readdir, writeFile } from "node:fs/promises";
import { constants as fsConstants } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { createConnection } from "node:net";
import * as modalUI from "./runtime/modal-ui.mjs";
import { verifyRuntimePackageIdentity } from "./runtime/extension-identity.mjs";

const require = createRequire(import.meta.url);
const { createHash } = require("node:crypto");
const { readFileSync, unlinkSync } = require("node:fs");
const safeJSONParse = JSON.parse.bind(JSON);
const safeJSONStringify = JSON.stringify.bind(JSON);
const wrapperDir = dirname(fileURLToPath(import.meta.url));
const packageRoot = resolve(wrapperDir, "..");
const wrapperPackageName = wrapperDir.split(/[\\/]/).at(-1);
const runtimeEventSchemaVersion = 1;
const runtimeObserverQueueLimit = 64;
const runtimeBootstrapLimit = 48;
const runtimeBootstrapEvents = [];
const runtimeObservers = new Map();
const blockedMetadataWords = new Set([
    "argument", "arguments", "authorization", "body", "code", "command", "commands",
    "content", "contents", "credential", "credentials", "file", "files", "header", "headers",
    "input", "inputs", "message", "messages", "output", "outputs", "password", "path", "paths",
    "prompt", "prompts", "query", "response", "responses", "result", "results", "secret", "secrets",
    "source", "sources", "summary", "summaries", "url", "urls"
]);
let runtimeEventSequence = 0;
let runtimeBootstrapDropped = 0;
let runtimeBootstrapSealed = false;
const modalCanvasLimit = 16;
const modalHostCanvasLimit = 256;
const modalActionLimit = 16;
const modalSubscriptionLimit = 32;
const modalTextLimit = 64 * 1024;
const modalBlackBoxOwnerExtensionId = "black-box";
const trustedBuiltinSourceTypes = new Set(["embedded", "signed-release"]);
const modalCanvases = new Map();
const modalInstances = new Map();
const modalUpdateQueues = new Map();
const modalSubscribers = new Map();
const modalFallbacks = new Map();
const modalNativeSurfaces = new Map();
let modalInstanceSequence = 0;
let modalBrokerConfig = loadModalBrokerConfig();
seedModalNativeSurfaces(modalBrokerConfig);
const modalDiagnostics = {
    registered: 0,
    active: 0,
    opened: 0,
    updated: 0,
    closed: 0,
    actionInvocations: 0,
    subscriptionCount: 0,
    fallbackCount: 0,
    pipeFailures: 0,
    quotaFailures: 0,
    lastFailureKind: null
};

function runtimeMetadataKeyAllowed(key) {
    if (!/^[A-Za-z][A-Za-z0-9._-]{0,63}$/.test(key)) return false;
    const words = key.replace(/([a-z0-9])([A-Z])/g, "$1_$2")
        .toLowerCase().split(/[^a-z0-9]+/).filter(Boolean);
    if (words.some((word) => blockedMetadataWords.has(word))) return false;
    const joined = words.join("");
    return !/(?:access|auth|bearer|refresh|session)token/.test(joined) && joined !== "token";
}

function sanitizeRuntimeMetadata(value, depth = 0) {
    if (value === null || typeof value === "boolean") return value;
    if (typeof value === "number") return Number.isFinite(value) ? value : undefined;
    if (typeof value === "string") {
        const printable = value.replace(/[\u0000-\u001f\u007f]/g, " ").trim();
        return printable.length <= 192 ? printable : `${printable.slice(0, 191)}…`;
    }
    if (depth >= 4) return undefined;
    if (Array.isArray(value)) {
        return value.slice(0, 32)
            .map((entry) => sanitizeRuntimeMetadata(entry, depth + 1))
            .filter((entry) => entry !== undefined);
    }
    if (typeof value !== "object" || Object.getPrototypeOf(value) !== Object.prototype) return undefined;
    const result = {};
    for (const [key, entry] of Object.entries(value).slice(0, 32)) {
        if (!runtimeMetadataKeyAllowed(key)) continue;
        const sanitized = sanitizeRuntimeMetadata(entry, depth + 1);
        if (sanitized !== undefined) result[key] = sanitized;
    }
    return result;
}

function freezeRuntimeValue(value) {
    if (!value || typeof value !== "object" || Object.isFrozen(value)) return value;
    for (const entry of Object.values(value)) freezeRuntimeValue(entry);
    return Object.freeze(value);
}

function immutableRuntimeCopy(value) {
    return freezeRuntimeValue(sanitizeRuntimeMetadata(value));
}

function runtimeObserverAccepts(observer, event) {
    return observer.eventTypes === null || observer.eventTypes.has(event.type);
}

function reportRuntimeObserverFailure(observer, phase, error) {
    if (process.env.COPILOT_RUNTIME_EXTENSION_DEBUG !== "1") return;
    const failureKind = sanitizeRuntimeMetadata(error?.name ?? "Error") || "Error";
    process.stderr.write(`[runtime-extension-host] observer '${observer.id}' ${phase} failed (${failureKind})\n`);
}

async function drainRuntimeObserver(observer) {
    if (observer.draining || !observer.active) return;
    observer.draining = true;
    try {
        while (observer.active && observer.queue.length > 0) {
            const event = observer.queue.shift();
            try {
                await observer.onEvent(immutableRuntimeCopy(event));
                observer.delivered++;
            } catch (error) {
                observer.failures++;
                reportRuntimeObserverFailure(observer, "delivery", error);
            }
        }
    } finally {
        observer.draining = false;
        observer.drainPromise = null;
        if (observer.active && observer.queue.length > 0) scheduleRuntimeObserver(observer);
    }
}

function scheduleRuntimeObserver(observer) {
    if (!observer.active || observer.scheduled || observer.draining) return;
    observer.scheduled = true;
    queueMicrotask(() => {
        observer.scheduled = false;
        if (!observer.active || observer.draining) return;
        observer.drainPromise = drainRuntimeObserver(observer);
    });
}

function enqueueRuntimeObserver(observer, event) {
    if (!observer.active || !runtimeObserverAccepts(observer, event)) return;
    if (observer.queue.length >= runtimeObserverQueueLimit) {
        observer.dropped++;
        return;
    }
    observer.queue.push(event);
    scheduleRuntimeObserver(observer);
}

function normalizeRuntimeEventTypes(eventTypes) {
    if (eventTypes === undefined) return null;
    if (!Array.isArray(eventTypes) && !(eventTypes instanceof Set)) {
        throw new Error("Runtime observer eventTypes must be an array or Set.");
    }
    const normalized = new Set();
    for (const eventType of eventTypes) {
        if (typeof eventType !== "string" || !/^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*$/.test(eventType)) {
            throw new Error(`Invalid runtime observer event type '${String(eventType)}'.`);
        }
        normalized.add(eventType);
    }
    return normalized;
}

function runtimeObserverDiagnostics(observer) {
    return immutableRuntimeCopy({
        id: observer.id,
        ownerExtensionId: observer.ownerExtensionId,
        eventTypes: observer.eventTypes === null ? null : [...observer.eventTypes].sort(),
        active: observer.active,
        queueDepth: observer.queue.length,
        delivered: observer.delivered,
        dropped: observer.dropped,
        failures: observer.failures,
        disposeFailures: observer.disposeFailures
    });
}

function disposeRuntimeObserver(observer) {
    if (!observer?.active) return;
    observer.active = false;
    observer.queue.length = 0;
    runtimeObservers.delete(observer.key ?? observer.id);
    if (typeof observer.dispose === "function") {
        try {
            const result = observer.dispose();
            if (result && typeof result.then === "function") {
                result.catch((error) => {
                    observer.disposeFailures++;
                    reportRuntimeObserverFailure(observer, "dispose", error);
                });
            }
        } catch (error) {
            observer.disposeFailures++;
            reportRuntimeObserverFailure(observer, "dispose", error);
        }
    }
    emitRuntimeEvent("runtime.observer.disposed", { observerId: observer.id, ownerExtensionId: observer.ownerExtensionId });
}

function registerRuntimeObserver(definition, options = {}) {
    if (!definition || typeof definition !== "object") {
        throw new Error("A runtime observer definition is required.");
    }
    const { id, onEvent, dispose } = definition;
    if (typeof id !== "string" || !/^[a-z0-9][a-z0-9._-]{0,63}$/.test(id)) {
        throw new Error("Runtime observers require a stable lowercase id.");
    }
    if (typeof onEvent !== "function") throw new Error(`Runtime observer '${id}' requires onEvent().`);
    if (dispose !== undefined && typeof dispose !== "function") {
        throw new Error(`Runtime observer '${id}' dispose must be a function.`);
    }
    const ownerExtensionId = validateModalOwnerExtensionId(options.ownerExtensionId);
    const observerKey = ownerExtensionId ? ownerExtensionId + ":" + id : id;
    if (runtimeObservers.has(observerKey)) throw new Error(`Runtime observer '${id}' is already registered.`);
    const observer = {
        key: observerKey,
        id,
        ownerExtensionId,
        eventTypes: normalizeRuntimeEventTypes(definition.eventTypes),
        onEvent,
        dispose,
        active: true,
        queue: [],
        scheduled: false,
        draining: false,
        drainPromise: null,
        delivered: 0,
        dropped: 0,
        failures: 0,
        disposeFailures: 0
    };
    runtimeObservers.set(observerKey, observer);
    for (const event of runtimeBootstrapEvents) enqueueRuntimeObserver(observer, event);
    emitRuntimeEvent("runtime.observer.registered", {
        observerId: id,
        ownerExtensionId,
        filtered: observer.eventTypes !== null,
        eventTypeCount: observer.eventTypes?.size ?? 0
    });
    const unregister = () => disposeRuntimeObserver(observer);
    Object.defineProperty(unregister, "diagnostics", {
        enumerable: true,
        value: () => runtimeObserverDiagnostics(observer)
    });
    return unregister;
}

function emitRuntimeEvent(type, metadata = {}) {
    if (typeof type !== "string" || !/^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*$/.test(type)) {
        throw new Error(`Invalid runtime event type '${String(type)}'.`);
    }
    const event = freezeRuntimeValue({
        schemaVersion: runtimeEventSchemaVersion,
        sequence: ++runtimeEventSequence,
        timestamp: new Date().toISOString(),
        type,
        metadata: immutableRuntimeCopy(metadata) ?? Object.freeze({})
    });
    if (!runtimeBootstrapSealed) {
        if (runtimeBootstrapEvents.length >= runtimeBootstrapLimit) {
            runtimeBootstrapEvents.shift();
            runtimeBootstrapDropped++;
        }
        runtimeBootstrapEvents.push(event);
    }
    for (const observer of runtimeObservers.values()) enqueueRuntimeObserver(observer, event);
    return event;
}

function getRuntimeObserverDiagnostics(options = {}) {
    const ownerExtensionId = validateModalOwnerExtensionId(options.ownerExtensionId);
    const observers = [...runtimeObservers.values()]
        .filter((observer) => !ownerExtensionId || observer.ownerExtensionId === ownerExtensionId);
    return immutableRuntimeCopy({
        schemaVersion: runtimeEventSchemaVersion,
        lastSequence: runtimeEventSequence,
        bootstrap: {
            sealed: runtimeBootstrapSealed,
            buffered: runtimeBootstrapEvents.length,
            dropped: runtimeBootstrapDropped,
            limit: runtimeBootstrapLimit
        },
        queueLimit: runtimeObserverQueueLimit,
        observers: observers.map(runtimeObserverDiagnostics)
    });
}

async function flushRuntimeObservers() {
    for (;;) {
        await Promise.resolve();
        const pending = [...runtimeObservers.values()]
            .map((observer) => observer.drainPromise).filter(Boolean);
        if (pending.length > 0) await Promise.allSettled(pending);
        if ([...runtimeObservers.values()].every((observer) =>
            !observer.scheduled && !observer.draining && observer.queue.length === 0)) return;
    }
}

function loadModalBrokerConfig() {
    const bootstrapPaths = [process.env.AFTERBURNER_NATIVE_BOOTSTRAP, process.env.AFTERBURNER_MODAL_BOOTSTRAP]
        .filter((value, index, values) => typeof value === "string" && value && values.indexOf(value) === index);
    delete process.env.AFTERBURNER_NATIVE_BOOTSTRAP;
    delete process.env.AFTERBURNER_MODAL_BOOTSTRAP;
    delete process.env.AFTERBURNER_MODAL_SECRET;
    delete process.env.AFTERBURNER_MODAL_OWNER_EXTENSION_ID;
    delete process.env.AFTERBURNER_MODAL_CANVAS_ID;
    delete process.env.AFTERBURNER_MODAL_SURFACE_ID;
    delete process.env.AFTERBURNER_MODAL_PIPE;
    if (bootstrapPaths.length === 0) return Object.freeze({ modalSurfaces: Object.freeze([]), verifiedExtensions: Object.freeze([]) });
    const modalSurfaces = [];
    const verifiedExtensions = [];
    let verifiedBootstrapSessionId = "";
    let legacyPipe;
    for (const bootstrapPath of bootstrapPaths) {
        try {
            const parsed = safeJSONParse(readFileSync(bootstrapPath, "utf8"));
            try { unlinkSync(bootstrapPath); } catch {}
            if (typeof parsed.pipe === "string" && parsed.pipe) legacyPipe = parsed.pipe;
            if (typeof parsed.sessionRoute === "string" && /^[A-Za-z0-9_-]{32,128}$/.test(parsed.sessionRoute) && !process.env.AFTERBURNER_SESSION_ROUTE) {
                process.env.AFTERBURNER_SESSION_ROUTE = parsed.sessionRoute;
            }
            const fileSessionId = typeof parsed.sessionId === "string" && /^[A-Za-z0-9_-]{32,128}$/.test(parsed.sessionId) ? parsed.sessionId : "";
            if (fileSessionId) verifiedBootstrapSessionId = fileSessionId;
            modalSurfaces.push(...normalizeModalBootstrapSurfaces(parsed.modalSurfaces, parsed.pipe, fileSessionId));
            verifiedExtensions.push(...normalizeNativeIdentityAssertions(parsed.verifiedExtensions));
        } catch {}
    }
    return Object.freeze({
        pipe: legacyPipe,
        sessionId: verifiedBootstrapSessionId,
        modalSurfaces: Object.freeze(modalSurfaces),
        verifiedExtensions: Object.freeze(verifiedExtensions)
    });
}

function normalizeModalBootstrapSurfaces(values, legacyPipe, bootstrapSessionId = "") {
    if (!Array.isArray(values)) return Object.freeze([]);
    const surfaces = [];
    const seen = new Set();
    for (const value of values) {
        if (!value || typeof value !== "object") continue;
        try {
            const ownerExtensionId = validateModalOwnerExtensionId(value.ownerExtensionId);
            const canvasId = validateModalId(value.canvasId, "modal canvas id");
            const surfaceId = validateModalId(value.surfaceId, "modal surface id");
            const pipe = typeof value.pipe === "string" && value.pipe ? value.pipe : legacyPipe;
            const sessionId = typeof value.sessionId === "string" && /^[A-Za-z0-9_-]{32,128}$/.test(value.sessionId) ? value.sessionId : bootstrapSessionId;
            if (!ownerExtensionId || !pipe || !sessionId) continue;
            const key = modalNativeSurfaceKey(ownerExtensionId, canvasId, surfaceId);
            if (seen.has(key)) continue;
            seen.add(key);
            surfaces.push(Object.freeze({ ownerExtensionId, canvasId, surfaceId, sessionId, pipe }));
        } catch {}
    }
    return Object.freeze(surfaces);
}

function normalizeNativeIdentityAssertions(values) {
    if (!Array.isArray(values)) return Object.freeze([]);
    const assertions = [];
    const seen = new Set();
    for (const value of values) {
        if (!value || typeof value !== "object") continue;
        const extensionId = typeof value.extensionId === "string" &&
            /^[a-z0-9][a-z0-9-]{0,63}$/.test(value.extensionId) ? value.extensionId : undefined;
        const activePath = typeof value.activePath === "string" && value.activePath ? resolve(value.activePath) : undefined;
        const manifestHash = typeof value.manifestHash === "string" &&
            /^sha256:[0-9a-f]{64}$/.test(value.manifestHash) ? value.manifestHash : undefined;
        const treeHash = typeof value.treeHash === "string" &&
            /^sha256:[0-9a-f]{64}$/.test(value.treeHash) ? value.treeHash : undefined;
        if (!extensionId || !activePath || !manifestHash || !treeHash) continue;
        const key = extensionId + "\0" + activePath.toLowerCase();
        if (seen.has(key)) continue;
        seen.add(key);
        assertions.push(Object.freeze({
            extensionId,
            activePath,
            manifestHash,
            treeHash,
            sourceType: value.sourceType,
            sourceValue: value.sourceValue,
            trustedBuiltin: value.trustedBuiltin === true
        }));
    }
    return Object.freeze(assertions);
}

function seedModalNativeSurfaces(config) {
    for (const surface of config?.modalSurfaces ?? []) {
        if (!surface || typeof surface !== "object") continue;
        if (typeof surface.pipe !== "string" || !surface.pipe) continue;
        if (typeof surface.sessionId !== "string" || !/^[A-Za-z0-9_-]{32,128}$/.test(surface.sessionId)) continue;
        modalNativeSurfaces.set(modalNativeSurfaceKey(surface.ownerExtensionId, surface.canvasId, surface.surfaceId), surface);
    }
}

function currentModalBrokerConfig() {
    return typeof modalBrokerConfig !== "undefined" ? modalBrokerConfig : null;
}

function modalBrokerPipe(surface) {
    return surface?.pipe ?? currentModalBrokerConfig()?.pipe;
}

function truncateModalText(value, limit = modalTextLimit) {
    const text = value === undefined || value === null ? "" : String(value);
    return text.length <= limit ? text : `${text.slice(0, limit - 1)}…`;
}

function modalHasBroker() {
    const config = currentModalBrokerConfig();
    seedModalNativeSurfaces(config);
    return [...modalNativeSurfaces.values()].some((surface) => typeof surface.pipe === "string" && surface.pipe);
}


function freezeModalValue(value) {
    if (value === null || typeof value !== "object") return value;
    if (Array.isArray(value)) return Object.freeze(value.map(freezeModalValue));
    const result = {};
    for (const [key, entry] of Object.entries(value)) result[key] = freezeModalValue(entry);
    return Object.freeze(result);
}

function modalPublicCopy(value) {
    return freezeModalValue(value === undefined ? undefined : safeJSONParse(safeJSONStringify(value)));
}

function validateModalId(id, noun = "modal canvas") {
    if (typeof id !== "string" || !/^[a-z0-9][a-z0-9._-]{0,63}$/.test(id)) {
        throw new Error(`${noun} requires a stable lowercase id.`);
    }
    return id;
}

function validateModalOwnerExtensionId(ownerExtensionId) {
    if (ownerExtensionId === undefined || ownerExtensionId === null || ownerExtensionId === "") return undefined;
    if (typeof ownerExtensionId !== "string" || !/^[a-z0-9][a-z0-9._:-]{0,127}$/.test(ownerExtensionId)) {
        const error = new Error("ownerExtensionId requires a stable lowercase id.");
        error.code = "ui.invalidEnvelope";
        throw error;
    }
    return ownerExtensionId;
}

function modalAuthorizationError(message, details = {}) {
    const error = new Error(message);
    error.code = "ui.authorizationDenied";
    error.details = details;
    return error;
}

function assertModalOwner(canvas, callerOwnerExtensionId, operation) {
    const ownerExtensionId = canvas?.ownerExtensionId;
    const caller = validateModalOwnerExtensionId(callerOwnerExtensionId);
    if (ownerExtensionId && caller !== ownerExtensionId) {
        throw modalAuthorizationError(`Modal canvas '${canvas.id}' ${operation} is owned by another extension.`, {
            modalId: canvas.id,
            ownerExtensionId: caller,
            expectedOwnerExtensionId: ownerExtensionId
        });
    }
}

function validateModalAction(action) {
    if (!action || typeof action !== "object") throw new Error("Modal actions must be objects.");
    if (typeof action.name !== "string" || !/^[a-z][a-z0-9._-]{0,63}$/.test(action.name)) {
        throw new Error("Modal actions require a stable lowercase name.");
    }
    if (action.handler !== undefined && typeof action.handler !== "function") {
        throw new Error(`Modal action '${action.name}' handler must be a function.`);
    }
    return Object.freeze({
        name: action.name,
        label: truncateModalText(action.label ?? action.name, 64),
        key: action.key === undefined ? undefined : truncateModalText(action.key, 16),
        description: action.description === undefined ? undefined : truncateModalText(action.description, 256),
        handler: action.handler
    });
}

function modalActionPublic(action) {
    return {
        name: action.name,
        label: action.label,
        ...(action.key ? { key: action.key } : {}),
        ...(action.description ? { description: action.description } : {})
    };
}

function modalTextFromValue(value) {
    if (typeof value === "string") return value;
    if (Array.isArray(value?.lines)) return value.lines.map((line) => String(line)).join("\n");
    if (typeof value?.text === "string") return value.text;
    if (typeof value?.markdown === "string") return value.markdown;
    if (value && typeof value === "object") return JSON.stringify(value, null, 2);
    return "";
}

function normalizeModalDocument(canvas, frame, normalized, previous = {}) {
    const revision = previous.document?.revision ? previous.document.revision + 1 : 1;
    if (frame.document && typeof modalUI.validateModalDocument === "function") {
        return modalUI.validateModalDocument({ ...frame.document, surfaceId: canvas.id, revision });
    }
    return modalUI.modalFrameToDocument(canvas, normalized, { revision });
}

function normalizeModalFrame(canvas, value, previous = {}) {
    const frame = typeof value === "string" ? { body: value } : (value && typeof value === "object" ? value : {});
    const bodyValue = frame.body !== undefined ? frame.body :
        frame.text !== undefined || frame.markdown !== undefined || frame.lines !== undefined ? frame : previous.body ?? "";
    const normalized = {
        id: canvas.id,
        title: truncateModalText(frame.title ?? previous.title ?? canvas.displayName ?? canvas.id, 256),
        status: truncateModalText(frame.status ?? previous.status ?? "", 2048),
        body: truncateModalText(modalTextFromValue(bodyValue)),
        footer: truncateModalText(frame.footer ?? previous.footer ?? "Esc/q closes · Copilot keeps running in the background", 2048),
        actions: canvas.actions.map(modalActionPublic)
    };
    const document = normalizeModalDocument(canvas, frame, normalized, previous);
    if (document) normalized.document = document;
    return normalized;
}

async function sendModalPipeMessage(message, timeoutMs = 5000, surface = null) {
    const pipe = modalBrokerPipe(surface);
    if (!pipe) return { ok: false, fallback: true, error: "modal-pipe-unavailable" };
    return new Promise((resolve) => {
        let settled = false;
        let buffered = "";
        const finish = (response) => {
            if (settled) return;
            settled = true;
            clearTimeout(timeout);
            socket.destroy();
            resolve(response);
        };
        const socket = createConnection(pipe);
        const timeout = setTimeout(() => finish({ ok: false, error: "modal-pipe-timeout" }), timeoutMs);
        socket.setEncoding("utf8");
        socket.once("connect", () => socket.write(`${safeJSONStringify(message)}\n`));
        socket.on("data", (chunk) => {
            buffered += chunk;
            const newline = buffered.indexOf("\n");
            if (newline >= 0) {
                try { finish(safeJSONParse(buffered.slice(0, newline))); }
                catch { finish({ ok: false, error: "modal-pipe-invalid-response" }); }
            }
        });
        socket.once("error", (error) => finish({ ok: false, error: error?.code ?? error?.name ?? "modal-pipe-error" }));
        socket.once("close", () => finish({ ok: false, error: "modal-pipe-closed" }));
    });
}

function notifyModalSubscribers(id, event) {
    const subscribers = modalSubscribers.get(id);
    if (!subscribers) return;
    const copy = modalPublicCopy(event);
    for (const subscriber of subscribers) {
        try { Promise.resolve(subscriber(copy)).catch(() => {}); }
        catch {}
    }
}

function formatModalFallback(frame) {
    return [
        `# ${frame.title}`,
        frame.status ? `_${frame.status}_` : "",
        frame.body,
        frame.footer ? `(${frame.footer})` : ""
    ].filter(Boolean).join("\n\n");
}

function modalOwnedKey(ownerExtensionId, id) {
    return `${ownerExtensionId ?? ""}\0${id}`;
}

function recordModalFallback(canvas, frame, error) {
    const fallback = {
        id: frame.id,
        text: formatModalFallback(frame),
        frame: modalPublicCopy(frame),
        error,
        updatedAt: new Date().toISOString()
    };
    modalFallbacks.set(modalOwnedKey(canvas?.ownerExtensionId, frame.id), fallback);
    return fallback;
}

function getModalFallback(id, options = {}) {
    id = validateModalId(id);
    const key = modalOwnedKey(options.ownerExtensionId, id);
    const canvas = modalCanvases.get(key);
    if (canvas) assertModalOwner(canvas, options.ownerExtensionId, "fallback");
    return modalPublicCopy(modalFallbacks.get(key) ?? null);
}

function modalGenerationMessage(generation) {
    return Number.isSafeInteger(generation) ? { generation } : {};
}

function modalNativeSurfaceKey(ownerExtensionId, canvasId, surfaceId) {
    return String(ownerExtensionId) + ":" + String(canvasId) + ":" + String(surfaceId);
}

function modalWireIdentity(surface) {
    return {
        sessionId: surface.sessionId,
        ownerExtensionId: surface.ownerExtensionId,
        canvasId: surface.canvasId,
        surfaceId: surface.surfaceId
    };
}

async function ensureModalNativeSurface(canvas) {
    const config = currentModalBrokerConfig();
    seedModalNativeSurfaces(config);
    if (!canvas?.ownerExtensionId) return { ok: false, fallback: true, error: "modal-owner-required" };
    const key = modalNativeSurfaceKey(canvas.ownerExtensionId, canvas.canvasId, canvas.surfaceId);
    const cached = modalNativeSurfaces.get(key);
    if (cached?.pipe) return { ok: true, surface: cached };
    return { ok: false, fallback: true, error: "modal-surface-unavailable" };
}

function modalActionWireProjection(action) {
    return {
        name: action.name,
        label: action.label,
        ...(action.key ? { key: action.key } : {}),
        ...(action.description ? { description: action.description } : {})
    };
}

function modalFrameWireProjection(operation, frame, generation, surface, acknowledgedEventSequence) {
    return {
        operation,
        id: surface.surfaceId,
        ...modalWireIdentity(surface),
        ...modalGenerationMessage(generation),
        title: frame.title ?? "",
        status: frame.status ?? "",
        body: frame.body ?? "",
        footer: frame.footer ?? "",
        actions: Array.isArray(frame.actions) ? frame.actions.map(modalActionWireProjection) : [],
        ...(Number.isSafeInteger(acknowledgedEventSequence) ? { ackEventSequence: acknowledgedEventSequence } : {}),
        ...(frame.document ? { document: frame.document } : {})
    };
}

function modalControlWireProjection(operation, generation, surface) {
    return { operation, id: surface.surfaceId, ...modalWireIdentity(surface), ...modalGenerationMessage(generation) };
}

async function presentModalFrame(type, frame, options = {}) {
    const surfaceResult = await ensureModalNativeSurface(options.canvas);
    if (!surfaceResult.ok) {
        modalDiagnostics.fallbackCount++;
        if (!surfaceResult.fallback) modalDiagnostics.pipeFailures++;
        modalDiagnostics.lastFailureKind = surfaceResult.error ?? "modal-unavailable";
        const fallback = recordModalFallback(options.canvas, frame, modalDiagnostics.lastFailureKind);
        return { ok: false, fallback: true, attemptedNative: false, text: fallback.text, error: fallback.error };
    }
    const response = await sendModalPipeMessage(modalFrameWireProjection(type, frame, options.generation, surfaceResult.surface, options.ackEventSequence), 5000, surfaceResult.surface);
    if (!response?.ok) {
        modalDiagnostics.fallbackCount++;
        if (!response?.fallback) modalDiagnostics.pipeFailures++;
        modalDiagnostics.lastFailureKind = response?.error ?? "modal-unavailable";
        const fallback = recordModalFallback(options.canvas, frame, modalDiagnostics.lastFailureKind);
        return { ok: false, fallback: true, attemptedNative: true, text: fallback.text, error: fallback.error };
    }
    modalFallbacks.delete(modalOwnedKey(options.canvas?.ownerExtensionId, frame.id));
    return { ok: true, attemptedNative: true };
}

function normalizeModalEventType(type) {
    const normalized = typeof type === "string" ? type.toLowerCase().replace(/_/g, "-") : "";
    if (["action", "modal-action", "ui.modal-canvas.action"].includes(normalized)) return "action";
    if (["close", "modal-close", "ui.modal-canvas.close"].includes(normalized)) return "close";
    if (["closed", "modal-closed", "ui.modal-canvas.closed"].includes(normalized)) return "closed";
    if (["activate", "change", "submit", "focus", "blur"].includes(normalized)) return normalized;
    return normalized;
}

function modalEventActionName(event) {
    return [event?.actionName, event?.action, event?.name].find((value) => typeof value === "string") ?? "";
}

function modalEventGeneration(event) {
    const value = event?.generation;
    if (value === undefined || value === null) return null;
    const generation = Number(value);
    return Number.isSafeInteger(generation) ? generation : NaN;
}

function modalEventMatchesGeneration(event, generation) {
    const eventGeneration = modalEventGeneration(event);
    if (!Number.isSafeInteger(generation)) return eventGeneration === null;
    return eventGeneration === generation;
}

async function drainModalClosedEvent(id, generation, canvas) {
    const surfaceResult = await ensureModalNativeSurface(canvas);
    if (!surfaceResult.ok) return false;
    const response = await sendModalPipeMessage(modalControlWireProjection("poll", generation, surfaceResult.surface), 250, surfaceResult.surface);
    return response?.ok === true &&
        response.event?.id === id &&
        normalizeModalEventType(response.event?.type) === "closed" &&
        modalEventMatchesGeneration(response.event, generation);
}

async function closeModalCanvasInstance(id, options = {}) {
    const { sendHostClose = true, drainClosed = true, reason = "api", generation: expectedGeneration, ownerExtensionId } = options;
    const key = modalOwnedKey(ownerExtensionId, id);
    const instance = modalInstances.get(key);
    const canvas = modalCanvases.get(key) ?? instance?.canvas;
    if (canvas) assertModalOwner(canvas, ownerExtensionId, "close");
    if (!instance || (expectedGeneration !== undefined && instance.generation !== expectedGeneration)) {
        return { ok: true, fallback: !modalHasBroker(), alreadyClosed: true };
    }
    const hostGeneration = instance.generation;
    const closingGeneration = ++modalInstanceSequence;
    modalInstances.set(key, { ...instance, generation: closingGeneration, closing: true });
    if (modalInstances.get(key)?.generation === closingGeneration) {
        modalInstances.delete(key);
        modalFallbacks.delete(key);
        modalDiagnostics.active = modalInstances.size;
    }
    if (instance.unsubscribe) {
        try { await instance.unsubscribe(); } catch {}
    }
    let response = { ok: true, fallback: false };
    if (sendHostClose && (!instance.fallback || instance.hostMayBeActive)) {
        const surfaceResult = await ensureModalNativeSurface(canvas);
        response = surfaceResult.ok
            ? await sendModalPipeMessage(modalControlWireProjection("close", hostGeneration, surfaceResult.surface), 5000, surfaceResult.surface)
            : { ok: false, fallback: surfaceResult.fallback === true, error: surfaceResult.error };
        if (!response?.ok && !response?.fallback) {
            modalDiagnostics.pipeFailures++;
            modalDiagnostics.lastFailureKind = response?.error ?? "modal-close-failed";
        } else if (response?.ok && drainClosed) {
            await drainModalClosedEvent(id, hostGeneration, canvas);
        }
    }
    modalDiagnostics.closed++;
    modalDiagnostics.active = modalInstances.size;
    emitRuntimeEvent("ui.modal_canvas.closed", { modalId: id, fallback: instance.fallback === true, reason, generation: hostGeneration });
    notifyModalSubscribers(key, { type: "closed", reason, generation: hostGeneration });
    return {
        ok: response?.ok === true || instance.fallback === true,
        fallback: response?.fallback === true || instance.fallback === true
    };
}

async function pollModalCanvasEvents(id, canvas, generation) {
    const key = modalOwnedKey(canvas.ownerExtensionId, id);
    for (;;) {
        const instance = modalInstances.get(key);
        if (!instance || instance.generation !== generation || instance.fallback || instance.closing) return;
        const surfaceResult = await ensureModalNativeSurface(canvas);
        if (!surfaceResult.ok) {
            modalDiagnostics.pipeFailures++;
            modalDiagnostics.lastFailureKind = surfaceResult.error ?? "modal-poll-failed";
            return;
        }
        const response = await sendModalPipeMessage(modalControlWireProjection("poll", generation, surfaceResult.surface), 35000, surfaceResult.surface);
        const current = modalInstances.get(key);
        if (!current || current.generation !== generation || current.fallback || current.closing) return;
        if (!response?.ok) {
            if (response?.error !== "modal-pipe-timeout") {
                modalDiagnostics.pipeFailures++;
                modalDiagnostics.lastFailureKind = response?.error ?? "modal-poll-failed";
                return;
            }
            continue;
        }
        const event = response.event;
        if (!event) continue;
        if (event.id !== id || !modalEventMatchesGeneration(event, generation)) continue;
        const eventType = normalizeModalEventType(event.type);
        if (eventType === "closed") {
            await closeModalCanvasInstance(id, { sendHostClose: false, drainClosed: false, reason: "host", generation, ownerExtensionId: canvas.ownerExtensionId });
            return;
        }
        if (eventType === "close") {
            await closeModalCanvasInstance(id, { sendHostClose: false, drainClosed: false, reason: event.key === "escape" ? "escape" : "host-request", generation, ownerExtensionId: canvas.ownerExtensionId });
            return;
        }
        if (eventType === "action") {
            const actionName = modalEventActionName(event);
            const action = canvas.actions.find(candidate => candidate.name === actionName);
            if (!action) continue;
            try {
                await invokeModalAction(id, action.name, { source: "terminal", key: event.key }, {
                    generation,
                    ownerExtensionId: canvas.ownerExtensionId,
                    ackEventSequence: Number.isSafeInteger(Number(event.sequence)) ? Number(event.sequence) : undefined
                });
            }
            catch (error) {
                modalDiagnostics.lastFailureKind = error?.name ?? "modal-action-failed";
            }
            continue;
        }
        if (["activate", "change", "submit", "focus", "blur"].includes(eventType)) {
            const semanticEvent = modalPublicCopy({
                type: eventType,
                targetId: typeof event.targetId === "string" ? event.targetId : "",
                documentRevision: Number.isSafeInteger(Number(event.documentRevision)) ? Number(event.documentRevision) : 0,
                generation,
                sequence: Number.isSafeInteger(Number(event.sequence)) ? Number(event.sequence) : 0,
                key: typeof event.key === "string" ? event.key : undefined,
                actionName: typeof event.actionName === "string" ? event.actionName : undefined,
                value: event.value
            });
            notifyModalSubscribers(key, semanticEvent);
            if (eventType === "activate" && semanticEvent.actionName) {
                const action = canvas.actions.find(candidate => candidate.name === semanticEvent.actionName);
                if (action) {
                    try {
                        await invokeModalAction(id, action.name, semanticEvent, {
                            generation,
                            ownerExtensionId: canvas.ownerExtensionId,
                            ackEventSequence: semanticEvent.sequence
                        });
                    } catch (error) {
                        modalDiagnostics.lastFailureKind = error?.name ?? "modal-action-failed";
                    }
                }
            }
            if (typeof canvas.onEvent === "function") {
                try {
                    await canvas.onEvent(semanticEvent, modalControls(id, generation, canvas.ownerExtensionId, semanticEvent.sequence));
                } catch (error) {
                    modalDiagnostics.lastFailureKind = error?.name ?? "modal-event-failed";
                }
            }
        }
    }
}

function startModalEventPolling(id, canvas, generation) {
    const key = modalOwnedKey(canvas.ownerExtensionId, id);
    const instance = modalInstances.get(key);
    if (!instance || instance.generation !== generation || instance.fallback) return;
    const pollPromise = pollModalCanvasEvents(id, canvas, generation).catch((error) => {
        modalDiagnostics.pipeFailures++;
        modalDiagnostics.lastFailureKind = error?.name ?? "modal-poll-failed";
    });
    modalInstances.set(key, { ...instance, pollPromise });
}

function modalControls(id, generation, ownerExtensionId, ackEventSequence) {
    const ownerOptions = ownerExtensionId ? { ownerExtensionId } : {};
    return Object.freeze({
        update: (next) => updateModalCanvas(id, next, { generation, ackEventSequence, ...ownerOptions }),
        close: () => closeModalCanvas(id, { generation, ...ownerOptions }),
        invoke: (name, input) => invokeModalAction(id, name, input, { generation, ...ownerOptions }),
        fallback: () => getModalFallback(id, ownerOptions),
        diagnostics: () => getModalDiagnostics(ownerOptions)
    });
}

async function openModalCanvas(id, input = {}, options = {}) {
    id = validateModalId(id);
    const key = modalOwnedKey(options.ownerExtensionId, id);
    const canvas = modalCanvases.get(key);
    if (!canvas) throw new Error(`Unknown modal canvas '${id}'.`);
    assertModalOwner(canvas, options.ownerExtensionId, "open");
    if (modalInstances.has(key)) {
        const error = new Error(`Modal canvas '${id}' is already open.`);
        error.code = "ui.surfaceAlreadyOpen";
        throw error;
    }
    const opened = typeof canvas.open === "function" ? await canvas.open(input) : {};
    const rendered = typeof canvas.render === "function" ? await canvas.render({ input, state: opened }) : opened;
    const frame = normalizeModalFrame(canvas, rendered);
    const generation = ++modalInstanceSequence;
    modalInstances.set(key, { frame, canvas, openedAt: new Date().toISOString(), fallback: false, hostMayBeActive: false, generation });
    let unsubscribe;
    try {
        if (typeof canvas.subscribe === "function") {
            unsubscribe = await canvas.subscribe(modalControls(id, generation, canvas.ownerExtensionId));
            if (unsubscribe !== undefined && typeof unsubscribe !== "function") {
                throw new Error(`Modal canvas '${id}' subscribe() must return a function or undefined.`);
            }
        }
    } catch (error) {
        const current = modalInstances.get(key);
        if (current?.generation === generation) modalInstances.delete(key);
        modalDiagnostics.active = modalInstances.size;
        throw error;
    }
    if (unsubscribe) modalInstances.set(key, { ...(modalInstances.get(key) ?? {}), unsubscribe });
    const presentation = await presentModalFrame("open", frame, { generation, canvas });
    const current = modalInstances.get(key);
    if (current?.generation === generation) {
        modalInstances.set(key, {
            ...current,
            frame,
            fallback: presentation.fallback === true,
            hostMayBeActive: presentation.attemptedNative === true
        });
        if (presentation.ok === true && presentation.fallback !== true) startModalEventPolling(id, canvas, generation);
    }
    modalDiagnostics.opened++;
    modalDiagnostics.active = modalInstances.size;
    emitRuntimeEvent("ui.modal_canvas.opened", { modalId: id, fallback: presentation.fallback === true, actionCount: canvas.actions.length, generation });
    notifyModalSubscribers(key, { type: "opened", frame, generation });
    return { ...presentation, frame: modalPublicCopy(frame) };
}

async function updateModalCanvas(id, next = {}, options = {}) {
    id = validateModalId(id);
    const key = modalOwnedKey(options.ownerExtensionId, id);
    const canvas = modalCanvases.get(key);
    if (!canvas) throw new Error(`Unknown modal canvas '${id}'.`);
    assertModalOwner(canvas, options.ownerExtensionId, "update");
    const instance = modalInstances.get(key);
    if (!instance || (options.generation !== undefined && instance.generation !== options.generation)) {
        throw new Error(`Modal canvas '${id}' is not open.`);
    }
    const generation = options.generation ?? instance.generation;
    const previous = modalUpdateQueues.get(key) ?? Promise.resolve();
    const queued = previous.catch(() => {}).then(() =>
        updateModalCanvasNow(id, next, { ...options, generation })
    );
    modalUpdateQueues.set(key, queued);
    try {
        return await queued;
    } finally {
        if (modalUpdateQueues.get(key) === queued) modalUpdateQueues.delete(key);
    }
}

async function updateModalCanvasNow(id, next = {}, options = {}) {
    id = validateModalId(id);
    const key = modalOwnedKey(options.ownerExtensionId, id);
    const canvas = modalCanvases.get(key);
    if (!canvas) throw new Error(`Unknown modal canvas '${id}'.`);
    assertModalOwner(canvas, options.ownerExtensionId, "update");
    const instance = modalInstances.get(key);
    if (!instance || (options.generation !== undefined && instance.generation !== options.generation)) {
        throw new Error(`Modal canvas '${id}' is not open.`);
    }
    const generation = instance.generation;
    const rendered = typeof canvas.render === "function" ? await canvas.render({ input: next, state: next }) : next;
    const frame = normalizeModalFrame(canvas, rendered, instance.frame ?? {});
    const presentation = await presentModalFrame("update", frame, {
        generation,
        canvas,
        ackEventSequence: options.ackEventSequence
    });
    const current = modalInstances.get(key);
    const commitFrame = presentation.ok === true || presentation.attemptedNative !== true;
    if (current?.generation === generation && commitFrame) {
        modalInstances.set(key, {
            ...current,
            frame,
            fallback: current.fallback,
            hostMayBeActive: current.hostMayBeActive === true || presentation.attemptedNative === true
        });
        modalDiagnostics.updated++;
        modalDiagnostics.active = modalInstances.size;
        emitRuntimeEvent("ui.modal_canvas.updated", { modalId: id, fallback: presentation.fallback === true, generation });
        notifyModalSubscribers(key, { type: "updated", frame, generation });
    }
    return { ...presentation, frame: modalPublicCopy(frame) };
}

async function closeModalCanvas(id, options = {}) {
    id = validateModalId(id);
    return closeModalCanvasInstance(id, options);
}

async function invokeModalAction(id, name, input = {}, options = {}) {
    id = validateModalId(id);
    if (typeof name !== "string") throw new Error("Modal action name is required.");
    const key = modalOwnedKey(options.ownerExtensionId, id);
    const canvas = modalCanvases.get(key);
    if (!canvas) throw new Error(`Unknown modal canvas '${id}'.`);
    assertModalOwner(canvas, options.ownerExtensionId, "action");
    const instance = modalInstances.get(key);
    if (options.generation !== undefined && instance?.generation !== options.generation) {
        return { ok: false, stale: true };
    }
    const action = canvas.actions.find((candidate) => candidate.name === name);
    if (!action) throw new Error(`Unknown modal action '${name}' for '${id}'.`);
    modalDiagnostics.actionInvocations++;
    emitRuntimeEvent("ui.modal_canvas.action_started", { modalId: id, actionName: name, generation: options.generation });
    try {
        const result = action.handler
            ? await action.handler(input, modalControls(id, options.generation, canvas.ownerExtensionId, options.ackEventSequence))
            : null;
        emitRuntimeEvent("ui.modal_canvas.action_completed", { modalId: id, actionName: name, generation: options.generation });
        return result;
    } catch (error) {
        emitRuntimeEvent("ui.modal_canvas.action_failed", { modalId: id, actionName: name, failureKind: error?.name ?? "Error", generation: options.generation });
        throw error;
    }
}

function subscribeModalCanvas(id, listener, options = {}) {
    id = validateModalId(id);
    const key = modalOwnedKey(options.ownerExtensionId, id);
    const canvas = modalCanvases.get(key);
    if (!canvas) throw new Error(`Unknown modal canvas '${id}'.`);
    assertModalOwner(canvas, options.ownerExtensionId, "subscription");
    if (typeof listener !== "function") throw new Error("Modal canvas subscriber must be a function.");
    const subscribers = modalSubscribers.get(key) ?? new Set();
    if (subscribers.size >= modalSubscriptionLimit) {
        modalDiagnostics.quotaFailures++;
        throw new Error(`Modal canvas '${id}' exceeded the subscriber quota.`);
    }
    subscribers.add(listener);
    modalSubscribers.set(key, subscribers);
    modalDiagnostics.subscriptionCount++;
    return () => {
        if (subscribers.delete(listener)) modalDiagnostics.subscriptionCount--;
        if (subscribers.size === 0) modalSubscribers.delete(key);
    };
}

async function disposeModalCanvas(id, options = {}) {
    id = validateModalId(id);
    const key = modalOwnedKey(options.ownerExtensionId, id);
    const canvas = modalCanvases.get(key);
    if (canvas) assertModalOwner(canvas, options.ownerExtensionId, "dispose");
    await closeModalCanvas(id, options);
    modalCanvases.delete(key);
    modalDiagnostics.registered = modalCanvases.size;
    modalDiagnostics.active = modalInstances.size;
    emitRuntimeEvent("ui.modal_canvas.disposed", { modalId: id });
}

function registerModalCanvas(definition, options = {}) {
    if (!definition || typeof definition !== "object") throw new Error("A modal canvas definition is required.");
    const ownerExtensionId = validateModalOwnerExtensionId(options.ownerExtensionId ?? definition.ownerExtensionId);
    const ownerCanvasCount = [...modalCanvases.values()].filter((canvas) => canvas.ownerExtensionId === ownerExtensionId).length;
    if (ownerCanvasCount >= modalCanvasLimit || modalCanvases.size >= modalHostCanvasLimit) {
        modalDiagnostics.quotaFailures++;
        throw new Error("Modal canvas registration quota exceeded.");
    }
    const id = validateModalId(definition.id);
    const canvasId = validateModalId(definition.canvasId ?? id, "modal canvas id");
    const surfaceId = validateModalId(definition.surfaceId ?? id, "modal surface id");
    if (surfaceId !== id) throw new Error("Modal canvas id must match surfaceId for legacy handle registration.");
    const key = modalOwnedKey(ownerExtensionId, id);
    const existing = modalCanvases.get(key);
    if (existing) {
        assertModalOwner(existing, ownerExtensionId, "registration");
        throw new Error(`Modal canvas '${id}' is already registered.`);
    }
    const actions = (definition.actions ?? []).map(validateModalAction);
    if (actions.length > modalActionLimit) {
        modalDiagnostics.quotaFailures++;
        throw new Error(`Modal canvas '${id}' exceeded the action quota.`);
    }
    const actionNames = new Set();
    for (const action of actions) {
        if (actionNames.has(action.name)) throw new Error(`Modal canvas '${id}' has duplicate action '${action.name}'.`);
        actionNames.add(action.name);
    }
    const canvas = Object.freeze({
        id,
        ownerExtensionId,
        canvasId,
        surfaceId,
        displayName: truncateModalText(definition.displayName ?? id, 128),
        description: truncateModalText(definition.description ?? "", 512),
        open: definition.open,
        render: definition.render,
        subscribe: definition.subscribe,
        onEvent: definition.onEvent,
        actions
    });
    if (canvas.open !== undefined && typeof canvas.open !== "function") throw new Error(`Modal canvas '${id}' open must be a function.`);
    if (canvas.render !== undefined && typeof canvas.render !== "function") throw new Error(`Modal canvas '${id}' render must be a function.`);
    if (canvas.subscribe !== undefined && typeof canvas.subscribe !== "function") throw new Error(`Modal canvas '${id}' subscribe must be a function.`);
    if (canvas.onEvent !== undefined && typeof canvas.onEvent !== "function") throw new Error(`Modal canvas '${id}' onEvent must be a function.`);
    modalCanvases.set(key, canvas);
    modalDiagnostics.registered = modalCanvases.size;
    emitRuntimeEvent("ui.modal_canvas.registered", { modalId: id, actionCount: actions.length, hasBroker: modalHasBroker() });
    const ownerOptions = ownerExtensionId ? { ownerExtensionId } : {};
    return Object.freeze({
        id,
        ownerExtensionId,
        open: (input) => openModalCanvas(id, input, ownerOptions),
        update: (next) => updateModalCanvas(id, next, ownerOptions),
        close: () => closeModalCanvas(id, ownerOptions),
        invoke: (name, input) => invokeModalAction(id, name, input, ownerOptions),
        subscribe: (listener) => subscribeModalCanvas(id, listener, ownerOptions),
        fallback: () => getModalFallback(id, ownerOptions),
        diagnostics: () => getModalDiagnostics(ownerOptions),
        dispose: () => disposeModalCanvas(id, ownerOptions)
    });
}

function getModalDiagnostics(options = {}) {
    const ownerExtensionId = validateModalOwnerExtensionId(options.ownerExtensionId);
    const canvases = [...modalCanvases.values()].filter((canvas) => !ownerExtensionId || canvas.ownerExtensionId === ownerExtensionId);
    return immutableRuntimeCopy({
        schemaVersion: 1,
        hasBroker: modalHasBroker(),
        registered: modalDiagnostics.registered,
        active: modalDiagnostics.active,
        opened: modalDiagnostics.opened,
        updated: modalDiagnostics.updated,
        closed: modalDiagnostics.closed,
        actionInvocations: modalDiagnostics.actionInvocations,
        subscriptionCount: modalDiagnostics.subscriptionCount,
        fallbackCount: modalDiagnostics.fallbackCount,
        pipeFailures: modalDiagnostics.pipeFailures,
        quotaFailures: modalDiagnostics.quotaFailures,
        lastFailureKind: modalDiagnostics.lastFailureKind,
        canvases: canvases.map((canvas) => ({
            id: canvas.id,
            ownerExtensionId: canvas.ownerExtensionId,
            canvasId: canvas.canvasId,
            surfaceId: canvas.surfaceId,
            displayName: canvas.displayName,
            actionCount: canvas.actions.length,
            active: modalInstances.has(modalOwnedKey(canvas.ownerExtensionId, canvas.id)),
            fallback: modalInstances.get(modalOwnedKey(canvas.ownerExtensionId, canvas.id))?.fallback === true,
            generation: modalInstances.get(modalOwnedKey(canvas.ownerExtensionId, canvas.id))?.generation ?? null
        }))
    });
}
function disposeRuntimeObservers() {
    for (const observer of [...runtimeObservers.values()]) disposeRuntimeObserver(observer);
}

function compareVersions(left, right) {
    const parse = (value) => value.match(/^(\d+)\.(\d+)\.(\d+)-(\d+)$/)?.slice(1).map(Number);
    const a = parse(left);
    const b = parse(right);
    if (!a && !b) return left.localeCompare(right);
    if (!a) return -1;
    if (!b) return 1;
    for (let index = 0; index < a.length; index++) {
        if (a[index] !== b[index]) return a[index] - b[index];
    }
    return 0;
}

async function validateOriginalPackage(root, expectedAppHash, expectedRuntimeHash) {
    const appPath = join(root, "app.js");
    const nativePath = join(root, "prebuilds", `${process.platform}-${process.arch}`, "runtime.node");
    await access(appPath, fsConstants.R_OK);
    await access(nativePath, fsConstants.R_OK);
    for (const [path, expected, label] of [
        [appPath, expectedAppHash, "app.js"],
        [nativePath, expectedRuntimeHash, "runtime.node"]
    ]) {
        if (!expected) continue;
        const actual = createHash("sha256").update(await readFile(path)).digest("hex");
        if (actual !== expected.toLowerCase())
            throw new Error(`Explicit Copilot base ${label} hash mismatch.`);
    }
    return root;
}

async function findOriginalPackage() {
    const explicitRoot = process.env.AFTERBURNER_BASE_PACKAGE?.trim();
    if (explicitRoot) {
        const root = resolve(explicitRoot);
        if (root === wrapperDir) throw new Error("Explicit Copilot base package points to the Afterburner wrapper.");
        return validateOriginalPackage(
            root,
            process.env.AFTERBURNER_BASE_APP_SHA256?.trim(),
            process.env.AFTERBURNER_BASE_RUNTIME_SHA256?.trim()
        );
    }
    const candidates = [];
    for (const name of await readdir(packageRoot)) {
        if (name === wrapperPackageName) continue;
        const root = join(packageRoot, name);
        try {
            await access(join(root, "app.js"), fsConstants.R_OK);
            await access(join(root, "prebuilds", `${process.platform}-${process.arch}`, "runtime.node"), fsConstants.R_OK);
            candidates.push({ name, root });
        } catch {}
    }
    candidates.sort((left, right) => compareVersions(right.name, left.name));
    if (candidates.length === 0) throw new Error("No original Copilot package was found.");
    return candidates[0].root;
}

const copilotRoot = await findOriginalPackage();
const runtimePath = join(copilotRoot, "prebuilds", `${process.platform}-${process.arch}`, "runtime.node");
const originalAppPath = join(copilotRoot, "app.js");
const runtime = require(runtimePath);
if (process.env.COPILOT_RUNTIME_EXTENSION_DEBUG === "1") {
    process.stderr.write(`[runtime-extension-host] loaded from ${wrapperDir}; base ${copilotRoot}\n`);
}
const pickerAdapters = [];
const pickerAdapterOrders = new WeakMap();
let pickerAdapterRegistrationSequence = 0;
const appSourceTransforms = [];
const externalTaskProviders = new Map();
const externalTaskSubscriptions = new Map();
let externalTaskRevision = 0;
const upstreamMetadata = new Map();
const effortOverrides = new Map();
const contextOverrides = new Map();
const nativeContextTiers = ["default", "long_context"];
const externalTaskLifecycle = new Map();
const nativeTaskKinds = new Map();
const copilotPackageVersion = copilotRoot.split(/[\\/]/).at(-1);

function opaqueRuntimeId(prefix, value) {
    if (value === undefined || value === null || value === "") return undefined;
    const digest = createHash("sha256").update(String(value)).digest("base64url").slice(0, 20);
    return `${prefix}_${digest}`;
}

function parseRuntimeJson(value) {
    if (typeof value !== "string") return value && typeof value === "object" ? value : null;
    try {
        const parsed = JSON.parse(value);
        return parsed && typeof parsed === "object" ? parsed : null;
    } catch {
        return null;
    }
}

function safeRuntimeEnum(value, allowed) {
    return typeof value === "string" && allowed.includes(value) ? value : undefined;
}

function externalEntityKind(task) {
    return task?.kind === "agent" || task?.type === "agent" ? "agent" : "task";
}

function externalLifecycleMetadata(providerId, taskOrChange) {
    const remoteId = typeof taskOrChange?.id === "string" ? taskOrChange.id : undefined;
    return {
        providerId,
        ...(remoteId ? { entityId: externalTaskId(providerId, remoteId) } : {}),
        entityKind: externalEntityKind(taskOrChange),
        state: safeRuntimeEnum(taskOrChange?.state ?? taskOrChange?.status,
            ["queued", "pending", "running", "waiting", "idle", "completed", "failed", "cancelled", "recovery-required"]),
        changeKind: safeRuntimeEnum(taskOrChange?.type,
            ["added", "changed", "updated", "removed", "completed", "failed", "cancelled"])
    };
}

emitRuntimeEvent("runtime.bootstrap", {
    runtimeVersion: copilotPackageVersion,
    platform: process.platform,
    architecture: process.arch,
    schemaVersion: runtimeEventSchemaVersion
});

function externalTaskId(providerId, remoteId) {
    const value = createHash("sha256")
        .update(`${providerId}:${remoteId}`).digest("base64url").slice(0, 48);
    return `ext_${value}`;
}

function validateExternalTaskProvider(provider) {
    if (!provider || !/^[a-z0-9][a-z0-9-]{0,31}$/.test(provider.id ?? ""))
        throw new Error("External task providers require a stable lowercase id.");
    for (const operation of ["snapshot", "read", "cancel"]) {
        if (typeof provider[operation] !== "function")
            throw new Error(`External task provider '${provider.id}' is missing ${operation}().`);
    }
}

function registerExternalTaskProvider(provider) {
    validateExternalTaskProvider(provider);
    if (externalTaskProviders.has(provider.id))
        throw new Error(`External task provider '${provider.id}' is already registered.`);
    externalTaskProviders.set(provider.id, provider);
    emitRuntimeEvent("external.provider.registered", {
        providerId: provider.id,
        supportsSubscription: typeof provider.subscribe === "function",
        supportsWrite: typeof provider.write === "function"
    });
    if (typeof provider.subscribe === "function") {
        const unsubscribe = provider.subscribe((change) => {
            externalTaskRevision++;
            const metadata = externalLifecycleMetadata(provider.id, change);
            emitRuntimeEvent(`external.${metadata.entityKind}.changed`, metadata);
        });
        if (typeof unsubscribe === "function") externalTaskSubscriptions.set(provider.id, unsubscribe);
    }
    if (process.env.COPILOT_RUNTIME_EXTENSION_DEBUG === "1")
        process.stderr.write(`[runtime-extension-host] registered external task provider ${provider.id}\n`);
    return () => {
        externalTaskSubscriptions.get(provider.id)?.();
        externalTaskSubscriptions.delete(provider.id);
        externalTaskProviders.delete(provider.id);
        for (const [nativeId, lifecycle] of externalTaskLifecycle) {
            if (lifecycle.providerId === provider.id) externalTaskLifecycle.delete(nativeId);
        }
        emitRuntimeEvent("external.provider.unregistered", { providerId: provider.id });
    };
}

async function externalTaskSnapshot() {
    const projected = [];
    const observedIds = new Set();
    const observedProviders = new Set();
    for (const [providerId, provider] of externalTaskProviders) {
        observedProviders.add(providerId);
        const tasks = await provider.snapshot();
        if (!Array.isArray(tasks)) throw new Error(`External task provider '${providerId}' returned a non-array snapshot.`);
        for (const task of tasks) {
            if (!task || typeof task.id !== "string" || typeof task.title !== "string") continue;
            const nativeId = externalTaskId(providerId, task.id);
            const metadata = externalLifecycleMetadata(providerId, task);
            const previous = externalTaskLifecycle.get(nativeId);
            observedIds.add(nativeId);
            externalTaskLifecycle.set(nativeId, {
                providerId,
                entityKind: metadata.entityKind,
                state: metadata.state
            });
            if (!previous) emitRuntimeEvent(`external.${metadata.entityKind}.discovered`, metadata);
            else if (previous.state !== metadata.state) {
                emitRuntimeEvent(`external.${metadata.entityKind}.state_changed`, {
                    ...metadata,
                    previousState: previous.state
                });
            }
            projected.push({
                ...task,
                nativeId,
                providerId,
                remoteId: task.id
            });
        }
    }
    for (const [nativeId, lifecycle] of externalTaskLifecycle) {
        if (observedProviders.has(lifecycle.providerId) && !observedIds.has(nativeId)) {
            externalTaskLifecycle.delete(nativeId);
            emitRuntimeEvent(`external.${lifecycle.entityKind}.removed`, {
                providerId: lifecycle.providerId,
                entityId: nativeId,
                previousState: lifecycle.state
            });
        }
    }
    const stateCounts = {};
    for (const task of projected) {
        const state = safeRuntimeEnum(task.state ?? task.status,
            ["queued", "pending", "running", "waiting", "idle", "completed", "failed", "cancelled", "recovery-required"]);
        if (state) stateCounts[state] = (stateCounts[state] ?? 0) + 1;
    }
    emitRuntimeEvent("external.task.snapshot", {
        providerCount: observedProviders.size,
        entityCount: projected.length,
        stateCounts
    });
    return projected;
}

async function invokeExternalTask(nativeId, operation, value) {
    const task = (await externalTaskSnapshot()).find((candidate) => candidate.nativeId === nativeId);
    if (!task) throw new Error(`Unknown external task '${nativeId}'.`);
    const provider = externalTaskProviders.get(task.providerId);
    const entityKind = externalEntityKind(task);
    const allowedOperation = safeRuntimeEnum(operation, ["read", "write", "cancel"]);
    if (!allowedOperation) throw new Error(`Unsupported external task operation '${operation}'.`);
    emitRuntimeEvent(`external.${entityKind}.operation_started`, {
        providerId: task.providerId,
        entityId: task.nativeId,
        operation: allowedOperation
    });
    try {
        let result;
        if (operation === "read") result = await provider.read(task.remoteId);
        else if (operation === "write") {
            if (typeof provider.write !== "function")
                throw new Error(`External task provider '${task.providerId}' does not support write.`);
            result = await provider.write(task.remoteId, value);
        } else result = await provider.cancel(task.remoteId);
        emitRuntimeEvent(`external.${entityKind}.operation_completed`, {
            providerId: task.providerId,
            entityId: task.nativeId,
            operation: allowedOperation
        });
        return result;
    } catch (error) {
        emitRuntimeEvent(`external.${entityKind}.operation_failed`, {
            providerId: task.providerId,
            entityId: task.nativeId,
            operation: allowedOperation,
            failureKind: error?.name ?? "Error"
        });
        throw error;
    }
}

function adapterFor(selectionId) {
    return pickerAdapters.find((adapter) => adapter.matches(selectionId));
}

function formatTokenCount(tokens) {
    if (!Number.isFinite(tokens)) return "—";
    if (tokens >= 1_000_000) {
        const millions = tokens / 1_000_000;
        return `${Number.isInteger(millions) ? millions : millions.toFixed(2).replace(/0+$/, "").replace(/\.$/, "")}M`;
    }
    if (tokens >= 1_000) return `${Math.round(tokens / 1_000)}K`;
    return String(tokens);
}

function effortLabel(effort) {
    return effort === "xhigh" ? "Extra high" : effort.charAt(0).toUpperCase() + effort.slice(1);
}

function metadataFor(selectionId) {
    const adapter = adapterFor(selectionId);
    if (!adapter) return null;
    const upstreamId = adapter.upstreamModelId(selectionId);
    const upstream = upstreamMetadata.get(upstreamId);
    return {
        supportedReasoningEfforts: upstream?.supportedReasoningEfforts ??
            adapter.supportedReasoningEfforts?.(selectionId) ??
            runtime.modelSupportedReasoningEfforts(upstreamId),
        defaultReasoningEffort: upstream?.defaultReasoningEffort ?? adapter.defaultReasoningEffort?.(selectionId) ?? "medium",
        maxContextWindowTokens: upstream?.capabilities?.limits?.max_context_window_tokens ?? adapter.maxContextWindowTokens?.(selectionId) ?? null,
        maxOutputTokens: upstream?.capabilities?.limits?.max_output_tokens ?? adapter.maxOutputTokens?.(selectionId) ?? null,
        contextWindowOptions: adapter.contextWindowOptions?.(selectionId) ?? []
    };
}

function contextOverrideKey(sessionId, selectionId) {
    return `${sessionId}:${selectionId}`;
}

function selectedContextWindow(sessionId, selectionId, metadata) {
    return contextOverrides.get(contextOverrideKey(sessionId, selectionId)) ?? metadata.maxContextWindowTokens;
}

function contextCapabilityOverride(sessionId, selectionId) {
    const metadata = metadataFor(selectionId);
    if (!metadata) return null;
    const maxContextWindowTokens = selectedContextWindow(sessionId, selectionId, metadata);
    if (!Number.isFinite(maxContextWindowTokens)) return null;
    const maxOutputTokens = Number.isFinite(metadata.maxOutputTokens) ? metadata.maxOutputTokens : 0;
    return {
        maxPromptTokens: Math.max(1, maxContextWindowTokens - maxOutputTokens),
        maxContextWindowTokens,
        ...(maxOutputTokens > 0 ? { maxOutputTokens } : {})
    };
}

function withBidirectionalNativeContext(row, source) {
    if (!row.contextToggleable || metadataFor(source?.value)) return row;
    const rowContextTier = row.rowKey.split("::")[1];
    const contextTier = source?.contextTier ?? rowContextTier ?? nativeContextTiers[0];
    const contextIndex = nativeContextTiers.indexOf(contextTier);
    if (contextIndex < 0) return row;
    const previousContextTier = nativeContextTiers[contextIndex - 1];
    const nextContextTier = nativeContextTiers[contextIndex + 1];
    return {
        ...row,
        contextCanLower: previousContextTier !== undefined,
        contextCanRaise: nextContextTier !== undefined,
        previousContextTier,
        nextContextTier
    };
}

function enrichModel(model) {
    const metadata = metadataFor(model?.id);
    if (!metadata) return model;
    return {
        ...model,
        supportedReasoningEfforts: metadata.supportedReasoningEfforts,
        defaultReasoningEffort: metadata.defaultReasoningEffort
    };
}

function rememberUpstream(models) {
    for (const model of models ?? []) {
        if (typeof model?.id === "string" && !adapterFor(model.id)) upstreamMetadata.set(model.id, model);
    }
}

function safeRuntimeIdentifier(value, prefix) {
    if (typeof value !== "string" || value.length === 0) return undefined;
    return /^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/.test(value)
        ? value
        : opaqueRuntimeId(prefix, value);
}

function reportInstrumentationFailure(seam, error) {
    if (process.env.COPILOT_RUNTIME_EXTENSION_DEBUG !== "1") return;
    const failureKind = sanitizeRuntimeMetadata(error?.name ?? "Error") || "Error";
    process.stderr.write(`[runtime-extension-host] observer seam '${seam}' failed (${failureKind})\n`);
}

function finishObservedRuntimeCall(result, seam, onSuccess, onFailure) {
    const succeed = (value) => {
        try { onSuccess?.(value); } catch (error) { reportInstrumentationFailure(seam, error); }
        return value;
    };
    const fail = (error) => {
        try { onFailure?.(error); } catch (instrumentationError) {
            reportInstrumentationFailure(seam, instrumentationError);
        }
        throw error;
    };
    return result && typeof result.then === "function" ? result.then(succeed, fail) : succeed(result);
}

function installObservedRuntimeMethod(name, handler, installedSeams) {
    if (typeof runtime[name] !== "function") return;
    const original = runtime[name].bind(runtime);
    runtime[name] = (...args) => {
        try {
            return handler(original, args);
        } catch (error) {
            if (error?.__afterburnerOriginalFailure === true) throw error.cause;
            reportInstrumentationFailure(name, error);
            return original(...args);
        }
    };
    installedSeams.push(name);
}

function callObservedOriginal(original, args, seam, onSuccess, onFailure) {
    let result;
    try {
        result = original(...args);
    } catch (error) {
        try { onFailure?.(error); } catch (instrumentationError) {
            reportInstrumentationFailure(seam, instrumentationError);
        }
        const wrapped = new Error("Observed runtime call failed", { cause: error });
        wrapped.__afterburnerOriginalFailure = true;
        throw wrapped;
    }
    return finishObservedRuntimeCall(result, seam, onSuccess, onFailure);
}

function extensionMetadata(entry) {
    const extensionId = safeRuntimeIdentifier(
        entry?.id ?? entry?.name ?? entry?.plugin?.name ?? entry?.manifest?.name,
        "ext"
    );
    const version = typeof (entry?.version ?? entry?.plugin?.version ?? entry?.manifest?.version) === "string"
        ? entry?.version ?? entry?.plugin?.version ?? entry?.manifest?.version
        : undefined;
    return { extensionId, version };
}

function nativeTaskKey(taskStoreId, taskId) {
    return `${String(taskStoreId)}:${String(taskId)}`;
}

function nativeTaskMetadata(taskStoreId, taskId, task) {
    const entityKind = task?.type === "agent" || task?.kind === "agent" ? "agent" :
        nativeTaskKinds.get(nativeTaskKey(taskStoreId, taskId)) ?? "task";
    const effectiveId = taskId ?? task?.id ?? task?.taskId ?? task?.agentId;
    const parentId = task?.parentId ?? task?.parentTaskId;
    const state = safeRuntimeEnum(task?.state ?? task?.status,
        ["queued", "pending", "running", "waiting", "idle", "completed", "failed", "cancelled"]);
    return {
        entityKind,
        entityId: effectiveId === undefined ? undefined :
            opaqueRuntimeId(entityKind === "agent" ? "agt" : "tsk", `${taskStoreId}:${effectiveId}`),
        taskStoreId: opaqueRuntimeId("store", taskStoreId),
        parentId: parentId === undefined ? undefined : opaqueRuntimeId("tsk", `${taskStoreId}:${parentId}`),
        state,
        background: typeof task?.background === "boolean" ? task.background : undefined
    };
}

function modelSelectionMetadata(sessionId, selection) {
    return {
        sessionId: opaqueRuntimeId("ses", sessionId),
        modelId: typeof (selection?.modelId ?? selection?.model) === "string"
            ? selection?.modelId ?? selection?.model
            : undefined,
        previousModelId: typeof selection?.previousModel === "string" ? selection.previousModel : undefined,
        reasoningEffort: safeRuntimeEnum(selection?.reasoningEffort,
            ["minimal", "low", "medium", "high", "xhigh", "max"]),
        contextTier: safeRuntimeEnum(selection?.contextTier, ["default", "long_context"]),
        targetKind: safeRuntimeEnum(selection?.targetKind,
            ["session", "plan", "repo", "local", "subagent-agent"]),
        reasoningExplicit: typeof selection?.reasoningEffortExplicit === "boolean"
            ? selection.reasoningEffortExplicit : undefined,
        contextExplicit: typeof selection?.contextTierExplicit === "boolean"
            ? selection.contextTierExplicit : undefined
    };
}

function installRuntimeObserverSeams() {
    const installedSeams = [];

    installObservedRuntimeMethod("pluginsLoadPluginExtensionDirs", (original, args) => {
        const options = args[0] ?? {};
        emitRuntimeEvent("extension.discovery.started", {
            activeCount: Array.isArray(options.activePlugins) ? options.activePlugins.length : 0,
            additionalCount: Array.isArray(options.additionalPlugins) ? options.additionalPlugins.length : 0
        });
        return callObservedOriginal(original, args, "pluginsLoadPluginExtensionDirs", (result) => {
            const entries = Array.isArray(result?.entries) ? result.entries : [];
            for (const entry of entries) emitRuntimeEvent("extension.discovered", {
                ...extensionMetadata(entry),
                extensionKind: "copilot-plugin"
            });
            emitRuntimeEvent("extension.discovery.completed", {
                discoveredCount: entries.length,
                warningCount: Array.isArray(result?.warnings) ? result.warnings.length : 0
            });
        }, (error) => emitRuntimeEvent("extension.discovery.failed", {
            failureKind: error?.name ?? "Error"
        }));
    }, installedSeams);

    installObservedRuntimeMethod("customAgentsDiscoverJson", (original, args) =>
        callObservedOriginal(original, args, "customAgentsDiscoverJson", (result) => {
            const agents = parseRuntimeJson(result)?.agents;
            if (!Array.isArray(agents)) return;
            for (const agent of agents) emitRuntimeEvent("agent.discovered", {
                agentId: safeRuntimeIdentifier(agent?.id ?? agent?.name, "agt"),
                agentKind: agent?.source === "builtin" ? "builtin" : "custom"
            });
            emitRuntimeEvent("agent.discovery.completed", { discoveredCount: agents.length });
        }, (error) => emitRuntimeEvent("agent.discovery.failed", {
            failureKind: error?.name ?? "Error"
        })), installedSeams);

    installObservedRuntimeMethod("sessionSetInstalledPluginsJson", (original, args) => {
        const plugins = parseRuntimeJson(args[1]);
        return callObservedOriginal(original, args, "sessionSetInstalledPluginsJson", () => {
            const entries = Array.isArray(plugins) ? plugins : [];
            for (const entry of entries) emitRuntimeEvent("extension.configured", {
                ...extensionMetadata(entry),
                extensionKind: "session-plugin",
                enabled: typeof entry?.enabled === "boolean" ? entry.enabled : undefined
            });
        }, (error) => emitRuntimeEvent("extension.configuration.failed", {
            failureKind: error?.name ?? "Error"
        }));
    }, installedSeams);

    for (const name of ["hookSessionAddPlugins", "hookSessionReplacePlugins"]) {
        installObservedRuntimeMethod(name, (original, args) => {
            const plugins = parseRuntimeJson(args[1]);
            return callObservedOriginal(original, args, name, () => {
                for (const plugin of Array.isArray(plugins) ? plugins : []) {
                    emitRuntimeEvent("extension.activated", {
                        extensionId: safeRuntimeIdentifier(plugin?.pluginName, "ext"),
                        extensionKind: "copilot-hook",
                        sessionId: opaqueRuntimeId("ses", args[0]),
                        activationMode: name === "hookSessionReplacePlugins" ? "replace" : "add"
                    });
                }
            }, (error) => emitRuntimeEvent("extension.failed", {
                extensionKind: "copilot-hook",
                sessionId: opaqueRuntimeId("ses", args[0]),
                failureKind: error?.name ?? "Error"
            }));
        }, installedSeams);
    }

    installObservedRuntimeMethod("sessionCustomAgentsSetLoadedJson", (original, args) => {
        const agents = parseRuntimeJson(args[1]);
        return callObservedOriginal(original, args, "sessionCustomAgentsSetLoadedJson", () => {
            for (const agent of Array.isArray(agents) ? agents : []) {
                emitRuntimeEvent("agent.loaded", {
                    sessionId: opaqueRuntimeId("ses", args[0]),
                    agentId: safeRuntimeIdentifier(agent?.id ?? agent?.name, "agt"),
                    agentKind: agent?.source === "builtin" ? "builtin" : "custom"
                });
            }
        }, (error) => emitRuntimeEvent("agent.load_failed", {
            sessionId: opaqueRuntimeId("ses", args[0]),
            failureKind: error?.name ?? "Error"
        }));
    }, installedSeams);

    installObservedRuntimeMethod("sessionCustomAgentsSetSelectedJson", (original, args) => {
        const agent = parseRuntimeJson(args[1]);
        return callObservedOriginal(original, args, "sessionCustomAgentsSetSelectedJson", () => {
            emitRuntimeEvent(agent ? "agent.selected" : "agent.selection_cleared", {
                sessionId: opaqueRuntimeId("ses", args[0]),
                agentId: safeRuntimeIdentifier(agent?.id ?? agent?.name, "agt"),
                modelId: typeof agent?.model === "string" ? agent.model : undefined
            });
        });
    }, installedSeams);

    installObservedRuntimeMethod("sessionCustomAgentsDeselect", (original, args) =>
        callObservedOriginal(original, args, "sessionCustomAgentsDeselect", () => {
            emitRuntimeEvent("agent.selection_cleared", {
                sessionId: opaqueRuntimeId("ses", args[0])
            });
        }), installedSeams);

    for (const name of ["sessionExtensionsInvokeJson", "sessionPluginsInvokeJson"]) {
        installObservedRuntimeMethod(name, (original, args) => {
            const operation = args.find((value) => typeof value === "string" &&
                ["list", "enable", "disable", "reload"].includes(value));
            return callObservedOriginal(original, args, name, () => {
                if (operation) emitRuntimeEvent("extension.management.completed", {
                    operation,
                    sessionId: opaqueRuntimeId("ses", args[0])
                });
            }, (error) => emitRuntimeEvent("extension.management.failed", {
                operation,
                sessionId: opaqueRuntimeId("ses", args[0]),
                failureKind: error?.name ?? "Error"
            }));
        }, installedSeams);
    }

    installObservedRuntimeMethod("modelCliPersistPickerSelection", (original, args) => {
        const selection = args[0] ?? {};
        return callObservedOriginal(original, args, "modelCliPersistPickerSelection", () => {
            emitRuntimeEvent("ui.model_picker.selection_persisted",
                modelSelectionMetadata(selection.sessionId, selection));
        }, (error) => emitRuntimeEvent("ui.model_picker.selection_failed", {
            ...modelSelectionMetadata(selection.sessionId, selection),
            failureKind: error?.name ?? "Error"
        }));
    }, installedSeams);

    installObservedRuntimeMethod("sessionApplyModelChangeJson", (original, args) => {
        const selection = parseRuntimeJson(args[1]) ?? {};
        return callObservedOriginal(original, args, "sessionApplyModelChangeJson", () => {
            emitRuntimeEvent("model.selection.applied", {
                ...modelSelectionMetadata(args[0], selection),
                changeCause: safeRuntimeEnum(selection.cause, ["host", "user", "resume", "startup"])
            });
        }, (error) => emitRuntimeEvent("model.selection.failed", {
            ...modelSelectionMetadata(args[0], selection),
            failureKind: error?.name ?? "Error"
        }));
    }, installedSeams);

    installObservedRuntimeMethod("sessionModelSwitchToJson", (original, args) => {
        const selection = parseRuntimeJson(args[1]) ?? {};
        return callObservedOriginal(original, args, "sessionModelSwitchToJson", () => {
            emitRuntimeEvent("model.selection.switched", modelSelectionMetadata(args[0], selection));
        }, (error) => emitRuntimeEvent("model.selection.failed", {
            ...modelSelectionMetadata(args[0], selection),
            failureKind: error?.name ?? "Error"
        }));
    }, installedSeams);

    installObservedRuntimeMethod("sessionModelSelectionSetSelectedModel", (original, args) =>
        callObservedOriginal(original, args, "sessionModelSelectionSetSelectedModel", () => {
            emitRuntimeEvent("model.selection.state_changed", modelSelectionMetadata(args[0], { modelId: args[1] }));
        }), installedSeams);

    installObservedRuntimeMethod("sessionModelSelectionSetReasoningEffort", (original, args) =>
        callObservedOriginal(original, args, "sessionModelSelectionSetReasoningEffort", () => {
            emitRuntimeEvent("model.reasoning.state_changed",
                modelSelectionMetadata(args[0], { reasoningEffort: args[1] }));
        }), installedSeams);

    installObservedRuntimeMethod("sessionModelSelectionSetAutoTier", (original, args) =>
        callObservedOriginal(original, args, "sessionModelSelectionSetAutoTier", () => {
            emitRuntimeEvent("model.auto_tier.state_changed", {
                sessionId: opaqueRuntimeId("ses", args[0]),
                autoTier: typeof args[1] === "string" ? args[1] : undefined
            });
        }), installedSeams);

    installObservedRuntimeMethod("sessionTaskRegisterJson", (original, args) => {
        const task = parseRuntimeJson(args[1]) ?? {};
        const taskId = task.id ?? task.taskId ?? task.agentId;
        const metadata = nativeTaskMetadata(args[0], taskId, task);
        if (taskId !== undefined) nativeTaskKinds.set(nativeTaskKey(args[0], taskId), metadata.entityKind);
        return callObservedOriginal(original, args, "sessionTaskRegisterJson", () => {
            emitRuntimeEvent(`${metadata.entityKind}.registered`, metadata);
        }, (error) => emitRuntimeEvent(`${metadata.entityKind}.registration_failed`, {
            ...metadata,
            failureKind: error?.name ?? "Error"
        }));
    }, installedSeams);

    installObservedRuntimeMethod("sessionTaskStartAgent", (original, args) => {
        const metadata = nativeTaskMetadata(args[0], args[1], { type: "agent", state: "running" });
        nativeTaskKinds.set(nativeTaskKey(args[0], args[1]), "agent");
        emitRuntimeEvent("agent.starting", metadata);
        return callObservedOriginal(original, args, "sessionTaskStartAgent", () => {
            emitRuntimeEvent("agent.started", metadata);
        }, (error) => emitRuntimeEvent("agent.failed", {
            ...metadata,
            failureKind: error?.name ?? "Error"
        }));
    }, installedSeams);

    for (const [name, action, state] of [
        ["sessionTaskCompleteJson", "completed", "completed"],
        ["sessionTaskFailJson", "failed", "failed"],
        ["sessionTaskCancelJson", "cancelled", "cancelled"],
        ["sessionTaskRemoveJson", "removed", undefined]
    ]) {
        installObservedRuntimeMethod(name, (original, args) => {
            const metadata = nativeTaskMetadata(args[0], args[1], { state });
            return callObservedOriginal(original, args, name, (result) => {
                const outcome = parseRuntimeJson(result);
                const applied = action === "completed" || action === "failed" ||
                    (action === "cancelled" && outcome?.cancelled === true) ||
                    (action === "removed" && outcome?.removed === true);
                emitRuntimeEvent(`${metadata.entityKind}.${applied ? action : `${action}_not_applied`}`, metadata);
                if (action === "removed" && applied) nativeTaskKinds.delete(nativeTaskKey(args[0], args[1]));
            }, (error) => emitRuntimeEvent(`${metadata.entityKind}.${action}_failed`, {
                ...metadata,
                failureKind: error?.name ?? "Error"
            }));
        }, installedSeams);
    }

    installObservedRuntimeMethod("sessionDispatchTaskTransitionFlow", (original, args) =>
        callObservedOriginal(original, args, "sessionDispatchTaskTransitionFlow", () => {
            emitRuntimeEvent("task.transition", {
                sessionId: opaqueRuntimeId("ses", args[0]),
                transitionKind: typeof args[1] === "string" && /^[a-z0-9._-]{1,64}$/i.test(args[1])
                    ? args[1] : undefined
            });
        }), installedSeams);

    installObservedRuntimeMethod("sessionStartSubagentWithHost", (original, args) => {
        const details = parseRuntimeJson(args[1]) ?? {};
        const metadata = {
            sessionId: opaqueRuntimeId("ses", args[0]),
            agentId: opaqueRuntimeId("agt", details.agentId ?? details.taskRegistryAgentId)
        };
        emitRuntimeEvent("agent.starting", metadata);
        return callObservedOriginal(original, args, "sessionStartSubagentWithHost", () => {
            emitRuntimeEvent("agent.started", metadata);
        }, (error) => emitRuntimeEvent("agent.failed", {
            ...metadata,
            failureKind: error?.name ?? "Error"
        }));
    }, installedSeams);

    installObservedRuntimeMethod("sessionPlanSubagentCompletionJson", (original, args) => {
        const details = parseRuntimeJson(args[2]) ?? {};
        const metadata = {
            sessionId: opaqueRuntimeId("ses", args[0]),
            agentId: opaqueRuntimeId("agt", details.agentId),
            cancelled: details.cancelled === true,
            modelId: typeof details.modelOverride === "string" ? details.modelOverride : undefined
        };
        return callObservedOriginal(original, args, "sessionPlanSubagentCompletionJson", (result) => {
            const completion = parseRuntimeJson(result);
            emitRuntimeEvent(completion?.failed === true ? "agent.failed" : "agent.completed", metadata);
        }, (error) => emitRuntimeEvent("agent.failed", {
            ...metadata,
            failureKind: error?.name ?? "Error"
        }));
    }, installedSeams);

    installObservedRuntimeMethod("sessionMarkSubagentFailed", (original, args) =>
        callObservedOriginal(original, args, "sessionMarkSubagentFailed", () => {
            emitRuntimeEvent("agent.failed", { sessionId: opaqueRuntimeId("ses", args[0]) });
        }), installedSeams);

    emitRuntimeEvent("runtime.seams.installed", {
        runtimeVersion: copilotPackageVersion,
        seamCount: installedSeams.length,
        seams: installedSeams
    });
}

function installPickerBridge() {
    const originalMergeModelMetadata = runtime.sessionByokMergeModelMetadataEntries.bind(runtime);
    runtime.sessionByokMergeModelMetadataEntries = (sessionId, models) => {
        rememberUpstream(models);
        return originalMergeModelMetadata(sessionId, models).map(enrichModel);
    };

    const originalByokModelMetadata = runtime.sessionByokModelMetadataEntries.bind(runtime);
    runtime.sessionByokModelMetadataEntries = (sessionId) =>
        originalByokModelMetadata(sessionId).map(enrichModel);

    const OriginalPickerHandle = runtime.CliModelPickerHandle;
    runtime.CliModelPickerHandle = class RuntimeExtensionPickerHandle {
        constructor(options) {
            const inner = new OriginalPickerHandle(options);
            const sessionId = options.sessionId;
            let opened = false;
            const pickerMetadata = {
                sessionId: opaqueRuntimeId("ses", sessionId),
                targetKind: safeRuntimeEnum(options.targetKind,
                    ["session", "plan", "repo", "local", "subagent-agent"])
            };
            const snapshot = () => {
                const projection = inner.snapshot();
                const augmented = {
                    ...projection,
                    rows: projection.rows.map((row) => {
                        const metadata = metadataFor(row.value);
                        if (!metadata) return row;
                        return {
                            ...row,
                            effort: effortOverrides.get(row.value) ?? row.effort ?? metadata.defaultReasoningEffort,
                            reasoningEffortExplicit: true,
                            contextTier: "default",
                            contextTierExplicit: true,
                            contextWindowTokens: selectedContextWindow(sessionId, row.value, metadata)
                        };
                    })
                };
                if (!opened) {
                    opened = true;
                    emitRuntimeEvent("ui.model_picker.opened", {
                        ...pickerMetadata,
                        rowCount: augmented.rows.length
                    });
                }
                return augmented;
            };
            return new Proxy(inner, {
                get(target, property, receiver) {
                    if (property === "snapshot") return snapshot;
                    if (property === "setReasoningEffort") {
                        return (selectionId, effort) => {
                            const metadata = {
                                ...pickerMetadata,
                                modelId: selectionId,
                                reasoningEffort: safeRuntimeEnum(effort,
                                    ["minimal", "low", "medium", "high", "xhigh", "max"]),
                                customModel: Boolean(adapterFor(selectionId))
                            };
                            if (!adapterFor(selectionId)) {
                                const result = target.setReasoningEffort(selectionId, effort);
                                return finishObservedRuntimeCall(result, "CliModelPickerHandle.setReasoningEffort", () => {
                                    emitRuntimeEvent("ui.model_picker.reasoning_changed", metadata);
                                });
                            }
                            effortOverrides.set(selectionId, effort);
                            emitRuntimeEvent("ui.model_picker.reasoning_changed", metadata);
                            return snapshot();
                        };
                    }
                    if (property === "setContextTier") {
                        return (selectionId, contextWindowTokens) => {
                            const customModel = Boolean(adapterFor(selectionId));
                            const metadata = {
                                ...pickerMetadata,
                                modelId: selectionId,
                                customModel,
                                ...(customModel
                                    ? { contextWindowTokens: Number(contextWindowTokens) }
                                    : { contextTier: typeof contextWindowTokens === "string" ? contextWindowTokens : undefined })
                            };
                            if (!customModel) {
                                const result = target.setContextTier(selectionId, contextWindowTokens);
                                return finishObservedRuntimeCall(result, "CliModelPickerHandle.setContextTier", () => {
                                    emitRuntimeEvent("ui.model_picker.context_changed", metadata);
                                });
                            }
                            contextOverrides.set(
                                contextOverrideKey(sessionId, selectionId),
                                Number(contextWindowTokens)
                            );
                            emitRuntimeEvent("ui.model_picker.context_changed", metadata);
                            return snapshot();
                        };
                    }
                    if (property === "dispose" && typeof target.dispose === "function") {
                        return (...args) => finishObservedRuntimeCall(
                            target.dispose(...args),
                            "CliModelPickerHandle.dispose",
                            () => emitRuntimeEvent("ui.model_picker.closed", pickerMetadata)
                        );
                    }
                    const value = Reflect.get(target, property, receiver);
                    return typeof value === "function" ? value.bind(target) : value;
                }
            });
        }
    };

    const originalProjectInlineTable = runtime.modelProjectInlineTable.bind(runtime);
    runtime.modelProjectInlineTable = (input) => {
        const result = originalProjectInlineTable(input);
        let changed = false;
        let customAugmented = false;
        const rowsById = new Map((input.rows ?? []).map((row) => [row.value, row]));
        const rows = result.rows.map((row) => {
            const source = rowsById.get(row.rowKey.split("::", 1)[0]);
            const nativeContextRow = withBidirectionalNativeContext(row, source);
            const metadata = metadataFor(source?.value);
            if (!metadata) {
                if (nativeContextRow !== row) changed = true;
                return nativeContextRow;
            }
            changed = true;
            customAugmented = true;
            const efforts = metadata.supportedReasoningEfforts;
            const effort = source.effort ?? metadata.defaultReasoningEffort;
            const effortIndex = efforts.indexOf(effort);
            const contextWindowTokens = source.contextWindowTokens ?? metadata.maxContextWindowTokens;
            const contextOptions = metadata.contextWindowOptions;
            const contextIndex = contextOptions.indexOf(contextWindowTokens);
            const previousContextWindow = contextIndex > 0
                ? contextOptions[contextIndex - 1]
                : undefined;
            const nextContextWindow = contextOptions.length > 1
                ? contextOptions[contextIndex + 1]
                : undefined;
            const contextText = formatTokenCount(contextWindowTokens);
            const contextSegments = contextOptions.length > 1
                ? contextOptions.flatMap((option, index) => [
                    ...(index > 0 ? [{ key: `separator-${option}`, text: " ", active: false }] : []),
                    {
                        key: `context-${option}`,
                        text: formatTokenCount(option),
                        active: option === contextWindowTokens
                    }
                ])
                : [{ key: "context-size", text: contextText, active: true }];
            return {
                ...row,
                reasoningConfigurable: efforts.length > 1,
                reasoningCellText: effortLabel(effort),
                reasoningCanLower: effortIndex > 0,
                reasoningCanRaise: effortIndex >= 0 && effortIndex < efforts.length - 1,
                previousEffort: effortIndex > 0 ? efforts[effortIndex - 1] : undefined,
                nextEffort: effortIndex >= 0 && effortIndex < efforts.length - 1 ? efforts[effortIndex + 1] : undefined,
                contextToggleable: contextOptions.length > 1,
                contextCanLower: previousContextWindow !== undefined,
                contextCanRaise: nextContextWindow !== undefined,
                previousContextTier: previousContextWindow === undefined ? undefined : String(previousContextWindow),
                nextContextTier: nextContextWindow === undefined ? undefined : String(nextContextWindow),
                contextCellText: contextText,
                contextSegments
            };
        });
        if (!changed) return result;
        return {
            ...result,
            rows,
            ...(customAugmented ? { hasReasoningColumn: true, hasContextColumn: true } : {})
        };
    };

    const originalModelSwitchTo = runtime.sessionModelSwitchToJson.bind(runtime);
    runtime.sessionModelSwitchToJson = (sessionId, request) => {
        const parsed = typeof request === "string" ? JSON.parse(request) : request;
        const override = contextCapabilityOverride(sessionId, parsed?.modelId);
        if (!override) return originalModelSwitchTo(sessionId, request);
        const enriched = {
            ...parsed,
            contextTier: undefined,
            modelCapabilities: {
                ...(parsed.modelCapabilities ?? {}),
                limits: {
                    ...(parsed.modelCapabilities?.limits ?? {}),
                    max_prompt_tokens: override.maxPromptTokens,
                    max_context_window_tokens: override.maxContextWindowTokens,
                    max_output_tokens: override.maxOutputTokens
                }
            }
        };
        const result = originalModelSwitchTo(
            sessionId,
            typeof request === "string" ? JSON.stringify(enriched) : enriched
        );
        return finishObservedRuntimeCall(result, "contextCapabilityOverride", () => {
            emitRuntimeEvent("model.context.capability_override", {
                sessionId: opaqueRuntimeId("ses", sessionId),
                modelId: parsed?.modelId,
                contextWindowTokens: override.maxContextWindowTokens,
                maxGenerationTokens: override.maxOutputTokens
            });
        });
    };
}

function registerModelPickerAdapter(adapter, activationOrder = Number.MAX_SAFE_INTEGER) {
    if (!adapter || typeof adapter.matches !== "function" || typeof adapter.upstreamModelId !== "function") {
        throw new Error("A model picker adapter must define matches() and upstreamModelId().");
    }
    pickerAdapterOrders.set(adapter, {
        activationOrder: Number.isSafeInteger(activationOrder) ? activationOrder : Number.MAX_SAFE_INTEGER,
        registrationSequence: pickerAdapterRegistrationSequence++
    });
    pickerAdapters.push(adapter);
    pickerAdapters.sort((left, right) => {
        const leftOrder = pickerAdapterOrders.get(left);
        const rightOrder = pickerAdapterOrders.get(right);
        return leftOrder.activationOrder - rightOrder.activationOrder ||
            leftOrder.registrationSequence - rightOrder.registrationSequence;
    });
    let selectionIds = [];
    try { selectionIds = adapter.selectionIds?.() ?? []; }
    catch (error) { reportInstrumentationFailure("registerModelPickerAdapter", error); }
    emitRuntimeEvent("model.adapter.registered", {
        adapterIndex: pickerAdapters.length,
        selectionCount: Array.isArray(selectionIds) ? selectionIds.length : 0,
        modelIds: Array.isArray(selectionIds) ? selectionIds : []
    });
    if (process.env.COPILOT_RUNTIME_EXTENSION_DEBUG === "1") {
        process.stderr.write(`[runtime-extension-host] registered picker adapter ${pickerAdapters.length}\n`);
    }
}

function registerAppSourceTransform(transform, extensionId = "runtime", activationOrder = Number.MAX_SAFE_INTEGER) {
    if (typeof transform !== "function") {
        throw new Error("An app source transform must be a function.");
    }
    appSourceTransforms.push({ transform, extensionId, activationOrder });
}

function runtimePhaseStartedAt() {
    return process.hrtime.bigint();
}

function runtimePhaseDurationMs(startedAt) {
    return Number(((Number(process.hrtime.bigint() - startedAt) / 1e6).toFixed(3)));
}

async function runTimedRuntimePhase(eventType, metadata, operation) {
    const startedAt = runtimePhaseStartedAt();
    emitRuntimeEvent(`${eventType}.started`, metadata);
    try {
        const result = await operation();
        const durationMs = runtimePhaseDurationMs(startedAt);
        emitRuntimeEvent(`${eventType}.completed`, { ...metadata, durationMs });
        if (process.env.COPILOT_RUNTIME_EXTENSION_DEBUG === "1") {
            const subject = metadata?.extensionId ? ` '${metadata.extensionId}'` : "";
            process.stderr.write(`[runtime-extension-host] ${eventType}${subject} completed in ${durationMs}ms\n`);
        }
        return { result, durationMs };
    } catch (error) {
        emitRuntimeEvent(`${eventType}.failed`, {
            ...metadata,
            durationMs: runtimePhaseDurationMs(startedAt),
            failureKind: error?.name ?? "Error"
        });
        throw error;
    }
}

async function runSerialRuntimeActivationLane(activations) {
    for (const activate of activations) await activate();
}

async function activateRuntimeExtensionLanes(copilotActivations, afterburnerActivations) {
    await Promise.all([
        runSerialRuntimeActivationLane(copilotActivations),
        runSerialRuntimeActivationLane(afterburnerActivations)
    ]);
}

async function activateRuntimeExtensionGroups(copilotActivations, blockingAfterburnerActivations,
    deferredAfterburnerActivations) {
    const deferredCompletion = runSerialRuntimeActivationLane(deferredAfterburnerActivations);
    await activateRuntimeExtensionLanes(copilotActivations, blockingAfterburnerActivations);
    return { deferredCompletion };
}

function immutableNativeRuntimeExtension(assertion, manifest) {
    return assertion?.trustedBuiltin === true &&
        trustedBuiltinSourceTypes.has(assertion.sourceType) &&
        manifest?.visibility === "builtin";
}

function runtimeExtensionBlocksAppImport(manifest) {
    return Array.isArray(manifest?.capabilities) &&
        manifest.capabilities.includes("application-source-transform");
}

function parseJsonc(text) {
    let output = "";
    let inString = false;
    let escaped = false;
    let lineComment = false;
    let blockComment = false;
    for (let index = 0; index < text.length; index++) {
        const current = text[index];
        const next = text[index + 1];
        if (lineComment) {
            if (current === "\n") {
                lineComment = false;
                output += current;
            }
            continue;
        }
        if (blockComment) {
            if (current === "*" && next === "/") {
                blockComment = false;
                index++;
            }
            continue;
        }
        if (inString) {
            output += current;
            if (escaped) escaped = false;
            else if (current === "\\") escaped = true;
            else if (current === "\"") inString = false;
            continue;
        }
        if (current === "\"") {
            inString = true;
            output += current;
        } else if (current === "/" && next === "/") {
            lineComment = true;
            index++;
        } else if (current === "/" && next === "*") {
            blockComment = true;
            index++;
        } else {
            output += current;
        }
    }
    return safeJSONParse(output.replace(/,\s*([}\]])/g, "$1"));
}

function runtimeHostGrantResolver(context) {
    return (request) => {
        const resolver = globalThis.__afterburnerUiGrantResolver ?? globalThis.__afterburnerRuntimeGrantResolver;
        if (typeof resolver !== "function") return null;
        return resolver({ ...request, pluginRoot: context.pluginRoot, extensionId: context.extensionId, manifest: context.manifest });
    };
}

function ownerRuntimeIdentifier(value) {
    const text = String(value ?? "extension").toLowerCase();
    if (/^[a-z0-9][a-z0-9._:-]{0,127}$/.test(text)) return text;
    return `ext-${createHash("sha256").update(String(value ?? "extension")).digest("hex").slice(0, 20)}`;
}

function runtimeManifestWithoutBuiltinPrivileges(manifest) {
    if (!manifest || typeof manifest !== "object") return manifest;
    const sanitized = { ...manifest, visibility: manifest.visibility === "builtin" ? "private" : manifest.visibility };
    if (Array.isArray(manifest.capabilities)) {
        sanitized.capabilities = manifest.capabilities.filter((capability) => !["modal-canvas", "runtime-observer"].includes(capability));
    }
    return sanitized;
}

function runtimeExtensionContext(pluginRoot, options = {}) {
    const rawManifest = options.manifest;
    const extensionId = ownerRuntimeIdentifier(options.extensionId ?? rawManifest?.id ?? pluginRoot?.split(/[\\/]/).filter(Boolean).at(-1));
    const baseContext = Object.freeze({
        pluginRoot,
        manifest: rawManifest,
        rawManifest,
        extensionId,
        manifestHash: options.manifestHash,
        packageTreeHash: options.packageTreeHash,
        registrySource: options.registrySource,
        nativeIdentityAssertion: options.nativeIdentityAssertion ?? null,
        nativeTrustedBuiltin: options.nativeIdentityAssertion?.trustedBuiltin === true
    });
    const isReservedBuiltin = extensionId === modalBlackBoxOwnerExtensionId || rawManifest?.id === modalBlackBoxOwnerExtensionId;
    if (!isReservedBuiltin || trustedRuntimeIdentity(baseContext, modalBlackBoxOwnerExtensionId)) return baseContext;
    return Object.freeze({ ...baseContext, manifest: runtimeManifestWithoutBuiltinPrivileges(rawManifest), untrustedReservedBuiltin: true });
}

function normalizeRuntimeAuthorizationDecision(decision) {
    if (decision === undefined || decision === null) return null;
    if (decision === true || decision === "allow") return { allowed: true };
    if (decision === false || decision === "deny") return { allowed: false, reason: "grant-denied" };
    if (typeof decision === "object") {
        if (decision.allowed === true || decision.granted === true || decision.effect === "allow") return { allowed: true, reason: decision.reason, grantId: decision.grantId ?? decision.id };
        if (decision.allowed === false || decision.granted === false || decision.effect === "deny") {
            return { allowed: false, reason: decision.reason ?? decision.code ?? "grant-denied", grantId: decision.grantId ?? decision.id };
        }
    }
    return null;
}

function manifestDeclaresRuntimeCapability(manifest, capability) {
    return Array.isArray(manifest?.capabilities) && manifest.capabilities.includes(capability);
}

function nativeIdentityAssertionFor(extensionId, activePath) {
    const normalizedId = ownerRuntimeIdentifier(extensionId);
    const normalizedPath = typeof activePath === "string" && activePath ? resolve(activePath).toLowerCase() : undefined;
    if (!normalizedId || !normalizedPath || typeof currentModalBrokerConfig !== "function") return null;
    return (currentModalBrokerConfig()?.verifiedExtensions ?? []).find((entry) =>
        entry.extensionId === normalizedId &&
        typeof entry.activePath === "string" && resolve(entry.activePath).toLowerCase() === normalizedPath) ?? null;
}

function nativeIdentityVerified(context) {
    const assertion = context.nativeIdentityAssertion;
    return assertion?.extensionId === context.extensionId &&
        typeof assertion.activePath === "string" &&
        resolve(assertion.activePath).toLowerCase() === resolve(context.pluginRoot).toLowerCase() &&
        typeof context.manifestHash === "string" && assertion.manifestHash === context.manifestHash &&
        typeof context.packageTreeHash === "string" && assertion.treeHash === context.packageTreeHash;
}

function nativeTrustedBuiltinAssertion(context, expectedId) {
    const assertion = context.nativeIdentityAssertion;
    return nativeIdentityVerified(context) && context.nativeTrustedBuiltin === true && assertion?.extensionId === expectedId &&
        assertion.trustedBuiltin === true && assertion.sourceValue === expectedId &&
        trustedBuiltinSourceTypes.has(assertion.sourceType) &&
        typeof assertion.manifestHash === "string" && assertion.manifestHash === context.manifestHash &&
        typeof assertion.treeHash === "string" && assertion.treeHash === context.packageTreeHash;
}

function runtimeObserverGrantDecision(context, grantResolver) {
    const request = {
        operation: "runtimeObserver",
        ownerExtensionId: context.extensionId,
        capability: "runtime-observer",
        resource: "afterburner.runtime/events"
    };
    if (context.manifest?.id && context.manifest.id !== context.extensionId) {
        return { allowed: false, reason: "manifest-owner-mismatch" };
    }
    if (!manifestDeclaresRuntimeCapability(context.manifest, "runtime-observer")) {
        return { allowed: false, reason: "capability-not-declared" };
    }
    if (!nativeIdentityVerified(context)) {
        return { allowed: false, reason: "native-identity-unverified" };
    }
    const hostDecision = normalizeRuntimeAuthorizationDecision(grantResolver?.(request));
    if (hostDecision?.allowed === false) return hostDecision;
    return { allowed: true, reason: hostDecision?.reason, grantId: hostDecision?.grantId };
}

function assertRuntimeObserverGrant(context, grantResolver) {
    const decision = runtimeObserverGrantDecision(context, grantResolver);
    if (decision.allowed) return decision;
    throw modalAuthorizationError("Runtime observer registration was denied by extension policy.", {
        ownerExtensionId: context.extensionId,
        capability: "runtime-observer",
        resource: "afterburner.runtime/events",
        reason: decision.reason
    });
}

function trustedRuntimeIdentity(context, expectedId = context.extensionId) {
    const manifest = context.rawManifest ?? context.manifest;
    return context.extensionId === expectedId && manifest?.id === expectedId && manifest?.visibility === "builtin" &&
        nativeTrustedBuiltinAssertion(context, expectedId);
}

function declaredModalSurfaceIds(context) {
    const capabilities = new Set(Array.isArray(context.manifest?.capabilities) ? context.manifest.capabilities : []);
    if (!capabilities.has("modal-canvas")) return new Set();
    const ui = context.manifest?.ui;
    if (ui?.protocol === "afterburner.ui" && ui?.revision === 1 && Array.isArray(ui.surfaces)) {
        return new Set(ui.surfaces
            .filter((surface) => surface?.kind === "modal" && typeof surface.id === "string")
            .map((surface) => surface.id));
    }
    return new Set();
}

function modalSurfaceGrantDecision(context, id, grantResolver) {
    if (context.manifest?.id && ownerRuntimeIdentifier(context.manifest.id) !== context.extensionId) {
        return { allowed: false, reason: "manifest-owner-mismatch" };
    }
    if (!nativeIdentityVerified(context)) {
        return { allowed: false, reason: "native-identity-unverified" };
    }
    if (!declaredModalSurfaceIds(context).has(id)) {
        return { allowed: false, reason: "surface-not-declared" };
    }
    const hostDecision = normalizeRuntimeAuthorizationDecision(grantResolver?.({
        operation: "ui.surface.modal",
        ownerExtensionId: context.extensionId,
        surfaceId: id,
        capability: "modal-canvas",
        resource: `afterburner.ui/modal/${id}`
    }));
    if (hostDecision?.allowed === false) return hostDecision;
    return { allowed: true, reason: hostDecision?.reason, grantId: hostDecision?.grantId };
}

function assertModalSurfaceGrant(context, id, grantResolver) {
    const decision = modalSurfaceGrantDecision(context, id, grantResolver);
    if (decision.allowed) return decision;
    const error = new Error(`Modal canvas '${id}' was denied by UI authorization policy.`);
    error.code = "ui.authorizationDenied";
    error.details = { ownerExtensionId: context.extensionId, surfaceId: id, reason: decision.reason };
    throw error;
}

function runtimeExtensionApi(pluginRoot, options = {}) {
    const context = runtimeExtensionContext(pluginRoot, options);
    const grantResolver = runtimeHostGrantResolver(context);
    const ownerOptions = Object.freeze({ ownerExtensionId: context.extensionId });
    const runtimeObserverAllowed = runtimeObserverGrantDecision(context, grantResolver).allowed === true;
    const modalSurfaceIds = declaredModalSurfaceIds(context);
    const modalAllowed = nativeIdentityVerified(context) && modalSurfaceIds.size > 0;
    const scopedRegisterModelPickerAdapter = (adapter) =>
        registerModelPickerAdapter(adapter, options.activationOrder);
    const scopedRegisterAppSourceTransform = (transform) =>
        registerAppSourceTransform(transform, context.extensionId, options.activationOrder);
    const scopedRegisterRuntimeObserver = (definition) => {
        assertRuntimeObserverGrant(context, grantResolver);
        return registerRuntimeObserver(definition, ownerOptions);
    };
    const scopedRegisterModalCanvas = (definition) => {
        const id = validateModalId(definition?.id);
        assertModalSurfaceGrant(context, id, grantResolver);
        return registerModalCanvas({
            ...definition,
            id,
            canvasId: id,
            surfaceId: id,
            ownerExtensionId: context.extensionId
        }, ownerOptions);
    };
    const scopedRegisterSurface = (definition) => {
        if (definition?.kind !== undefined && definition.kind !== "modal") {
            const error = new Error(`Unsupported native UI surface kind '${definition.kind}'.`);
            error.code = "ui.unsupportedSurfaceKind";
            throw error;
        }
        return scopedRegisterModalCanvas(definition);
    };
    const ui = Object.freeze({
        ...modalUI,
        ...(modalAllowed ? {
            registerSurface: scopedRegisterSurface,
            registerModalCanvas: scopedRegisterModalCanvas
        } : {})
    });
    return {
        runtime,
        pluginRoot,
        extensionId: context.extensionId,
        sessionRoute: (() => {
            const value = globalThis.process?.env?.AFTERBURNER_SESSION_ROUTE?.trim();
            return typeof value === "string" && /^[A-Za-z0-9_-]{32,128}$/.test(value) ? value : undefined;
        })(),
        ui,
        registerModelPickerAdapter: scopedRegisterModelPickerAdapter,
        registerAppSourceTransform: scopedRegisterAppSourceTransform,
        registerExternalTaskProvider,
        ...(runtimeObserverAllowed ? {
            registerRuntimeObserver: scopedRegisterRuntimeObserver,
            getRuntimeObserverDiagnostics: () => getRuntimeObserverDiagnostics(ownerOptions)
        } : {}),
        ...(modalAllowed ? {
            registerSurface: scopedRegisterSurface,
            registerModalCanvas: scopedRegisterModalCanvas,
            openModalCanvas: (id, input) => openModalCanvas(id, input, ownerOptions),
            updateModalCanvas: (id, next) => updateModalCanvas(id, next, ownerOptions),
            closeModalCanvas: (id) => closeModalCanvas(id, ownerOptions),
            invokeModalAction: (id, name, input) => invokeModalAction(id, name, input, ownerOptions),
            subscribeModalCanvas: (id, listener) => subscribeModalCanvas(id, listener, ownerOptions),
            getModalDiagnostics: () => getModalDiagnostics(ownerOptions),
            getModalFallback: (id) => getModalFallback(id, ownerOptions)
        } : {}),
        externalTaskSnapshot,
        invokeExternalTask,
        getContextCapabilityOverride: (selectionId) => contextCapabilityOverride(null, selectionId)
    };
}

async function loadRuntimeExtensions() {
    const configPath = join(process.env.COPILOT_HOME ?? join(process.env.USERPROFILE ?? "", ".copilot"), "config.json");
    let config;
    try {
        config = parseJsonc(await readFile(configPath, "utf8"));
    } catch (error) {
        emitRuntimeEvent("extension.configuration.failed", { failureKind: error?.name ?? "Error" });
        if (process.env.COPILOT_RUNTIME_EXTENSION_DEBUG === "1") {
            process.stderr.write(`[runtime-extension-host] config load failed: ${error?.message ?? String(error)}\n`);
        }
        config = {};
    }
    const copilotActivations = [];
    let activationOrder = 0;
    for (const plugin of config.installedPlugins ?? []) {
        if (plugin.enabled !== true || typeof plugin.cache_path !== "string") continue;
        const metadata = {
            extensionId: safeRuntimeIdentifier(plugin.name, "ext"),
            version: typeof plugin.version === "string" ? plugin.version : undefined,
            extensionKind: "copilot-runtime"
        };
        let afterburnerManifest;
        try {
            afterburnerManifest = safeJSONParse(await readFile(join(plugin.cache_path, "afterburner.json"), "utf8"));
        } catch (error) {
            if (error?.code !== "ENOENT") {
                emitRuntimeEvent("extension.discovery.failed", {
                    ...metadata,
                    failureKind: error?.name ?? "Error"
                });
                throw error;
            }
        }
        if (afterburnerManifest) continue;
        const entrypoint = join(plugin.cache_path, "runtime/extension.mjs");
        const extensionActivationOrder = activationOrder++;
        copilotActivations.push(async () => {
            try {
                await access(entrypoint, fsConstants.R_OK);
                emitRuntimeEvent("extension.discovered", metadata);
                const imported = await runTimedRuntimePhase("extension.import", metadata,
                    () => import(pathToFileURL(entrypoint).href));
                if (typeof imported.result.activate !== "function") throw new Error("runtime/extension.mjs must export activate().");
                const activated = await runTimedRuntimePhase("extension.activation", metadata,
                    () => imported.result.activate(runtimeExtensionApi(plugin.cache_path, {
                        ...metadata,
                        activationOrder: extensionActivationOrder,
                        manifest: afterburnerManifest
                    })));
                emitRuntimeEvent("extension.activated", { ...metadata, durationMs: activated.durationMs });
                if (process.env.COPILOT_RUNTIME_EXTENSION_DEBUG === "1") {
                    process.stderr.write(`[runtime-extension-host] activated '${plugin.name}' in ${activated.durationMs}ms\n`);
                }
            } catch (error) {
                if (error?.code !== "ENOENT") {
                    emitRuntimeEvent("extension.failed", {
                        ...metadata,
                        failureKind: error?.name ?? "Error"
                    });
                    process.stderr.write(`Warning: runtime extension '${plugin.name}' failed: ${error?.message ?? String(error)}\n`);
                }
            }
        });
    }
    const registryPath = join(process.env.AFTERBURNER_HOME ?? join(process.env.USERPROFILE ?? "", ".afterburner"),
        "registry.json");
    let registry;
    try {
        registry = safeJSONParse(await readFile(registryPath, "utf8"));
    } catch (error) {
        if (error?.code !== "ENOENT") {
            emitRuntimeEvent("extension.failed", {
                extensionKind: "afterburner-registry",
                failureKind: error?.name ?? "Error"
            });
            process.stderr.write(`Warning: Afterburner extension registry failed: ${error?.message ?? String(error)}\n`);
        }
        registry = { extensions: {} };
    }
    const disabledExtensions = new Set((process.env.AFTERBURNER_DISABLED_EXTENSIONS ?? "")
        .split(",").map(value => value.trim()).filter(Boolean));
    const blockingAfterburnerActivations = [];
    const deferredAfterburnerActivations = [];
    for (const [id, entry] of Object.entries(registry.extensions ?? {})) {
        if (disabledExtensions.has("*") || disabledExtensions.has(id)) continue;
        if (entry?.enabled !== true || typeof entry.activePath !== "string") continue;
        const extensionActivationOrder = activationOrder++;
        const assertion = nativeIdentityAssertionFor(id, entry.activePath);
        let metadata = {
            extensionId: safeRuntimeIdentifier(id, "ext"),
            extensionKind: "afterburner"
        };
        try {
            const manifestData = await readFile(join(entry.activePath, "afterburner.json"), "utf8");
            const manifest = safeJSONParse(manifestData);
            const immutableIdentity = immutableNativeRuntimeExtension(assertion, manifest);
            const verified = await runTimedRuntimePhase("extension.identity", metadata,
                () => verifyRuntimePackageIdentity(entry.activePath, manifestData, assertion));
            const verifiedIdentity = verified.result;
            const identityMetadata = {
                manifestHash: verifiedIdentity.manifestHash,
                packageTreeHash: verifiedIdentity.treeHash,
                registrySource: { type: assertion.sourceType, value: assertion.sourceValue },
                nativeIdentityAssertion: assertion
            };
            metadata = {
                ...metadata,
                version: typeof manifest.version === "string" ? manifest.version : undefined
            };
            const activationLane = immutableIdentity && !runtimeExtensionBlocksAppImport(manifest)
                ? deferredAfterburnerActivations
                : blockingAfterburnerActivations;
            activationLane.push(async () => {
                try {
                emitRuntimeEvent("extension.discovered", metadata);
                const entrypoint = join(entry.activePath, manifest.runtime.entrypoint);
                const imported = await runTimedRuntimePhase("extension.import", metadata,
                    () => import(pathToFileURL(entrypoint).href));
                if (typeof imported.result.activate !== "function")
                    throw new Error(`Afterburner extension '${id}' must export activate().`);
                const activated = await runTimedRuntimePhase("extension.activation", metadata,
                    () => imported.result.activate(runtimeExtensionApi(entry.activePath, {
                        ...metadata,
                        ...identityMetadata,
                        activationOrder: extensionActivationOrder,
                        manifest
                    })));
                emitRuntimeEvent("extension.activated", { ...metadata, durationMs: activated.durationMs });
                if (process.env.COPILOT_RUNTIME_EXTENSION_DEBUG === "1")
                    process.stderr.write(`[runtime-extension-host] activated Afterburner extension '${id}' in ${activated.durationMs}ms\n`);
                } catch (error) {
                    emitRuntimeEvent("extension.failed", {
                        ...metadata,
                        failureKind: error?.name ?? "Error"
                    });
                    process.stderr.write(`Warning: Afterburner extension '${id}' failed: ${error?.message ?? String(error)}\n`);
                }
            });
        } catch (error) {
            emitRuntimeEvent("extension.failed", {
                ...metadata,
                failureKind: error?.name ?? "Error"
            });
            process.stderr.write(`Warning: Afterburner extension '${id}' failed: ${error?.message ?? String(error)}\n`);
        }
    }
    const { deferredCompletion } = await activateRuntimeExtensionGroups(
        copilotActivations, blockingAfterburnerActivations, deferredAfterburnerActivations);
    appSourceTransforms.sort((left, right) => left.activationOrder - right.activationOrder);
    return { deferredCompletion };
}

async function transformedAppPath() {
    if (appSourceTransforms.length === 0) return originalAppPath;
    let source = await readFile(originalAppPath, "utf8");
    for (let index = 0; index < appSourceTransforms.length; index++) {
        const { transform, extensionId } = appSourceTransforms[index];
        const transformed = await runTimedRuntimePhase("app.transform", {
            extensionId,
            transformIndex: index
        }, async () => {
            const result = await transform(source, { copilotRoot, originalAppPath });
            if (typeof result !== "string") {
                throw new Error("An app source transform returned a non-string value.");
            }
            return result;
        });
        source = transformed.result;
    }
    const outputPath = join(copilotRoot, ".afterburner-app.mjs");
    const cacheHit = !(await writeRuntimeOutputIfChanged(outputPath, source));
    emitRuntimeEvent("app.transform.output", {
        transformCount: appSourceTransforms.length,
        cacheHit
    });
    return outputPath;
}

async function writeRuntimeOutputIfChanged(outputPath, source) {
    let existingSource;
    try {
        existingSource = await readFile(outputPath, "utf8");
    } catch (error) {
        if (error?.code !== "ENOENT") throw error;
    }
    if (existingSource === source) return false;
    await writeFile(outputPath, source, "utf8");
    return true;
}

installRuntimeObserverSeams();
installPickerBridge();
const { deferredCompletion: deferredRuntimeActivations } = await loadRuntimeExtensions();
if (process.env.COPILOT_RUNTIME_EXTENSION_SELF_TEST === "1") await deferredRuntimeActivations;
if (deferredRuntimeActivations) {
    void deferredRuntimeActivations.then(
        () => { runtimeBootstrapSealed = true; },
        () => { runtimeBootstrapSealed = true; }
    );
} else {
    runtimeBootstrapSealed = true;
}
const runtimeBootstrapLastSequence = runtimeEventSequence;
process.once("exit", disposeRuntimeObservers);
if (process.env.COPILOT_RUNTIME_EXTENSION_SELF_TEST === "1") {
    const observerEvents = [];
    let observerDisposeCount = 0;
    const unregisterSelfTestObserver = registerRuntimeObserver({
        id: "afterburner-self-test",
        onEvent: (event) => {
            observerEvents.push(event);
            if (!Object.isFrozen(event) || !Object.isFrozen(event.metadata)) {
                throw new Error("Runtime observer received a mutable event.");
            }
        },
        dispose: () => { observerDisposeCount++; }
    });
    const unregisterFailingObserver = registerRuntimeObserver({
        id: "afterburner-self-test-failure",
        eventTypes: ["runtime.self_test.probe"],
        onEvent: () => { throw new Error("Expected observer isolation probe."); }
    });
    let copiedProbeEvent;
    const unregisterCopyObserver = registerRuntimeObserver({
        id: "afterburner-self-test-copy",
        eventTypes: ["runtime.self_test.probe"],
        onEvent: (event) => { copiedProbeEvent = event; }
    });
    const rows = pickerAdapters.flatMap((adapter) =>
        (adapter.selectionIds?.() ?? []).map((selectionId) => ({
            value: selectionId,
            label: selectionId,
            source: "custom",
            availability: "available",
            effort: metadataFor(selectionId)?.defaultReasoningEffort ?? "medium"
        }))
    );
    const contextCycles = {};
    const capabilityOverrides = {};
    for (const adapter of pickerAdapters) {
        for (const selectionId of adapter.selectionIds?.() ?? []) {
            const metadata = metadataFor(selectionId);
            contextCycles[selectionId] = metadata.contextWindowOptions.map((contextWindowTokens) => {
                const projection = runtime.modelProjectInlineTable({
                    rows: [{
                        value: selectionId,
                        label: selectionId,
                        source: "custom",
                        availability: "available",
                        effort: metadata.defaultReasoningEffort,
                        contextWindowTokens
                    }],
                    screenReaderEnabled: false,
                    costHeader: "Cost"
                });
                return projection.rows[0];
            });
            capabilityOverrides[selectionId] = metadata.contextWindowOptions.map((contextWindowTokens) => {
                contextOverrides.set(contextOverrideKey("self-test", selectionId), contextWindowTokens);
                return contextCapabilityOverride("self-test", selectionId);
            });
        }
    }
    const projection = runtime.modelProjectInlineTable({ rows, screenReaderEnabled: false, costHeader: "Cost" });
    const nativeContextNavigation = nativeContextTiers.map((contextTier) =>
        withBidirectionalNativeContext(
            {
                rowKey: `native/model::${contextTier}`,
                contextToggleable: true,
                nextContextTier: contextTier === "default" ? "long_context" : "default"
            },
            { value: "native/model", contextTier }
        )
    );
    const externalTasks = await externalTaskSnapshot();
    const externalTaskRead = externalTasks.length > 0
        ? await invokeExternalTask(externalTasks[0].nativeId, "read")
        : null;
    await flushRuntimeObservers();
    let releaseBackpressure;
    const backpressure = new Promise((resolve) => { releaseBackpressure = resolve; });
    const unregisterBackpressureObserver = registerRuntimeObserver({
        id: "afterburner-self-test-backpressure",
        eventTypes: ["runtime.self_test.backpressure"],
        onEvent: () => backpressure
    });
    for (let index = 0; index < runtimeObserverQueueLimit + 16; index++) {
        emitRuntimeEvent("runtime.self_test.backpressure", { index });
    }
    releaseBackpressure();
    await flushRuntimeObservers();
    emitRuntimeEvent("runtime.self_test.probe", {
        modelId: "self-test/model",
        state: "ready",
        prompt: "must-not-be-observed"
    });
    await flushRuntimeObservers();
    const observerDiagnostics = getRuntimeObserverDiagnostics();
    const observerProbeEvent = observerEvents.find((event) => event.type === "runtime.self_test.probe");
    const observerProbe = {
        immutable: observerEvents.every((event) => Object.isFrozen(event) && Object.isFrozen(event.metadata)),
        independentCopies: observerProbeEvent !== undefined && copiedProbeEvent !== undefined &&
            observerProbeEvent !== copiedProbeEvent && observerProbeEvent.metadata !== copiedProbeEvent.metadata,
        monotonic: observerEvents.every((event, index) => index === 0 || event.sequence > observerEvents[index - 1].sequence),
        sensitiveMetadataRemoved: observerProbeEvent !== undefined && !("prompt" in observerProbeEvent.metadata),
        bootstrapReplayCount: observerEvents.filter((event) =>
            event.sequence <= runtimeBootstrapLastSequence).length
    };
    let modalSubscriberEvents = 0;
    let modalActionCount = 0;
    const savedModalPipe = process.env.AFTERBURNER_MODAL_PIPE;
    const savedModalConfig = modalBrokerConfig;
    delete process.env.AFTERBURNER_MODAL_PIPE;
    modalBrokerConfig = Object.freeze({});
    const fallbackProbe = registerModalCanvas({
        id: "afterburner-self-test-fallback",
        displayName: "Afterburner self-test fallback",
        open: async () => ({ body: "fallback body" })
    });
    let updateBeforeOpenRejected = false;
    try { await fallbackProbe.update({ body: "should not open" }); }
    catch { updateBeforeOpenRejected = true; }
    const fallbackOpen = await fallbackProbe.open();
    const fallbackSnapshot = fallbackProbe.fallback();
    await fallbackProbe.close();
    await fallbackProbe.dispose();
    if (savedModalPipe === undefined) delete process.env.AFTERBURNER_MODAL_PIPE;
    else process.env.AFTERBURNER_MODAL_PIPE = savedModalPipe;
    modalBrokerConfig = savedModalConfig;
    const brokerAvailable = modalHasBroker();
    const selfTestOwnerOptions = { ownerExtensionId: "afterburner-self-test" };
    const modalHandle = registerModalCanvas({
        id: "afterburner-self-test-modal",
        displayName: "Afterburner self-test modal",
        actions: [{
            name: "refresh",
            label: "Refresh",
            handler: async (_input, controls) => {
                modalActionCount++;
                await controls.update({ body: "updated from action" });
                return { ok: true };
            }
        }],
        open: async () => ({ body: "opened" })
    }, selfTestOwnerOptions);
    const unsubscribeModal = modalHandle.subscribe(() => { modalSubscriberEvents++; });
    const modalOpen = await modalHandle.open();
    const modalUpdate = await modalHandle.update({ body: "updated" });
    const modalAction = await modalHandle.invoke("refresh", {});
    const modalClose = await modalHandle.close();
    unsubscribeModal();
    const modal = {
        fallbackAPIOK: fallbackOpen.fallback === true && typeof fallbackOpen.text === "string" &&
            fallbackOpen.text.includes("fallback body") && fallbackSnapshot?.text === fallbackOpen.text,
        brokerExpected: brokerAvailable,
        brokerTransportOK: brokerAvailable && modalOpen.ok === true && modalUpdate.ok === true &&
            modalClose.ok === true && modalOpen.fallback !== true && modalUpdate.fallback !== true,
        updateBeforeOpenRejected,
        openFallback: modalOpen.fallback === true,
        actionOK: modalAction?.ok === true && modalActionCount === 1,
        subscriberEvents: modalSubscriberEvents,
        diagnostics: getModalDiagnostics()
    };
    await modalHandle.dispose();
    unregisterBackpressureObserver();
    unregisterCopyObserver();
    unregisterFailingObserver();
    unregisterSelfTestObserver();
    const disposedSelfTestObserver = unregisterSelfTestObserver.diagnostics();
    process.stdout.write(`${JSON.stringify({
        projection,
        contextCycles,
        capabilityOverrides,
        nativeContextNavigation,
        externalTasks,
        externalTaskRevision,
        externalTaskRead,
        runtimeObservers: {
            events: observerEvents
                .filter((event) => event.type !== "runtime.self_test.backpressure")
                .slice(-32),
            diagnostics: observerDiagnostics,
            probe: observerProbe,
            disposedSelfTestObserver,
            disposeCount: observerDisposeCount
        },
        modal
    }, null, 2)}\n`);
    process.exit(0);
}

Object.defineProperty(globalThis, "__copilotRuntimeAddon__", {
    configurable: false,
    enumerable: false,
    writable: false,
    value: {
        addon: runtime,
        processStateInitialized: false,
        diagnostics: Object.freeze({
            getRuntimeObserverDiagnostics: () => getRuntimeObserverDiagnostics(),
            getModalDiagnostics: () => getModalDiagnostics()
        })
    }
});
const transformedPath = await transformedAppPath();
await runTimedRuntimePhase("app.import", { transformed: transformedPath !== originalAppPath },
    () => import(pathToFileURL(transformedPath).href));
