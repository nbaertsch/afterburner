import { readFileSync } from "node:fs";
import { readFile } from "node:fs/promises";
import { join } from "node:path";

export async function activate({ pluginRoot, registerModelPickerAdapter }) {
    const config = JSON.parse(
        await readFile(join(pluginRoot, "extensions", "byok-models", "models.json"), "utf8")
    );
    const contextWindowOptions = config.contextWindowOptions ?? [];
    const metadataPath = join(
        process.env.COPILOT_HOME ?? join(process.env.USERPROFILE ?? "", ".copilot"),
        "runtime-extension-data",
        "afterburner-byok-models",
        "model-metadata.json"
    );
    const upstreamBySelectionId = new Map(
        config.models.map((model) => [`${model.provider}/${model.id}`, model.modelId])
    );
    const runtimeMetadata = (selectionId) => {
        try {
            const metadata = JSON.parse(readFileSync(metadataPath, "utf8"));
            return metadata.models?.find((model) => model.selectionId === selectionId) ?? null;
        } catch {
            return null;
        }
    };

    registerModelPickerAdapter({
        matches: (selectionId) => upstreamBySelectionId.has(selectionId),
        upstreamModelId: (selectionId) => upstreamBySelectionId.get(selectionId),
        selectionIds: () => [...upstreamBySelectionId.keys()],
        supportedReasoningEfforts: (selectionId) => runtimeMetadata(selectionId)?.supportedReasoningEfforts,
        defaultReasoningEffort: (selectionId) => runtimeMetadata(selectionId)?.defaultReasoningEffort,
        maxContextWindowTokens: (selectionId) => runtimeMetadata(selectionId)?.maxContextWindowTokens,
        maxOutputTokens: (selectionId) => runtimeMetadata(selectionId)?.maxOutputTokens,
        contextWindowOptions: () => contextWindowOptions
    });
}
