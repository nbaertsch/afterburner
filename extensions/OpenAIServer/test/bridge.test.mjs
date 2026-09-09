import assert from "node:assert/strict";
import { createServer } from "node:http";
import { mkdir, readFile, readdir, utimes, writeFile } from "node:fs/promises";
import { test } from "node:test";
import { dirname, join } from "node:path";
import { tmpdir } from "node:os";
import { randomBytes } from "node:crypto";
import { CopilotSessionAdapter, createBridge, loadConfig, normalizeModel } from "../extensions/OpenAIServer/bridge.mjs";
import { MENU_ID, menuActions, modalFrame } from "../extensions/OpenAIServer/menu.mjs";
import { completeBridgeActionRequest, completeModalOpenRequest, consumeBridgeActionRequests, consumeModalOpenRequests, readBridgeState, requestBridgeAction, requestModalOpen, writeBridgeState } from "../extensions/OpenAIServer/modal-ipc.mjs";
import { atomicWriteFile, routeAckDirectory, routeQueuePath, waitForRouteAck } from "../shared/route-ipc.mjs";
import { configuredPathCandidates, displayConfigPath } from "../extensions/OpenAIServer/names.mjs";

const ROUTE_A = "routeAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA";
const ROUTE_B = "routeBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB";

async function withBridge(adapter, fn, config = { port: 0 }, identity = { routeId: ROUTE_A }) {
    const bridge = createBridge({ adapter, config, identity });
    await bridge.start();
    try {
        return await fn(bridge);
    } finally {
        await bridge.stop();
    }
}

async function json(url, options = {}) {
    const response = await fetch(url, options);
    return { response, body: await response.json() };
}

async function withRouteEnv(home, route, fn) {
    const previousHome = process.env.AFTERBURNER_HOME;
    const previousRoute = process.env.AFTERBURNER_SESSION_ROUTE;
    process.env.AFTERBURNER_HOME = home;
    if (route) process.env.AFTERBURNER_SESSION_ROUTE = route;
    else delete process.env.AFTERBURNER_SESSION_ROUTE;
    try { return await fn(); }
    finally {
        if (previousHome === undefined) delete process.env.AFTERBURNER_HOME;
        else process.env.AFTERBURNER_HOME = previousHome;
        if (previousRoute === undefined) delete process.env.AFTERBURNER_SESSION_ROUTE;
        else process.env.AFTERBURNER_SESSION_ROUTE = previousRoute;
    }
}

test("loadConfig enforces localhost binding and valid ports", () => {
    assert.equal(loadConfig({ port: 0 }).host, "127.0.0.1");
    assert.throws(() => loadConfig({ host: "0.0.0.0" }), /localhost/);
    assert.throws(() => loadConfig({ port: 70000 }), /port/);
});

test("loadConfig accepts legacy environment at lower precedence", () => {
    assert.equal(loadConfig({}, { AFTERBURNER_COPILOT_OPENAI_PORT: "1234" }).port, 1234);
    assert.equal(loadConfig({}, { AFTERBURNER_OPENAI_SERVER_PORT: "2345", AFTERBURNER_COPILOT_OPENAI_PORT: "1234" }).port, 2345);
    const config = loadConfig({}, { AFTERBURNER_COPILOT_OPENAI_API_KEY: "legacy-key" });
    assert.equal(config.requireApiKey, true);
    assert.equal(config.apiKey, "legacy-key");
});

test("configuration paths prefer canonical names and retain legacy fallback", async () => {
    const home = join(tmpdir(), `afterburner-openai-config-${process.pid}-${randomBytes(4).toString("hex")}`);
    const env = { AFTERBURNER_HOME: home };
    assert.deepEqual(configuredPathCandidates(env).slice(-2), [join(home, "config", "openai-server.json"), join(home, "config", "copilot-openai.json")]);
    await mkdir(join(home, "config"), { recursive: true });
    await writeFile(join(home, "config", "copilot-openai.json"), "{}", "utf8");
    assert.equal(displayConfigPath(env), join(home, "config", "copilot-openai.json"));
});

