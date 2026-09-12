export function updateSubagentSettings(session, settings) {
    const rpc = session?.rpc?.tools?.updateSubagentSettings;
    if (typeof rpc === "function") return rpc.call(session.rpc.tools, { settings });
    const nested = session?.tools?.updateSubagentSettings;
    if (typeof nested === "function") return nested.call(session.tools, { settings });
    const direct = session?.updateSubagentSettings;
    if (typeof direct === "function") return direct.call(session, { settings });
    throw new Error("Copilot session does not expose updateSubagentSettings");
}
