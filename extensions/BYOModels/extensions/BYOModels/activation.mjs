import { immediateRegistrations } from "./model-metadata.mjs";

export async function activateModelRegistration({
    models,
    cache,
    register,
    report,
    refresh
}) {
    const immediate = immediateRegistrations(models, cache);
    if (immediate.length > 0) {
        await register(immediate);
    }
    const summary = immediate.length > 0
        ? ` Models: ${immediate.map((model) =>
            `${model.provider}/${model.id} <- ${model.modelId}`).join(", ")}.`
        : "";
    await report(
        `Registered ${immediate.length} BYOModels model(s) immediately from validated cached/configured capabilities. ` +
        `Live capability refresh is completing before extension activation returns.${summary}`
    );
    return await refresh(immediate);
}
