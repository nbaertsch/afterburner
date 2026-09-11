import { createHash, randomBytes, timingSafeEqual } from "node:crypto";
import { createServer } from "node:http";
import { Readable } from "node:stream";
import { pipeline } from "node:stream/promises";
import { rewriteLegacyTools } from "./legacy-tools.mjs";
import { readResponseStream, ResponseStreamError, terminalResponseFromEventStream } from "./response-stream.mjs";

const healthPath = "/__afterburner/byomodels/health";
const healthMarker = "afterburner-byomodels-proxy-v1";
const listenTimeoutMs = 5_000;
const upstreamTimeoutMs = 5 * 60_000;
export const proxyCapabilityHeader = "x-afterburner-proxy-capability";

function newProxyCapability() {
    return randomBytes(32).toString("base64url");
}

function safeEqual(left, right) {
    if (typeof left !== "string" || typeof right !== "string" || !left || !right) return false;
    const leftBytes = Buffer.from(left);
    const rightBytes = Buffer.from(right);
    return leftBytes.length === rightBytes.length && timingSafeEqual(leftBytes, rightBytes);
}

function bearerToken(request) {
    const authorization = request.headers.authorization;
    if (typeof authorization !== "string") return "";
    const match = authorization.match(/^Bearer\s+(.+)$/i);
    return match?.[1]?.trim() ?? "";
}

function hasProxyCapability(request, capability) {
    return safeEqual(request.headers[proxyCapabilityHeader], capability) ||
        safeEqual(bearerToken(request), capability);
}

function unauthorized(response) {
    response.writeHead(401, { "content-type": "application/json" });
    response.end(JSON.stringify({ error: { message: "unauthorized BYOModels compatibility proxy request" } }));
}

function compatibleInputItemId(id) {
    return `ab_${createHash("sha256").update(id).digest("base64url")}`;
}

export function rewriteOversizedInputItemIds(payload, maximumLength) {
    if (!Array.isArray(payload?.input)) return 0;
    let rewritten = 0;
    for (const item of payload.input) {
        if (typeof item?.id !== "string" || item.id.length <= maximumLength) continue;
        item.id = compatibleInputItemId(item.id);
        rewritten++;
    }
    return rewritten;
}

export function proxyConfiguration(provider) {
    const maximumLength = Number.isInteger(provider.requestCompatibility?.maxInputItemIdLength)
        ? provider.requestCompatibility.maxInputItemIdLength
        : 0;
    const configuredHeaders = Object.entries(provider.headers ?? {})
        .map(([name, value]) => [name.trim().toLowerCase(), value])
        .sort(([left], [right]) => left < right ? -1 : left > right ? 1 : 0)
        .map(([name, value]) => `${name}:${value}`)
        .join("\n");
    const configuration = [
        String(maximumLength),
        String(provider.requestCompatibility?.forceStreaming === true),
        provider.auth?.type ?? "",
        provider.auth?.resource ?? "",
        configuredHeaders,
        ...(provider.requestCompatibility?.legacyTools === true ? ["legacyTools"] : []),
        ...(provider.requestCompatibility?.bufferResponses === true ? ["bufferResponses"] : [])
    ].join("\0");
    return createHash("sha256").update(configuration).digest("base64url");
}

function proxyIdentity(provider, upstream) {
    return {
        marker: healthMarker,
        provider: provider.name,
        upstream: upstream.href,
        configuration: proxyConfiguration(provider)
    };
}

