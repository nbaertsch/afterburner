import assert from "node:assert/strict";
import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { spawnSync } from "node:child_process";
import test from "node:test";

const repoRoot = fileURLToPath(new URL("..", import.meta.url));
const script = join(repoRoot, "tests", "replay-blackbox-tui.mjs");

async function createCaptureFixture(t) {
  const dir = await mkdtemp(join(tmpdir(), "afterburn-replay-test-"));
  await mkdir(dir, { recursive: true });
  t.after(async () => {
    await rm(dir, { recursive: true, force: true });
  });
  const operator = {
    schemaVersion: 1,
    generatedAt: "2026-01-01T00:00:00.000Z",
    afterburn: "afterburn.exe",
    terminal: { columns: 140, rows: 40, kind: "Windows ConPTY via node-pty" },
    goal: "fixture",
    inputCount: 2,
    outputChunkCount: 2,
    steps: [
      { name: "open modal", key: "/black-box-modal", latencyMs: 12, assertions: ["modal visible"], viewportText: "Afterburner Black Box Live\nNative Afterburner modal overlay" },
      { name: "export", key: "e", latencyMs: 7, assertions: ["export visible"], viewportText: "Afterburner Black Box Export\npathRef manifest recordCount" }
    ]
  };
  const result = {
    schemaVersion: 1,
    status: "passed",
    message: "fixture passed",
    replayValidation: { passed: true, inputCount: 2, outputChunkCount: 2, missingInputs: [], missingSteps: [], emptyViewports: [], missingFiles: [] },
    latencyBudget: { passed: true },
    visualInspection: { passed: true }
  };
  const cast = [
    JSON.stringify({ version: 2, width: 140, height: 40, timestamp: 1, env: { TERM: "xterm-256color" } }),
    JSON.stringify([0.0, "o", "Afterburner Black Box Live\n"]),
    JSON.stringify([0.1, "i", "e"]),
    JSON.stringify([0.2, "o", "Afterburner Black Box Export\n"])
  ].join("\n") + "\n";
  await writeFile(join(dir, "blackbox-modal-tui-result.json"), JSON.stringify(result), "utf8");
  await writeFile(join(dir, "blackbox-modal-tui-operator.json"), JSON.stringify(operator), "utf8");
  await writeFile(join(dir, "blackbox-modal-tui-operator.md"), "# fixture\n", "utf8");
  await writeFile(join(dir, "blackbox-modal-tui.cast"), cast, "utf8");
  await writeFile(join(dir, "blackbox-modal-tui-io.jsonl"), "{}\n", "utf8");
  return dir;
}

function runReplay(args) {
  return spawnSync(process.execPath, [script, ...args], { encoding: "utf8", cwd: repoRoot });
}

test("TUI replay viewer summarizes capture artifacts", async t => {
  const dir = await createCaptureFixture(t);
  const result = runReplay([dir, "summary"]);
  assert.equal(result.status, 0, result.stderr);
  const summary = JSON.parse(result.stdout);
  assert.equal(summary.status, "passed");
  assert.equal(summary.replayValidation.passed, true);
  assert.deepEqual(summary.operatorSteps.map(step => step.name), ["open modal", "export"]);
});

test("TUI replay viewer shows selected operator viewport", async t => {
  const dir = await createCaptureFixture(t);
  const result = runReplay([dir, "steps", "export", "--no-clear"]);
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /# 1\. export/);
  assert.match(result.stdout, /Afterburner Black Box Export/);
  assert.doesNotMatch(result.stdout, /open modal/);
});

test("TUI replay viewer replays cast output", async t => {
  const dir = await createCaptureFixture(t);
  const result = runReplay([dir, "replay", "--speed", "100", "--max-delay-ms", "1"]);
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /Afterburner Black Box Live/);
  assert.match(result.stdout, /Afterburner Black Box Export/);
});
