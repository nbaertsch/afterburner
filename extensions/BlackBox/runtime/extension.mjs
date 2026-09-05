import { startBlackBoxService } from "../lib/service.mjs";
import { registerEnterpriseSurface, subscribeObservability } from "../lib/ui-surface.mjs";

const INSTANCE = Symbol.for("afterburner.black-box.runtime");
const LIVE_MODAL_ID = "afterburner-black-box-live";
const MODAL_REFRESH_THROTTLE_MS = 250;

function isolatedWarning(code) {
    if (process.env.COPILOT_RUNTIME_EXTENSION_DEBUG === "1") {
        process.stderr.write(`[black-box] ${code}\n`);
    }
}

function formatLine(record) {
    const attributes = record?.attributes ?? {};
    const duration = Number.isFinite(attributes.durationMs) ? ` ${attributes.durationMs}ms` : "";
    const outcome = typeof attributes.success === "boolean" ? ` success=${attributes.success}` : "";
    return `${record?.timestamp ?? "unknown"} ${record?.kind ?? "event"} ${record?.eventType ?? "unknown"}${duration}${outcome}`;
}

function unavailableFrame() {
    return {
        title: "Afterburner Black Box Live",
        status: "Black Box metadata is unavailable.",
        body: "The Black Box modal could not refresh. Use /black-box for the text status panel.",
        footer: "Esc/q closes · metadata-only fallback"
    };
}

async function renderLiveModal(service) {
    const status = await service.status();
    const records = await service.tail({ limit: 12 });
    return {
        title: "Afterburner Black Box Live",
        status: `${status.storage.segmentCount} segment(s), ${status.analytics.totalRecords} record(s), ${status.queue.droppedRecords} dropped`,
        body: records.length > 0 ? records.map(formatLine).join("\n") : "No metadata events recorded yet.",
        footer: "Esc/q closes · /black-box-tail keeps text fallback available"
    };
}

async function safeRenderLiveModal(service) {
    try { return await renderLiveModal(service); }
    catch {
        isolatedWarning("modal-canvas-render-failed");
        return unavailableFrame();
    }
}

async function updateControls(controls, frame) {
    if (typeof controls?.update !== "function") return frame;
    try { return await controls.update(frame); }
    catch {
        isolatedWarning("modal-canvas-update-failed");
        return { ok: false, error: "modal-canvas-update-failed", frame };
    }
}

async function closeControls(controls) {
    if (typeof controls?.close !== "function") return { ok: false, error: "modal-canvas-close-unavailable" };
    try { return await controls.close(); }
    catch {
        isolatedWarning("modal-canvas-close-failed");
        return { ok: false, error: "modal-canvas-close-failed" };
    }
}

async function doctorFrame(service) {
    try {
        return {
            title: "Afterburner Black Box Doctor",
            body: JSON.stringify(await service.doctor(), null, 2),
            footer: "Metadata-only diagnostics · r refreshes live view"
        };
    } catch {
        isolatedWarning("modal-canvas-doctor-failed");
        return {
            title: "Afterburner Black Box Doctor",
            status: "Doctor check failed.",
            body: JSON.stringify({ healthy: false, error: "black-box-doctor-failed" }, null, 2),
            footer: "Use /black-box-doctor for text diagnostics"
        };
    }
}

function subscribeLiveModal(service, controls) {
    if (typeof service.subscribeRecords !== "function" || typeof controls?.update !== "function") return () => {};
    let disposed = false;
    let timer = null;
    let refreshing = false;
    let pending = false;
    let lastRefreshAt = 0;

    const clear = () => {
        if (timer) clearTimeout(timer);
        timer = null;
    };
    const schedule = () => {
        if (disposed || timer) return;
        const delay = Math.max(0, MODAL_REFRESH_THROTTLE_MS - (Date.now() - lastRefreshAt));
        timer = setTimeout(run, delay);
        timer.unref?.();
    };
    const run = async () => {
        timer = null;
        if (disposed) return;
        if (refreshing) {
            pending = true;
            return;
        }
        refreshing = true;
        lastRefreshAt = Date.now();
        try { await updateControls(controls, await safeRenderLiveModal(service)); }
        finally {
            refreshing = false;
            if (pending && !disposed) {
                pending = false;
                schedule();
            }
        }
    };
    const unsubscribe = service.subscribeRecords(schedule);
    return () => {
        disposed = true;
        clear();
        try { unsubscribe?.(); } catch {}
    };
}

