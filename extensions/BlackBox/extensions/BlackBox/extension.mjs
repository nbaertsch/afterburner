import * as copilotSdk from "@github/copilot-sdk";
import { joinSession } from "@github/copilot-sdk/extension";
import { startSessionExtension } from "../../lib/session-extension.mjs";

try {
    const createCanvas = typeof copilotSdk.createCanvas === "function" ? copilotSdk.createCanvas : undefined;
    const openModalCanvas = typeof copilotSdk.openModalCanvas === "function" ? copilotSdk.openModalCanvas : undefined;
    const instance = await startSessionExtension({ createCanvas, openModalCanvas, joinSession });
    for (const signal of ["SIGINT", "SIGTERM"]) {
        process.once(signal, async () => {
            await instance.dispose().catch(() => {});
            process.exit(0);
        });
    }
} catch {
    process.stderr.write("Warning: Black Box session extension failed in isolation.\n");
}
