import { spawnSync } from "node:child_process";
import { mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import process from "node:process";
import { join, resolve } from "node:path";
import pty from "node-pty";

const afterburn = resolve(process.argv[2]);
const fakeCopilot = resolve(process.argv[3]);

const stripAnsi = value => value
  .replace(/\x1b\][^\x07]*(?:\x07|\x1b\\)/g, "")
  .replace(/\x1b\[[0-?]*[ -/]*[@-~]/g, "");

const createPackage = root => {
  const packageRoot = join(root, "packages");
  const packagePath = join(packageRoot, "1.0.83-3");
  const runtimePath = join(packagePath, "prebuilds", "win32-x64");
  mkdirSync(runtimePath, { recursive: true });
  writeFileSync(join(packagePath, "app.js"), "export {};", "utf8");
  writeFileSync(join(runtimePath, "runtime.node"), "fixture", "utf8");
  return packageRoot;
};

const createVerifiedBlackBoxRegistry = root => {
  const home = join(root, "afterburner");
  const normal = join(root, "normal");
  mkdirSync(normal, { recursive: true });
  const result = spawnSync(afterburn, ["install", "black-box"], {
    cwd: process.cwd(),
    encoding: "utf8",
    env: {
      ...process.env,
      AFTERBURNER_HOME: home,
      AFTERBURNER_NORMAL_COPILOT_HOME: normal,
      AFTERBURNER_DISABLE_BUILTIN_RELEASE_FETCH: "1"
    }
  });
  if (result.status !== 0) {
    throw new Error(`failed to create verified Black Box registry: status=${result.status} stdout=${result.stdout} stderr=${result.stderr}`);
  }
};

const lastRestoredScreen = raw => {
  const repaintPrefix = "\x1b[?25l\x1b[0m\x1b[H\x1b[2J";
  const index = raw.lastIndexOf(repaintPrefix);
  return stripAnsi(index >= 0 ? raw.slice(index) : raw);
};

const semanticModalState = raw => {
  const token = /\x1b\[\?1049([hl])|copilot-during-modal|stale raw replay should not render/g;
  const state = {
    sawAltEnter: false,
    sawAltExit: false,
    modalActive: false,
    leakedDuringModal: false,
    sawDuringAfterClose: false,
    renderedStaleRequest: false
  };
  for (const match of raw.matchAll(token)) {
    if (match[1] === "h") {
      state.sawAltEnter = true;
      state.modalActive = true;
      continue;
    }
    if (match[1] === "l") {
      state.sawAltExit = true;
      state.modalActive = false;
      continue;
    }
    if (match[0] === "copilot-during-modal") {
      if (state.modalActive) state.leakedDuringModal = true;
      if (!state.modalActive && state.sawAltExit) state.sawDuringAfterClose = true;
      continue;
    }
    if (match[0] === "stale raw replay should not render") {
      state.renderedStaleRequest = true;
    }
  }
  return state;
};

const baseEnvironment = (root, capturePath, packageRoot, extra = {}) => {
  const environment = {
    ...process.env,
    AFTERBURNER_HOME: join(root, "afterburner"),
    AFTERBURNER_NORMAL_COPILOT_HOME: join(root, "normal"),
    AFTERBURNER_COPILOT_EXECUTABLE: fakeCopilot,
    AFTERBURNER_COPILOT_PACKAGE_ROOTS: packageRoot,
    AFTERBURNER_ALLOW_UNPROFILED: "1",
    AFTERBURNER_SKIP_PREFLIGHT: "1",
    AFTERBURNER_TEST_CAPTURE: capturePath,
    AFTERBURNER_TEST_MODAL: "1",
    AFTERBURNER_TERMINAL_BROKER: "1",
    ...extra
  };
  delete environment.COPILOT_AGENT_SESSION_ID;
  delete environment.COPILOT_LOADER_PID;
  delete environment.COPILOT_SUPERVISED;
  return environment;
};

const assertCapture = (capture, { expectQuery = false, expectReopenIsolation = false, expectResize = false, expectNoChildInterrupt = false, expectRuntimeClient = false, expectActions = false } = {}) => {
  if (!capture.env?.AFTERBURNER_MODAL_BOOTSTRAP) {
    throw new Error(`modal broker bootstrap env was not forwarded\n${JSON.stringify(capture)}`);
  }
  if (capture.env.AFTERBURNER_MODAL_PIPE || capture.env.AFTERBURNER_MODAL_SECRET || capture.env.AFTERBURNER_MODAL_OWNER_EXTENSION_ID ||
      capture.env.AFTERBURNER_MODAL_CANVAS_ID || capture.env.AFTERBURNER_MODAL_SURFACE_ID) {
    throw new Error(`modal broker leaked reusable transport, secret, or implicit identity env\n${JSON.stringify(capture)}`);
  }
  if (!capture.args?.includes("--prefer-version") || capture.args.at(-1) !== "--version") {
    throw new Error(`argv was not preserved through broker launch\n${JSON.stringify(capture.args)}`);
  }
  if (expectRuntimeClient) {
    if (!capture.modal?.runtimeClient || capture.modal?.invalidRequest) {
      throw new Error(`runtime modal client did not use the native broker cleanly\n${JSON.stringify(capture.modal)}`);
    }
  } else if (!capture.modal?.staleRejected) {
    throw new Error(`stale modal token was not rejected\n${JSON.stringify(capture.modal)}`);
  }
  if (!capture.modal?.opened || !capture.modal?.updated) {
    throw new Error(`modal open/update live activity was not acknowledged\n${JSON.stringify(capture.modal)}`);
  }
  if (expectQuery) {
    const event = capture.modal?.event;
    if (event?.type !== "closed" || event?.id !== "black-box" || event?.generation !== 1) {
      throw new Error(`modal query did not receive generation-1 close reply\n${JSON.stringify(capture.modal)}`);
    }
  }
  if (expectActions) {
    const actions = capture.modal?.actionEvents ?? [];
    const observed = actions.map(event => `${event.type}:${event.actionName}:${event.key}`);
    const want = ["action:refresh:r", "action:doctor:d", "action:close:q"];
    if (JSON.stringify(observed) !== JSON.stringify(want)) {
      throw new Error(`modal action keys were not routed\n${JSON.stringify(capture.modal)}`);
    }
    const escape = capture.modal?.escapeEvent;
    if (escape?.type !== "close" || escape?.id !== "black-box" || escape?.generation !== 2 || escape?.key !== "escape") {
      throw new Error(`modal Escape key was not routed\n${JSON.stringify(capture.modal)}`);
    }
  }
  if (expectReopenIsolation) {
    const reopen = capture.modal?.reopen;
    if (!reopen?.opened || !reopen?.staleCloseRejected || !reopen?.stalePollRejected ||
        reopen?.event?.type !== "closed" || reopen?.event?.id !== "black-box" || reopen?.event?.generation !== 2) {
      throw new Error(`modal reopen did not isolate generations\n${JSON.stringify(capture.modal)}`);
    }
  }
  if (expectResize) {
    const sizes = capture.modal?.sizes ?? [];
    if (!sizes.includes("140x40") || !sizes.includes("160x44")) {
      throw new Error(`resize was not propagated into Copilot ConPTY\n${JSON.stringify(sizes)}`);
    }
  }
  if (expectNoChildInterrupt) {
    if ((capture.interrupts ?? 0) !== 0 || (capture.stdinCtrlCBytes ?? 0) !== 0) {
      throw new Error(`modal Ctrl+C reached Copilot\n${JSON.stringify({
        interrupts: capture.interrupts,
        stdinBytes: capture.stdinBytes,
        stdinCtrlCBytes: capture.stdinCtrlCBytes
      })}`);
    }
  }
};

const assertRepaintUsedResizedDimensions = raw => {
  const marker = "repaint-row-160x44";
  const screen = lastRestoredScreen(raw).replaceAll("\r", "");
  const markerLine = screen.split("\n").findIndex(line => line.includes(marker));
  if (markerLine < 0) {
    throw new Error("modal close repaint did not include resized-screen marker");
  }
  if (markerLine < 43) {
    throw new Error(`modal close repaint used stale height; marker was on line ${markerLine + 1}, want 44`);
  }
};

const assertModalCleanupRestoredTerminal = raw => {
  const semantic = semanticModalState(raw);
  if (semantic.sawAltEnter && !semantic.sawAltExit) {
    throw new Error(`modal cleanup left the alternate screen active\n${JSON.stringify(semantic)}`);
  }
  if (!stripAnsi(raw).includes("copilot-during-modal")) {
    throw new Error("child did not reach the open-modal cleanup path");
  }
};

const runScenario = ({
  name,
  extraEnv = {},
  expectedExit = 0,
  expectQuery = false,
  expectReopenIsolation = false,
  expectResize = false,
  expectNoChildInterrupt = false,
  expectRuntimeClient = false,
  expectActions = false,
  expectRepaint = false,
  expectCleanup = false,
  sendCtrlC = false
}) => new Promise((resolveScenario, rejectScenario) => {
  const root = join(tmpdir(), `afterburn-modal-${name}-${process.pid}-${Date.now()}`);
  const capturePath = join(root, "capture.json");
  const packageRoot = createPackage(root);
  createVerifiedBlackBoxRegistry(root);
  const child = pty.spawn(afterburn, ["--version"], {
    name: "xterm-256color",
    cols: 140,
    rows: 40,
    cwd: process.cwd(),
    env: baseEnvironment(root, capturePath, packageRoot, {
      AFTERBURNER_MODAL_PIPE: "stale-pipe",
      AFTERBURNER_MODAL_SECRET: "stale-secret",
      ...extraEnv
    })
  });

  let raw = "";
  let resized = false;
  let ctrlCSent = false;
  let actionStep = 0;
  let finished = false;

  const cleanup = () => rmSync(root, { recursive: true, force: true });
  const terminateChild = () => {
    try { process.kill(child.pid); } catch {}
  };
  const finish = error => {
    if (finished) return;
    finished = true;
    clearTimeout(timeout);
    if (error) {
      terminateChild();
      cleanup();
      rejectScenario(new Error(`${name}: ${error}\n--- terminal output ---\n${stripAnsi(raw).slice(-12000)}`));
      return;
    }
    cleanup();
    resolveScenario();
  };

  child.onData(data => {
    raw += data;
    const semantic = semanticModalState(raw);
    if (semantic.leakedDuringModal) {
      finish("Copilot output was replayed while the modal semantic screen was active");
      return;
    }
    if (semantic.renderedStaleRequest) {
      finish("unauthorized stale modal request rendered to the terminal");
      return;
    }
    const text = stripAnsi(raw);
    if (!resized && expectResize && text.includes("Afterburner Black Box Live")) {
      resized = true;
      child.resize(160, 44);
    }
    if (!ctrlCSent && sendCtrlC && text.includes("Afterburner Black Box Live")) {
      ctrlCSent = true;
      child.write("\x03");
    }
    if (expectActions) {
      if (actionStep === 0 && text.includes("live activity update frame")) {
        actionStep = 1;
        child.write("r");
      } else if (actionStep === 1 && text.includes("refresh action updated frame")) {
        actionStep = 2;
        child.write("d");
      } else if (actionStep === 2 && text.includes("doctor action observed")) {
        actionStep = 3;
        child.write("q");
      } else if (actionStep === 3 && text.includes("Afterburner Black Box Escape")) {
        actionStep = 4;
        child.write("\x1b");
      }
    }
  });

  child.onExit(({ exitCode }) => {
    if (finished) return;
    try {
      if (exitCode !== expectedExit) {
        throw new Error(`afterburn exited ${exitCode}, want ${expectedExit}`);
      }
      const capture = JSON.parse(readFileSync(capturePath, "utf8"));
      assertCapture(capture, { expectQuery, expectReopenIsolation, expectResize, expectNoChildInterrupt, expectRuntimeClient, expectActions });
      const semantic = semanticModalState(raw);
      const text = stripAnsi(raw);
      if (!text.includes("Afterburner Black Box Live") ||
          (!text.includes("live metadata frame") && !text.includes("live activity update frame"))) {
        throw new Error("modal frame was not rendered semantically");
      }
      if (expectReopenIsolation && !text.includes("Afterburner Black Box Reopened")) {
        throw new Error("reopened modal frame was not rendered semantically");
      }
      if (semantic.sawAltEnter && !semantic.sawAltExit) {
        throw new Error(`modal alternate screen was not restored\n${JSON.stringify(semantic)}`);
      }
      if (semantic.leakedDuringModal) {
        throw new Error(`modal output was replayed while active\n${JSON.stringify(semantic)}`);
      }
      if (expectRepaint) {
        assertRepaintUsedResizedDimensions(raw);
      }
      if (expectCleanup) {
        assertModalCleanupRestoredTerminal(raw);
      }
      if (sendCtrlC && !ctrlCSent) {
        throw new Error("modal Ctrl+C test never sent Ctrl+C while modal was visible");
      }
      if (expectActions && actionStep !== 4) {
        throw new Error(`modal action test did not send every key, stopped at step ${actionStep}`);
      }
      if (expectedExit === 0 && !extraEnv.AFTERBURNER_TEST_MODAL_EXIT_OPEN && !stripAnsi(raw).includes("copilot-after-modal")) {
        throw new Error("child did not complete normal modal close path");
      }
      finish();
    } catch (error) {
      finish(error.message);
    }
  });

  const timeout = setTimeout(() => finish("timed out waiting for native modal ConPTY flow"), 45000);
});

const scenarios = [
  {
    name: "live-poll",
    expectQuery: true
  },
  {
    name: "reopen-generation-isolation",
    extraEnv: {
      AFTERBURNER_TEST_MODAL_REOPEN: "1"
    },
    expectQuery: true,
    expectReopenIsolation: true
  },
  {
    name: "graceful-resize",
    extraEnv: {
      AFTERBURNER_TEST_MODAL_FORCE_EXIT: "1",
      AFTERBURNER_TEST_REPORT_SIZE: "1",
      AFTERBURNER_TEST_MODAL_REPAINT_MARKER: "1"
    },
    expectQuery: true,
    expectResize: true,
    expectRepaint: true
  },
  {
    name: "modal-ctrl-c-isolated",
    extraEnv: {
      AFTERBURNER_TEST_MODAL_FORCE_EXIT: "1",
      AFTERBURNER_TEST_CAPTURE_INPUT: "1"
    },
    sendCtrlC: true,
    expectQuery: true,
    expectNoChildInterrupt: true
  },
  {
    name: "black-box-actions",
    extraEnv: {
      AFTERBURNER_TEST_MODAL_ACTIONS: "1",
      AFTERBURNER_TEST_MODAL_FORCE_EXIT: "1"
    },
    expectActions: true,
    expectQuery: true
  },
  {
    name: "normal-exit-open-modal",
    extraEnv: {
      AFTERBURNER_TEST_MODAL_EXIT_OPEN: "1",
      AFTERBURNER_TEST_MODAL_FORCE_EXIT: "1"
    },
    expectCleanup: true
  },
  {
    name: "crash-open-modal",
    extraEnv: {
      AFTERBURNER_TEST_MODAL_CRASH_OPEN: "1"
    },
    expectedExit: 44,
    expectCleanup: true
  }
];

const failures = [];
for (const scenario of scenarios) {
  try {
    await runScenario(scenario);
  } catch (error) {
    failures.push(error?.stack ?? String(error));
  }
}

if (failures.length > 0) {
  process.stderr.write(`${failures.length} native modal scenario(s) failed\n${failures.join("\n\n")}\n`);
  process.exit(1);
}

process.stdout.write("native-modal-conpty-ok\n");
process.exit(0);