function modalCanvasRegistrar(api = {}) {
    if (typeof api.registerModalCanvas === "function") return { target: api, fn: api.registerModalCanvas };
    if (typeof api.ui?.registerModalCanvas === "function") return { target: api.ui, fn: api.ui.registerModalCanvas };
    return null;
}

function registerLiveModal(api, service) {
    const register = modalCanvasRegistrar(api);
    if (!register) {
        isolatedWarning("modal-canvas-unavailable");
        return null;
    }
    try {
        return register.fn.call(register.target, {
            id: LIVE_MODAL_ID,
            displayName: "Afterburner Black Box Live",
            description: "Temporary full-screen live view of sanitized Black Box metadata.",
            actions: [
                {
                    name: "refresh",
                    label: "Refresh",
                    key: "r",
                    handler: async (_input, controls) => updateControls(controls, await safeRenderLiveModal(service))
                },
                {
                    name: "doctor",
                    label: "Doctor",
                    key: "d",
                    handler: async (_input, controls) => updateControls(controls, await doctorFrame(service))
                },
                {
                    name: "close",
                    label: "Close",
                    key: "q",
                    handler: async (_input, controls) => closeControls(controls)
                }
            ],
            open: async () => safeRenderLiveModal(service),
            subscribe: async controls => subscribeLiveModal(service, controls)
        });
    } catch {
        isolatedWarning("modal-canvas-registration-failed");
        return null;
    }
}

export async function activate(api = {}) {
    try {
        if (globalThis[INSTANCE]) return globalThis[INSTANCE];
        if (typeof api.registerRuntimeObserver !== "function") {
            isolatedWarning("runtime-observer-unavailable");
            return null;
        }
        const service = await startBlackBoxService({ mode: "runtime" });
        const onEvent = async (event) => {
            try { return await service.observeRuntime(event); }
            catch { return false; }
        };
        const observer = { id: "black-box", onEvent };
        let dispose;
        try {
            dispose = api.registerRuntimeObserver(observer);
        } catch {
            await service.close().catch(() => {});
            isolatedWarning("runtime-observer-registration-failed");
            return null;
        }
        let observability;
        try { observability = await subscribeObservability(api, service); }
        catch { isolatedWarning("ui-observability-registration-failed"); }
        const enterprise = await registerEnterpriseSurface(api, service).catch(() => {
            isolatedWarning("enterprise-surface-registration-failed");
            return null;
        });
        const modal = registerLiveModal(api, service);
        if (enterprise && process.env.AFTERBURNER_BLACK_BOX_OPEN_SURFACE_ON_START === "1") {
            await enterprise.open({}).catch(() => isolatedWarning("enterprise-surface-open-failed"));
        } else if (modal && process.env.AFTERBURNER_BLACK_BOX_OPEN_MODAL_ON_START === "1") {
            await modal.open().catch(() => isolatedWarning("modal-canvas-open-failed"));
        }
        const instance = {
            service,
            observer,
            observability,
            enterprise,
            modal,
            async dispose() {
                try {
                    if (typeof dispose === "function") await dispose();
                    else if (typeof dispose?.dispose === "function") await dispose.dispose();
                } catch {}
                try {
                    if (typeof observability === "function") await observability();
                    else if (typeof observability?.dispose === "function") await observability.dispose();
                } catch {}
                try { await enterprise?.dispose?.(); } catch {}
                try { await modal?.close?.(); } catch {}
                try { modal?.dispose?.(); } catch {}
                await service.close().catch(() => {});
                delete globalThis[INSTANCE];
            }
        };
        globalThis[INSTANCE] = instance;
        return instance;
    } catch {
        isolatedWarning("activation-failed");
        return null;
    }
}
