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
        const open = joinedSession?.rpc?.canvas?.open;
        if (typeof open !== "function") throw new Error("canvas open API unavailable");
        return open({
            extensionId: "black-box",
            canvasId,
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
