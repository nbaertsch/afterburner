import { readFile } from "node:fs/promises";
import { joinSession } from "@github/copilot-sdk/extension";
import { CopilotSessionAdapter, createBridge, loadConfig } from "./bridge.mjs";
import { completeBridgeActionRequest, consumeBridgeActionRequests, requestModalOpen, writeBridgeState } from "./modal-ipc.mjs";
import { configuredPathCandidates, displayConfigPath } from "./names.mjs";

let activeConfigPath = displayConfigPath();

async function readConfig() {
    for (const candidate of configuredPathCandidates()) {
        try {
            const config = JSON.parse(await readFile(candidate, "utf8"));
            activeConfigPath = candidate;
            return config;
        } catch (error) {
            if (error?.code === "ENOENT") continue;
            throw new Error(`Read OpenAI server config: ${error.message}`);
        }
    }
    activeConfigPath = displayConfigPath();
    return {};
}

let session;
let bridge;
let lastStart;

function status() {
    return {
        configPath: activeConfigPath,
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
    if (!snapshot?.active) return "OpenAI server is not running.";
    return `OpenAI server listening at ${snapshot.url}; requests=${snapshot.requestCount}; errors=${snapshot.errorCount}; models=${snapshot.modelCount}.`;
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
}

async function handleMenuCommand(input = {}) {
    const invokedAs = typeof input === "string" ? input.trim() : (input?.name ?? input?.command ?? "openai-server");
    const snapshot = await startBridge();
    await writeBridgeState(snapshot, "interactive menu open");
    const opened = await requestModalOpen({ openedFrom: invokedAs === "copilot-openai" ? "/copilot-openai" : "/openai-server" });
    if (opened.ok !== true) {
        await session.log(`OpenAI Server native menu unavailable: ${opened.error ?? "not acknowledged"}`);
    }
}

session = await joinSession({
    commands: [
        {
            name: "openai-server",
            description: "Open the interactive OpenAI Server management menu.",
            handler: handleMenuCommand
        },
        {
            name: "copilot-openai",
            description: "Deprecated alias for /openai-server.",
            hidden: true,
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
