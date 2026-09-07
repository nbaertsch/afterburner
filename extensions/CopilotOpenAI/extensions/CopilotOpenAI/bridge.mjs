import { createServer } from "node:http";
import { randomBytes } from "node:crypto";

export const bridgeProtocolVersion = 1;
export const defaultConfig = Object.freeze({
    enabled: true,
    host: "127.0.0.1",
    port: 41425,
    requireApiKey: false,
    apiKey: undefined,
    maxBodyBytes: 1024 * 1024
});

const hopByHopHeaders = new Set([
    "connection", "keep-alive", "proxy-authenticate", "proxy-authorization",
    "te", "trailer", "transfer-encoding", "upgrade"
]);

export function loadConfig(raw = {}, env = process.env) {
    const config = { ...defaultConfig, ...raw };
    if (env.AFTERBURNER_COPILOT_OPENAI_PORT) {
        config.port = Number(env.AFTERBURNER_COPILOT_OPENAI_PORT);
    }
    if (env.AFTERBURNER_COPILOT_OPENAI_API_KEY) {
        config.apiKey = env.AFTERBURNER_COPILOT_OPENAI_API_KEY;
        config.requireApiKey = true;
    }
    if (config.host !== "127.0.0.1" && config.host !== "localhost") {
        throw new Error("Copilot OpenAI bridge only supports localhost binding.");
    }
    if (!Number.isInteger(config.port) || config.port < 0 || config.port > 65535) {
        throw new Error("Copilot OpenAI bridge port must be an integer between 0 and 65535.");
    }
    if (!Number.isInteger(config.maxBodyBytes) || config.maxBodyBytes < 1024 || config.maxBodyBytes > 16 * 1024 * 1024) {
        throw new Error("Copilot OpenAI bridge maxBodyBytes must be between 1 KiB and 16 MiB.");
    }
    if (config.requireApiKey && typeof config.apiKey !== "string") {
        config.apiKey = randomBytes(24).toString("base64url");
    }
    return config;
}

export class CopilotSessionAdapter {
    constructor(session, options = {}) {
        this.session = session;
        this.options = options;
    }

    async listModels() {
        const catalog = await this.session?.rpc?.model?.list?.();
        const list = Array.isArray(catalog?.list) ? catalog.list : [];
        return list.map(normalizeModel).filter(Boolean);
    }

    async createChatCompletion(request, options = {}) {
        const rpc = this.session?.rpc;
        const candidates = [
            rpc?.chat?.completions?.create,
            rpc?.chat?.completion?.create,
            rpc?.chat?.create,
            rpc?.completion?.create,
            rpc?.model?.complete
        ].filter(fn => typeof fn === "function");
        if (candidates.length > 0) {
            return candidates[0].call(undefined, request, options);
        }
        if (typeof this.session?.sendAndWait === "function") {
            const response = await this.session.sendAndWait({ prompt: messagesToPrompt(request?.messages) });
            return { model: request?.model, content: extractSessionResponseContent(response) };
        }
        throw openAIError(501, "unsupported_endpoint", "The active Copilot session does not expose a chat completion RPC.");
    }
}

function messagesToPrompt(messages) {
    if (!Array.isArray(messages)) return "";
    return messages.map(message => {
        const role = typeof message?.role === "string" ? message.role : "user";
        const content = normalizeMessageContent(message?.content);
        return content ? `${role}: ${content}` : "";
    }).filter(Boolean).join("\n\n");
}

function normalizeMessageContent(content) {
    if (typeof content === "string") return content;
    if (!Array.isArray(content)) return "";
    return content.map(part => typeof part?.text === "string" ? part.text : "").filter(Boolean).join("\n");
}

function extractSessionResponseContent(response) {
    return response?.data?.content ?? response?.content ?? response?.message?.content ?? String(response ?? "");
}

export function normalizeModel(model) {
    if (!model || typeof model.id !== "string" || !model.id) return undefined;
    return {
        id: model.id,
        object: "model",
        created: 0,
        owned_by: "github-copilot",
        afterburner: {
            name: model.name ?? model.displayName ?? model.id,
            max_prompt_tokens: model.capabilities?.limits?.max_prompt_tokens ?? null,
            max_context_window_tokens: model.capabilities?.limits?.max_context_window_tokens ?? null,
            max_output_tokens: model.capabilities?.limits?.max_output_tokens ?? null,
            supports_vision: model.capabilities?.supports?.vision === true,
            supports_reasoning_effort: Boolean(model.capabilities?.supports?.reasoning_effort)
        }
    };
}

