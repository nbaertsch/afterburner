import { watch } from "node:fs";
import { startBlackBoxService } from "../lib/service.mjs";
import { buildModalFrame, hashDisplayPath } from "../lib/modal-surface.mjs";

const INSTANCE = Symbol.for("afterburner.black-box.runtime");
const LIVE_MODAL_ID = "afterburner-black-box-live";
export const MODAL_ACTIVATION_POLL_MS = 2000;
const MODAL_REFRESH_THROTTLE_MS = 1000;

function isolatedWarning(code) {
    if (process.env.COPILOT_RUNTIME_EXTENSION_DEBUG === "1") {
        process.stderr.write(`[black-box] ${code}\n`);
    }
}

function unavailableFrame() {
    return {
        title: "Afterburner Black Box Live",
        status: "Black Box metadata is unavailable.",
        body: "The Black Box modal could not refresh. Use /black-box for the text status panel.",
        footer: "Esc/q closes · metadata-only fallback"
    };
}

async function modalState(service, overrides = {}) {
    const status = await service.status();
    const records = await service.tail({ limit: 12 });
    return {
        status,
        records,
        view: { offset: 0, limit: 12 },
        sort: { field: "timestamp", direction: "desc" },
        lifecycle: { stream: "open", queueDepth: status.queue?.records ?? 0, backpressure: false, reconnectCount: 0 },
        ...overrides
    };
}

async function renderLiveModal(service, ui, overrides = {}) {
    return buildModalFrame(ui, await modalState(service, overrides));
}

async function safeRenderLiveModal(service, ui, overrides = {}) {
    try { return await renderLiveModal(service, ui, overrides); }
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

function modalFrameFingerprint(frame = {}) {
    return JSON.stringify({
        title: frame.title,
        status: frame.status,
        body: frame.body,
        footer: frame.footer,
        documentRevision: frame.document?.revision
    });
}

async function doctorFrame(service, ui, viewState = {}) {
    try {
        viewState.activeTab = "doctor";
        viewState.doctor = await service.doctor();
        return await renderLiveModal(service, ui, {
            ...viewState,
            title: "Afterburner Black Box Doctor"
        });
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

async function exportFrame(service, ui, viewState = {}) {
    try {
        const result = await service.exportBundle({ maxRecords: 100 });
        viewState.activeTab = "export";
        viewState.exportResult = { ok: true, pathRef: hashDisplayPath(result.path), manifest: result.manifest };
        return await renderLiveModal(service, ui, {
            ...viewState,
            title: "Afterburner Black Box Export"
        });
    } catch {
        isolatedWarning("modal-canvas-export-failed");
        return {
            title: "Afterburner Black Box Export",
            status: "Export failed.",
            body: JSON.stringify({ ok: false, error: "black-box-export-failed" }, null, 2),
            footer: "Use /black-box-export for text export"
        };
    }
}

function subscribeLiveModal(service, controls, ui, viewState = {}) {
    if (typeof service.subscribeRecords !== "function" || typeof controls?.update !== "function") return () => {};
    let disposed = false;
    let timer = null;
    let refreshing = false;
    let pending = false;
    let lastRefreshAt = 0;
    let lastFrameFingerprint = "";

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
        try {
            const frame = await safeRenderLiveModal(service, ui, viewState);
            const fingerprint = modalFrameFingerprint(frame);
            if (fingerprint !== lastFrameFingerprint) {
                lastFrameFingerprint = fingerprint;
                await updateControls(controls, frame);
            }
        }
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
    if (typeof api.ui?.registerSurface === "function") return { target: api.ui, fn: api.ui.registerSurface };
    if (typeof api.registerSurface === "function") return { target: api, fn: api.registerSurface };
    if (typeof api.registerModalCanvas === "function") return { target: api, fn: api.registerModalCanvas };
    if (typeof api.ui?.registerModalCanvas === "function") return { target: api.ui, fn: api.ui.registerModalCanvas };
    return null;
}

function registerLiveModal(api, service) {
    const register = modalCanvasRegistrar(api);
    const ui = api?.ui;
    const modalViewState = { activeTab: "timeline", doctor: null, exportResult: null };
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
                    handler: async (_input, controls) => {
                        modalViewState.activeTab = "timeline";
                        return updateControls(controls, await safeRenderLiveModal(service, ui, modalViewState));
                    }
                },
                {
                    name: "doctor",
                    label: "Doctor",
                    key: "d",
                    handler: async (_input, controls) => {
                        modalViewState.activeTab = "doctor";
                        return updateControls(controls, await doctorFrame(service, ui, modalViewState));
                    }
                },
                {
                    name: "export",
                    label: "Export",
                    key: "e",
                    handler: async (_input, controls) => {
                        modalViewState.activeTab = "export";
                        return updateControls(controls, await exportFrame(service, ui, modalViewState));
                    }
                },
                {
                    name: "close",
                    label: "Close",
                    key: "q",
                    handler: async (_input, controls) => closeControls(controls)
                }
            ],
            open: async () => {
                modalViewState.activeTab = "timeline";
                return safeRenderLiveModal(service, ui, modalViewState);
            },
            render: async context => context.state,
            subscribe: async controls => subscribeLiveModal(service, controls, ui, modalViewState)
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
        const modal = registerLiveModal(api, service);
        let activationTimer = null;
        let activationWatcher = null;
        let activationPolling = false;
        const pollActivationRequests = async () => {
            if (!modal || activationPolling) return;
            activationPolling = true;
            try {
                let requests = [];
                try { requests = await service.consumeModalOpenRequests(); }
                catch { return; }
                for (const request of requests) {
                    if (request.surfaceId !== "afterburner-black-box-live") continue;
                    try {
                        await modal.open(request.input ?? {});
                        await service.completeModalOpenRequest(request, { ok: true });
                    } catch {
                        isolatedWarning("modal-canvas-open-failed");
                        await service.completeModalOpenRequest(request, { ok: false, error: "modal-canvas-open-failed" }).catch(() => {});
                    }
                }
            }
            finally {
                activationPolling = false;
            }
        };
        const subscribeActivationWakeups = async () => {
            if (typeof service.modalActivationWatch !== "function") return null;
            try {
                const target = await service.modalActivationWatch();
                const watcher = watch(target.directory, { persistent: false }, (_event, filename) => {
                    if (!filename || String(filename) === target.file) void pollActivationRequests();
                });
                watcher.unref?.();
                watcher.on?.("error", () => isolatedWarning("modal-activation-watch-failed"));
                return watcher;
            } catch {
                isolatedWarning("modal-activation-watch-unavailable");
                return null;
            }
        };
        if (modal) {
            activationTimer = setInterval(pollActivationRequests, MODAL_ACTIVATION_POLL_MS);
            activationTimer.unref?.();
            activationWatcher = await subscribeActivationWakeups();
            await pollActivationRequests();
        }
        const instance = {
            service,
            observer,
            modal,
            async dispose() {
                try {
                    if (typeof dispose === "function") await dispose();
                    else if (typeof dispose?.dispose === "function") await dispose.dispose();
                } catch {}
                try { if (activationTimer) clearInterval(activationTimer); } catch {}
                try { activationWatcher?.close?.(); } catch {}
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