test("session extension exposes one primary management command and legacy alias", async () => {
    const source = await readFile(new URL("../extensions/OpenAIServer/extension.mjs", import.meta.url), "utf8");
    const wrapper = await readFile(new URL("../com.github.copilot/extensions/OpenAIServer/extension.mjs", import.meta.url), "utf8");
    const legacyWrapper = await readFile(new URL("../com.github.copilot/extensions/CopilotOpenAI/extension.mjs", import.meta.url), "utf8");
    const menuSource = await readFile(new URL("../extensions/OpenAIServer/menu.mjs", import.meta.url), "utf8");
    const wrappers = await readdir(new URL("../com.github.copilot/extensions", import.meta.url), { withFileTypes: true });
    assert.doesNotMatch(source, /createCanvas/);
    assert.match(source, /requestModalOpen/);
    assert.match(source, /canvases:\s*\[\]/);
    assert.match(source, /setInterval\(\(\) => \{ void pollBridgeActions\(\); \}, 100\)/);
    assert.doesNotMatch(source, /actionPump\.unref/);
    const commandNames = [...source.matchAll(/name:\s*"([^"]+)"/g)].map(match => match[1]).filter(name => /^(?:openai-server|copilot-openai)/.test(name));
    assert.deepEqual(commandNames, ["openai-server", "copilot-openai"]);
    assert.match(source, /Deprecated alias for \/openai-server/);
    for (const removed of ["openai-server-start", "openai-server-stop", "openai-server-doctor", "copilot-openai-start", "copilot-openai-stop", "copilot-openai-doctor"]) {
        assert.doesNotMatch(source, new RegExp(`name:\\s*"${removed}"`));
    }
    assert.doesNotMatch(source, /session\.rpc\.canvas\.open/);
    assert.match(source, /requestModalOpen/);
    assert.match(menuSource, /MENU_ID = CANONICAL_ID/);
    for (const action of ["start", "stop", "status", "doctor"]) {
        assert.match(menuSource, new RegExp(`name:\\s*"${action}"`));
    }
    assert.doesNotMatch(menuSource, /Interactive actions|Keyboard shortcuts|Use Tab\/Shift\+Tab|s start · x stop/);
    assert.doesNotMatch(source, /\/(?:openai-server|copilot-openai) (?:start|stop|status|doctor)/);
    assert.deepEqual(wrappers.filter(entry => entry.isDirectory()).map(entry => entry.name).sort(), ["CopilotOpenAI", "OpenAIServer"]);
    assert.equal(wrapper.trim(), "export * from \"../../../extensions/OpenAIServer/extension.mjs\";");
    assert.equal(legacyWrapper.trim(), "export * from \"../../../extensions/OpenAIServer/extension.mjs\";");
    assert.doesNotMatch(wrapper, /activateExtension\(|export const instance/);
    assert.match(source, /session\s*=\s*await joinSession/);
    assert.doesNotMatch(source, /export async function activate/);
});

test("native modal frame reports status without inline shortcut duplication", () => {
    const snapshot = { active: true, endpoint: "127.0.0.1:41425", modelCount: 2, requestCount: 3, errorCount: 0, apiKeyRequired: false };
    const frame = modalFrame({ snapshot, configuredPath: "C:\\afterburner\\config\\openai-server.json", detail: "interactive menu open" });
    assert.equal(MENU_ID, "openai-server");
    assert.equal(frame.title, "OpenAI Server");
    assert.match(frame.status, /running/);
    assert.match(frame.body, /Endpoint: 127\.0\.0\.1:41425/);
    assert.doesNotMatch(frame.body, /Keyboard shortcuts|Use Tab|Interactive actions|\[s\] Start|s start · x stop/);
    assert.equal(frame.footer, undefined);
    assert.deepEqual(menuActions.map(action => action.name), ["start", "stop", "status", "doctor", "close"]);
    assert.deepEqual(menuActions.map(action => action.key), ["s", "x", "r", "d", "q"]);
});

test("modal IPC ignores legacy unscoped state and requests when route scoped", async () => {
    const originalHome = process.env.AFTERBURNER_HOME;
    const originalRoute = process.env.AFTERBURNER_SESSION_ROUTE;
    const home = join(tmpdir(), `afterburner-openai-server-${process.pid}-${randomBytes(4).toString("hex")}`);
    process.env.AFTERBURNER_HOME = home;
    process.env.AFTERBURNER_SESSION_ROUTE = ROUTE_A;
    try {
        const legacyStateDir = join(home, "state", "copilot-openai");
        await mkdir(legacyStateDir, { recursive: true });
        await writeFile(join(legacyStateDir, "bridge-state.json"), JSON.stringify({ schemaVersion: 1, active: true, endpoint: "127.0.0.1:7" }), "utf8");
        assert.notEqual((await readBridgeState()).endpoint, "127.0.0.1:7");
        await writeFile(join(legacyStateDir, "modal-activation.jsonl"), `${JSON.stringify({ schemaVersion: 1, requestId: "legacy", surfaceId: "copilot-openai", createdAt: new Date().toISOString() })}\n`, "utf8");
        assert.deepEqual(await consumeModalOpenRequests(), []);
    } finally {
        if (originalHome === undefined) delete process.env.AFTERBURNER_HOME;
        else process.env.AFTERBURNER_HOME = originalHome;
        if (originalRoute === undefined) delete process.env.AFTERBURNER_SESSION_ROUTE;
        else process.env.AFTERBURNER_SESSION_ROUTE = originalRoute;
    }
});

test("modal IPC isolates route-owned action records from legacy and wrong-route consumers", async () => {
    const originalHome = process.env.AFTERBURNER_HOME;
    const originalRoute = process.env.AFTERBURNER_SESSION_ROUTE;
    const home = join(tmpdir(), `afterburner-openai-actions-${process.pid}-${randomBytes(4).toString("hex")}`);
    process.env.AFTERBURNER_HOME = home;
    try {
        const stateDir = join(home, "state", "openai-server");
        await mkdir(stateDir, { recursive: true });
        const first = { schemaVersion: 2, routeId: ROUTE_A, requestId: "first-action", surfaceId: "openai-server", action: "status", createdAt: new Date().toISOString() };
        const second = { schemaVersion: 2, routeId: ROUTE_B, requestId: "second-action", surfaceId: "openai-server", action: "doctor", createdAt: new Date().toISOString() };
        await writeFile(join(stateDir, "modal-actions.jsonl"), `${JSON.stringify(first)}\n`, "utf8");
        process.env.AFTERBURNER_SESSION_ROUTE = ROUTE_A;
        const firstPath = routeQueuePath(stateDir, "modal-actions.jsonl");
        await mkdir(dirname(firstPath), { recursive: true });
        await writeFile(firstPath, `${JSON.stringify(first)}\n`, "utf8");
        process.env.AFTERBURNER_SESSION_ROUTE = ROUTE_B;
        const secondPath = routeQueuePath(stateDir, "modal-actions.jsonl");
        await mkdir(dirname(secondPath), { recursive: true });
        await writeFile(secondPath, `${JSON.stringify(second)}\n`, "utf8");
        assert.deepEqual((await consumeBridgeActionRequests()).map(request => request.requestId), [second.requestId]);
        process.env.AFTERBURNER_SESSION_ROUTE = ROUTE_A;
        assert.deepEqual((await consumeBridgeActionRequests()).map(request => request.requestId), [first.requestId]);
        assert.match(await readFile(join(stateDir, "modal-actions.jsonl"), "utf8"), /first-action/);
    } finally {
        if (originalHome === undefined) delete process.env.AFTERBURNER_HOME;
        else process.env.AFTERBURNER_HOME = originalHome;
        if (originalRoute === undefined) delete process.env.AFTERBURNER_SESSION_ROUTE;
        else process.env.AFTERBURNER_SESSION_ROUTE = originalRoute;
    }
});

test("modal IPC startup cleanup removes stale artifacts without deleting live route work", async () => {
    const home = join(tmpdir(), `afterburner-openai-cleanup-${process.pid}-${randomBytes(4).toString("hex")}`);
    await withRouteEnv(home, ROUTE_A, async () => {
        const stateDir = join(home, "state", "openai-server");
        await mkdir(stateDir, { recursive: true });
        const stale = join(stateDir, "stale.lock");
        await writeFile(stale, "old", "utf8");
        const old = new Date(Date.now() - 172_800_000);
        await utimes(stale, old, old);
        const pending = requestBridgeAction("status", { timeoutMs: 2000 });
        await new Promise(resolve => setTimeout(resolve, 50));
        await assert.rejects(readFile(stale, "utf8"), /ENOENT/);
        const requests = await consumeBridgeActionRequests();
        assert.equal(requests.length, 1);
        await completeBridgeActionRequest(requests[0], { ok: true, state: { cleaned: true } });
        assert.equal((await pending).ok, true);
    });
});

test("modal IPC does not steal live claimed-but-unacknowledged actions after the lease", async () => {
    const home = join(tmpdir(), `afterburner-openai-claim-${process.pid}-${randomBytes(4).toString("hex")}`);
    await withRouteEnv(home, ROUTE_A, async () => {
        const pending = requestBridgeAction("status", { timeoutMs: 5000 });
        await new Promise(resolve => setTimeout(resolve, 50));
        const firstClaim = await consumeBridgeActionRequests();
        assert.equal(firstClaim.length, 1);
        assert.deepEqual(await consumeBridgeActionRequests(), []);
        await new Promise(resolve => setTimeout(resolve, 2100));
        assert.deepEqual(await consumeBridgeActionRequests(), []);
        await completeBridgeActionRequest(firstClaim[0], { ok: true, state: { active: true } });
        assert.equal((await pending).ok, true);
    });
});

test("modal IPC retries partial acks and rejects stale replayed acks", async () => {
    const home = join(tmpdir(), `afterburner-openai-ack-${process.pid}-${randomBytes(4).toString("hex")}`);
    await withRouteEnv(home, ROUTE_A, async () => {
        const stateDir = join(home, "state", "openai-server");
        const request = { schemaVersion: 2, routeId: ROUTE_A, requestId: "ackrequest", surfaceId: "openai-server", createdAt: new Date().toISOString() };
        const ackDir = join(stateDir, "modal-activation-acks");
        const routeAckDir = routeAckDirectory(ackDir, ROUTE_A);
        await mkdir(routeAckDir, { recursive: true });
        await writeFile(join(routeAckDir, "ackrequest.json"), "{", "utf8");
        const pending = waitForRouteAck(ackDir, request, { timeoutMs: 2000 });
        await new Promise(resolve => setTimeout(resolve, 50));
        await atomicWriteFile(join(routeAckDir, "ackrequest.json"), JSON.stringify({ schemaVersion: 2, routeId: ROUTE_A, requestId: "ackrequest", surfaceId: "openai-server", ok: true, completedAt: "2000-01-01T00:00:00.000Z" }) + "\n");
        await new Promise(resolve => setTimeout(resolve, 50));
        await atomicWriteFile(join(routeAckDir, "ackrequest.json"), JSON.stringify({ schemaVersion: 2, routeId: ROUTE_A, requestId: "ackrequest", surfaceId: "openai-server", ok: true, completedAt: new Date().toISOString() }) + "\n");
        assert.equal((await pending).ok, true);
    });
});

test("modal IPC routes modal opens, acknowledgements, and bridge state", async () => {
    const home = join(tmpdir(), `afterburner-openai-route-${process.pid}-${randomBytes(4).toString("hex")}`);
    await withRouteEnv(home, ROUTE_A, async () => {
        const pending = requestModalOpen({ timeoutMs: 2000 });
        const queue = routeQueuePath(join(home, "state", "openai-server"), "modal-activation.jsonl", {
            env: { AFTERBURNER_SESSION_ROUTE: ROUTE_A }
        });
        const queueDeadline = Date.now() + 2000;
        while (true) {
            const body = await readFile(queue, "utf8").catch(error => {
                if (error?.code === "ENOENT") return "";
                throw error;
            });
            if (body.includes('"surfaceId":"openai-server"')) break;
            if (Date.now() >= queueDeadline) throw new Error("modal activation request was not queued");
            await new Promise(resolve => setTimeout(resolve, 10));
        }
        await withRouteEnv(home, ROUTE_B, async () => {
            assert.deepEqual(await consumeModalOpenRequests(), []);
        });
        const requests = await consumeModalOpenRequests();
        assert.equal(requests.length, 1);
        assert.equal(await withRouteEnv(home, ROUTE_B, async () => completeModalOpenRequest(requests[0], { ok: true })), false);
        await completeModalOpenRequest(requests[0], { ok: true });
        assert.equal((await pending).ok, true);
        const stateA = await writeBridgeState({ active: true, endpoint: "127.0.0.1:1" }, "owned");
        assert.equal("routeId" in stateA, false);
        await withRouteEnv(home, ROUTE_B, async () => {
            assert.notEqual((await readBridgeState()).endpoint, "127.0.0.1:1");
        });
        assert.equal((await readBridgeState()).endpoint, "127.0.0.1:1");
    });
});

test("modal IPC immediately recovers a lock abandoned by a terminated process", async () => {
    const originalHome = process.env.AFTERBURNER_HOME;
    const originalRoute = process.env.AFTERBURNER_SESSION_ROUTE;
    const home = join(tmpdir(), `afterburner-openai-abandoned-lock-${process.pid}-${randomBytes(4).toString("hex")}`);
    process.env.AFTERBURNER_HOME = home;
    process.env.AFTERBURNER_SESSION_ROUTE = ROUTE_A;
    try {
        const stateDir = join(home, "state", "openai-server");
        const actionFile = routeQueuePath(stateDir, "modal-actions.jsonl");
        const request = { schemaVersion: 2, routeId: ROUTE_A, requestId: "after-abandoned-lock", surfaceId: "openai-server", action: "status", createdAt: new Date().toISOString() };
        await mkdir(dirname(actionFile), { recursive: true });
        await writeFile(actionFile, `${JSON.stringify(request)}\n`, "utf8");
        await writeFile(`${actionFile}.lock`, JSON.stringify({ pid: 2_147_483_647, acquiredAt: Date.now() }), "utf8");
        const startedAt = Date.now();
        const requests = await consumeBridgeActionRequests();
        assert.equal(requests[0]?.requestId, request.requestId);
        assert.ok(Date.now() - startedAt < 1_000);
    } finally {
        if (originalHome === undefined) delete process.env.AFTERBURNER_HOME;
        else process.env.AFTERBURNER_HOME = originalHome;
        if (originalRoute === undefined) delete process.env.AFTERBURNER_SESSION_ROUTE;
        else process.env.AFTERBURNER_SESSION_ROUTE = originalRoute;
    }
});

test("normalizes Copilot catalog entries into OpenAI model objects", () => {
    assert.deepEqual(normalizeModel({
        id: "gpt-5.5",
        name: "GPT 5.5",
        capabilities: { limits: { max_prompt_tokens: 1, max_context_window_tokens: 2, max_output_tokens: 3 }, supports: { vision: true, reasoning_effort: ["low"] } }
    }), {
        id: "gpt-5.5",
        object: "model",
        created: 0,
        owned_by: "github-copilot",
        afterburner: {
            name: "GPT 5.5",
            max_prompt_tokens: 1,
            max_context_window_tokens: 2,
            max_output_tokens: 3,
            supports_vision: true,
            supports_reasoning_effort: true
        }
    });
});

test("serves /v1/models from adapter", async () => {
    await withBridge({
        listModels: async () => [normalizeModel({ id: "copilot-test" })],
        createChatCompletion: async () => ({ content: "unused" })
    }, async bridge => {
        const { response, body } = await json(`${bridge.state.url}/v1/models`);
        assert.equal(response.status, 200);
        assert.equal(body.object, "list");
        assert.equal(body.data[0].id, "copilot-test");
        assert.match(bridge.healthSnapshot().endpoint, /^127\.0\.0\.1:\d+$/);
    });
});

test("serves non-streaming chat completions", async () => {
    await withBridge({
        listModels: async () => [],
        createChatCompletion: async request => ({ id: "abc", model: request.model, content: "hello" })
    }, async bridge => {
        const { response, body } = await json(`${bridge.state.url}/v1/chat/completions`, {
            method: "POST",
            headers: { "content-type": "application/json" },
            body: JSON.stringify({ model: "copilot-test", messages: [{ role: "user", content: "hi" }] })
        });
        assert.equal(response.status, 200);
        assert.equal(body.object, "chat.completion");
        assert.equal(body.choices[0].message.content, "hello");
    });
});

test("uses session.sendAndWait when no lower-level chat RPC exists", async () => {
    const calls = [];
    const adapter = new CopilotSessionAdapter({
        rpc: { model: { list: async () => ({ list: [] }) } },
        sendAndWait: async input => {
            calls.push(input);
            return { data: { content: "real session response" } };
        }
    });
    await withBridge(adapter, async bridge => {
        const { response, body } = await json(`${bridge.state.url}/v1/chat/completions`, {
            method: "POST",
            headers: { "content-type": "application/json" },
            body: JSON.stringify({ model: "copilot-test", messages: [{ role: "user", content: "hello" }] })
        });
        assert.equal(response.status, 200);
        assert.equal(calls[0].prompt, "user: hello");
        assert.equal(body.choices[0].message.content, "real session response");
    });
});

test("serves streaming chat completions as OpenAI SSE", async () => {
    async function* chunks() {
        yield { content: "hel" };
        yield { content: "lo", finish_reason: "stop" };
    }
    await withBridge({
        listModels: async () => [],
        createChatCompletion: async () => chunks()
    }, async bridge => {
        const response = await fetch(`${bridge.state.url}/v1/chat/completions`, {
            method: "POST",
            headers: { "content-type": "application/json" },
            body: JSON.stringify({ model: "copilot-test", stream: true, messages: [] })
        });
        assert.equal(response.status, 200);
        assert.match(response.headers.get("content-type"), /text\/event-stream/);
        const text = await response.text();
        assert.match(text, /chat\.completion\.chunk/);
        assert.match(text, /data: \[DONE\]/);
    });
});

test("requires local API key when configured", async () => {
    await withBridge({
        listModels: async () => [],
        createChatCompletion: async () => ({ content: "unused" })
    }, async bridge => {
        const denied = await fetch(`${bridge.state.url}/v1/models`);
        assert.equal(denied.status, 401);
        const allowed = await fetch(`${bridge.state.url}/v1/models`, { headers: { authorization: "Bearer secret" } });
        assert.equal(allowed.status, 200);
    }, { port: 0, requireApiKey: true, apiKey: "secret" });
});

test("health endpoints redact session and route identity and keep legacy compatibility", async () => {
    const bridge = createBridge({
        adapter: { listModels: async () => [], createChatCompletion: async () => ({ content: "unused" }) },
        config: { port: 0 },
        identity: { sessionId: "sensitive-session-id", routeId: ROUTE_A }
    });
    await bridge.start();
    try {
        const { body } = await json(`${bridge.state.url}/__afterburner/openai-server/health`);
        assert.equal(body.marker, "afterburner-openai-server-v1");
        assert.equal(body.identity.extensionId, "openai-server");
        assert.notEqual(body.identity.sessionId, "sensitive-session-id");
        assert.notEqual(body.identity.routeFingerprint, ROUTE_A);
        assert.match(body.identity.routeFingerprint, /^session-/);
        assert.equal(typeof body.identity.routeProof, "string");
        const legacy = await json(`${bridge.state.url}/__afterburner/copilot-openai/health`);
        assert.equal(legacy.body.marker, "afterburner-copilot-openai-bridge-v1");
        assert.equal(legacy.body.identity.extensionId, "copilot-openai");
        assert.equal(legacy.body.identity.canonicalExtensionId, "openai-server");
    } finally {
        await bridge.stop();
    }
});

test("modal IPC fails closed without trusted route", async () => {
    const originalRoute = process.env.AFTERBURNER_SESSION_ROUTE;
    delete process.env.AFTERBURNER_SESSION_ROUTE;
    try {
        assert.equal((await consumeModalOpenRequests()).length, 0);
        const result = await (await import("../extensions/OpenAIServer/modal-ipc.mjs")).requestBridgeAction("status", { timeoutMs: 10 });
        assert.equal(result.ok, false);
        assert.equal(result.error, "trusted-route-unavailable");
    } finally {
        if (originalRoute === undefined) delete process.env.AFTERBURNER_SESSION_ROUTE;
        else process.env.AFTERBURNER_SESSION_ROUTE = originalRoute;
    }
});

test("start is idempotent for one bridge instance", async () => {
    const bridge = createBridge({
        adapter: { listModels: async () => [], createChatCompletion: async () => ({ content: "unused" }) },
        config: { port: 0 }
    });
    await bridge.start();
    try {
        const first = bridge.state.url;
        await bridge.start();
        assert.equal(bridge.state.url, first);
    } finally {
        await bridge.stop();
    }
});

test("shared-port detection rejects spoofed public route metadata without route proof", async () => {
    const spoof = createServer((request, response) => {
        if (request.url === "/__afterburner/openai-server/health") {
            response.setHeader("content-type", "application/json");
            response.end(JSON.stringify({ marker: "afterburner-openai-server-v1", protocolVersion: 1, modelCount: 4, identity: { routeFingerprint: "session-spoof", routeId: "session-spoof" } }));
            return;
        }
        response.statusCode = 404;
        response.end();
    });
    await new Promise(resolve => spoof.listen(0, "127.0.0.1", resolve));
    const bridge = createBridge({
        adapter: { listModels: async () => [], createChatCompletion: async () => ({ content: "unused" }) },
        config: { port: spoof.address().port },
        identity: { routeId: ROUTE_A }
    });
    try {
        await assert.rejects(bridge.start(), /EADDRINUSE/);
    } finally {
        await bridge.stop();
        await new Promise(resolve => spoof.close(resolve));
    }
});

test("shared-port detection rejects an unowned legacy health marker", async () => {
    const legacyOwner = createServer((request, response) => {
        if (request.url === "/__afterburner/copilot-openai/health") {
            response.setHeader("content-type", "application/json");
            response.end(JSON.stringify({ marker: "afterburner-copilot-openai-bridge-v1", protocolVersion: 1, modelCount: 4 }));
            return;
        }
        response.statusCode = 404;
        response.end();
    });
    await new Promise(resolve => legacyOwner.listen(0, "127.0.0.1", resolve));
    const port = legacyOwner.address().port;
    const bridge = createBridge({
        adapter: { listModels: async () => [], createChatCompletion: async () => ({ content: "unused" }) },
        config: { port },
        identity: { routeId: ROUTE_A }
    });
    try {
        await assert.rejects(bridge.start(), /EADDRINUSE/);
    } finally {
        await bridge.stop();
        await new Promise(resolve => legacyOwner.close(resolve));
    }
});

test("second bridge shares a compatible existing fixed-port owner", async () => {
    const owner = createBridge({
        adapter: { listModels: async () => [], createChatCompletion: async () => ({ content: "owner" }) },
        config: { port: 0 },
        identity: { routeId: ROUTE_A }
    });
    await owner.start();
    const port = owner.state.port;
    const shared = createBridge({
        adapter: { listModels: async () => [], createChatCompletion: async () => ({ content: "shared" }) },
        config: { port },
        identity: { routeId: ROUTE_A }
    });
    try {
        await shared.start();
        assert.equal(shared.state.shared, true);
        assert.equal(shared.state.url, owner.state.url);
        const { body } = await json(`${shared.state.url}/v1/chat/completions`, {
            method: "POST",
            headers: { "content-type": "application/json" },
            body: JSON.stringify({ model: "copilot-test", messages: [] })
        });
        assert.equal(body.choices[0].message.content, "owner");
    } finally {
        await shared.stop();
        await owner.stop();
    }
});

test("API-key protected bridge shares a compatible fixed-port owner", async () => {
    const apiKey = "shared-secret";
    const owner = createBridge({
        adapter: { listModels: async () => [], createChatCompletion: async () => ({ content: "owner" }) },
        config: { port: 0, requireApiKey: true, apiKey },
        identity: { routeId: ROUTE_A }
    });
    await owner.start();
    const port = owner.state.port;
    const shared = createBridge({
        adapter: { listModels: async () => [], createChatCompletion: async () => ({ content: "shared" }) },
        config: { port, requireApiKey: true, apiKey },
        identity: { routeId: ROUTE_A }
    });
    try {
        await shared.start();
        assert.equal(shared.state.shared, true);
        assert.equal(shared.state.url, owner.state.url);
        const { body } = await json(`${shared.state.url}/v1/chat/completions`, {
            method: "POST",
            headers: {
                authorization: `Bearer ${apiKey}`,
                "content-type": "application/json"
            },
            body: JSON.stringify({ model: "copilot-test", messages: [] })
        });
        assert.equal(body.choices[0].message.content, "owner");
    } finally {
        await shared.stop();
        await owner.stop();
    }
});
