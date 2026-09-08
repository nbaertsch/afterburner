import { execFile } from "node:child_process";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import { dirname, join } from "node:path";
import { promisify } from "node:util";
import { createCanvas } from "@github/copilot-sdk";
import { joinSession } from "@github/copilot-sdk/extension";
import { startRequestCompatibilityProxy } from "./request-compatibility.mjs";

const execFileAsync = promisify(execFile);
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
        const proxyPort = provider.requestCompatibility?.proxyPort;
        if (proxyPort !== undefined &&
            (!Number.isInteger(proxyPort) || proxyPort < 1024 || proxyPort > 65535)) {
            throw new Error(
                `Provider '${provider.name}' must define requestCompatibility.proxyPort between 1024 and 65535.`
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
        modelIds.add(selectionId);
    }
}

validateConfig();

const azureTokenCache = new Map();
const compatibilityProxyRewrites = new Map();

function requiredEnvironmentValue(provider, auth, field) {
    const variable = auth.environmentVariable;
    const value = typeof variable === "string" ? process.env[variable]?.trim() : "";
    if (!value) {
        throw new Error(`Provider '${provider.name}' requires environment variable '${variable}' for ${field}.`);
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
                windowsHide: true
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
    if (auth?.type === "azure-cli" && !auth.resource) {
        throw new Error(`Provider '${provider.name}' must define auth.resource.`);
    }
    const getBearerToken = auth?.type === "azure-cli"
        ? () => acquireAzureCliToken(auth.resource)
        : undefined;
    const compatibilityProxy = await startRequestCompatibilityProxy(provider, {
        getBearerToken,
        onRewrite: (rewritten) => {
            compatibilityProxyRewrites.set(
                provider.name,
                (compatibilityProxyRewrites.get(provider.name) ?? 0) + rewritten
            );
        }
    });
    if (compatibilityProxy) sdkProvider.baseUrl = compatibilityProxy.baseUrl;

    if (!auth) {
        return sdkProvider;
    }

    switch (auth.type) {
        case "azure-cli":
            return {
                ...sdkProvider,
                ...(compatibilityProxy
                    ? { bearerToken: "afterburner-loopback-auth" }
                    : { bearerTokenProvider: () => acquireAzureCliToken(auth.resource) })
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
        default:
            throw new Error(`Unsupported authentication type for provider '${provider.name}': ${auth.type}`);
    }
}

function hydrateModelFromCatalog(model, catalog) {
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

function buildRuntimeMetadata(models, catalog) {
    return models.map((model) => {
        const upstream = catalog.find((candidate) => candidate?.id === model.modelId);
        const reasoningProperty = upstream?.configSchema?.properties?.reasoningEffort;
        const reasoningSupport = upstream?.capabilities?.supports?.reasoning_effort;
        const supportedReasoningEfforts = Array.isArray(reasoningSupport)
            ? reasoningSupport.filter((effort) => typeof effort === "string")
            : Array.isArray(reasoningProperty?.enum)
                ? reasoningProperty.enum.filter((effort) => typeof effort === "string")
                : [];
        return {
            selectionId: `${model.provider}/${model.id}`,
            upstreamModelId: model.modelId,
            maxContextWindowTokens: upstream?.capabilities?.limits?.max_context_window_tokens ?? null,
            maxOutputTokens: upstream?.capabilities?.limits?.max_output_tokens ?? null,
            supportedReasoningEfforts,
            defaultReasoningEffort: typeof reasoningProperty?.default === "string"
                ? reasoningProperty.default
                : null
        };
    });
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
session = await joinSession({
    providers: hydratedProviders,
    commands: [
        {
            name: "byomodels",
            description: "List registered BYOModels deployments and status.",
            handler: async () => {
                const status = getStatus();
                await session.log(
                    `BYOModels status: ${status.providerCount} provider(s), ${status.modelCount} model(s), ` +
                    `token cache ${status.activeTokenCache ? "active" : "empty"}, ` +
                    `plugin data ${status.pluginDataAvailable ? "available" : "unavailable"}. ` +
                    `Models: ${formatModelSummary()}`
                );
            }
        }
    ],
    canvases: [modelsCanvas]
});

const catalog = await session.rpc.model.list();
const catalogModels = catalog.list ?? [];
registeredModels = config.models.map((model) => hydrateModelFromCatalog(model, catalogModels));
await writeRuntimeMetadata({
    version: 1,
    models: buildRuntimeMetadata(config.models, catalogModels)
});
await session.rpc.provider.add({ models: registeredModels });

await session.log(
    `Registered ${registeredModels.length} BYOModels model(s) from live upstream capabilities: ` +
        registeredModels.map((model) => `${model.provider}/${model.id} <- ${model.modelId}`).join(", ")
);
