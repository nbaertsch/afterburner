import assert from "node:assert/strict";
import { readFile, readdir } from "node:fs/promises";
import { test } from "node:test";
import { CopilotSessionAdapter, createBridge, loadConfig, normalizeModel } from "../extensions/CopilotOpenAI/bridge.mjs";

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

test("session extension exposes one management command with menu actions", async () => {
    const source = await readFile(new URL("../extensions/CopilotOpenAI/extension.mjs", import.meta.url), "utf8");
    const wrapper = await readFile(new URL("../com.github.copilot/extensions/CopilotOpenAI/extension.mjs", import.meta.url), "utf8");
    const wrappers = await readdir(new URL("../com.github.copilot/extensions", import.meta.url), { withFileTypes: true });
    assert.match(source, /createCanvas/);
    assert.match(source, /canvases:\s*\[managementCanvas\]/);
    const commandNames = [...source.matchAll(/name:\s*"(copilot-openai[^"]*)"/g)].map(match => match[1]);
    assert.deepEqual(commandNames, ["copilot-openai"]);
    for (const removed of ["copilot-openai-start", "copilot-openai-stop", "copilot-openai-doctor"]) {
        assert.doesNotMatch(source, new RegExp(`name:\\s*"${removed}"`));
    }
    assert.match(source, /session\.rpc\.canvas\.open/);
    assert.match(source, /afterburner-copilot-openai-menu/);
    for (const action of ["start", "stop", "status", "doctor"]) {
        assert.match(source, new RegExp(`name:\\s*"${action}"`));
    }
    assert.match(source, /interactive management menu/i);
    assert.doesNotMatch(source, /\/copilot-openai (?:start|stop|status|doctor)/);
    assert.deepEqual(wrappers.filter(entry => entry.isDirectory()).map(entry => entry.name), ["CopilotOpenAI"]);
    assert.equal(wrapper.trim(), "export * from \"../../../extensions/CopilotOpenAI/extension.mjs\";");
    assert.doesNotMatch(wrapper, /activateExtension\(|export const instance/);
    assert.match(source, /session\s*=\s*await joinSession/);
    assert.doesNotMatch(source, /export async function activate/);
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

test("health endpoint redacts session identity", async () => {
    const bridge = createBridge({
        adapter: { listModels: async () => [], createChatCompletion: async () => ({ content: "unused" }) },
        config: { port: 0 },
        identity: { sessionId: "sensitive-session-id" }
    });
    await bridge.start();
    try {
        const { body } = await json(`${bridge.state.url}/__afterburner/copilot-openai/health`);
        assert.equal(body.marker, "afterburner-copilot-openai-bridge-v1");
        assert.notEqual(body.identity.sessionId, "sensitive-session-id");
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
