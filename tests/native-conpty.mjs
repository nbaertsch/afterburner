import process from "node:process";
import { execFileSync } from "node:child_process";
import { createServer } from "node:http";
import { tmpdir } from "node:os";
import { cpSync, mkdirSync, readFileSync, readdirSync, rmSync, statSync, writeFileSync } from "node:fs";
import { dirname, delimiter, join, resolve } from "node:path";
import pty from "node-pty";

const executable = resolve(process.argv[2] ?? ".native-build/afterburn.exe");
const repoRoot = process.cwd();
const root = join(tmpdir(), `afterburn-native-conpty-${process.pid}-${Date.now()}`);
const afterburnerHome = join(root, "afterburner");
const normalCopilotHome = join(root, "normal-copilot");
const isolatedUserHome = join(root, "user");
const workspace = join(root, "workspace");
const byoModelsConfig = join(afterburnerHome, "config", "byomodels.json");
const providerServer = createServer((request, response) => {
  response.writeHead(200, { "content-type": "application/json" });
  response.end(JSON.stringify({ id: "native-conpty-fixture", object: "list", data: [] }));
});
await new Promise(resolveServer => providerServer.listen(0, "127.0.0.1", resolveServer));
const providerBaseUrl = `http://127.0.0.1:${providerServer.address().port}`;

