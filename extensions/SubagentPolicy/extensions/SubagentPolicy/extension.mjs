import { readFile } from "node:fs/promises";
import { joinSession } from "@github/copilot-sdk/extension";
import { completeModalOpenRequest, completePolicyAction, consumeModalOpenRequests, consumePolicyActions, requestModalOpen, writePolicyState } from "./policy-ipc.mjs";
import { configPath } from "./names.mjs";
import { loadPolicyConfig, sdkSettings } from "./policy.mjs";

let session;
let config;
let activePolicy = null;
let lastError = null;
let actionPump;
let disposed = false;

async function readConfig() {
    const path = configPath();
    try {
        return { path, config: loadPolicyConfig(JSON.parse(await readFile(path, "utf8"))) };
    } catch (error) {
        if (error?.code === "ENOENT") return { path, config: loadPolicyConfig() };
        throw new Error(`Read subagent policy config: ${error.message}`);
    }
}

function snapshot(detail = undefined) {
    return {
        activePolicy,
        defaultPolicy: config?.defaultPolicy ?? null,
        configPath: configPath(),
        policies: config?.policies ?? {},
        detail,
        error: lastError
    };
}

async function publish(detail) {
    return writePolicyState(snapshot(detail));
}

async function applyPolicy(id) {
    const policy = config?.policies?.[id];
    if (!policy) throw new Error(`unknown policy: ${id}`);
    await session.tools.updateSubagentSettings({ settings: sdkSettings(policy) });
    activePolicy = id;
    lastError = null;
    return publish(`${policy.displayName} applied`);
}

async function clearPolicy() {
    await session.tools.updateSubagentSettings({ settings: null });
    activePolicy = null;
    lastError = null;
    return publish("session override cleared");
}

async function reloadConfig() {
    const loaded = await readConfig();
    config = loaded.config;
    lastError = null;
    if (activePolicy && !config.policies[activePolicy]) await clearPolicy();
    return publish("configuration reloaded");
}

async function handleAction(request) {
    try {
        let state;
        if (request.action === "clear") state = await clearPolicy();
        else if (request.action === "reload") state = await reloadConfig();
        else if (request.action === "solo" || request.action === "conservative" || request.action === "balanced" || request.action === "burst") {
            state = await applyPolicy(request.action);
        } else throw new Error("unknown action");
        await completePolicyAction(request, { ok: true, state });
    } catch (error) {
        lastError = String(error?.message ?? error);
        await completePolicyAction(request, { ok: false, error: lastError, state: await publish("action failed") });
    }
}

async function pollActions() {
    for (const request of await consumePolicyActions().catch(() => [])) await handleAction(request);
}

async function handleCommand() {
    const state = await publish("interactive menu open");
    const opened = await requestModalOpen({ activePolicy: state.activePolicy });
    if (!opened.ok) await session.log(`Subagent Policy menu unavailable: ${opened.error ?? "not acknowledged"}`);
}

const joined = await joinSession({
    commands: [{
        name: "subagent-policy",
        description: "Open the session subagent policy menu.",
        handler: handleCommand
    }],
    canvases: []
});
session = joined.session ?? joined;
config = (await readConfig()).config;
actionPump = setInterval(() => { void pollActions(); }, 100);
actionPump.unref?.();
if (!disposed && config.defaultPolicy) {
    try {
        await applyPolicy(config.defaultPolicy);
        await session.log(`Subagent Policy applied default '${config.defaultPolicy}'.`);
    } catch (error) {
        lastError = String(error?.message ?? error);
        await publish("default policy failed");
        await session.log(`Subagent Policy default failed: ${lastError}`);
    }
} else {
    await publish("ready");
}

export async function dispose() {
    disposed = true;
    if (actionPump) clearInterval(actionPump);
    await joined?.dispose?.();
}
