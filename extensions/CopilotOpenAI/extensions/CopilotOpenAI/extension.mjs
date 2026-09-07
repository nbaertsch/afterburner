import { readFile } from "node:fs/promises";
import { join } from "node:path";
import { joinSession } from "@github/copilot-sdk/extension";
import { CopilotSessionAdapter, createBridge, loadConfig } from "./bridge.mjs";

const afterburnerHome = process.env.AFTERBURNER_HOME ?? join(process.env.USERPROFILE ?? "", ".afterburner");
const configuredPath = process.env.AFTERBURNER_COPILOT_OPENAI_CONFIG?.trim() ||
    join(afterburnerHome, "config", "copilot-openai.json");

function commandText(input) {
    if (typeof input === "string") return input.trim();
    if (typeof input?.arguments === "string") return input.arguments.trim();
    if (typeof input?.text === "string") return input.text.trim();
    return "";
}

async function readConfig() {
    try {
        return JSON.parse(await readFile(configuredPath, "utf8"));
    } catch (error) {
        if (error?.code === "ENOENT") return {};
        throw new Error(`Read Copilot OpenAI bridge config: ${error.message}`);
    }
}

let session;
let bridge;
let lastStart;

function status() {
    return {
        configPath: configuredPath,
        ...bridge?.healthSnapshot?.(),
        apiKeyRequired: Boolean(bridge?.state?.apiKey)
    };
}

async function startBridge() {
    if (!bridge) {
        const config = loadConfig(await readConfig());
        bridge = createBridge({
            adapter: new CopilotSessionAdapter(session, config),
            config,
            logger: console,
            identity: { sessionId: process.env.COPILOT_AGENT_SESSION_ID }
        });
    }
    lastStart = await bridge.start();
    return lastStart;
}

function formatStatus(snapshot = status()) {
    if (!snapshot?.active) return "Copilot OpenAI bridge is not running.";
    return `Copilot OpenAI bridge listening at ${snapshot.url}; requests=${snapshot.requestCount}; errors=${snapshot.errorCount}; models=${snapshot.modelCount}.`;
}

function menuText(snapshot = status(), detail = undefined) {
    const active = snapshot?.active === true;
    return [
        "Copilot OpenAI Bridge management",
        "=================================",
        `State   : ${active ? "running" : "stopped"}`,
        `Endpoint: ${snapshot?.endpoint ?? "not allocated"}`,
        `Models  : ${snapshot?.modelCount ?? 0}`,
        `Requests: ${snapshot?.requestCount ?? 0}`,
        `Errors  : ${snapshot?.errorCount ?? 0}`,
        `API key : ${snapshot?.apiKeyRequired ? "required" : "not required"}`,
        `Config  : ${configuredPath}`,
        detail ? `Detail  : ${detail}` : "",
        "",
        "Use one command:",
        "  /copilot-openai   Open this management panel and start/reuse the bridge",
        "",
        "Panel actions performed:",
        "  ✓ status refreshed",
        "  ✓ localhost bridge started or reused",
        "  ✓ sanitized diagnostics displayed below"
    ].filter(Boolean).join("\n");
}

async function handleMenuCommand(input) {
    const action = commandText(input).replace(/^\/?copilot-openai\b/i, "").trim().split(/\s+/).filter(Boolean)[0]?.toLowerCase() || "open";
    if (action !== "open" && action !== "menu" && action !== "help") {
        await session.log(menuText(status(), `unsupported typed action '${action}'; use /copilot-openai to open the managed bridge panel`));
        return;
    }
    const snapshot = await startBridge();
    const diagnostics = status();
    const summary = `Ready: /copilot-openai endpoint=${diagnostics.endpoint ?? "not allocated"} active=${diagnostics.active === true} diagnostics=sanitized`;
    await session.log(`${menuText(snapshot, "bridge ready")}\n\nDiagnostics:\n${JSON.stringify(diagnostics, null, 2)}\n\n${summary}`);
}

session = await joinSession({
    commands: [
        {
            name: "copilot-openai",
            description: "Open and operate the Copilot OpenAI bridge management menu.",
            handler: handleMenuCommand
        }
    ],
    canvases: []
});

if ((await readConfig()).enabled === true) {
    await startBridge();
    await session.log(formatStatus(lastStart));
}

export async function dispose() {
    await bridge?.stop?.();
}
