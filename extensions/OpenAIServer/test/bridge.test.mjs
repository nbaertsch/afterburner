import assert from "node:assert/strict";
import { createServer } from "node:http";
import { appendFile, mkdir, readFile, readdir, writeFile } from "node:fs/promises";
import { test } from "node:test";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { randomBytes } from "node:crypto";
import { CopilotSessionAdapter, createBridge, loadConfig, normalizeModel } from "../extensions/OpenAIServer/bridge.mjs";
import { MENU_ID, menuActions, modalFrame } from "../extensions/OpenAIServer/menu.mjs";
import { consumeBridgeActionRequests, consumeModalOpenRequests, readBridgeState } from "../extensions/OpenAIServer/modal-ipc.mjs";
import { configuredPathCandidates, displayConfigPath } from "../extensions/OpenAIServer/names.mjs";

async function withBridge(adapter, fn, config = { port: 0 }) {
    const bridge = createBridge({ adapter, config });
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

test("modal IPC reads legacy state and requests", async () => {
    const originalHome = process.env.AFTERBURNER_HOME;
    const home = join(tmpdir(), `afterburner-openai-server-${process.pid}-${randomBytes(4).toString("hex")}`);
    process.env.AFTERBURNER_HOME = home;
    try {
        const legacyStateDir = join(home, "state", "copilot-openai");
        await mkdir(legacyStateDir, { recursive: true });
        await writeFile(join(legacyStateDir, "bridge-state.json"), JSON.stringify({ schemaVersion: 1, active: true, endpoint: "127.0.0.1:7" }), "utf8");
        assert.equal((await readBridgeState()).endpoint, "127.0.0.1:7");
        await writeFile(join(legacyStateDir, "modal-activation.jsonl"), `${JSON.stringify({ schemaVersion: 1, requestId: "legacy", surfaceId: "copilot-openai", createdAt: new Date().toISOString() })}\n`, "utf8");
        const requests = await consumeModalOpenRequests();
        assert.equal(requests.length, 1);
        assert.equal(requests[0].requestId, "legacy");
    } finally {
        if (originalHome === undefined) delete process.env.AFTERBURNER_HOME;
        else process.env.AFTERBURNER_HOME = originalHome;
    }
});

test("modal IPC atomically claims action batches without deleting concurrent appends", async () => {
    const originalHome = process.env.AFTERBURNER_HOME;
    const home = join(tmpdir(), `afterburner-openai-actions-${process.pid}-${randomBytes(4).toString("hex")}`);
    process.env.AFTERBURNER_HOME = home;
    try {
        const stateDir = join(home, "state", "openai-server");
        const actionFile = join(stateDir, "modal-actions.jsonl");
        await mkdir(stateDir, { recursive: true });
        for (let index = 0; index < 50; index++) {
            const first = { schemaVersion: 1, requestId: `first-${index}`, action: "status", createdAt: new Date().toISOString() };
            const second = { schemaVersion: 1, requestId: `second-${index}`, action: "doctor", createdAt: new Date().toISOString() };
            await writeFile(actionFile, `${JSON.stringify(first)}\n`, "utf8");
            const consuming = consumeBridgeActionRequests();
            await appendFile(actionFile, `${JSON.stringify(second)}\n`, "utf8");
            const batches = [...await consuming, ...await consumeBridgeActionRequests()];
            assert.deepEqual(new Set(batches.map(request => request.requestId)), new Set([first.requestId, second.requestId]));
        }
    } finally {
        if (originalHome === undefined) delete process.env.AFTERBURNER_HOME;
        else process.env.AFTERBURNER_HOME = originalHome;
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

test("health endpoints redact session identity and keep legacy compatibility", async () => {
    const bridge = createBridge({
        adapter: { listModels: async () => [], createChatCompletion: async () => ({ content: "unused" }) },
        config: { port: 0 },
        identity: { sessionId: "sensitive-session-id" }
    });
    await bridge.start();
    try {
        const { body } = await json(`${bridge.state.url}/__afterburner/openai-server/health`);
        assert.equal(body.marker, "afterburner-openai-server-v1");
        assert.equal(body.identity.extensionId, "openai-server");
        assert.notEqual(body.identity.sessionId, "sensitive-session-id");
        const legacy = await json(`${bridge.state.url}/__afterburner/copilot-openai/health`);
        assert.equal(legacy.body.marker, "afterburner-copilot-openai-bridge-v1");
        assert.equal(legacy.body.identity.extensionId, "copilot-openai");
        assert.equal(legacy.body.identity.canonicalExtensionId, "openai-server");
    } finally {
        await bridge.stop();
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

test("shared-port detection accepts a legacy health marker", async () => {
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
        config: { port }
    });
    try {
        const snapshot = await bridge.start();
        assert.equal(snapshot.shared, true);
        assert.equal(snapshot.modelCount, 4);
    } finally {
        await bridge.stop();
        await new Promise(resolve => legacyOwner.close(resolve));
    }
});

test("second bridge shares a compatible existing fixed-port owner", async () => {
    const owner = createBridge({
        adapter: { listModels: async () => [], createChatCompletion: async () => ({ content: "owner" }) },
        config: { port: 0 }
    });
    await owner.start();
    const port = owner.state.port;
    const shared = createBridge({
        adapter: { listModels: async () => [], createChatCompletion: async () => ({ content: "shared" }) },
        config: { port }
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
