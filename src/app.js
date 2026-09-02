import { createRequire } from "node:module";
import { access, readFile, readdir } from "node:fs/promises";
import { constants as fsConstants } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const require = createRequire(import.meta.url);
const wrapperDir = dirname(fileURLToPath(import.meta.url));
const packageRoot = resolve(wrapperDir, "..");
const wrapperPackageName = wrapperDir.split(/[\\/]/).at(-1);

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

async function findOriginalPackage() {
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
const upstreamMetadata = new Map();
const effortOverrides = new Map();
const contextOverrides = new Map();

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
            const snapshot = () => {
                const projection = inner.snapshot();
                return {
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
            };
            return new Proxy(inner, {
                get(target, property, receiver) {
                    if (property === "snapshot") return snapshot;
                    if (property === "setReasoningEffort") {
                        return (selectionId, effort) => {
                            if (!adapterFor(selectionId)) return target.setReasoningEffort(selectionId, effort);
                            effortOverrides.set(selectionId, effort);
                            return snapshot();
                        };
                    }
                    if (property === "setContextTier") {
                        return (selectionId, contextWindowTokens) => {
                            if (!adapterFor(selectionId)) {
                                return target.setContextTier(selectionId, contextWindowTokens);
                            }
                            contextOverrides.set(
                                contextOverrideKey(sessionId, selectionId),
                                Number(contextWindowTokens)
                            );
                            return snapshot();
                        };
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
        let augmented = false;
        const rowsById = new Map((input.rows ?? []).map((row) => [row.value, row]));
        const rows = result.rows.map((row) => {
            const source = rowsById.get(row.rowKey.split("::", 1)[0]);
            const metadata = metadataFor(source?.value);
            if (!metadata) return row;
            augmented = true;
            const efforts = metadata.supportedReasoningEfforts;
            const effort = source.effort ?? metadata.defaultReasoningEffort;
            const effortIndex = efforts.indexOf(effort);
            const contextWindowTokens = source.contextWindowTokens ?? metadata.maxContextWindowTokens;
            const contextOptions = metadata.contextWindowOptions;
            const contextIndex = contextOptions.indexOf(contextWindowTokens);
            const nextContextWindow = contextOptions.length > 1
                ? contextOptions[(contextIndex + 1) % contextOptions.length]
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
                nextContextTier: nextContextWindow === undefined ? undefined : String(nextContextWindow),
                contextCellText: contextText,
                contextSegments
            };
        });
        return augmented ? { ...result, rows, hasReasoningColumn: true, hasContextColumn: true } : result;
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
        return originalModelSwitchTo(sessionId, typeof request === "string" ? JSON.stringify(enriched) : enriched);
    };
}

function registerModelPickerAdapter(adapter) {
    if (!adapter || typeof adapter.matches !== "function" || typeof adapter.upstreamModelId !== "function") {
        throw new Error("A model picker adapter must define matches() and upstreamModelId().");
    }
    pickerAdapters.push(adapter);
    if (process.env.COPILOT_RUNTIME_EXTENSION_DEBUG === "1") {
        process.stderr.write(`[runtime-extension-host] registered picker adapter ${pickerAdapters.length}\n`);
    }
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

async function loadRuntimeExtensions() {
    const configPath = join(process.env.USERPROFILE ?? "", ".copilot", "config.json");
    let config;
    try {
        config = parseJsonc(await readFile(configPath, "utf8"));
    } catch (error) {
        if (process.env.COPILOT_RUNTIME_EXTENSION_DEBUG === "1") {
            process.stderr.write(`[runtime-extension-host] config load failed: ${error?.message ?? String(error)}\n`);
        }
        return;
    }
    for (const plugin of config.installedPlugins ?? []) {
        if (plugin.enabled !== true || typeof plugin.cache_path !== "string") continue;
        const entrypoint = join(plugin.cache_path, "runtime", "extension.mjs");
        try {
            await access(entrypoint, fsConstants.R_OK);
            const module = await import(pathToFileURL(entrypoint).href);
            if (typeof module.activate !== "function") throw new Error("runtime/extension.mjs must export activate().");
            await module.activate({
                runtime,
                pluginRoot: plugin.cache_path,
                registerModelPickerAdapter,
                getContextCapabilityOverride: (selectionId) =>
                    contextCapabilityOverride(null, selectionId)
            });
            if (process.env.COPILOT_RUNTIME_EXTENSION_DEBUG === "1") {
                process.stderr.write(`[runtime-extension-host] activated '${plugin.name}'\n`);
            }
        } catch (error) {
            if (error?.code !== "ENOENT") {
                process.stderr.write(`Warning: runtime extension '${plugin.name}' failed: ${error?.message ?? String(error)}\n`);
            }
        }
    }
}

installPickerBridge();
await loadRuntimeExtensions();
if (process.env.COPILOT_RUNTIME_EXTENSION_SELF_TEST === "1") {
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
    process.stdout.write(`${JSON.stringify({ projection, contextCycles, capabilityOverrides }, null, 2)}\n`);
    process.exit(0);
}globalThis.__copilotRuntimeAddon__ = { addon: runtime, processStateInitialized: false };
await import(pathToFileURL(originalAppPath).href);
