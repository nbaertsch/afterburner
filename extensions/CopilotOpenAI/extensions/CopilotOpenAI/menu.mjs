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

export function menuBody({ snapshot = {}, configuredPath, detail = undefined } = {}) {
    return [
        "Copilot OpenAI Bridge management",
        "=================================",
        menuStatus(snapshot, detail),
        `API key: ${snapshot?.apiKeyRequired ? "required" : "not required"}`,
        `Config: ${configuredPath}`,
        "",
        "Interactive actions:",
        "• Start / reuse bridge",
        "• Stop bridge",
        "• Refresh status",
        "• Doctor diagnostics"
    ].join("\n");
}

export function createManagementCanvas({ createCanvas, getStatus, startBridge, stopBridge, configuredPath }) {
    const canvasState = async (detail = undefined) => {
        const snapshot = getStatus();
        return {
            title: "Copilot OpenAI Bridge",
            status: menuStatus(snapshot, detail),
            body: menuBody({ snapshot, configuredPath, detail }),
            diagnostics: snapshot
        };
    };

    return createCanvas({
        id: "afterburner-copilot-openai-menu",
        displayName: "Copilot OpenAI Bridge",
        description: "Interactive management menu for the localhost OpenAI-compatible Copilot bridge.",
        actions: [
            {
                name: "start",
                label: "Start",
                description: "Start or reuse the localhost bridge.",
                handler: async () => canvasState(`bridge ready at ${(await startBridge()).endpoint ?? "not allocated"}`)
            },
            {
                name: "stop",
                label: "Stop",
                description: "Stop this session's bridge listener.",
                handler: async () => {
                    await stopBridge();
                    return canvasState("bridge stopped");
                }
            },
            {
                name: "status",
                label: "Status",
                description: "Refresh bridge status.",
                handler: async () => canvasState("status refreshed")
            },
            {
                name: "doctor",
                label: "Doctor",
                description: "Show sanitized bridge diagnostics.",
                handler: async () => ({ ...(await canvasState("diagnostics refreshed")), diagnostics: getStatus() })
            }
        ],
        open: async () => {
            const snapshot = await startBridge();
            return {
                title: "Copilot OpenAI Bridge",
                status: menuStatus(snapshot, "interactive menu open"),
                body: menuBody({ snapshot, configuredPath, detail: "interactive menu open" }),
                diagnostics: getStatus()
            };
        }
    });
}
