import { spawn } from "node:child_process";
import { installSidecarRuntime } from "../dist/afterburner-ui.mjs";

installSidecarRuntime({
  echo: async (payload) => ({ payload, envKeys: Object.keys(process.env).sort() }),
  spawnChild: async () => {
    const child = spawn(process.execPath, ["-e", "setInterval(() => {}, 1000)"], { stdio: "ignore", detached: process.platform === "win32" });
    child.unref();
    return { pid: child.pid };
  },
  crash: async () => process.exit(23)
});
