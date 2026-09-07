import { readFile } from "node:fs/promises";
import { join } from "node:path";
import { createCanvas } from "@github/copilot-sdk";
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

function menuStatus(snapshot = status(), detail = undefined) {
    const active = snapshot?.active === true;
    return [
        `State: ${active ? "running" : "stopped"}`,
        `Endpoint: ${snapshot?.endpoint ?? "not allocated"}`,
        `Models: ${snapshot?.modelCount ?? 0}`,
        `Requests: ${snapshot?.requestCount ?? 0}`,
        `Errors: ${snapshot?.errorCount ?? 0}`,
        detail ? `Detail: ${detail}` : undefined
    ].filter(Boolean).join(" · ");
}

function menuBody(snapshot = status(), detail = undefined) {
    return [
        "Copilot OpenAI Bridge management",
        "=================================",
        menuStatus(snapshot, detail),
        `API key: ${snapshot?.apiKeyRequired ? "required" : "not required"}`,
        `Config: ${configuredPath}`,
        "",
        "Interactive actions:",
        "• Start / reuse bridge",
        "• Stop bridge",
        "• Refresh status",
        "• Doctor diagnostics"
    ].join("\n");
}

async function canvasState(detail = undefined) {
    const snapshot = status();
    return {
        title: "Copilot OpenAI Bridge",
        status: menuStatus(snapshot, detail),
        body: menuBody(snapshot, detail),
        diagnostics: snapshot
    };
}

const managementCanvas = createCanvas({
    id: "afterburner-copilot-openai-menu",
    displayName: "Copilot OpenAI Bridge",
    description: "Interactive management menu for the localhost OpenAI-compatible Copilot bridge.",
    actions: [
        {
            name: "start",
            label: "Start",
            description: "Start or reuse the localhost bridge.",
            handler: async () => canvasState(`bridge ready at ${(await startBridge()).endpoint ?? "not allocated"}`)
        },
        {
            name: "stop",
            label: "Stop",
            description: "Stop this session's bridge listener.",
            handler: async () => {
                await bridge?.stop?.();
                return canvasState("bridge stopped");
            }
        },
        {
            name: "status",
            label: "Status",
            description: "Refresh bridge status.",
            handler: async () => canvasState("status refreshed")
        },
        {
            name: "doctor",
            label: "Doctor",
            description: "Show sanitized bridge diagnostics.",
            handler: async () => ({ ...(await canvasState("diagnostics refreshed")), diagnostics: status() })
        }
    ],
    open: async () => {
        const snapshot = await startBridge();
        return {
            title: "Copilot OpenAI Bridge",
            status: menuStatus(snapshot, "interactive menu open"),
            body: menuBody(snapshot, "interactive menu open"),
            diagnostics: status()
        };
    }
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
