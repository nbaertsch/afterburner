import { createHash } from "node:crypto";
import { createServer } from "node:http";
import { Readable } from "node:stream";

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

export async function startRequestCompatibilityProxy(
    provider,
    { onRewrite = () => {}, getBearerToken } = {}
) {
    const maximumLength = provider.requestCompatibility?.maxInputItemIdLength;
    if (!Number.isInteger(maximumLength) || maximumLength < 16) return null;
    const upstream = new URL(provider.baseUrl);
    const server = createServer(async (request, response) => {
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
                    message: `BYOK request compatibility proxy failed: ${error?.message ?? String(error)}`
                }
            }));
        }
    });
    await new Promise((resolve, reject) => {
        server.once("error", reject);
        server.listen(0, "127.0.0.1", resolve);
    });
    const address = server.address();
    if (!address || typeof address === "string") {
        server.close();
        throw new Error(`Unable to bind compatibility proxy for provider '${provider.name}'.`);
    }
    return {
        baseUrl: `http://127.0.0.1:${address.port}`,
        close: () => new Promise((resolve, reject) =>
            server.close((error) => error ? reject(error) : resolve())
        )
    };
}
