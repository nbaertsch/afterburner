import { CANONICAL_ID, CANONICAL_TITLE } from "./names.mjs";

export const MENU_ID = CANONICAL_ID;

export const menuActions = Object.freeze([
    { name: "start", label: "Start", key: "s", description: "Start or reuse bridge" },
    { name: "stop", label: "Stop", key: "x", description: "Stop listener" },
    { name: "status", label: "Status", key: "r", description: "Refresh state" },
    { name: "doctor", label: "Doctor", key: "d", description: "Show diagnostics" },
    { name: "close", label: "Close", key: "q", description: "Close menu" }
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
    const lines = [
        `Endpoint: ${snapshot?.endpoint ?? "not allocated"}`,
        `Models: ${snapshot?.modelCount ?? 0}`,
        `Requests: ${snapshot?.requestCount ?? 0}`,
        `Errors: ${snapshot?.errorCount ?? 0}`,
        `API key: ${snapshot?.apiKeyRequired ? "required" : "not required"}`,
        configuredPath ? `Config: ${configuredPath}` : undefined,
        detail ? `Detail: ${detail}` : undefined
    ].filter(Boolean);
    if (diagnostics) {
        lines.push("", "Sanitized diagnostics:", JSON.stringify(snapshot, null, 2));
    }
    return lines.join("\n");
}

export function modalFrame({ snapshot = {}, configuredPath, detail = undefined, diagnostics = false } = {}) {
    return {
        id: MENU_ID,
        title: CANONICAL_TITLE,
        status: menuStatus(snapshot, detail),
        body: menuBody({ snapshot, configuredPath, detail, diagnostics }),
        actions: menuActions
    };
}
