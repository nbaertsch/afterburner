export const MENU_ID = "copilot-openai";

export const menuActions = Object.freeze([
    { name: "start", label: "Start", key: "s", description: "Start or reuse the localhost bridge." },
    { name: "stop", label: "Stop", key: "x", description: "Stop this session's bridge listener." },
    { name: "status", label: "Status", key: "r", description: "Refresh bridge status." },
    { name: "doctor", label: "Doctor", key: "d", description: "Show sanitized bridge diagnostics." },
    { name: "close", label: "Close", key: "q", description: "Close this menu." }
]);

export function menuStatus(snapshot = {}, detail = undefined) {
    const active = snapshot?.active === true;
    return [
        `State: ${active ? "running" : "stopped"}`,
        `Endpoint: ${snapshot?.endpoint ?? "not allocated"}`,
        `Models: ${snapshot?.modelCount ?? 0}`,
        `Requests: ${snapshot?.requestCount ?? 0}`,
        `Errors: ${snapshot?.errorCount ?? 0}`,
        detail ? `Detail: ${detail}` : undefined
    ].filter(Boolean).join(" · ");
}

export function menuBody({ snapshot = {}, configuredPath, detail = undefined, diagnostics = false } = {}) {
    return [
        "Use Tab/Shift+Tab to focus actions; Enter or Space activates the focused action.",
        "Keyboard shortcuts: s Start · x Stop · r Status · d Doctor · q Close",
        "",
        `API key: ${snapshot?.apiKeyRequired ? "required" : "not required"}`,
        `Config: ${configuredPath}`,
        detail ? `Detail: ${detail}` : undefined,
        "",
        diagnostics ? "Sanitized diagnostics:" : "Interactive actions:",
        diagnostics ? JSON.stringify(snapshot, null, 2) : "• Start / reuse bridge\n• Stop bridge\n• Refresh status\n• Doctor diagnostics"
    ].filter(Boolean).join("\n");
}

export function modalFrame({ snapshot = {}, configuredPath, detail = undefined, diagnostics = false } = {}) {
    return {
        id: MENU_ID,
        title: "Copilot OpenAI Bridge",
        status: menuStatus(snapshot, detail),
        body: menuBody({ snapshot, configuredPath, detail, diagnostics }),
        footer: "s start · x stop · r status · d doctor · q close",
        actions: menuActions
    };
}
