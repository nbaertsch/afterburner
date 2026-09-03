import process from "node:process";
import { resolve } from "node:path";
import { readdirSync, rmSync } from "node:fs";
import pty from "node-pty";

const executable = resolve(process.argv[2] ?? ".native-build/afterburn.exe");
const environment = { ...process.env };
delete environment.COPILOT_AGENT_SESSION_ID;
delete environment.COPILOT_LOADER_PID;
delete environment.COPILOT_SUPERVISED;
environment.COPILOT_RUNTIME_EXTENSION_DEBUG = "1";
const afterburnerHome = environment.AFTERBURNER_HOME ??
  resolve(environment.USERPROFILE, ".afterburner");
const launchHomesRoot = resolve(afterburnerHome, "launch-homes");
const existingLaunchHomes = new Set(
  (() => {
    try { return readdirSync(launchHomesRoot); } catch { return []; }
  })()
);

const child = pty.spawn(executable, ["--disable-extension", "steward-burn"], {
  name: "xterm-256color",
  cols: 140,
  rows: 40,
  cwd: process.cwd(),
  env: environment
});

let raw = "";
let openedPicker = false;
let filteredPicker = false;
let approved = false;
let trusted = false;
let finished = false;
let pickerInterval;

const cleanupLaunchHomes = () => {
  let entries;
  try { entries = readdirSync(launchHomesRoot); } catch { return; }
  for (const entry of entries) {
    if (!existingLaunchHomes.has(entry) && entry.startsWith("launch-")) {
      rmSync(resolve(launchHomesRoot, entry), { recursive: true, force: true });
    }
  }
};

const stripAnsi = value => value
  .replace(/\x1b\][^\x07]*(?:\x07|\x1b\\)/g, "")
  .replace(/\x1b\[[0-?]*[ -/]*[@-~]/g, "");

const finish = (error) => {
  if (finished) return;
  finished = true;
  clearTimeout(timeout);
  clearInterval(pickerInterval);
  if (error) {
    process.stderr.write(`${error}\n--- terminal output ---\n${stripAnsi(raw).slice(-12000)}\n`);
    child.kill();
    process.exitCode = 1;
    return;
  }
  child.write("\x1b");
  child.write("\x03");
  setTimeout(() => child.kill(), 500);
};

child.onData(data => {
  raw += data;
  const text = stripAnsi(raw);
  if (text.includes("Restore interrupted sessions")) {
    child.write("\x1b");
  }
  if (!approved && text.includes("wants elevated permissions")) {
    approved = true;
    child.write("\r");
  }
  if (!trusted && text.includes("Do you trust the files in this folder?")) {
    trusted = true;
    child.write("\r");
  }
  if (!openedPicker &&
      text.includes("[runtime-extension-host] registered picker adapter") &&
      text.includes("[runtime-extension-host] activated Afterburner extension 'byo-models'") &&
      text.includes("Registered 3 BYOModels model(s)") &&
      text.includes("Plan:")) {
    openedPicker = true;
    const openPicker = () => {
      if (!filteredPicker && !finished) {
        child.write("/model\r");
      }
    };
    setTimeout(openPicker, 3000);
    pickerInterval = setInterval(openPicker, 7000);
  }
  if (openedPicker && !filteredPicker &&
      (text.includes("Search models") || text.includes("Changes apply to this session only"))) {
    filteredPicker = true;
    clearInterval(pickerInterval);
    const interactionOffset = raw.length;
    setTimeout(() => child.write("colosseum-prod/gpt-5-5"), 1200);
    setTimeout(() => child.write("\x1b[D"), 3200);
    setTimeout(() => child.write("\x1b[D"), 4200);
    setTimeout(() => child.write("\x1b[C"), 5200);
    setTimeout(() => child.write("\x1b[C"), 6200);
    setTimeout(() => child.write("\x1b[C"), 7200);
    setTimeout(() => child.write("\x1b[C"), 8200);
    setTimeout(() => child.write("\t"), 9200);
    setTimeout(() => child.write("\x1b[D"), 10200);
    setTimeout(() => child.write("\x1b[D"), 11200);
    setTimeout(() => child.write("\x1b[D"), 12200);
    setTimeout(() => child.write("\t"), 13200);
    setTimeout(() => {
      const interaction = stripAnsi(raw.slice(interactionOffset));
      if (interaction.includes("Colosseum Prod GPT-5.5") &&
          interaction.includes("None") &&
          interaction.includes("Low") &&
          interaction.includes("Medium") &&
          interaction.includes("High") &&
          interaction.includes("Extra high") &&
          interaction.includes("reasoning effort") &&
          interaction.includes("context window") &&
          interaction.includes("1.05M") &&
          interaction.includes("768K") &&
          interaction.includes("512K") &&
          interaction.includes("256")) {
        const applicationOffset = raw.length;
        child.write("\r");
        setTimeout(() => {
          const application = stripAnsi(raw.slice(applicationOffset));
          if (application.includes("Model changed") &&
              application.includes("colosseum-prod/gpt-5-5")) {
            process.stdout.write("native-conpty-ok\n");
            finish();
          } else {
            finish("native model picker did not apply the selected BYOModels model");
          }
        }, 4000);
      } else {
        finish("native model picker did not expose every reasoning/context transition");
      }
    }, 15000);
  }
});

child.onExit(({ exitCode }) => {
  cleanupLaunchHomes();
  if (!finished) finish(`native Afterburner exited before picker validation (exit ${exitCode})`);
});

const timeout = setTimeout(() => finish("timed out waiting for native Afterburner model picker"), 90000);
