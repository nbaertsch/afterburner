import { createRequire } from "node:module";
import { access, readFile, readdir, writeFile } from "node:fs/promises";
import { constants as fsConstants } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const require = createRequire(import.meta.url);
const { createHash } = require("node:crypto");
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
    runtimeObservers.delete(observer.id);
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
    emitRuntimeEvent("runtime.observer.disposed", { observerId: observer.id });
}

function registerRuntimeObserver(definition) {
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
    if (runtimeObservers.has(id)) throw new Error(`Runtime observer '${id}' is already registered.`);
    const observer = {
        id,
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
    runtimeObservers.set(id, observer);
    for (const event of runtimeBootstrapEvents) enqueueRuntimeObserver(observer, event);
    emitRuntimeEvent("runtime.observer.registered", {
        observerId: id,
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

function getRuntimeObserverDiagnostics() {
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
        observers: [...runtimeObservers.values()].map(runtimeObserverDiagnostics)
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

// uiModRegistry backs the framework-owned "mod point" layer described in
// docs/ui-mods.md. Extension authors register semantic UI intent (e.g. "add
// a badge next to this model row") instead of hand-writing minified-bundle
// string surgery; Afterburner owns the profile-specific anchors and applies
// them centrally. registerAppSourceTransform remains available as a raw,
// unstable, first-party-only escape hatch for capabilities the framework
// does not yet generalize (e.g. BYOModels' context-arrow controls).
const uiModRegistry = {
    modelPickerRowDecorators: []
};
const uiModDiagnostics = {
    schemaVersion: 1,
    modPoints: {}
};

// modelPickerRowRendererByProfile maps each known compatibility profile ID to
// the exact minified function name Copilot's model picker uses to render a
// row's context cell, plus the exact minified "row identity map" builder
// anchor for that same profile. These names are already validated by
// BYOModels' own passing tests (extensions/BYOModels/runtime/extension.mjs),
// which patch the identical renderer functions for the context-arrow
// feature; the row-decorator mod point reuses the same verified renderer
// anchors rather than introducing new, unverified ones. The row-identity map
// anchor is a second, independently verified anchor (confirmed against the
// real installed Copilot bundles for all three profiles) that builds a
// Map<rowKey, originalRow> local to the model picker component; patching its
// construction lets the row-decorator mod point recover the original
// selection id (originalRow.value) from the opaque rendered row object (e),
// which itself only carries e.rowKey, not the raw selection id.
const modelPickerRowRendererByProfile = {
    "copilot-1.0.83-1-win32-x64": {
        rendererName: "vzr",
        rowIdentityMapAnchor: "j=(0,Kn.useMemo)(()=>{let Ue=new Map;for(let nt of r){let ut=ne.get(nt.value);ut&&Ue.set(ut.rowKey,nt)}return Ue},[r,ne])"
    },
    "copilot-1.0.83-2-win32-x64": {
        rendererName: "Wzr",
        rowIdentityMapAnchor: "j=(0,Vn.useMemo)(()=>{let Ue=new Map;for(let nt of r){let ut=ne.get(nt.value);ut&&Ue.set(ut.rowKey,nt)}return Ue},[r,ne])"
    },
    "copilot-1.0.83-3-win32-x64": {
        rendererName: "U6r",
        rowIdentityMapAnchor: "V=(0,Wn.useMemo)(()=>{let Le=new Map;for(let tt of r){let Ct=re.get(tt.value);Ct&&Le.set(Ct.rowKey,tt)}return Le},[r,re])"
    }
};

function currentCompatibilityProfileID() {
    return process.env.AFTERBURNER_COMPATIBILITY_PROFILE ?? "";
}

// registerModelPickerRowDecorator lets an extension attach short badge text
// after a model picker row's context cell (e.g. " 🔧 BYO"). Decorators are
// applied in registration order; each decorator receives a frozen descriptor
// containing the row's selectionId (matching registerModelPickerAdapter's
// selectionIds()/selection ids elsewhere in this API) and must return either
// a string or null/undefined (no badge). Decorator exceptions are isolated
// per-decorator and never abort rendering for other decorators or crash
// Copilot.
function registerModelPickerRowDecorator(decorator) {
    if (!decorator || typeof decorator.id !== "string" || decorator.id.trim() === "") {
        throw new Error("A model picker row decorator must define a non-empty string id.");
    }
    if (typeof decorator.render !== "function") {
        throw new Error(`Model picker row decorator "${decorator.id}" must define render().`);
    }
    if (uiModRegistry.modelPickerRowDecorators.some((existing) => existing.id === decorator.id)) {
        throw new Error(`Model picker row decorator id "${decorator.id}" is already registered.`);
    }
    uiModRegistry.modelPickerRowDecorators.push(decorator);
    emitRuntimeEvent("ui.mod.registered", {
        modPoint: "model-picker.row-decorator.v1",
        modId: decorator.id
    });
}

// getUiModDiagnostics reports, per known mod point, whether the current
// Copilot profile is supported and which decorators are registered. This is
// the framework's introspection surface for `afterburn doctor`-style tooling
// and for extension authors debugging why a mod point is unavailable.
function getUiModDiagnostics() {
    const profileID = currentCompatibilityProfileID();
    const profile = modelPickerRowRendererByProfile[profileID];
    return {
        schemaVersion: 1,
        profileId: profileID || null,
        modPoints: {
            "model-picker.row-decorator.v1": {
                available: Boolean(profile),
                reason: profile ? undefined : "profile-does-not-declare-mod-point",
                registeredMods: uiModRegistry.modelPickerRowDecorators.map((decorator) => decorator.id)
            }
        }
    };
}

// applyModelPickerRowDecorators patches the profile's already-verified model
// picker row renderer function, plus the row-identity map builder, to
// additionally resolve each rendered row's real selectionId and call into
// registered decorators, appending their returned text to the rendered
// context cell. It fails closed: if the current Copilot package does not
// match a known profile, or either expected anchor is not found in the
// bundle (exactly once each), no patch is applied and the source is
// returned unchanged -- decorators simply become unavailable rather than
// corrupting the UI.
function applyModelPickerRowDecorators(source) {
    if (uiModRegistry.modelPickerRowDecorators.length === 0) return source;
    const profileID = currentCompatibilityProfileID();
    const profile = modelPickerRowRendererByProfile[profileID];
    if (!profile) {
        if (process.env.COPILOT_RUNTIME_EXTENSION_DEBUG === "1") {
            process.stderr.write(`[runtime-extension-host] model-picker.row-decorator.v1 unavailable for profile "${profileID}"\n`);
        }
        return source;
    }
    const rendererAnchor = `function ${profile.rendererName}(e,t,n){return`;
    const rendererOccurrences = source.split(rendererAnchor).length - 1;
    const identityOccurrences = source.split(profile.rowIdentityMapAnchor).length - 1;
    if (rendererOccurrences !== 1 || identityOccurrences !== 1) {
        if (process.env.COPILOT_RUNTIME_EXTENSION_DEBUG === "1") {
            process.stderr.write(`[runtime-extension-host] model-picker.row-decorator.v1 anchor mismatch (renderer=${rendererOccurrences}, identity=${identityOccurrences}) for profile "${profileID}"\n`);
        }
        return source;
    }
    const identityBridge = "__afterburnerModelPickerRowIdentity";
    const renderBridge = "__afterburnerModelPickerRowDecorators";
    globalThis[identityBridge] = new Map();
    globalThis[renderBridge] = (row) => {
        const selectionId = globalThis[identityBridge].get(row.rowKey);
        const parts = [];
        for (const decorator of uiModRegistry.modelPickerRowDecorators) {
            try {
                const text = decorator.render(Object.freeze({ selectionId: selectionId ?? null }));
                if (typeof text === "string" && text.length > 0) parts.push(text);
            } catch (error) {
                reportInstrumentationFailure(`modelPickerRowDecorator:${decorator.id}`, error);
            }
        }
        return parts.join("");
    };
    // Rewrite the identity-map builder so each entry is also mirrored into
    // globalThis.__afterburnerModelPickerRowIdentity as rowKey -> selectionId,
    // giving the renderer patch below a way to recover the true selection id
    // from the opaque rendered row object (which only carries .rowKey).
    const identityReplacement = profile.rowIdentityMapAnchor.replace(
        /\.set\(([A-Za-z0-9_]+)\.rowKey,\s*([A-Za-z0-9_]+)\)/,
        (match, mapRowVar, originalRowVar) =>
            `.set(${mapRowVar}.rowKey,${originalRowVar}),globalThis.${identityBridge}.set(${mapRowVar}.rowKey,${originalRowVar}.value)`
    );
    if (identityReplacement === profile.rowIdentityMapAnchor) {
        // Defensive: the regex should always match the known anchor shape;
        // if it somehow does not, fail closed rather than silently skipping
        // identity tracking (which would make decorators see null selectionId).
        if (process.env.COPILOT_RUNTIME_EXTENSION_DEBUG === "1") {
            process.stderr.write(`[runtime-extension-host] model-picker.row-decorator.v1 identity rewrite failed for profile "${profileID}"\n`);
        }
        return source;
    }
    const rendererReplacement = `function ${profile.rendererName}(e,t,n){e={...e,contextCellText:e.contextCellText+(globalThis.${renderBridge}?.(e)??"")};return`;
    return source
        .replace(profile.rowIdentityMapAnchor, identityReplacement)
        .replace(rendererAnchor, rendererReplacement);
}


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

function registerModelPickerAdapter(adapter) {
    if (!adapter || typeof adapter.matches !== "function" || typeof adapter.upstreamModelId !== "function") {
        throw new Error("A model picker adapter must define matches() and upstreamModelId().");
    }
    pickerAdapters.push(adapter);
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

function registerAppSourceTransform(transform) {
    if (typeof transform !== "function") {
        throw new Error("An app source transform must be a function.");
    }
    appSourceTransforms.push(transform);
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
    return JSON.parse(output.replace(/,\s*([}\]])/g, "$1"));
}

function runtimeExtensionApi(pluginRoot) {
    return {
        runtime,
        pluginRoot,
        registerModelPickerAdapter,
        registerAppSourceTransform,
        registerModelPickerRowDecorator,
        getUiModDiagnostics,
        registerExternalTaskProvider,
        registerRuntimeObserver,
        getRuntimeObserverDiagnostics,
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
    for (const plugin of config.installedPlugins ?? []) {
        if (plugin.enabled !== true || typeof plugin.cache_path !== "string") continue;
        const metadata = {
            extensionId: safeRuntimeIdentifier(plugin.name, "ext"),
            version: typeof plugin.version === "string" ? plugin.version : undefined,
            extensionKind: "copilot-runtime"
        };
        let afterburnerManifest;
        try {
            afterburnerManifest = JSON.parse(await readFile(join(plugin.cache_path, "afterburner.json"), "utf8"));
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
        try {
            await access(entrypoint, fsConstants.R_OK);
            emitRuntimeEvent("extension.discovered", metadata);
            emitRuntimeEvent("extension.activation.started", metadata);
            const module = await import(pathToFileURL(entrypoint).href);
            if (typeof module.activate !== "function") throw new Error("runtime/extension.mjs must export activate().");
            await module.activate(runtimeExtensionApi(plugin.cache_path));
            emitRuntimeEvent("extension.activated", metadata);
            if (process.env.COPILOT_RUNTIME_EXTENSION_DEBUG === "1") {
                process.stderr.write(`[runtime-extension-host] activated '${plugin.name}'\n`);
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
    }
    const registryPath = join(process.env.AFTERBURNER_HOME ?? join(process.env.USERPROFILE ?? "", ".afterburner"),
        "registry.json");
    let registry;
    try {
        registry = JSON.parse(await readFile(registryPath, "utf8"));
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
    for (const [id, entry] of Object.entries(registry.extensions ?? {})) {
        if (disabledExtensions.has("*") || disabledExtensions.has(id)) continue;
        if (entry?.enabled !== true || typeof entry.activePath !== "string") continue;
        let metadata = {
            extensionId: safeRuntimeIdentifier(id, "ext"),
            extensionKind: "afterburner"
        };
        try {
            const manifest = JSON.parse(await readFile(join(entry.activePath, "afterburner.json"), "utf8"));
            metadata = {
                ...metadata,
                version: typeof manifest.version === "string" ? manifest.version : undefined
            };
            emitRuntimeEvent("extension.discovered", metadata);
            emitRuntimeEvent("extension.activation.started", metadata);
            const entrypoint = join(entry.activePath, manifest.runtime.entrypoint);
            const module = await import(pathToFileURL(entrypoint).href);
            if (typeof module.activate !== "function")
                throw new Error(`Afterburner extension '${id}' must export activate().`);
            await module.activate(runtimeExtensionApi(entry.activePath));
            emitRuntimeEvent("extension.activated", metadata);
            if (process.env.COPILOT_RUNTIME_EXTENSION_DEBUG === "1")
                process.stderr.write(`[runtime-extension-host] activated Afterburner extension '${id}'\n`);
        } catch (error) {
            emitRuntimeEvent("extension.failed", {
                ...metadata,
                failureKind: error?.name ?? "Error"
            });
            process.stderr.write(`Warning: Afterburner extension '${id}' failed: ${error?.message ?? String(error)}\n`);
        }
    }
}

async function transformedAppPath() {
    if (appSourceTransforms.length === 0 && uiModRegistry.modelPickerRowDecorators.length === 0) return originalAppPath;
    let source = await readFile(originalAppPath, "utf8");
    source = applyModelPickerRowDecorators(source);
    for (const transform of appSourceTransforms) {
        source = await transform(source, { copilotRoot, originalAppPath });
        if (typeof source !== "string") {
            throw new Error("An app source transform returned a non-string value.");
        }
    }
    const outputPath = join(copilotRoot, ".afterburner-app.mjs");
    await writeFile(outputPath, source, "utf8");
    return outputPath;
}

installRuntimeObserverSeams();
installPickerBridge();
await loadRuntimeExtensions();
runtimeBootstrapSealed = true;
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
        }
    }, null, 2)}\n`);
    process.exit(0);
}globalThis.__copilotRuntimeAddon__ = {
    addon: runtime,
    processStateInitialized: false,
    registerRuntimeObserver,
    getRuntimeObserverDiagnostics
};
await import(pathToFileURL(await transformedAppPath()).href);
