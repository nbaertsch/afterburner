export function updateSubagentSettings(session, settings) {
    const rpc = session?.rpc?.tools?.updateSubagentSettings;
    if (typeof rpc === "function") return rpc.call(session.rpc.tools, { settings });
    const nested = session?.tools?.updateSubagentSettings;
    if (typeof nested === "function") return nested.call(session.tools, { settings });
    const direct = session?.updateSubagentSettings;
    if (typeof direct === "function") return direct.call(session, { settings });
    throw new Error("Copilot session does not expose updateSubagentSettings");
}

export async function availableModelIDs(session) {
    const list = session?.models?.list;
    const catalog = session?.models?.getBuiltInCatalog;
    let models;
    if (typeof list === "function") models = await list.call(session.models);
    else if (typeof catalog === "function") models = await catalog.call(session.models);
    else throw new Error("Copilot session does not expose models.list() or models.getBuiltInCatalog()");
    const entries = Array.isArray(models) ? models : models?.models ?? models?.items ?? [];
    return new Set(entries.flatMap(model => {
        const provider = model?.provider ?? model?.providerId;
        const id = model?.id ?? model?.modelId;
        return [
            typeof model?.selectionId === "string" ? model.selectionId : null,
            typeof provider === "string" && typeof id === "string" ? `${provider}/${id}` : null
        ].filter(Boolean);
    }));
}
