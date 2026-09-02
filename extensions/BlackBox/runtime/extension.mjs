import { startBlackBoxService } from "../lib/service.mjs";

const INSTANCE = Symbol.for("afterburner.black-box.runtime");

function isolatedWarning(code) {
    if (process.env.COPILOT_RUNTIME_EXTENSION_DEBUG === "1") {
        process.stderr.write(`[black-box] ${code}\n`);
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
        const instance = {
            service,
            observer,
            async dispose() {
                try {
                    if (typeof dispose === "function") await dispose();
                    else if (typeof dispose?.dispose === "function") await dispose.dispose();
                } catch {}
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
