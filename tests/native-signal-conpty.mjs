import { mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import process from "node:process";
import pty from "node-pty";

const afterburn = resolve(process.argv[2]);
const fakeCopilot = resolve(process.argv[3]);
const root = join(tmpdir(), `afterburn-signal-${process.pid}-${Date.now()}`);
const packageRoot = join(root, "packages");
const packagePath = join(packageRoot, "1.0.83-3");
const runtimePath = join(packagePath, "prebuilds", "win32-x64");
const capturePath = join(root, "capture.json");
mkdirSync(runtimePath, { recursive: true });
writeFileSync(join(packagePath, "app.js"), "export {};");
writeFileSync(join(runtimePath, "runtime.node"), "fixture");

const environment = {
  ...process.env,
  AFTERBURNER_HOME: join(root, "afterburner"),
  AFTERBURNER_NORMAL_COPILOT_HOME: join(root, "normal"),
  AFTERBURNER_COPILOT_EXECUTABLE: fakeCopilot,
  AFTERBURNER_COPILOT_PACKAGE_ROOTS: packageRoot,
  AFTERBURNER_ALLOW_UNPROFILED: "1",
  AFTERBURNER_SKIP_PREFLIGHT: "1",
  AFTERBURNER_TEST_CAPTURE: capturePath,
  AFTERBURNER_TEST_WAIT_FOR_INTERRUPT: "1",
  AFTERBURNER_TERMINAL_BROKER: "1"
};
delete environment.COPILOT_AGENT_SESSION_ID;
delete environment.COPILOT_LOADER_PID;
delete environment.COPILOT_SUPERVISED;

const resume = "7c21f952-884d-498d-937b-47b4ce7b3b19";
const child = pty.spawn(afterburn, [`--resume=${resume}`], {
  name: "xterm-256color",
  cols: 140,
  rows: 40,
  cwd: process.cwd(),
  env: environment
});

let output = "";
let interrupted = false;
let finished = false;
const cleanup = () => rmSync(root, { recursive: true, force: true });
const terminateChild = () => {
  try { process.kill(child.pid); } catch {}
};
const fail = message => {
  if (finished) return;
  finished = true;
  clearTimeout(timeout);
  terminateChild();
  cleanup();
  process.stderr.write(`${message}\n${output}\n`);
  setTimeout(() => process.exit(1), 50);
};

child.onData(data => {
  output += data;
  if (!interrupted && output.includes("fake-copilot-ready")) {
    interrupted = true;
    const capture = JSON.parse(readFileSync(capturePath, "utf8"));
    if (!capture.args.includes(`--resume=${resume}`)) {
      fail("resume argument was not forwarded exactly");
      return;
    }
    if (capture.env.AFTERBURNER_MODAL_PIPE || capture.env.AFTERBURNER_MODAL_SECRET || capture.env.AFTERBURNER_MODAL_OWNER_EXTENSION_ID ||
        capture.env.AFTERBURNER_MODAL_CANVAS_ID || capture.env.AFTERBURNER_MODAL_SURFACE_ID) {
      fail(`terminal broker leaked reusable modal transport, secret, or implicit identity\n${JSON.stringify(capture)}`);
      return;
    }
    child.write("\x03");
  }
});

child.onExit(({ exitCode }) => {
  if (finished) return;
  finished = true;
  clearTimeout(timeout);
  cleanup();
  if (!output.includes("fake-copilot-interrupted")) {
    process.stderr.write(`child did not receive forwarded interrupt (exit ${exitCode})\n${output}\n`);
    setTimeout(() => process.exit(1), 50);
    return;
  }
  if (exitCode !== 130) {
    process.stderr.write(`forwarded interrupt produced exit ${exitCode}, want 130\n${output}\n`);
    setTimeout(() => process.exit(1), 50);
    return;
  }
  process.stdout.write("native-signal-conpty-ok\n");
  setTimeout(() => process.exit(0), 50);
});

const timeout = setTimeout(() => fail("timed out waiting for Ctrl+C propagation"), 30000);