export function createBridge({ adapter, config = {}, logger = undefined, identity = {} }) {
    if (!adapter || typeof adapter.listModels !== "function") {
        throw new Error("Copilot OpenAI bridge requires an adapter with listModels().");
    }
    const effective = loadConfig(config);
    const state = {
        active: false,
        startedAt: null,
        url: null,
        port: effective.port,
        requestCount: 0,
        errorCount: 0,
        lastError: null,
        modelCount: 0,
        apiKey: effective.apiKey,
        shared: false
    };
    let server;

    async function handler(request, response) {
        state.requestCount++;
        try {
            removeUnsafeResponseHeaders(response);
            if (!authorize(request, effective)) {
                return writeJSON(response, 401, openAIErrorBody("invalid_api_key", "Missing or invalid local API key."));
            }
            const url = new URL(request.url ?? "/", `http://${effective.host}`);
            if (request.method === "GET" && url.pathname === "/__afterburner/copilot-openai/health") {
                return writeJSON(response, 200, healthSnapshot());
            }
            if (request.method === "GET" && url.pathname === "/v1/models") {
                const data = await adapter.listModels();
                state.modelCount = data.length;
                return writeJSON(response, 200, { object: "list", data });
            }
            if (request.method === "POST" && url.pathname === "/v1/chat/completions") {
                const body = await readJSON(request, effective.maxBodyBytes);
                const result = await adapter.createChatCompletion(body, { signal: request.signal });
                if (body?.stream === true) return writeStream(response, result, body.model);
                return writeJSON(response, 200, normalizeCompletion(result, body.model));
            }
            return writeJSON(response, 404, openAIErrorBody("not_found", `Unsupported Copilot OpenAI bridge endpoint: ${request.method} ${url.pathname}`));
        } catch (error) {
            state.errorCount++;
            state.lastError = sanitizeError(error);
            logger?.warn?.(`Copilot OpenAI bridge request failed: ${state.lastError}`);
            const status = Number.isInteger(error?.status) ? error.status : 500;
            writeJSON(response, status, openAIErrorBody(error?.code ?? "bridge_error", state.lastError));
        }
    }

    function healthSnapshot() {
        return {
            marker: "afterburner-copilot-openai-bridge-v1",
            protocolVersion: bridgeProtocolVersion,
            active: state.active,
            shared: state.shared,
            url: state.url,
            endpoint: state.active && state.port ? `${effective.host}:${state.port}` : null,
            requestCount: state.requestCount,
            errorCount: state.errorCount,
            lastError: state.lastError,
            modelCount: state.modelCount,
            identity: {
                extensionId: "copilot-openai",
                sessionId: identity.sessionId ? hashPublic(identity.sessionId) : null
            }
        };
    }

    return {
        state,
        healthSnapshot,
        async start() {
            if (server || state.shared) return healthSnapshot();
            server = createServer(handler);
            try {
                await new Promise((resolve, reject) => {
                    server.once("error", reject);
                    server.listen(effective.port, effective.host, () => {
                        server.off("error", reject);
                        const address = server.address();
                        state.port = typeof address === "object" && address ? address.port : effective.port;
                        state.url = `http://${effective.host}:${state.port}`;
                        state.startedAt = new Date().toISOString();
                        state.active = true;
                        state.shared = false;
                        resolve();
                    });
                });
            } catch (error) {
                server = undefined;
                if (error?.code !== "EADDRINUSE" || effective.port === 0) throw error;
                const shared = await verifySharedBridge(effective.host, effective.port);
                if (!shared) throw error;
                state.port = effective.port;
                state.url = `http://${effective.host}:${effective.port}`;
                state.startedAt = shared.startedAt ?? null;
                state.active = true;
                state.shared = true;
                state.modelCount = shared.modelCount ?? 0;
            }
            return healthSnapshot();
        },
        async stop() {
            if (state.shared && !server) {
                state.active = false;
                state.shared = false;
                return;
            }
            if (!server) return;
            const closing = server;
            server = undefined;
            await new Promise((resolve, reject) => closing.close(error => error ? reject(error) : resolve()));
            state.active = false;
            state.shared = false;
        }
    };
}

