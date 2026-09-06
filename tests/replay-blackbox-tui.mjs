#!/usr/bin/env node
import { existsSync, readFileSync } from "node:fs";
import { join, resolve } from "node:path";
import process from "node:process";

const usage = `Usage: node tests\\replay-blackbox-tui.mjs <capture-dir> [options]

Replay or inspect artifacts produced by tests\\real-blackbox-modal-tui.mjs.

Options:
  --mode replay|steps|summary  replay: emit recorded terminal IO; steps: show operator viewports; summary: list files and validation state. Default: steps.
  --speed <n>                 Replay speed multiplier. Default: 4.
  --max-delay-ms <ms>         Cap inter-frame replay delay. Default: 250.
  --step <name-or-number>     In steps mode, show only one operator step.
  --no-clear                  Do not clear the terminal between step viewports.
  -h, --help                  Show this help.
`;

const option = name => {
  const inline = process.argv.find(value => value.startsWith(`${name}=`));
  if (inline) return inline.slice(name.length + 1);
  const index = process.argv.indexOf(name);
  return index >= 0 ? process.argv[index + 1] : undefined;
};
const hasFlag = name => process.argv.includes(name);

if (hasFlag("--help") || hasFlag("-h")) {
  process.stdout.write(usage);
  process.exit(0);
}

const args = process.argv.slice(2);
const optionNamesWithValues = new Set(["--mode", "--speed", "--max-delay-ms", "--step"]);
const positional = [];
for (let index = 0; index < args.length; index++) {
  const value = args[index];
  if (optionNamesWithValues.has(value)) {
    index++;
    continue;
  }
  if ([...optionNamesWithValues].some(name => value.startsWith(`${name}=`))) continue;
  if (value.startsWith("--")) continue;
  positional.push(value);
}
const captureDirArg = positional[0];
if (!captureDirArg) {
  process.stderr.write(usage);
  process.exit(2);
}

const captureDir = resolve(captureDirArg);
const positionalMode = ["replay", "steps", "summary"].includes(positional[1]) ? positional[1] : undefined;
const mode = option("--mode") ?? positionalMode ?? "steps";
const speed = Math.max(0.01, Number(option("--speed") ?? 4));
const maxDelayMs = Math.max(0, Number(option("--max-delay-ms") ?? 250));
const stepFilter = option("--step") ?? (positionalMode ? positional[2] : positional[1]);
const clearBetweenSteps = !hasFlag("--no-clear");

const file = name => join(captureDir, name);
const readJSON = name => JSON.parse(readFileSync(file(name), "utf8"));
const requireFile = name => {
  const path = file(name);
  if (!existsSync(path)) throw new Error(`Missing ${name} in ${captureDir}`);
  return path;
};

const sleep = ms => new Promise(resolve => setTimeout(resolve, ms));

function summary() {
  const result = readJSON("blackbox-modal-tui-result.json");
  const operator = readJSON("blackbox-modal-tui-operator.json");
  const files = [
    "blackbox-modal-tui.cast",
    "blackbox-modal-tui-io.jsonl",
    "blackbox-modal-tui-operator.json",
    "blackbox-modal-tui-operator.md",
    "blackbox-modal-tui-result.json",
    "blackbox-modal-tui-report.png"
  ].map(name => ({ name, present: existsSync(file(name)) }));
  process.stdout.write(`${JSON.stringify({
    captureDir,
    status: result.status,
    message: result.message,
    replayValidation: result.replayValidation,
    latencyBudget: result.latencyBudget,
    visualInspection: result.visualInspection,
    operatorSteps: operator.steps?.map(step => ({ name: step.name, key: step.key, latencyMs: step.latencyMs })) ?? [],
    files
  }, null, 2)}\n`);
}

async function replayCast() {
  const castPath = requireFile("blackbox-modal-tui.cast");
  const lines = readFileSync(castPath, "utf8").split(/\r?\n/).filter(Boolean);
  if (lines.length === 0) throw new Error("Cast file is empty");
  JSON.parse(lines.shift());
  let previous = 0;
  for (const line of lines) {
    const [time, stream, payload] = JSON.parse(line);
    const delay = Math.min(maxDelayMs, Math.max(0, ((time - previous) * 1000) / speed));
    if (delay > 0) await sleep(delay);
    previous = time;
    if (stream === "o") process.stdout.write(payload);
    else if (stream === "i") process.stdout.write(`\x1b[2m${payload.replace(/\x1b/g, "<Esc>").replace(/\r/g, "<Enter>")}\x1b[0m`);
  }
}

function printSteps() {
  const operator = readJSON("blackbox-modal-tui-operator.json");
  let steps = operator.steps ?? [];
  if (stepFilter) {
    const numeric = Number(stepFilter);
    steps = Number.isInteger(numeric)
      ? steps.filter((_, index) => index + 1 === numeric)
      : steps.filter(step => step.name.toLowerCase() === stepFilter.toLowerCase());
  }
  if (steps.length === 0) throw new Error(`No operator steps matched ${stepFilter ?? "capture"}`);
  for (const [index, step] of steps.entries()) {
    if (clearBetweenSteps) process.stdout.write("\x1b[2J\x1b[H");
    process.stdout.write(`# ${index + 1}. ${step.name}\n`);
    process.stdout.write(`key=${step.key} latencyMs=${step.latencyMs ?? "n/a"}\n`);
    process.stdout.write(`assertions=${(step.assertions ?? []).join("; ")}\n\n`);
    process.stdout.write(`${step.viewportText}\n`);
    if (index < steps.length - 1 && process.stdin.isTTY) {
      process.stdout.write("\nPress Enter for next step...");
      const buffer = Buffer.alloc(1);
      try { process.stdin.read(buffer); } catch {}
    }
  }
}

try {
  if (mode === "summary") summary();
  else if (mode === "replay") await replayCast();
  else if (mode === "steps") printSteps();
  else throw new Error(`Unknown mode '${mode}'`);
} catch (error) {
  process.stderr.write(`${error.message}\n`);
  process.exit(1);
}
