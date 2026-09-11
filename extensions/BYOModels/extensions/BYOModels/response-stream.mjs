const maximumResponseBytes = 64 * 1024 * 1024;

export class ResponseStreamError extends Error {
    constructor(message, code = "upstream_stream_invalid", status = 502) {
        super(message);
        this.name = "ResponseStreamError";
        this.code = code;
        this.status = status;
    }
}

function upstreamError(value, fallback, fallbackCode) {
    let error = value;
    let code = fallbackCode;
    let message = fallback;
    for (let depth = 0; depth < 4; depth++) {
        if (typeof error === "string") {
            try { error = JSON.parse(error); }
            catch {
                if (error.trim()) message = error;
                break;
            }
        } else {
            if (typeof error?.code === "string" && error.code.trim()) code = error.code;
            if (typeof error?.message === "string" && error.message.trim()) message = error.message;
            else if (typeof error?.title === "string" && error.title.trim()) message = error.title;
            if (error?.error == null) break;
            error = error.error;
        }
    }
    const status = code === "rate_limit_exceeded" ? 429 :
        ["server_error", "internal_server_error", "service_unavailable"].includes(code) ? 503 : 422;
    return new ResponseStreamError(message, code, status);
}

export function terminalResponseFromEventStream(source) {
    let terminal;
    let failure;
    let eventName = "";
    let data = [];
    let lastEvent = "none";
    function flush() {
        const name = eventName;
        eventName = "";
        if (!data.length) return;
        const raw = data.join("\n");
        data = [];
        if (raw.trim() === "[DONE]") return;
        let event;
        try { event = JSON.parse(raw); }
        catch {
            throw new ResponseStreamError(`Invalid JSON in upstream Responses stream (last event: ${lastEvent}).`);
        }
        const type = event?.type ?? name;
        if (typeof type === "string" && type) lastEvent = type.slice(0, 120);
        if (type === "error" || event?.error != null) {
            failure = upstreamError(event,
                "Upstream Responses error event did not include a recognized error message.", "upstream_error");
        } else if (["response.completed", "response.failed", "response.incomplete"].includes(type)) {
            const response = event.response;
            if (!response || typeof response !== "object" || Array.isArray(response)) {
                throw new ResponseStreamError(`Upstream ${type} event has no response object.`);
            }
            if (type === "response.failed" || response.status === "failed" || response.error) {
                failure = upstreamError(response,
                    `Upstream ${type} event reported failure without a recognized error message.`, "upstream_response_failed");
            } else if (type === "response.incomplete" || response.status === "incomplete") {
                const reason = response.incomplete_details?.reason ?? "unspecified";
                failure = new ResponseStreamError(
                    `Upstream response is incomplete: ${reason}.`, "upstream_response_incomplete", 422
                );
            } else if (response.status !== undefined && response.status !== "completed") {
                throw new ResponseStreamError(`Unexpected upstream terminal response status: ${response.status}.`);
            } else {
                if (!Array.isArray(response.output) || response.output.some(item =>
                    !item || typeof item !== "object" || Array.isArray(item) ||
                    (item.content !== undefined && (!Array.isArray(item.content) ||
                        item.content.some(part => !part || typeof part !== "object" || Array.isArray(part)))))) {
                    throw new ResponseStreamError("Upstream completed response has invalid output content.");
                }
                terminal = response;
            }
        }
    }
    for (const line of source.replace(/^\uFEFF/, "").split(/\r\n|\r|\n/)) {
        if (line === "") {
            flush();
        } else if (line.startsWith("event:")) {
            eventName = line.slice(6).replace(/^ /, "");
        } else if (line.startsWith("data:")) {
            data.push(line.slice(5).replace(/^ /, ""));
        }
    }
    flush();
    if (failure) throw failure;
    if (!terminal) {
        throw new ResponseStreamError(
            `Upstream Responses stream ended without a terminal response event (last event: ${lastEvent}).`,
            "upstream_stream_incomplete"
        );
    }
    return terminal;
}

export async function readResponseStream(response) {
    const reader = response.body?.getReader();
    if (!reader) throw new ResponseStreamError("Upstream Responses stream has no body.");
    const chunks = [];
    let size = 0;
    try {
        while (true) {
            const { done, value } = await reader.read();
            if (done) break;
            size += value.byteLength;
            if (size > maximumResponseBytes) {
                await reader.cancel();
                throw new ResponseStreamError("Upstream Responses stream exceeds the 64 MiB buffer limit.", "upstream_response_too_large");
            }
            chunks.push(value);
        }
    } catch (error) {
        if (error instanceof ResponseStreamError) throw error;
        throw new ResponseStreamError("Upstream Responses stream was interrupted before it finished.", "upstream_stream_interrupted");
    } finally {
        reader.releaseLock();
    }
    return Buffer.concat(chunks).toString("utf8");
}

