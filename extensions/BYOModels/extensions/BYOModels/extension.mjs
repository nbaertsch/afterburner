import { execFile } from "node:child_process";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import { dirname, join } from "node:path";
import { promisify } from "node:util";
import { createCanvas } from "@github/copilot-sdk";
import { joinSession } from "@github/copilot-sdk/extension";
import { activateModelRegistration } from "./activation.mjs";
import {
    capabilityCache,
    configuredRegistration,
    withDeadline
} from "./model-metadata.mjs";
import { proxyConfiguration, startRequestCompatibilityProxy } from "./request-compatibility.mjs";
import { buildRuntimeMetadata } from "./runtime-metadata.mjs";

const execFileAsync = promisify(execFile);
const rpcTimeoutMs = 10_000;
const sessionTimeoutMs = 30_000;
const azureCliTimeoutMs = 30_000;
const refreshTimeoutMs = 120_000;
const afterburnerHome = process.env.AFTERBURNER_HOME ??
    join(process.env.USERPROFILE ?? "", ".afterburner");
const configuredPath = process.env.AFTERBURNER_BYOMODELS_CONFIG?.trim() ||
    join(afterburnerHome, "config", "byomodels.json");
try {
    await readFile(configuredPath, "utf8");
} catch (error) {
    if (error?.code !== "ENOENT") throw error;
    throw new Error(
        `BYOModels configuration was not found at '${configuredPath}'. ` +
        "Create ~/.afterburner/config/byomodels.json or set AFTERBURNER_BYOMODELS_CONFIG."
    );
}
const configPath = new URL(`file:///${configuredPath.replace(/\\/g, "/")}`);
const providerHeaderNamePattern = /^[!#$%&'*+\-.^_`|~0-9A-Za-z]+$/;
const proxyManagedHeaders = new Set([
    "accept-encoding",
    "authorization",
    "connection",
    "content-length",
    "host",
    "keep-alive",
    "proxy-authenticate",
    "proxy-authorization",
    "te",
    "trailer",
    "transfer-encoding",
    "upgrade",
    "x-afterburner-proxy-capability"
]);
const runtimeMetadataPath = join(
    process.env.COPILOT_HOME ?? join(process.env.USERPROFILE ?? "", ".copilot"),
    "runtime-extension-data",
    "afterburner-byomodels",
    "model-metadata.json"
);
const config = JSON.parse(await readFile(configPath, "utf8"));

if (config.version !== 1) {
    throw new Error(`Unsupported BYOModels configuration version: ${config.version}`);
}

if (!Array.isArray(config.providers) || !Array.isArray(config.models)) {
    throw new Error("BYOModels configuration must define providers and models arrays.");
}

function validateConfig() {
    const providerNames = new Set();
    for (const provider of config.providers) {
        if (!provider || typeof provider.name !== "string" || !provider.name) {
            throw new Error("Every BYOModels provider must define a non-empty name.");
        }
        if (providerNames.has(provider.name)) {
            throw new Error(`Duplicate BYOModels provider name: ${provider.name}`);
        }
        if (provider.auth?.type === "bearer-token" &&
            (typeof provider.auth.value !== "string" || !provider.auth.value.trim())) {
            throw new Error(`Provider '${provider.name}' must define a non-empty auth.value.`);
        }
        const maximumLength = provider.requestCompatibility?.maxInputItemIdLength;
        if (maximumLength !== undefined &&
            (!Number.isInteger(maximumLength) || maximumLength < 16)) {
            throw new Error(
                `Provider '${provider.name}' must define requestCompatibility.maxInputItemIdLength as an integer of at least 16.`
            );
        }
        const forceStreaming = provider.requestCompatibility?.forceStreaming;
        if (forceStreaming !== undefined && typeof forceStreaming !== "boolean") {
            throw new Error(
                `Provider '${provider.name}' must define requestCompatibility.forceStreaming as a boolean.`
            );
        }
        const legacyTools = provider.requestCompatibility?.legacyTools;
        if (legacyTools !== undefined && typeof legacyTools !== "boolean") {
            throw new Error(
                `Provider '${provider.name}' must define requestCompatibility.legacyTools as a boolean.`
            );
        }
        const bufferResponses = provider.requestCompatibility?.bufferResponses;
        if (bufferResponses !== undefined && typeof bufferResponses !== "boolean") {
            throw new Error(
                `Provider '${provider.name}' must define requestCompatibility.bufferResponses as a boolean.`
            );
        }
        const proxyPort = provider.requestCompatibility?.proxyPort;
        if (proxyPort !== undefined &&
            (!Number.isInteger(proxyPort) || proxyPort < 1024 || proxyPort > 65535)) {
            throw new Error(
                `Provider '${provider.name}' must define requestCompatibility.proxyPort between 1024 and 65535.`
            );
        }
        if (provider.headers !== undefined &&
            (!provider.headers || typeof provider.headers !== "object" ||
                Array.isArray(provider.headers) ||
                Object.entries(provider.headers).some(([name, value]) =>
                    !providerHeaderNamePattern.test(name.trim()) ||
                    typeof value !== "string" ||
                    /[\x00-\x08\x0a-\x1f\x7f]/.test(value)))) {
            throw new Error(
                `Provider '${provider.name}' must define headers as an object of valid HTTP header names and single-line string values.`
            );
        }
        const headerNames = Object.keys(provider.headers ?? {})
            .map((name) => name.trim().toLowerCase());
        if (new Set(headerNames).size !== headerNames.length) {
            throw new Error(`Provider '${provider.name}' defines duplicate case-insensitive header names.`);
        }
        const managedHeader = headerNames.find((name) => proxyManagedHeaders.has(name));
        if (managedHeader) {
            throw new Error(
                `Provider '${provider.name}' cannot configure proxy-managed header '${managedHeader}'.`
            );
        }
        providerNames.add(provider.name);
    }

    const modelIds = new Set();
    for (const model of config.models) {
        if (!model || typeof model.provider !== "string" || typeof model.id !== "string") {
            throw new Error("Every BYOModels model must define provider and id strings.");
        }
        if (!providerNames.has(model.provider)) {
            throw new Error(`Model '${model.id}' references unknown provider '${model.provider}'.`);
        }

        const selectionId = `${model.provider}/${model.id}`;
        if (modelIds.has(selectionId)) {
            throw new Error(`Duplicate BYOModels model selection ID: ${selectionId}`);
        }
        const hasConfiguredCapabilities = ["maxPromptTokens", "maxContextWindowTokens", "maxOutputTokens", "capabilities"]
            .some((field) => Object.hasOwn(model, field));
        if (hasConfiguredCapabilities && !configuredRegistration(model)) {
            throw new Error(
                `Model '${selectionId}' must define complete numeric token limits and boolean vision/reasoning support.`
            );
        }
        modelIds.add(selectionId);
    }
}

validateConfig();

const azureTokenCache = new Map();
const compatibilityProxyRewrites = new Map();

function nativeCompatibilityProxies() {
    const raw = process.env.AFTERBURNER_BYOMODELS_PROXIES;
    delete process.env.AFTERBURNER_BYOMODELS_PROXIES;
    if (!raw) return new Map();
    let parsed;
    try {
        parsed = JSON.parse(raw);
    } catch {
        return new Map();
    }
    const endpoints = new Map();
    if (!Array.isArray(parsed)) return endpoints;
    for (const endpoint of parsed) {
        if (!endpoint || typeof endpoint.provider !== "string" ||
            typeof endpoint.baseUrl !== "string" || typeof endpoint.capability !== "string" ||
            endpoint.capability.length < 32 || typeof endpoint.configuration !== "string") continue;
        try {
            const url = new URL(endpoint.baseUrl);
            if (url.protocol !== "http:" || url.hostname !== "127.0.0.1" || url.username || url.password || url.search || url.hash) continue;
            endpoints.set(endpoint.provider, {
                baseUrl: url.href.replace(/\/$/, ""),
                capability: endpoint.capability,
                configuration: endpoint.configuration
            });
        } catch {}
    }
    return endpoints;
}

const nativeProxyEndpoints = nativeCompatibilityProxies();

function requiredEnvironmentValue(provider, auth, field) {
    const variable = auth.environmentVariable;
    const value = typeof variable === "string" ? process.env[variable]?.trim() : "";
    if (!value) {
        throw new Error(`Provider '${provider.name}' requires environment variable '${variable}' for ${field}.`);
    }
    return value;
}

function requiredConfiguredValue(provider, auth, field) {
    const value = typeof auth.value === "string" ? auth.value.trim() : "";
    if (!value) {
        throw new Error(`Provider '${provider.name}' requires auth.value for ${field}.`);
    }
    return value;
}

function readJwtExpiry(token) {
    const parts = token.split(".");
    if (parts.length < 2) {
        return 0;
    }

    try {
        const payload = parts[1].replace(/-/g, "+").replace(/_/g, "/");
        const padding = "=".repeat((4 - (payload.length % 4)) % 4);
        const claims = JSON.parse(Buffer.from(payload + padding, "base64").toString("utf8"));
        return Number.isFinite(claims.exp) ? claims.exp * 1000 : 0;
    } catch {
        return 0;
    }
}

function parseAzureCliToken(output) {
    let response;
    try {
        response = JSON.parse(output);
    } catch (error) {
        throw new Error(`Azure CLI returned invalid token JSON: ${error.message}`);
    }

    const token = typeof response.accessToken === "string" ? response.accessToken.trim() : "";
    if (!token) {
        throw new Error("Azure CLI returned an empty Foundry access token.");
    }

    const epochSeconds = Number(response.expires_on ?? response.expiresOnTimestamp);
    const expiresAt = Number.isFinite(epochSeconds) && epochSeconds > 0
        ? epochSeconds * 1000
        : readJwtExpiry(token);

    if (!expiresAt) {
        throw new Error("Azure CLI token response did not contain a valid expiration time.");
    }

    return { token, expiresAt };
}

async function acquireAzureCliToken(resource) {
    const cached = azureTokenCache.get(resource);
    if (cached?.token && Date.now() < cached.refreshAfter) {
        return cached.token;
    }

    if (cached?.pending) {
        return cached.pending;
    }

    const pending = (async () => {
        const executable = process.platform === "win32" ? "az.cmd" : "az";
        const { stdout } = await execFileAsync(
            executable,
            [
                "account",
                "get-access-token",
                "--resource",
                resource,
                "--output",
                "json"
            ],
            {
                encoding: "utf8",
                maxBuffer: 1024 * 1024,
                shell: process.platform === "win32",
                windowsHide: true,
                timeout: azureCliTimeoutMs
            }
        );

        const { token, expiresAt } = parseAzureCliToken(stdout);
        const now = Date.now();
        if (expiresAt <= now + 60 * 1000) {
            throw new Error("Azure CLI returned a Foundry access token that expires in less than one minute.");
        }

        azureTokenCache.set(resource, {
            token,
            expiresAt,
            refreshAfter: Math.max(now, expiresAt - 5 * 60 * 1000)
        });
        return token;
    })();
    azureTokenCache.set(resource, { pending });

    try {
        return await pending;
    } finally {
        const current = azureTokenCache.get(resource);
        if (current?.pending === pending) {
            azureTokenCache.delete(resource);
        }
    }
}

async function hydrateProvider(provider) {
    const { auth, requestCompatibility, ...sdkProvider } = provider;
    const modelAliases = Object.fromEntries(
        config.models
            .filter((model) => model.provider === provider.name && model.wireModel !== model.id)
            .map((model) => [model.wireModel, model.id])
    );
    const proxyProvider = { ...provider, modelAliases };
    if (auth?.type === "azure-cli" && !auth.resource) {
        throw new Error(`Provider '${provider.name}' must define auth.resource.`);
    }
    const getBearerToken = auth?.type === "azure-cli"
        ? () => acquireAzureCliToken(auth.resource)
        : undefined;
    const getUpstreamHeaders = auth?.type === "api-key-env"
        ? () => ({ "api-key": requiredEnvironmentValue(provider, auth, "API-key authentication") })
        : auth?.type === "bearer-token-env"
            ? () => ({ authorization: `Bearer ${requiredEnvironmentValue(provider, auth, "bearer-token authentication")}` })
            : auth?.type === "bearer-token"
                ? () => ({ authorization: `Bearer ${requiredConfiguredValue(provider, auth, "bearer-token authentication")}` })
            : undefined;
    let compatibilityProxy;
    const nativeProxy = nativeProxyEndpoints.get(provider.name);
    if (nativeProxy && nativeProxy.configuration === proxyConfiguration(proxyProvider)) {
        compatibilityProxy = nativeProxy;
    } else {
        compatibilityProxy = await startRequestCompatibilityProxy(proxyProvider, {
            getBearerToken,
            getUpstreamHeaders,
            onRewrite: (rewritten) => {
                compatibilityProxyRewrites.set(
                    provider.name,
                    (compatibilityProxyRewrites.get(provider.name) ?? 0) + rewritten
                );
            }
        });
    }
    if (compatibilityProxy) sdkProvider.baseUrl = compatibilityProxy.baseUrl;

    if (compatibilityProxy) {
        return {
            ...sdkProvider,
            bearerToken: compatibilityProxy.capability
        };
    }

    if (!auth) {
        return sdkProvider;
    }

    switch (auth.type) {
        case "azure-cli":
            return {
                ...sdkProvider,
                bearerTokenProvider: () => acquireAzureCliToken(auth.resource)
            };
        case "api-key-env":
            return {
                ...sdkProvider,
                apiKey: requiredEnvironmentValue(provider, auth, "API-key authentication")
            };
        case "bearer-token-env":
            return {
                ...sdkProvider,
                bearerToken: requiredEnvironmentValue(provider, auth, "bearer-token authentication")
            };
        case "bearer-token":
            return {
                ...sdkProvider,
                bearerToken: requiredConfiguredValue(provider, auth, "bearer-token authentication")
            };
        default:
            throw new Error(`Unsupported authentication type for provider '${provider.name}': ${auth.type}`);
    }
}

function hydrateModelFromCatalog(model, catalog) {
    const configured = configuredRegistration(model);
    if (configured) {
        return configured;
    }
    const upstream = catalog.find((candidate) => candidate?.id === model.modelId);
    if (!upstream) {
        throw new Error(`Upstream capability source '${model.modelId}' is unavailable.`);
    }

    const limits = upstream.capabilities?.limits;
    const supports = upstream.capabilities?.supports;
    if (!Number.isFinite(limits?.max_prompt_tokens) ||
        !Number.isFinite(limits?.max_context_window_tokens) ||
        !Number.isFinite(limits?.max_output_tokens)) {
        throw new Error(`Upstream capability source '${model.modelId}' has incomplete token limits.`);
    }

    const vision = limits.vision;
    return {
        ...model,
        maxPromptTokens: limits.max_prompt_tokens,
        maxContextWindowTokens: limits.max_context_window_tokens,
        maxOutputTokens: limits.max_output_tokens,
        capabilities: {
            supports: {
                vision: supports?.vision === true,
                reasoningEffort: Array.isArray(supports?.reasoning_effort)
                    ? supports.reasoning_effort.length > 0
                    : supports?.reasoning_effort === true
            },
            ...(vision ? {
                limits: {
                    vision: {
                        supported_media_types: [...vision.supported_media_types],
                        max_prompt_images: vision.max_prompt_images,
                        max_prompt_image_size: vision.max_prompt_image_size
                    }
                }
            } : {})
        }
    };
}

let metadataReadWarning;
async function readRuntimeMetadata() {
    try {
        return JSON.parse(await readFile(runtimeMetadataPath, "utf8"));
    } catch (error) {
        if (error?.code === "ENOENT") return null;
        if (error instanceof SyntaxError) {
            metadataReadWarning = `Ignored invalid BYOModels capability cache '${runtimeMetadataPath}': ${error.message}`;
            return null;
        }
        throw error;
    }
}

async function writeRuntimeMetadata(metadata) {
    const content = `${JSON.stringify(metadata, null, 2)}\n`;
    let existing;
    try {
        existing = await readFile(runtimeMetadataPath, "utf8");
    } catch (error) {
        if (error?.code !== "ENOENT") throw error;
    }
    if (existing === content) return;
    await mkdir(dirname(runtimeMetadataPath), { recursive: true });
    await writeFile(runtimeMetadataPath, content, "utf8");
}

let registeredModels = [];

function getStatus() {
    const now = Date.now();
    const activeTokens = [...azureTokenCache.values()]
        .filter((value) => value.token && now < value.refreshAfter);
    const earliestRefreshAfter = activeTokens.length > 0
        ? Math.min(...activeTokens.map((value) => value.refreshAfter))
        : 0;
    const earliestExpiresAt = activeTokens.length > 0
        ? Math.min(...activeTokens.map((value) => value.expiresAt))
        : 0;
    const models = registeredModels.map((model) => ({
        selectionId: `${model.provider}/${model.id}`,
        name: model.name,
        behaviorModel: model.modelId,
        wireDeployment: model.wireModel,
        maxPromptTokens: model.maxPromptTokens ?? null,
        maxContextWindowTokens: model.maxContextWindowTokens ?? null,
        maxOutputTokens: model.maxOutputTokens ?? null,
        supportsVision: model.capabilities?.supports?.vision === true,
        supportsReasoningEffort: model.capabilities?.supports?.reasoningEffort === true,
        capabilitiesSource: model.modelId,
        hasNamingDrift: model.id !== model.wireModel
    }));

    return {
        providerCount: config.providers.length,
        modelCount: models.length,
        activeTokenCache: activeTokens.length > 0,
        tokenRefreshAfter: earliestRefreshAfter
            ? new Date(earliestRefreshAfter).toISOString()
            : null,
        tokenExpiresAt: earliestExpiresAt
            ? new Date(earliestExpiresAt).toISOString()
            : null,
        pluginDataAvailable: Boolean(process.env.COPILOT_PLUGIN_DATA),
        compatibilityProxyRewrites: Object.fromEntries(compatibilityProxyRewrites),
        sources: {
            configured: config.sources ?? null
        },
        models
    };
}

function formatModelSummary() {
    return getStatus().models
        .map((model) =>
            `${model.selectionId} -> ${model.wireDeployment}` +
            ` (${model.maxPromptTokens} prompt / ${model.maxContextWindowTokens} context)` +
            (model.hasNamingDrift ? ` (behavior: ${model.behaviorModel}; naming drift)` : "")
        )
        .join("; ");
}

const modelsCanvas = createCanvas({
    id:     "afterburner-byomodels",
    displayName: "Afterburner BYOModels",
    description: "Shows the registered BYOModels registry, capabilities, and naming drift.",
    actions: [
        {
            name: "snapshot",
            description: "Return a sanitized snapshot of the BYOModels registry and token-cache state.",
            inputSchema: { type: "object", properties: {} },
            handler: async () => getStatus()
        }
    ],
    open: async () => {
        const status = getStatus();
        const driftCount = status.models.filter((model) => model.hasNamingDrift).length;
        return {
            title: "Afterburner BYOModels",
            status: `${status.modelCount} models registered; ${driftCount} naming drift item(s)`
        };
    }
});

let session;
const hydratedProviders = await Promise.all(config.providers.map(hydrateProvider));
session = await withDeadline(
    joinSession({
        providers: hydratedProviders,
        commands: [
            {
                name: "byomodels",
                description: "List registered BYOModels deployments and status.",
                handler: async () => {
                    const status = getStatus();
                    await logVisible(
                        `BYOModels status: ${status.providerCount} provider(s), ${status.modelCount} model(s), ` +
                        `token cache ${status.activeTokenCache ? "active" : "empty"}, ` +
                        `plugin data ${status.pluginDataAvailable ? "available" : "unavailable"}. ` +
                        `Models: ${formatModelSummary()}`
                    );
                }
            }
        ],
        canvases: [modelsCanvas]
    }),
    sessionTimeoutMs,
    "BYOModels session connection"
);

async function logVisible(message) {
    try {
        await withDeadline(session.log(message), rpcTimeoutMs, "BYOModels status reporting");
    } catch (error) {
        console.error(`${message} (status reporting failed: ${error?.message ?? String(error)})`);
    }
}

const cachedMetadata = await readRuntimeMetadata();
if (metadataReadWarning) {
    await logVisible(metadataReadWarning);
}

async function refreshCapabilities() {
    const catalog = await withDeadline(
        session.rpc.model.list(),
        rpcTimeoutMs,
        "BYOModels live capability discovery"
    );
    const catalogModels = catalog.list ?? [];
    const refreshedModels = config.models.map((model) => hydrateModelFromCatalog(model, catalogModels));
    const runtimeModels = buildRuntimeMetadata(config.models, catalogModels);
    const registeredKeys = new Set(registeredModels.map((m) => `${m.provider}/${m.id}`));
    const newModels = refreshedModels.filter((m) => !registeredKeys.has(`${m.provider}/${m.id}`));
    if (newModels.length > 0) {
        await withDeadline(
            session.rpc.provider.add({ models: newModels }),
            rpcTimeoutMs,
            "BYOModels refreshed model registration"
        );
    }
    registeredModels = refreshedModels;
    await writeRuntimeMetadata(capabilityCache(config.models, refreshedModels, runtimeModels));
    await logVisible(
        `Registered ${registeredModels.length} BYOModels model(s) from live upstream capabilities: ` +
            registeredModels.map((model) => `${model.provider}/${model.id} <- ${model.modelId}`).join(", ")
    );
}

registeredModels = await activateModelRegistration({
    models: config.models,
    cache: cachedMetadata,
    register: (models) => withDeadline(
        session.rpc.provider.add({ models }),
        rpcTimeoutMs,
        "BYOModels cached/configured model registration"
    ),
    report: logVisible,
    refresh: async (immediate) => {
        registeredModels = immediate;
        try {
            await withDeadline(refreshCapabilities(), refreshTimeoutMs, "BYOModels capability refresh");
        } catch (error) {
            const retained = registeredModels.length > 0
                ? ` Retaining ${registeredModels.length} validated cached/configured model(s).`
                : " No BYOModels models were registered.";
            await logVisible(
                `BYOModels capability refresh failed: ${error?.message ?? String(error)}.${retained}`
            );
        }
        return registeredModels;
    }
});