function upstreamTarget(upstream, requestUrl) {
    const incoming = new URL(requestUrl ?? "/", "http://127.0.0.1");
    const target = new URL(upstream);
    const basePath = target.pathname.replace(/\/$/, "");
    const requestPath = incoming.pathname.replace(/^\//, "");
    target.pathname = `${basePath}/${requestPath}`;
    const query = [target.search.slice(1), incoming.search.slice(1)].filter(Boolean).join("&");
    target.search = query ? `?${query}` : "";
    return target;
}

function sanitizeClientHeaders(headers) {
    for (const value of String(headers.connection ?? "").split(",")) {
        const name = value.trim().toLowerCase();
        if (name) delete headers[name];
    }
    for (const name of [
        "accept-encoding",
        "connection",
        "content-length",
        "host",
        "keep-alive",
        "proxy-authenticate",
        "proxy-authorization",
        "te",
        "trailer",
        "transfer-encoding",
        "upgrade",
        proxyCapabilityHeader
    ]) {
        delete headers[name];
    }
}

function upstreamClientHeaders(requestHeaders) {
    const headers = {};
    for (const name of ["accept", "content-type"]) {
        const value = requestHeaders[name];
        if (value !== undefined) headers[name] = value;
    }
    return headers;
}

function createProxyServer(
    provider,
    upstream,
    maximumLength,
    forceStreaming,
    identity,
    capability,
    onRewrite,
    getBearerToken,
    getUpstreamHeaders,
    upstreamTimeout
) {
    return createServer(async (request, response) => {
        const authorized = hasProxyCapability(request, capability);
        if (request.method === "GET" && request.url === healthPath) {
            response.writeHead(200, { "content-type": "application/json" });
            response.end(JSON.stringify(authorized ? identity : { marker: healthMarker, ready: true }));
            return;
        }
        if (!authorized) {
            unauthorized(response);
            return;
        }
        const abort = new AbortController();
        const upstreamTimer = setTimeout(
            () => abort.abort(new Error(`Upstream request timed out after ${upstreamTimeout}ms.`)),
            upstreamTimeout
        );
        upstreamTimer.unref?.();
        const onClose = () => {
            if (!response.writableFinished) abort.abort();
        };
        response.once("close", onClose);
        let upstreamRequestId;
        try {
            const chunks = [];
            for await (const chunk of request) chunks.push(chunk);
            let body = Buffer.concat(chunks);
            let translatedStreamingResponse = false;
            let payload;
            if (body.length > 0 && request.headers["content-type"]?.includes("application/json")) {
                payload = JSON.parse(body.toString("utf8"));
                const rewritten = Number.isInteger(maximumLength)
                    ? rewriteOversizedInputItemIds(payload, maximumLength)
                    : 0;
                if (rewritten > 0) {
                    onRewrite(rewritten);
                }
                const pathname = new URL(request.url ?? "/", "http://127.0.0.1").pathname;
                const legacyTools = provider.requestCompatibility?.legacyTools === true &&
                    request.method === "POST" && pathname.endsWith("/responses");
                if (legacyTools) rewriteLegacyTools(payload);
                if (forceStreaming && request.method === "POST" &&
                    pathname.endsWith("/responses") && payload.stream !== true) {
                    payload.stream = true;
                    translatedStreamingResponse = true;
                }
                if (rewritten > 0 || translatedStreamingResponse || legacyTools) {
                    body = Buffer.from(JSON.stringify(payload));
                }
            }

            const target = upstreamTarget(upstream, request.url);
            const headers = upstreamClientHeaders(request.headers);
            const capabilityAuthorization = safeEqual(bearerToken(request), capability);
            if (capabilityAuthorization) {
                delete headers.authorization;
            }
            sanitizeClientHeaders(headers);
            for (const [name, value] of Object.entries(provider.headers ?? {})) {
                headers[name.trim().toLowerCase()] = value;
            }
            if (getUpstreamHeaders) {
                Object.assign(headers, await getUpstreamHeaders());
            } else if (getBearerToken) {
                headers.authorization = `Bearer ${await getBearerToken()}`;
            }
            const upstreamResponse = await fetch(target, {
                method: request.method,
                headers,
                body: request.method === "GET" || request.method === "HEAD" ? undefined : body,
                signal: abort.signal,
                duplex: "half"
            });
            upstreamRequestId = upstreamResponse.headers.get("x-request-id") ??
                upstreamResponse.headers.get("apim-request-id") ?? upstreamResponse.headers.get("request-id");
            const responseHeaders = Object.fromEntries(upstreamResponse.headers);
            delete responseHeaders["content-length"];
            delete responseHeaders["content-encoding"];
            delete responseHeaders["transfer-encoding"];
            const responsesRequest = request.method === "POST" &&
                new URL(request.url ?? "/", "http://127.0.0.1").pathname.endsWith("/responses");
            if ((translatedStreamingResponse || (responsesRequest && provider.requestCompatibility?.bufferResponses)) &&
                upstreamResponse.ok &&
                upstreamResponse.headers.get("content-type")?.includes("text/event-stream")) {
                const stream = await readResponseStream(upstreamResponse);
                const completed = terminalResponseFromEventStream(stream);
                const refusals = (completed.output ?? []).flatMap(item =>
                    (item.content ?? []).filter(part => part.type === "refusal" && typeof part.refusal === "string")
                        .map(part => part.refusal));
                if (refusals.length) {
                    throw new ResponseStreamError(refusals.join("\n"), "upstream_refusal", 422);
                }
                if (translatedStreamingResponse) responseHeaders["content-type"] = "application/json";
                response.writeHead(upstreamResponse.status, responseHeaders);
                response.end(translatedStreamingResponse ? JSON.stringify(completed) :
                    `event: response.completed\ndata: ${JSON.stringify({ type: "response.completed", response: completed })}\n\n`);
                return;
            }
            if (!upstreamResponse.ok) {
                const chunks = [];
                if (upstreamResponse.body) {
                    for await (const chunk of Readable.fromWeb(upstreamResponse.body)) {
                        chunks.push(chunk);
                    }
                }
                const errBuffer = Buffer.concat(chunks);
                const errText = errBuffer.toString("utf8");
                if (upstreamResponse.status === 429 && /no healthy deployment/i.test(errText)) {
                    response.writeHead(422, { "content-type": "application/json" });
                    response.end(JSON.stringify({
                        error: {
                            code: "upstream_deployment_unhealthy",
                            message: `BYOModels '${provider.name}' [upstream_deployment_unhealthy]: Upstream deployment for model '${payload?.model ?? "unknown"}' is currently unhealthy or unavailable.`
                        }
                    }));
                    return;
                }
                response.writeHead(upstreamResponse.status, responseHeaders);
                response.end(errBuffer);
                return;
            }
            response.writeHead(upstreamResponse.status, responseHeaders);
            if (upstreamResponse.body) {
                await pipeline(Readable.fromWeb(upstreamResponse.body), response);
            } else {
                response.end();
            }
        } catch (error) {
            if (response.destroyed) return;
            if (response.headersSent) {
                response.destroy(error);
                return;
            }
            const visibleError = abort.signal.aborted && abort.signal.reason
                ? abort.signal.reason
                : error;
            const requestId = upstreamRequestId ? ` (upstream request: ${upstreamRequestId})` : "";
            const errorCode = error instanceof ResponseStreamError ? error.code : "byomodels_proxy_error";
            response.writeHead(error instanceof ResponseStreamError ? error.status : 502, { "content-type": "application/json" });
            response.end(JSON.stringify({
                error: {
                    code: errorCode,
                    message: `BYOModels '${provider.name}' [${errorCode}]: ${visibleError?.message ?? String(visibleError)}${requestId}`
                }
            }));
        } finally {
            clearTimeout(upstreamTimer);
            response.off("close", onClose);
        }
    });
}

function listen(server, port) {
    return new Promise((resolve, reject) => {
        const timeout = setTimeout(() => {
            cleanup();
            server.close();
            reject(new Error(`Compatibility proxy listener timed out after ${listenTimeoutMs}ms.`));
        }, listenTimeoutMs);
        timeout.unref?.();
        const cleanup = () => {
            clearTimeout(timeout);
            server.off("error", onError);
            server.off("listening", onListening);
        };
        const onError = error => {
            cleanup();
            reject(error);
        };
        const onListening = () => {
            cleanup();
            resolve();
        };
        server.once("error", onError);
        server.once("listening", onListening);
        server.listen(port, "127.0.0.1");
    });
}

export async function startRequestCompatibilityProxy(
    provider,
    {
        onRewrite = () => {},
        getBearerToken,
        getUpstreamHeaders,
        capability = newProxyCapability(),
        upstreamTimeout = upstreamTimeoutMs
    } = {}
) {
    const maximumLength = provider.requestCompatibility?.maxInputItemIdLength;
    const forceStreaming = provider.requestCompatibility?.forceStreaming === true;
    const legacyTools = provider.requestCompatibility?.legacyTools;
    const bufferResponses = provider.requestCompatibility?.bufferResponses;
    if (bufferResponses !== undefined && typeof bufferResponses !== "boolean") {
        throw new Error(`Provider '${provider.name}' has an invalid requestCompatibility.bufferResponses.`);
    }
    if (legacyTools !== undefined && typeof legacyTools !== "boolean") {
        throw new Error(`Provider '${provider.name}' has an invalid requestCompatibility.legacyTools.`);
    }
    if (maximumLength !== undefined &&
        (!Number.isInteger(maximumLength) || maximumLength < 16)) {
        throw new Error(
            `Provider '${provider.name}' has an invalid requestCompatibility.maxInputItemIdLength.`
        );
    }
    if ((!Number.isInteger(maximumLength) || maximumLength < 16) && !forceStreaming && !legacyTools && !bufferResponses) return null;
    const configuredPort = provider.requestCompatibility?.proxyPort ?? 0;
    if (!Number.isInteger(configuredPort) || configuredPort < 0 || configuredPort > 65535) {
        throw new Error(`Provider '${provider.name}' has an invalid requestCompatibility.proxyPort.`);
    }
    if (typeof capability !== "string" || capability.length < 32) {
        throw new Error(`Provider '${provider.name}' has an invalid request compatibility proxy capability.`);
    }
    if (!Number.isFinite(upstreamTimeout) || upstreamTimeout <= 0) {
        throw new Error(`Provider '${provider.name}' has an invalid upstream request timeout.`);
    }

    const upstream = new URL(provider.baseUrl);
    const identity = proxyIdentity(provider, upstream);
    let activeServer;
    let closed = false;

    const create = () =>
        createProxyServer(
            provider,
            upstream,
            maximumLength,
            forceStreaming,
            identity,
            capability,
            onRewrite,
            getBearerToken,
            getUpstreamHeaders,
            upstreamTimeout
        );

    async function bind(port) {
        const server = create();
        try {
            await listen(server, port);
            return server;
        } catch (error) {
            await new Promise(resolve => server.close(() => resolve()));
            throw error;
        }
    }

    try {
        activeServer = await bind(configuredPort);
    } catch (error) {
        if (configuredPort === 0 || error?.code !== "EADDRINUSE") {
            throw error;
        }
        activeServer = await bind(0);
    }

    const address = activeServer.address();
    const port = address && typeof address !== "string" ? address.port : 0;
    if (!port) {
        throw new Error(`Unable to bind compatibility proxy for provider '${provider.name}'.`);
    }
    return {
        baseUrl: `http://127.0.0.1:${port}`,
        preferredPort: configuredPort,
        port,
        capability,
        authorization: `Bearer ${capability}`,
        capabilityHeader: proxyCapabilityHeader,
        close: async () => {
            if (closed) return;
            closed = true;
            await new Promise((resolve, reject) =>
                activeServer.close(error => error ? reject(error) : resolve())
            );
            activeServer = undefined;
        }
    };
}