export async function pipeResponseStreamTokens(upstreamResponse, clientResponse, responseHeaders) {
    const reader = upstreamResponse.body?.getReader();
    if (!reader) throw new ResponseStreamError("Upstream Responses stream has no body.");
    const decoder = new TextDecoder();
    let buffer = "";
    let headersSent = false;
    const eventBuffer = [];
    let eventName = "";
    let dataLines = [];
    let lastEvent = "none";

    function flushBlock(rawBlock) {
        if (!dataLines.length && !eventName) return;
        const name = eventName;
        const rawData = dataLines.join("\n");
        eventName = "";
        dataLines = [];
        if (rawData.trim() === "[DONE]") {
            if (!headersSent) {
                throw new ResponseStreamError(
                    `Upstream Responses stream ended without a terminal response event (last event: ${lastEvent}).`,
                    "upstream_stream_incomplete"
                );
            }
            clientResponse.write("data: [DONE]\n\n");
            return;
        }

        let event = null;
        try {
            event = JSON.parse(rawData);
        } catch {
            throw new ResponseStreamError(`Invalid JSON in upstream Responses stream (last event: ${lastEvent}).`);
        }

        const type = event?.type ?? name;
        if (typeof type === "string" && type) lastEvent = type.slice(0, 120);

        if (type === "error" || event?.error != null) {
            const failure = upstreamError(event, "Upstream Responses error event did not include a recognized error message.", "upstream_error");
            if (!headersSent) {
                throw failure;
            } else {
                const errorText = `\n\n[Upstream Error: ${failure.code} - ${failure.message}]`;
                clientResponse.write(`event: response.output_text.delta\ndata: ${JSON.stringify({ type: "response.output_text.delta", delta: errorText })}\n\n`);
                clientResponse.write(`event: response.completed\ndata: ${JSON.stringify({
                    type: "response.completed",
                    response: {
                        id: event?.id ?? "err_response",
                        status: "completed",
                        output: [{
                            type: "message",
                            role: "assistant",
                            content: [{ type: "output_text", text: errorText }]
                        }]
                    }
                })}\n\n`);
                clientResponse.end();
                return;
            }
        }

        if (type === "response.failed" || event?.response?.status === "failed" || event?.response?.error) {
            const failure = upstreamError(event?.response ?? event, `Upstream ${type} event reported failure without a recognized error message.`, "upstream_response_failed");
            if (!headersSent) {
                throw failure;
            } else {
                const errorText = `\n\n[Upstream Error: ${failure.code} - ${failure.message}]`;
                clientResponse.write(`event: response.output_text.delta\ndata: ${JSON.stringify({ type: "response.output_text.delta", delta: errorText })}\n\n`);
                clientResponse.write(`event: response.completed\ndata: ${JSON.stringify({
                    type: "response.completed",
                    response: {
                        id: event?.response?.id ?? "err_response",
                        status: "completed",
                        output: [{
                            type: "message",
                            role: "assistant",
                            content: [{ type: "output_text", text: errorText }]
                        }]
                    }
                })}\n\n`);
                clientResponse.end();
                return;
            }
        }

        if (type === "response.incomplete" || event?.response?.status === "incomplete") {
            const reason = event?.response?.incomplete_details?.reason ?? "unspecified";
            if (!headersSent) {
                throw new ResponseStreamError(`Upstream response is incomplete: ${reason}.`, "upstream_response_incomplete", 422);
            }
        }

        if (type === "response.completed") {
            const refusals = (event?.response?.output ?? []).flatMap(item =>
                (item.content ?? []).filter(part => part.type === "refusal" && typeof part.refusal === "string")
                    .map(part => part.refusal)
            );
            if (refusals.length) {
                if (!headersSent) {
                    throw new ResponseStreamError(refusals.join("\n"), "upstream_refusal", 422);
                } else {
                    const refusalText = `\n\n[Refusal: ${refusals.join("\n")}]`;
                    clientResponse.write(`event: response.output_text.delta\ndata: ${JSON.stringify({ type: "response.output_text.delta", delta: refusalText })}\n\n`);
                    const transformed = structuredClone(event);
                    for (const item of transformed.response.output ?? []) {
                        item.content = (item.content ?? []).map(part =>
                            part.type === "refusal" ? { type: "output_text", text: part.refusal } : part
                        );
                    }
                    clientResponse.write(`event: response.completed\ndata: ${JSON.stringify(transformed)}\n\n`);
                    return;
                }
            }
        }

        const isContent = type === "response.output_text.delta" ||
            type === "response.output_item.added" ||
            type === "response.content_part.added" ||
            type === "response.completed" ||
            (Array.isArray(event?.response?.output) && event.response.output.length > 0) ||
            type?.includes("function_call");

        const rawEvent = rawBlock.trim() + "\n\n";

        if (!headersSent) {
            if (isContent) {
                clientResponse.writeHead(upstreamResponse.status, responseHeaders);
                headersSent = true;
                for (const buffered of eventBuffer) {
                    clientResponse.write(buffered);
                }
                eventBuffer.length = 0;
                clientResponse.write(rawEvent);
            } else {
                eventBuffer.push(rawEvent);
            }
        } else {
            clientResponse.write(rawEvent);
        }
    }

    try {
        while (true) {
            const { done, value } = await reader.read();
            if (done) break;
            buffer += decoder.decode(value, { stream: true });
            const parts = buffer.split(/\r?\n\r?\n/);
            buffer = parts.pop() ?? "";
            for (const part of parts) {
                if (!part.trim()) continue;
                for (const line of part.replace(/^\uFEFF/, "").split(/\r?\n/)) {
                    if (line.startsWith("event:")) eventName = line.slice(6).replace(/^ /, "");
                    else if (line.startsWith("data:")) dataLines.push(line.slice(5).replace(/^ /, ""));
                }
                flushBlock(part);
            }
        }

        if (buffer.trim()) {
            for (const line of buffer.replace(/^\uFEFF/, "").split(/\r?\n/)) {
                if (line.startsWith("event:")) eventName = line.slice(6).replace(/^ /, "");
                else if (line.startsWith("data:")) dataLines.push(line.slice(5).replace(/^ /, ""));
            }
            flushBlock(buffer);
        }

        if (!headersSent) {
            throw new ResponseStreamError(
                `Upstream Responses stream ended without a terminal response event (last event: ${lastEvent}).`,
                "upstream_stream_incomplete"
            );
        }
        clientResponse.end();
    } catch (error) {
        try { await reader.cancel(); } catch {}
        throw error;
    } finally {
        reader.releaseLock();
    }
}
