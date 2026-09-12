import { completeModalOpenRequest, consumeModalOpenRequests, readPolicyState, requestPolicyAction } from "../extensions/SubagentPolicy/policy-ipc.mjs";
import { modalFrame, menuActions } from "../extensions/SubagentPolicy/menu.mjs";
import { CANONICAL_ID, CANONICAL_TITLE } from "../extensions/SubagentPolicy/names.mjs";

function registrar(api = {}) {
    if (typeof api.ui?.registerSurface === "function") return { target: api.ui, fn: api.ui.registerSurface };
    if (typeof api.registerSurface === "function") return { target: api, fn: api.registerSurface };
    if (typeof api.registerModalCanvas === "function") return { target: api, fn: api.registerModalCanvas };
    return null;
}

async function update(action, controls) {
    if (action === "close") return controls?.close?.();
    const result = await requestPolicyAction(action);
    const frame = modalFrame(result.state ?? await readPolicyState(), result.ok ? undefined : `action failed: ${result.error ?? "unknown"}`);
    return typeof controls?.update === "function" ? controls.update(frame) : frame;
}

export async function activate(api = {}) {
    const register = registrar(api);
    if (!register) return { dispose() {} };
    const modal = register.fn.call(register.target, {
        id: CANONICAL_ID,
        displayName: CANONICAL_TITLE,
        description: "Apply session-scoped Copilot subagent routing and concurrency policies.",
        actions: menuActions.map(action => ({ ...action, handler: async (_input, controls) => update(action.name, controls) })),
        open: async () => modalFrame(await readPolicyState(), "interactive menu open"),
        render: async context => context.state
    });
    const poll = async () => {
        for (const request of await consumeModalOpenRequests().catch(() => [])) {
            try {
                await modal.open(request.input ?? {});
                await completeModalOpenRequest(request, { ok: true });
            } catch (error) {
                await completeModalOpenRequest(request, { ok: false, error: String(error?.message ?? error) });
            }
        }
    };
    const timer = setInterval(() => { void poll(); }, 250);
    timer.unref?.();
    await poll();
    return {
        modal,
        async dispose() {
            clearInterval(timer);
            try { await modal?.close?.(); } catch {}
            try { modal?.dispose?.(); } catch {}
        }
    };
}
