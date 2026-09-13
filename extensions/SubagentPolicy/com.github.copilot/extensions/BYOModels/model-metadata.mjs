import { createHash } from "node:crypto";

export const capabilityCacheVersion = 2;

export function modelConfigurationKey(models) {
    return createHash("sha256").update(JSON.stringify(models.map((model) => ({
        provider: model.provider,
        id: model.id,
        name: model.name,
        modelId: model.modelId,
        wireModel: model.wireModel
    })))).digest("base64url");
}

function validRegistration(model, configured) {
    const vision = model?.capabilities?.limits?.vision;
    return model?.provider === configured.provider &&
        model?.id === configured.id &&
        model?.name === configured.name &&
        model?.modelId === configured.modelId &&
        model?.wireModel === configured.wireModel &&
        Number.isFinite(model.maxPromptTokens) &&
        Number.isFinite(model.maxContextWindowTokens) &&
        Number.isFinite(model.maxOutputTokens) &&
        typeof model.capabilities?.supports?.vision === "boolean" &&
        typeof model.capabilities?.supports?.reasoningEffort === "boolean" &&
        (vision === undefined ||
            (Array.isArray(vision.supported_media_types) &&
                vision.supported_media_types.every((type) => typeof type === "string") &&
                Number.isFinite(vision.max_prompt_images) &&
                Number.isFinite(vision.max_prompt_image_size)));
}

function safeRegistration(model, configured) {
    const vision = model.capabilities.limits?.vision;
    return {
        ...structuredClone(configured),
        maxPromptTokens: model.maxPromptTokens,
        maxContextWindowTokens: model.maxContextWindowTokens,
        maxOutputTokens: model.maxOutputTokens,
        capabilities: {
            supports: {
                vision: model.capabilities.supports.vision,
                reasoningEffort: model.capabilities.supports.reasoningEffort
            },
            ...(vision ? { limits: { vision: structuredClone(vision) } } : {})
        }
    };
}

export function configuredRegistration(model) {
    return validRegistration(model, model) ? safeRegistration(model, model) : null;
}

export function cachedRegistrations(cache, models) {
    if (cache?.version !== capabilityCacheVersion ||
        cache.configuration !== modelConfigurationKey(models) ||
        !Array.isArray(cache.registrations)) {
        return [];
    }
    const cachedBySelection = new Map(
        cache.registrations.map((model) => [`${model?.provider}/${model?.id}`, model])
    );
    return models.flatMap((configured) => {
        const cached = cachedBySelection.get(`${configured.provider}/${configured.id}`);
        return validRegistration(cached, configured) ? [safeRegistration(cached, configured)] : [];
    });
}

export function immediateRegistrations(models, cache) {
    const cached = new Map(cachedRegistrations(cache, models)
        .map((model) => [`${model.provider}/${model.id}`, model]));
    return models.flatMap((model) => {
        const configured = configuredRegistration(model);
        return configured ? [configured] : cached.has(`${model.provider}/${model.id}`)
            ? [cached.get(`${model.provider}/${model.id}`)]
            : [];
    });
}

export function capabilityCache(models, registrations, runtimeModels) {
    return {
        version: capabilityCacheVersion,
        configuration: modelConfigurationKey(models),
        registrations,
        models: runtimeModels
    };
}

export async function withDeadline(operation, milliseconds, label) {
    let timer;
    try {
        return await Promise.race([
            operation,
            new Promise((_, reject) => {
                timer = setTimeout(
                    () => reject(new Error(`${label} timed out after ${milliseconds}ms.`)),
                    milliseconds
                );
            })
        ]);
    } finally {
        clearTimeout(timer);
    }
}
