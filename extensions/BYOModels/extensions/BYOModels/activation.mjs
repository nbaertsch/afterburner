import { immediateRegistrations } from "./model-metadata.mjs";

export function scheduleDetached(callback) {
    const handle = setTimeout(callback, 0);
    handle.unref?.();
}

export async function activateModelRegistration({
    models,
    cache,
    register,
    report,
    refresh,
    schedule = scheduleDetached
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
        `Extension activation is completing before detached live capability refresh.${summary}`
    );
    schedule(() => {
        void refresh(immediate).catch(() => {});
    });
    return immediate;
}