const stripAnsi = value => value
  .replace(/\x1b\][^\x07]*(?:\x07|\x1b\\)/g, "")
  .replace(/\x1b\[[0-?]*[ -/]*[@-~]/g, "");

const platformPackageRoot = () => join("pkg", "win32-x64");

const existingDirectory = path => {
  try { return statSync(path).isDirectory(); } catch { return false; }
};

const discoverPackageRoots = () => {
  const roots = new Set();
  for (const raw of (process.env.AFTERBURNER_COPILOT_PACKAGE_ROOTS ?? "").split(delimiter)) {
    if (raw.trim()) roots.add(resolve(raw.trim()));
  }
  const candidates = [
    process.env.USERPROFILE && join(process.env.USERPROFILE, ".copilot", platformPackageRoot()),
    process.env.LOCALAPPDATA && join(process.env.LOCALAPPDATA, "copilot", platformPackageRoot())
  ].filter(Boolean);
  for (const candidate of candidates) {
    if (existingDirectory(candidate)) roots.add(resolve(candidate));
  }
  return [...roots];
};

const writeJson = (path, value) => writeFileSync(path, `${JSON.stringify(value, null, 2)}\n`, "utf8");

const findCopilotSdk = () => {
  for (const packageRoot of discoverPackageRoots()) {
    let versions;
    try {
      if (!statSync(packageRoot).isDirectory()) continue;
      versions = [...new Set(readdirSync(packageRoot))];
    } catch {
      continue;
    }
    for (const version of versions.sort().reverse()) {
      const sdkPath = join(packageRoot, version, "copilot-sdk");
      if (existingDirectory(sdkPath)) return sdkPath;
    }
  }
  return undefined;
};

const installCopilotSdkShim = activePath => {
  const sdkSource = findCopilotSdk();
  if (!sdkSource) return;
  const sdkTarget = join(dirname(activePath), "node_modules", "@github", "copilot-sdk");
  mkdirSync(sdkTarget, { recursive: true });
  cpSync(sdkSource, sdkTarget, { recursive: true });
  writeJson(join(sdkTarget, "package.json"), {
    name: "@github/copilot-sdk",
    type: "module",
    exports: {
      ".": "./index.js",
      "./extension": "./extension.js"
    }
  });
};

const autoApprovePluginPermissions = activePath => {
  const entrypoint = join(activePath, "extensions", "BYOModels", "extension.mjs");
  let source = readFileSync(entrypoint, "utf8");
  source = source.replace(
    'import { joinSession } from "@github/copilot-sdk/extension";',
    'import { approveAll, joinSession } from "@github/copilot-sdk/extension";'
  );
  source = source.replace(
    "joinSession({",
    "joinSession({\n        onPermissionRequest: approveAll,"
  );
  writeFileSync(entrypoint, source, "utf8");
};

const isolatedEnvironment = (extra = {}) => {
  const environment = { ...process.env };
  for (const key of Object.keys(environment)) {
    if (/^(COPILOT_HOME|COPILOT_AGENT_SESSION_ID|COPILOT_LOADER_PID|COPILOT_SUPERVISED|AFTERBURNER_HOME|AFTERBURNER_NORMAL_COPILOT_HOME|AFTERBURNER_BYOMODELS_CONFIG|AFTERBURNER_DISABLED_EXTENSIONS|AFTERBURNER_COPILOT_EXECUTABLE)$/i.test(key)) {
      delete environment[key];
    }
  }
  environment.USERPROFILE = isolatedUserHome;
  environment.HOME = isolatedUserHome;
  environment.LOCALAPPDATA = join(root, "localappdata");
  environment.APPDATA = join(root, "appdata");
  environment.AFTERBURNER_HOME = afterburnerHome;
  environment.AFTERBURNER_NORMAL_COPILOT_HOME = normalCopilotHome;
  const packageRoots = discoverPackageRoots();
  if (packageRoots.length > 0) {
    environment.AFTERBURNER_COPILOT_PACKAGE_ROOTS = packageRoots.join(delimiter);
  }
  return { ...environment, ...extra };
};

const prepareIsolatedEnvironment = () => {
  mkdirSync(workspace, { recursive: true });
  mkdirSync(join(afterburnerHome, "config"), { recursive: true });
  mkdirSync(join(afterburnerHome, "copilot-home"), { recursive: true });
  mkdirSync(normalCopilotHome, { recursive: true });
  mkdirSync(isolatedUserHome, { recursive: true });
  mkdirSync(join(root, "localappdata"), { recursive: true });
  mkdirSync(join(root, "appdata"), { recursive: true });

  const packageSource = join(root, "byo-models-uat-source");
  cpSync(join(repoRoot, "extensions", "BYOModels"), packageSource, { recursive: true });
  const manifestPath = join(packageSource, "afterburner.json");
  const manifest = JSON.parse(readFileSync(manifestPath, "utf8"));
  manifest.id = "byo-models-uat";
  manifest.visibility = "private";
  writeJson(manifestPath, manifest);
  const archive = join(root, "byo-models-uat.zip");
  for (const args of [["extension", "pack", packageSource, archive], ["extension", "install", archive], ["extension", "enable", "byo-models-uat"]]) {
    execFileSync(executable, args, {
      cwd: workspace,
      env: isolatedEnvironment({ AFTERBURNER_DISABLE_BUILTIN_RELEASE_FETCH: "1" }),
      stdio: "pipe"
    });
  }
  const registry = JSON.parse(readFileSync(join(afterburnerHome, "registry.json"), "utf8"));
  const activePath = registry.extensions?.["byo-models-uat"]?.activePath;
  if (activePath) {
    installCopilotSdkShim(activePath);
  }
  writeJson(join(afterburnerHome, "copilot-home", "settings.json"), {
    experimental: true,
    enabledPlugins: { "afterburner-byomodels": true },
    extensions: { disabledExtensions: [] }
  });
  writeJson(join(afterburnerHome, "copilot-home", "config.json"), {
    appTipShown: true,
    askedSetupTerminals: ["windows-terminal"]
  });
  writeJson(byoModelsConfig, {
    version: 1,
    name: "Native ConPTY BYOModels",
    contextWindowOptions: [256000, 512000, 768000, 1050000],
    providers: [{
      name: "colosseum-prod",
      type: "azure",
      baseUrl: providerBaseUrl,
      wireApi: "responses",
      requestCompatibility: { maxInputItemIdLength: 64, proxyPort: 61951 }
    }, {
      name: "colosseum-alt",
      type: "azure",
      baseUrl: providerBaseUrl,
      wireApi: "responses",
      requestCompatibility: { maxInputItemIdLength: 64, proxyPort: 61951 }
    }],
    models: [
      { provider: "colosseum-prod", id: "gpt-5-5", name: "Colosseum Prod GPT-5.5", modelId: "gpt-5.5", wireModel: "gpt-5-5" },
      { provider: "colosseum-prod", id: "gpt-5-6-sol", name: "Colosseum Prod GPT-5.6 Sol", modelId: "gpt-5.6-sol", wireModel: "gpt-5-6-sol" },
      { provider: "colosseum-prod", id: "gpt-5-6-luna", name: "Colosseum Prod GPT-5.6 Luna", modelId: "gpt-5.6-luna", wireModel: "gpt-5-6-luna" },
      { provider: "colosseum-prod", id: "sol-reasoning", name: "Colosseum Prod Sol Reasoning", modelId: "gpt-5.6-sol", wireModel: "gpt-5-6-sol" },
      { provider: "colosseum-prod", id: "luna-long", name: "Colosseum Prod Luna Long", modelId: "gpt-5.6-luna", wireModel: "gpt-5-6-luna" },
      { provider: "colosseum-alt", id: "gpt-5-5", name: "Colosseum Alt GPT-5.5", modelId: "gpt-5.5", wireModel: "gpt-5-5" },
      { provider: "colosseum-alt", id: "gpt-5-6-sol", name: "Colosseum Alt GPT-5.6 Sol", modelId: "gpt-5.6-sol", wireModel: "gpt-5-6-sol" },
      { provider: "colosseum-alt", id: "gpt-5-6-luna", name: "Colosseum Alt GPT-5.6 Luna", modelId: "gpt-5.6-luna", wireModel: "gpt-5-6-luna" },
      { provider: "colosseum-alt", id: "luna-long", name: "Colosseum Alt Luna Long", modelId: "gpt-5.6-luna", wireModel: "gpt-5-6-luna" }
    ]
  });
};

prepareIsolatedEnvironment();

const environment = isolatedEnvironment({
  AFTERBURNER_BYOMODELS_CONFIG: byoModelsConfig,
  AFTERBURNER_SKIP_PREFLIGHT: "1",
  COPILOT_RUNTIME_EXTENSION_DEBUG: "1"
});

const child = pty.spawn(executable, [], {
  name: "xterm-256color",
  cols: 140,
  rows: 40,
  cwd: workspace,
  env: environment
});

let raw = "";
let openedPicker = false;
let filteredPicker = false;
let approved = false;
let trusted = false;
let terminalSetupDeclined = false;
let finished = false;
let pickerInterval;

const cleanup = (attempt = 0) => {
  try { providerServer.close(); } catch {}
  if (process.env.AFTERBURNER_KEEP_NATIVE_TMP === "1") return;
  try {
    rmSync(root, { recursive: true, force: true });
  } catch (error) {
    if (attempt >= 10 || error?.code !== "EBUSY") throw error;
    setTimeout(() => cleanup(attempt + 1), 250);
  }
};
const terminateChild = () => {
  try { child.write("\x1b"); } catch {}
  try { child.write("\x03"); } catch {}
  setTimeout(() => { try { process.kill(child.pid); } catch {} }, 500);
};

const finish = (error) => {
  if (finished) return;
  finished = true;
  clearTimeout(timeout);
  clearInterval(pickerInterval);
  if (error) {
    const text = stripAnsi(raw);
    const commandIndex = text.indexOf("Changes apply to this session only");
    const commandContext = commandIndex >= 0
      ? `\n--- first model-command context ---\n${text.slice(Math.max(0, commandIndex - 1200), commandIndex + 2400)}\n`
      : "";
    process.stderr.write(`${error}${commandContext}\n--- terminal output ---\n${text.slice(-12000)}\n`);
    terminateChild();
    process.exitCode = 1;
    setTimeout(() => {
      cleanup();
      process.exit(1);
    }, 1000);
    return;
  }
  child.write("\x1b");
  setTimeout(() => {
    terminateChild();
    setTimeout(() => {
      cleanup();
      process.exit(0);
    }, 750);
  }, 500);
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
  if (!terminalSetupDeclined && text.includes("Set up terminal for multi-line input support")) {
    terminalSetupDeclined = true;
    child.write("\x1b");
  }
  if (!openedPicker &&
      text.includes("[runtime-extension-host] registered picker adapter") &&
      /\[runtime-extension-host\] activated Afterburner extension 'byo-models(?:-uat)?'/.test(text) &&
      (text.includes("← open sidebar") || text.includes("/ commands") || text.includes("Plan:"))) {
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
      text.includes("Changes apply to this session only") &&
      text.includes("GPT-5.6 Sol")) {
    filteredPicker = true;
    clearInterval(pickerInterval);
    const byomodelsReady = text.includes("Registered 9 BYOModels model(s)") &&
      text.includes("colosseum-prod/gpt-5-5 <- gpt-5.5") &&
      text.includes("colosseum-alt/luna-long <- gpt-5.6-luna");
    if (!byomodelsReady) {
      finish("BYOModels registration did not complete; refusing built-in model-picker fallback");
      return;
    }
    const targetQuery = "colosseum-prod/gpt-5-5";
    const expectedModel = "colosseum-prod/gpt-5-5";
    const interactionOffset = raw.length;
    setTimeout(() => child.write(targetQuery), 1200);
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
      const current = stripAnsi(raw);
      const requirements = [
        [current, "Registered 9 BYOModels model(s)"],
        [current, "colosseum-prod/gpt-5-5 <- gpt-5.5"],
        [current, "colosseum-alt/luna-long <- gpt-5.6-luna"],
        [current, "Colosseum Prod GPT-5.5"],
        [current, "Colosseum Prod GPT-5.6 Sol"],
        [current, "Colosseum Prod GPT-5.6 Luna"],
        [interaction, "None"],
        [interaction, "Low"],
        [interaction, "Medium"],
        [interaction, "High"],
        [interaction, "Extra"],
        [interaction, "reasoning effort"],
        [interaction, "context window"],
        [interaction, "1.05M"],
        [interaction, "768K"],
        [interaction, "512K"],
        [interaction, "256K"]
      ];
      const missing = requirements.filter(([haystack, needle]) => !haystack.includes(needle)).map(([, needle]) => needle);
      if (missing.length === 0) {
        const applicationOffset = raw.length;
        child.write("\r");
        setTimeout(() => {
          const application = stripAnsi(raw.slice(applicationOffset));
          if (application.includes("Model changed") &&
              application.includes(expectedModel)) {
            process.stdout.write("native-conpty-ok\n");
            finish();
          } else {
            finish("native model picker did not apply the selected BYOModels model");
          }
        }, 4000);
      } else {
        finish(`native model picker did not expose every reasoning/context transition; missing ${missing.join(", ")}`);
      }
    }, 15000);
  }
});

child.onExit(({ exitCode }) => {
  cleanup();
  if (!finished) finish(`native Afterburner exited before picker validation (exit ${exitCode})`);
});

const timeout = setTimeout(() => finish("timed out waiting for native Afterburner model picker"), 90000);
