import { configuredRegistration } from "./model-metadata.mjs";

export function buildRuntimeMetadata(models, catalog) {
    return models.map((model) => {
        const upstream = catalog.find((candidate) => candidate?.id === model.modelId);
        const configured = configuredRegistration(model);
        const reasoningProperty = upstream?.configSchema?.properties?.reasoningEffort;
        const reasoningSupport = upstream?.capabilities?.supports?.reasoning_effort;
        const upstreamReasoningEfforts = Array.isArray(reasoningSupport)
            ? reasoningSupport.filter((effort) => typeof effort === "string")
            : Array.isArray(reasoningProperty?.enum)
                ? reasoningProperty.enum.filter((effort) => typeof effort === "string")
                : [];
        const supportsConfiguredReasoning = configured?.capabilities?.supports?.reasoningEffort;
        return {
            selectionId: `${model.provider}/${model.id}`,
            upstreamModelId: model.modelId,
            maxContextWindowTokens: configured?.maxContextWindowTokens ??
                upstream?.capabilities?.limits?.max_context_window_tokens ?? null,
            maxOutputTokens: configured?.maxOutputTokens ??
                upstream?.capabilities?.limits?.max_output_tokens ?? null,
            supportedReasoningEfforts: supportsConfiguredReasoning === false
                ? []
                : upstreamReasoningEfforts,
            defaultReasoningEffort: supportsConfiguredReasoning === false
                ? "none"
                : typeof reasoningProperty?.default === "string"
                    ? reasoningProperty.default
                    : null
        };
    });
}
