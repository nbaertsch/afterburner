import { cpSync, existsSync, mkdirSync, readdirSync } from "node:fs";
import { join } from "node:path";
import process from "node:process";
import pty from "node-pty";

const stripAnsi = value => value
  .replace(/\x1b\][^\x07]*(?:\x07|\x1b\\)/g, "")
  .replace(/\x1b\[[0-?]*[ -/]*[@-~]/g, "")
  .replace(/\x1b[()][A-Za-z0-9]/g, "");

const sleep = ms => new Promise(resolve => setTimeout(resolve, ms));

export async function bootstrapExperimentalCopilotProfile({
  afterburn,
  cwd,
  env,
  managedCopilotHome,
  label = "experimental-profile-bootstrap",
  timeoutMs = 60_000
}) {
  const child = pty.spawn(afterburn, ["--name", `${label}-${process.pid}-${Date.now()}`, "--no-remote"], {
    name: "xterm-256color",
    cols: 140,
    rows: 40,
    cwd,
    env
  });
  let raw = "";
  let closed = false;
  let trusted = false;
  let restored = false;
  let approved = false;
  let terminalSetupDeclined = false;
  let nativeAppSelectionMovedAt = 0;
  let nativeAppDeclined = false;
  child.onData(data => { raw += data; });
  child.onExit(() => { closed = true; });

  const deadline = Date.now() + timeoutMs;
  try {
    while (Date.now() < deadline) {
      const text = stripAnsi(raw);
      const recent = text.slice(-5000);
      if (!trusted && /Do you trust the files in this folder/i.test(recent)) {
        trusted = true;
        child.write("\r");
      }
      if (!restored && /Restore interrupted sessions/i.test(recent)) {
        restored = true;
        child.write("\x1b");
      }
      if (!approved && /wants elevated permissions/i.test(recent)) {
        approved = true;
        child.write("\r");
      }
      if (!terminalSetupDeclined && /Set up terminal for multi-line input support|Would you like to add this key binding/i.test(recent)) {
        terminalSetupDeclined = true;
        child.write("\x1b");
      }
      if (!nativeAppSelectionMovedAt && /Yes, install[\s\S]{0,200}No, thanks/i.test(recent)) {
        nativeAppSelectionMovedAt = Date.now();
        child.write("\x1b[C");
      } else if (!nativeAppDeclined && nativeAppSelectionMovedAt && Date.now() - nativeAppSelectionMovedAt >= 500) {
        nativeAppDeclined = true;
        child.write("\r");
      }
      const promptReady = /\/ commands|tab next tab|\? help|@ files · # issues/i.test(recent);
      if (promptReady && /Staff mode activated! Restart the app to enable staff features/i.test(text)) {
        await sleep(500);
        const launchRoot = join(managedCopilotHome, "..", "launch-homes");
        const launchHome = readdirSync(launchRoot, { withFileTypes: true })
          .filter(entry => entry.isDirectory())
          .map(entry => join(launchRoot, entry.name))
          .find(path => existsSync(join(path, "config.json")));
        if (!launchHome) throw new Error("experimental profile bootstrap did not produce config.json");
        mkdirSync(managedCopilotHome, { recursive: true });
        cpSync(join(launchHome, "config.json"), join(managedCopilotHome, "config.json"));
        return;
      }
      if (closed) throw new Error("Copilot exited during experimental profile bootstrap");
      await sleep(100);
    }
    throw new Error(`timed out bootstrapping experimental Copilot profile; tail=${JSON.stringify(stripAnsi(raw).replace(/\s+/g, " ").trim().slice(-1200))}`);
  } finally {
    if (!closed) {
      try { process.kill(child.pid); } catch {}
    }
  }
}
