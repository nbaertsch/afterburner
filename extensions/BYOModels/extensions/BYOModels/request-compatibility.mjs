import { createHash } from "node:crypto";
import { createServer } from "node:http";
import { Readable } from "node:stream";

const healthPath = "/__afterburner/byomodels/health";
const healthMarker = "afterburner-byomodels-proxy-v1";

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

function proxyIdentity(provider, upstream) {
    const maximumLength = Number.isInteger(provider.requestCompatibility?.maxInputItemIdLength)
        ? provider.requestCompatibility.maxInputItemIdLength
        : 0;
    const configuration = [
        String(maximumLength),
        String(provider.requestCompatibility?.forceStreaming === true),
        provider.auth?.type ?? "",
        provider.auth?.resource ?? ""
    ].join("\0");
    return {
        marker: healthMarker,
        provider: provider.name,
        upstream: upstream.href,
        configuration: createHash("sha256").update(configuration).digest("base64url")
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

function completedResponseFromEventStream(source) {
    let completed;
    for (const block of source.split(/\r?\n\r?\n/)) {
        const data = block
            .split(/\r?\n/)
            .filter((line) => line.startsWith("data:"))
            .map((line) => line.slice(5).trimStart())
            .join("\n");
        if (!data || data === "[DONE]") continue;
        const event = JSON.parse(data);
        if ((event.type === "response.completed" || event.type === "response.failed") &&
            event.response && typeof event.response === "object") {
            completed = event.response;
        }
        if (event.type === "error") {
            throw new Error(event.error?.message ?? event.message ?? "upstream streaming response failed");
        }
    }
    if (!completed) {
        throw new Error("upstream streaming response did not include a terminal response event");
    }
    return completed;
}

function createProxyServer(
    provider,
    upstream,
    maximumLength,
    forceStreaming,
    identity,
    onRewrite,
    getBearerToken
) {
    return createServer(async (request, response) => {
        if (request.method === "GET" && request.url === healthPath) {
            response.writeHead(200, { "content-type": "application/json" });
            response.end(JSON.stringify(identity));
            return;
        }
        try {
            const chunks = [];
            for await (const chunk of request) chunks.push(chunk);
            let body = Buffer.concat(chunks);
            let translatedStreamingResponse = false;
            if (body.length > 0 && request.headers["content-type"]?.includes("application/json")) {
                const payload = JSON.parse(body.toString("utf8"));
                const rewritten = Number.isInteger(maximumLength)
                    ? rewriteOversizedInputItemIds(payload, maximumLength)
                    : 0;
                if (rewritten > 0) {
                    onRewrite(rewritten);
                }
                const pathname = new URL(request.url ?? "/", "http://127.0.0.1").pathname;
                if (forceStreaming && request.method === "POST" &&
                    pathname.endsWith("/responses") && payload.stream !== true) {
                    payload.stream = true;
                    translatedStreamingResponse = true;
                }
                if (rewritten > 0 || translatedStreamingResponse) {
                    body = Buffer.from(JSON.stringify(payload));
                }
            }

            const target = upstreamTarget(upstream, request.url);
            const headers = { ...request.headers };
            delete headers.host;
            delete headers["content-length"];
            delete headers.connection;
            delete headers["transfer-encoding"];
            if (getBearerToken) {
                headers.authorization = `Bearer ${await getBearerToken()}`;
            }
            const upstreamResponse = await fetch(target, {
                method: request.method,
                headers,
                body: request.method === "GET" || request.method === "HEAD" ? undefined : body,
                duplex: "half"
            });
            const responseHeaders = Object.fromEntries(upstreamResponse.headers);
            delete responseHeaders["content-length"];
            delete responseHeaders["content-encoding"];
            delete responseHeaders["transfer-encoding"];
            if (translatedStreamingResponse && upstreamResponse.ok &&
                upstreamResponse.headers.get("content-type")?.includes("text/event-stream")) {
                const completed = completedResponseFromEventStream(await upstreamResponse.text());
                responseHeaders["content-type"] = "application/json";
                response.writeHead(upstreamResponse.status, responseHeaders);
                response.end(JSON.stringify(completed));
                return;
            }
            response.writeHead(upstreamResponse.status, responseHeaders);
            if (upstreamResponse.body) {
                Readable.fromWeb(upstreamResponse.body).pipe(response);
            } else {
                response.end();
            }
        } catch (error) {
            response.writeHead(502, { "content-type": "application/json" });
            response.end(JSON.stringify({
                error: {
                    message: `BYOModels request compatibility proxy failed: ${error?.message ?? String(error)}`
                }
            }));
        }
    });
}

function listen(server, port) {
    return new Promise((resolve, reject) => {
        const onError = error => {
            server.off("listening", onListening);
            reject(error);
        };
        const onListening = () => {
            server.off("error", onError);
            resolve();
        };
        server.once("error", onError);
        server.once("listening", onListening);
        server.listen(port, "127.0.0.1");
    });
}

export async function startRequestCompatibilityProxy(
    provider,
    { onRewrite = () => {}, getBearerToken } = {}
) {
    const maximumLength = provider.requestCompatibility?.maxInputItemIdLength;
    const forceStreaming = provider.requestCompatibility?.forceStreaming === true;
    if (maximumLength !== undefined &&
        (!Number.isInteger(maximumLength) || maximumLength < 16)) {
        throw new Error(
            `Provider '${provider.name}' has an invalid requestCompatibility.maxInputItemIdLength.`
        );
    }
    if ((!Number.isInteger(maximumLength) || maximumLength < 16) && !forceStreaming) return null;
    const configuredPort = provider.requestCompatibility?.proxyPort ?? 0;
    if (!Number.isInteger(configuredPort) || configuredPort < 0 || configuredPort > 65535) {
        throw new Error(`Provider '${provider.name}' has an invalid requestCompatibility.proxyPort.`);
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
            onRewrite,
            getBearerToken
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