async function verifySharedBridge(host, port) {
    try {
        const response = await fetch(`http://${host}:${port}/__afterburner/copilot-openai/health`, { signal: AbortSignal.timeout(2000) });
        if (!response.ok) return undefined;
        const body = await response.json();
        return body?.marker === "afterburner-copilot-openai-bridge-v1" && body?.protocolVersion === bridgeProtocolVersion
            ? body
            : undefined;
    } catch {
        return undefined;
    }
}

function authorize(request, config) {
    if (!config.requireApiKey) return true;
    const header = request.headers.authorization ?? "";
    return header === `Bearer ${config.apiKey}`;
}

async function readJSON(request, maximum) {
    let size = 0;
    const chunks = [];
    for await (const chunk of request) {
        size += chunk.length;
        if (size > maximum) throw openAIError(413, "request_too_large", "Request body exceeded configured limit.");
        chunks.push(chunk);
    }
    if (chunks.length === 0) return {};
    try {
        return JSON.parse(Buffer.concat(chunks).toString("utf8"));
    } catch {
        throw openAIError(400, "invalid_json", "Request body must be valid JSON.");
    }
}

function writeJSON(response, status, body) {
    response.statusCode = status;
    response.setHeader("content-type", "application/json; charset=utf-8");
    response.end(`${JSON.stringify(body)}\n`);
}

async function writeStream(response, result, model) {
    response.statusCode = 200;
    response.setHeader("content-type", "text/event-stream; charset=utf-8");
    response.setHeader("cache-control", "no-cache");
    response.setHeader("connection", "keep-alive");
    const iterable = toAsyncIterable(result);
    for await (const chunk of iterable) {
        response.write(`data: ${JSON.stringify(normalizeStreamChunk(chunk, model))}\n\n`);
    }
    response.write("data: [DONE]\n\n");
    response.end();
}

async function* toAsyncIterable(value) {
    if (value?.[Symbol.asyncIterator]) yield* value;
    else if (Array.isArray(value)) yield* value;
    else yield value;
}

function normalizeCompletion(value, defaultModel) {
    if (value?.object === "chat.completion") return value;
    const content = value?.choices?.[0]?.message?.content ?? value?.message?.content ?? value?.content ?? "";
    return {
        id: value?.id ?? `chatcmpl-afterburner-${Date.now()}`,
        object: "chat.completion",
        created: Math.floor(Date.now() / 1000),
        model: value?.model ?? defaultModel ?? "copilot",
        choices: [{ index: 0, message: { role: "assistant", content }, finish_reason: value?.finish_reason ?? "stop" }],
        usage: value?.usage
    };
}

function normalizeStreamChunk(value, defaultModel) {
    if (value?.object === "chat.completion.chunk") return value;
    const content = value?.choices?.[0]?.delta?.content ?? value?.delta?.content ?? value?.content ?? "";
    return {
        id: value?.id ?? `chatcmpl-afterburner-${Date.now()}`,
        object: "chat.completion.chunk",
        created: Math.floor(Date.now() / 1000),
        model: value?.model ?? defaultModel ?? "copilot",
        choices: [{ index: 0, delta: content ? { content } : {}, finish_reason: value?.finish_reason ?? null }]
    };
}

function openAIError(status, code, message) {
    const error = new Error(message);
    error.status = status;
    error.code = code;
    return error;
}

function openAIErrorBody(code, message) {
    return { error: { message, type: code, code } };
}

function sanitizeError(error) {
    return String(error?.message ?? error ?? "unknown error")
        .replace(/Bearer\s+[A-Za-z0-9._~+\/-]+=*/gi, "Bearer [redacted]")
        .replace(/[A-Za-z0-9_-]{24,}\.[A-Za-z0-9_-]{16,}\.[A-Za-z0-9_-]{16,}/g, "[redacted-jwt]")
        .replace(/[\r\n\t]+/g, " ")
        .slice(0, 500);
}

function removeUnsafeResponseHeaders(response) {
    for (const header of hopByHopHeaders) response.removeHeader(header);
}

function hashPublic(value) {
    let hash = 0;
    for (const char of String(value)) hash = ((hash << 5) - hash + char.charCodeAt(0)) | 0;
    return `session-${Math.abs(hash).toString(36)}`;
}
