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
    return {
        marker: healthMarker,
        provider: provider.name,
        upstream: upstream.href
    };
}

function createProxyServer(provider, upstream, maximumLength, identity, onRewrite, getBearerToken) {
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
            if (body.length > 0 && request.headers["content-type"]?.includes("application/json")) {
                const payload = JSON.parse(body.toString("utf8"));
                const rewritten = rewriteOversizedInputItemIds(payload, maximumLength);
                if (rewritten > 0) {
                    onRewrite(rewritten);
                    body = Buffer.from(JSON.stringify(payload));
                }
            }

            const target = new URL(request.url ?? "/", upstream);
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

async function verifySharedProxy(port, identity) {
    let response;
    try {
        response = await fetch(`http://127.0.0.1:${port}${healthPath}`, {
            signal: AbortSignal.timeout(2000)
        });
    } catch {
        return false;
    }
    if (!response.ok) return false;
    try {
        const value = await response.json();
        return value?.marker === identity.marker &&
            value?.provider === identity.provider &&
            value?.upstream === identity.upstream;
    } catch {
        return false;
    }
}

export async function startRequestCompatibilityProxy(
    provider,
    { onRewrite = () => {}, getBearerToken, standbyRetryMs = 1000 } = {}
) {
    const maximumLength = provider.requestCompatibility?.maxInputItemIdLength;
    if (!Number.isInteger(maximumLength) || maximumLength < 16) return null;
    const configuredPort = provider.requestCompatibility?.proxyPort ?? 0;
    if (!Number.isInteger(configuredPort) || configuredPort < 0 || configuredPort > 65535) {
        throw new Error(`Provider '${provider.name}' has an invalid requestCompatibility.proxyPort.`);
    }

    const upstream = new URL(provider.baseUrl);
    const identity = proxyIdentity(provider, upstream);
    let activeServer;
    let standbyTimer;
    let binding = false;
    let closed = false;

    const create = () =>
        createProxyServer(provider, upstream, maximumLength, identity, onRewrite, getBearerToken);
    const first = create();
    try {
        await listen(first, configuredPort);
        activeServer = first;
    } catch (error) {
        if (configuredPort === 0 || error?.code !== "EADDRINUSE" ||
            !await verifySharedProxy(configuredPort, identity)) {
            throw error;
        }
        standbyTimer = setInterval(async () => {
            if (closed || binding || activeServer) return;
            binding = true;
            const candidate = create();
            try {
                await listen(candidate, configuredPort);
                clearInterval(standbyTimer);
                standbyTimer = undefined;
                if (closed) {
                    await new Promise(resolve => candidate.close(() => resolve()));
                } else {
                    activeServer = candidate;
                }
            } catch (bindError) {
                if (bindError?.code !== "EADDRINUSE") {
                    clearInterval(standbyTimer);
                    standbyTimer = undefined;
                }
            } finally {
                binding = false;
            }
        }, standbyRetryMs);
        standbyTimer.unref?.();
    }

    const address = activeServer?.address();
    const port = configuredPort || (address && typeof address !== "string" ? address.port : 0);
    if (!port) {
        throw new Error(`Unable to bind compatibility proxy for provider '${provider.name}'.`);
    }
    return {
        baseUrl: `http://127.0.0.1:${port}`,
        close: async () => {
            closed = true;
            if (standbyTimer) clearInterval(standbyTimer);
            if (!activeServer) return;
            await new Promise((resolve, reject) =>
                activeServer.close(error => error ? reject(error) : resolve())
            );
            activeServer = undefined;
        }
    };
}
