import { readFile } from "node:fs/promises";
import { join } from "node:path";
import { joinSession } from "@github/copilot-sdk/extension";
import { CopilotSessionAdapter, createBridge, loadConfig } from "./bridge.mjs";
import { completeBridgeActionRequest, consumeBridgeActionRequests, requestModalOpen, writeBridgeState } from "./modal-ipc.mjs";

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

async function stopBridge() {
    await bridge?.stop?.();
    await writeBridgeState(status(), "bridge stopped");
}

async function handleBridgeAction(request) {
    try {
        if (request.action === "start") {
            await startBridge();
            return completeBridgeActionRequest(request, { ok: true, state: await writeBridgeState(status(), "bridge ready") });
        }
        if (request.action === "stop") {
            await stopBridge();
            return completeBridgeActionRequest(request, { ok: true, state: status() });
        }
        if (request.action === "status") {
            return completeBridgeActionRequest(request, { ok: true, state: await writeBridgeState(status(), "status refreshed") });
        }
        if (request.action === "doctor") {
            return completeBridgeActionRequest(request, { ok: true, state: await writeBridgeState(status(), "diagnostics refreshed") });
        }
        return completeBridgeActionRequest(request, { ok: false, error: "unknown-action", state: status() });
    } catch (error) {
        return completeBridgeActionRequest(request, { ok: false, error: String(error?.message ?? error), state: status() });
    }
}

let actionPump;
async function pollBridgeActions() {
    const requests = await consumeBridgeActionRequests().catch(() => []);
    for (const request of requests) await handleBridgeAction(request);
}

function startActionPump() {
    if (actionPump) return;
    actionPump = setInterval(() => { void pollBridgeActions(); }, 100);
    actionPump.unref?.();
}

async function handleMenuCommand() {
    const snapshot = await startBridge();
    await writeBridgeState(snapshot, "interactive menu open");
    const opened = await requestModalOpen({ openedFrom: "/copilot-openai" });
    if (opened.ok !== true) {
        await session.log(`Copilot OpenAI native menu unavailable: ${opened.error ?? "not acknowledged"}`);
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
    canvases: []
});
startActionPump();

if ((await readConfig()).enabled === true) {
    await startBridge();
    await writeBridgeState(status(), "auto-started");
    await session.log(formatStatus(lastStart));
}

export async function dispose() {
    if (actionPump) clearInterval(actionPump);
    await bridge?.stop?.();
}
