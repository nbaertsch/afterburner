import { readFile } from "node:fs/promises";
import { join } from "node:path";
import { createCanvas } from "@github/copilot-sdk";
import { joinSession } from "@github/copilot-sdk/extension";
import { CopilotSessionAdapter, createBridge, loadConfig } from "./bridge.mjs";

const afterburnerHome = process.env.AFTERBURNER_HOME ?? join(process.env.USERPROFILE ?? "", ".afterburner");
const configuredPath = process.env.AFTERBURNER_COPILOT_OPENAI_CONFIG?.trim() ||
    join(afterburnerHome, "config", "copilot-openai.json");

async function readConfig() {
    try {
        return JSON.parse(await readFile(configuredPath, "utf8"));
    } catch (error) {
        if (error?.code === "ENOENT") return {};
        throw new Error(`Read Copilot OpenAI bridge config: ${error.message}`);
    }
}

export async function activate() {
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

    const canvas = createCanvas({
        id: "afterburner-copilot-openai",
        displayName: "Afterburner Copilot OpenAI Bridge",
        description: "Shows localhost OpenAI-compatible bridge status for the active Copilot session.",
        actions: [{
            name: "snapshot",
            description: "Return sanitized Copilot OpenAI bridge status.",
            inputSchema: { type: "object", properties: {} },
            handler: async () => status()
        }],
        open: async () => ({
            title: "Copilot OpenAI Bridge",
            status: bridge?.state?.active ? `Listening at ${bridge.state.url}` : "not running"
        })
    });

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

    session = await joinSession({
        commands: [
            {
                name: "copilot-openai",
                description: "Show Copilot OpenAI-compatible bridge status.",
                handler: async () => session.log(formatStatus())
            },
            {
                name: "copilot-openai-start",
                description: "Start the localhost Copilot OpenAI-compatible bridge.",
                handler: async () => session.log(formatStatus(await startBridge()))
            },
            {
                name: "copilot-openai-stop",
                description: "Stop the localhost Copilot OpenAI-compatible bridge for this session.",
                handler: async () => {
                    await bridge?.stop?.();
                    await session.log("Copilot OpenAI bridge stopped.");
                }
            },
            {
                name: "copilot-openai-doctor",
                description: "Show sanitized Copilot OpenAI bridge diagnostics.",
                handler: async () => session.log(JSON.stringify(status(), null, 2))
            }
        ],
        canvases: [canvas]
    });

    if ((await readConfig()).enabled === true) {
        await startBridge();
        await session.log(formatStatus(lastStart));
    }

    return {
        async dispose() {
            await bridge?.stop?.();
        }
    };
}
