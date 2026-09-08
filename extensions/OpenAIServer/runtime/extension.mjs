import { completeModalOpenRequest, consumeModalOpenRequests, readBridgeState, requestBridgeAction } from "../extensions/OpenAIServer/modal-ipc.mjs";
import { MENU_ID, modalFrame } from "../extensions/OpenAIServer/menu.mjs";
import { CANONICAL_TITLE, displayConfigPath } from "../extensions/OpenAIServer/names.mjs";

const POLL_MS = 100;

function modalRegistrar(api = {}) {
    if (typeof api.ui?.registerSurface === "function") return { target: api.ui, fn: api.ui.registerSurface };
    if (typeof api.registerSurface === "function") return { target: api, fn: api.registerSurface };
    if (typeof api.registerModalCanvas === "function") return { target: api, fn: api.registerModalCanvas };
    if (typeof api.ui?.registerModalCanvas === "function") return { target: api.ui, fn: api.ui.registerModalCanvas };
    return null;
}

async function frame(detail = undefined, diagnostics = false) {
    const snapshot = await readBridgeState();
    return modalFrame({ snapshot, configuredPath: snapshot.configPath ?? displayConfigPath(), detail, diagnostics });
}

async function updateFromAction(action, controls) {
    if (action === "close") return controls?.close?.();
    const result = await requestBridgeAction(action);
    const next = modalFrame({
        snapshot: result.state ?? await readBridgeState(),
        configuredPath: result.state?.configPath ?? displayConfigPath(),
        detail: result.ok ? (result.state?.detail ?? (action === "doctor" ? "diagnostics refreshed" : `${action} complete`)) : `action failed: ${result.error ?? "unknown"}`,
        diagnostics: action === "doctor"
    });
    if (typeof controls?.update === "function") return controls.update(next);
    return next;
}

export async function activate(api = {}) {
    const register = modalRegistrar(api);
    if (!register) return { dispose() {} };
    let modal;
    try {
        modal = register.fn.call(register.target, {
            id: MENU_ID,
            displayName: CANONICAL_TITLE,
            description: "Visible terminal management menu for the localhost OpenAI-compatible server backed by the active Copilot session.",
            actions: [
                { name: "start", label: "Start", key: "s", description: "Start or reuse the localhost bridge.", handler: async (_input, controls) => updateFromAction("start", controls) },
                { name: "stop", label: "Stop", key: "x", description: "Stop this session's bridge listener.", handler: async (_input, controls) => updateFromAction("stop", controls) },
                { name: "status", label: "Status", key: "r", description: "Refresh bridge status.", handler: async (_input, controls) => updateFromAction("status", controls) },
                { name: "doctor", label: "Doctor", key: "d", description: "Show sanitized bridge diagnostics.", handler: async (_input, controls) => updateFromAction("doctor", controls) },
                { name: "close", label: "Close", key: "q", description: "Close this menu.", handler: async (_input, controls) => updateFromAction("close", controls) }
            ],
            open: async () => frame("interactive menu open"),
            render: async context => context.state
        });
    } catch {
        return { dispose() {} };
    }

    let opening = false;
    const poll = async () => {
        if (opening) return;
        opening = true;
        try {
            const requests = await consumeModalOpenRequests();
            for (const request of requests) {
                try {
                    await modal.open(request.input ?? {});
                    await completeModalOpenRequest(request, { ok: true });
                } catch (error) {
                    await completeModalOpenRequest(request, { ok: false, error: String(error?.message ?? error) });
                }
            }
        } finally {
            opening = false;
        }
    };
    const timer = setInterval(poll, POLL_MS);
    timer.unref?.();
    let watcher;
    try {
        const target = await import("../extensions/OpenAIServer/modal-ipc.mjs");
        // Polling is the correctness path; best-effort wakeup comes from the timer in all environments.
        void target;
    } catch {}
    await poll();
    return {
        modal,
        async dispose() {
            clearInterval(timer);
            try { watcher?.close?.(); } catch {}
            try { await modal?.close?.(); } catch {}
            try { modal?.dispose?.(); } catch {}
        }
    };
}
