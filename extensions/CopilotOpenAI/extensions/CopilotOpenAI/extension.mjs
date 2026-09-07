import { readFile } from "node:fs/promises";
import { join } from "node:path";
import { createCanvas } from "@github/copilot-sdk";
import { joinSession } from "@github/copilot-sdk/extension";
import { CopilotSessionAdapter, createBridge, loadConfig } from "./bridge.mjs";
import { createManagementCanvas } from "./menu.mjs";

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

const managementCanvas = createManagementCanvas({
    createCanvas,
    getStatus: status,
    startBridge,
    stopBridge: async () => bridge?.stop?.(),
    configuredPath
});

async function handleMenuCommand() {
    try {
        await session.rpc.canvas.open({
            canvasId: "afterburner-copilot-openai-menu",
            instanceId: "afterburner-copilot-openai-menu",
            input: { openedFrom: "/copilot-openai" }
        });
    } catch (error) {
        await session.log(`Copilot OpenAI interactive menu could not open: ${error?.message ?? String(error)}`);
    }
}

session = await joinSession({
    commands: [
        {
            name: "copilot-openai",
            description: "Open the interactive Copilot OpenAI bridge management menu.",
            handler: handleMenuCommand
        }
    ],
    canvases: [managementCanvas]
});

if ((await readConfig()).enabled === true) {
    await startBridge();
    await session.log(formatStatus(lastStart));
}

export async function dispose() {
    await bridge?.stop?.();
}
