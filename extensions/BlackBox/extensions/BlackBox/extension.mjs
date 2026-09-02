import { createCanvas } from "@github/copilot-sdk";
import { joinSession } from "@github/copilot-sdk/extension";
import { startSessionExtension } from "../../lib/session-extension.mjs";

try {
    const instance = await startSessionExtension({ createCanvas, joinSession });
    for (const signal of ["SIGINT", "SIGTERM"]) {
        process.once(signal, async () => {
            await instance.dispose().catch(() => {});
            process.exit(0);
        });
    }
} catch {
    process.stderr.write("Warning: Black Box session extension failed in isolation.\n");
}
