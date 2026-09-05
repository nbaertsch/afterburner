import * as copilotSdk from "@github/copilot-sdk";
import { joinSession } from "@github/copilot-sdk/extension";
import { startSessionExtension } from "../../lib/session-extension.mjs";

try {
    const createCanvas = typeof copilotSdk.createCanvas === "function" ? copilotSdk.createCanvas : undefined;
    let joinedSession;
    const join = async config => {
        joinedSession = await joinSession(config);
        return joinedSession;
    };
    const openModalCanvas = async (canvasId, input = {}) => {
        const canvasRpc = joinedSession?.rpc?.canvas;
        if (typeof canvasRpc?.open !== "function") throw new Error("canvas open API unavailable");
        const catalog = typeof canvasRpc.list === "function" ? await canvasRpc.list() : { canvases: [] };
        const declared = Array.isArray(catalog?.canvases) ? catalog.canvases : [];
        const candidate = declared.find(canvas => canvas?.canvasId === canvasId || canvas?.id === canvasId);
        if (!candidate) {
            throw new Error(`canvas not declared: ${canvasId}; available=${declared.map(canvas =>
                `${canvas?.extensionId ?? canvas?.providerId ?? "?"}/${canvas?.canvasId ?? canvas?.id ?? "?"}`).join(",")}`);
        }
        return canvasRpc.open({
            extensionId: candidate.extensionId ?? candidate.providerId,
            canvasId: candidate.canvasId ?? candidate.id,
            instanceId: `${canvasId}-session`,
            input
        });
    };
    const instance = await startSessionExtension({ createCanvas, openModalCanvas, joinSession: join });
    for (const signal of ["SIGINT", "SIGTERM"]) {
        process.once(signal, async () => {
            await instance.dispose().catch(() => {});
            process.exit(0);
        });
    }
} catch {
    process.stderr.write("Warning: Black Box session extension failed in isolation.\n");
}
