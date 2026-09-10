import assert from "node:assert/strict";
import { createServer } from "node:http";
import test from "node:test";
import { readResponseStream, terminalResponseFromEventStream } from "../extensions/BYOModels/extensions/BYOModels/response-stream.mjs";
import { proxyConfiguration, startRequestCompatibilityProxy } from "../extensions/BYOModels/extensions/BYOModels/request-compatibility.mjs";

const sse = event => `event: ${event.type}\ndata: ${JSON.stringify(event)}\n\n`;
const completed = {
  id: "resp_complete", object: "response", status: "completed", error: null,
  output: [{ type: "message", role: "assistant", content: [{ type: "output_text", text: "hello", annotations: [] }] }]
};

test("buffer limit cancels oversized upstream responses", async () => {
  let cancelled = false;
  const body = new ReadableStream({
    start(controller) { controller.enqueue(new Uint8Array(64 * 1024 * 1024 + 1)); },
    cancel() { cancelled = true; }
  });
  await assert.rejects(readResponseStream(new Response(body)), error => error.code === "upstream_response_too_large");
  assert.equal(cancelled, true);
});

test("terminal parsing preserves successful output, tool calls, and refusals", () => {
  for (const response of [
    completed,
    { ...completed, output: [{ type: "function_call", name: "add", call_id: "call_1", arguments: '{"a":1}' }] },
    { ...completed, output: [{ type: "message", role: "assistant", content: [{ type: "refusal", refusal: "Synthetic refusal." }] }] }
  ]) {
    assert.deepEqual(terminalResponseFromEventStream(sse({ type: "response.completed", response })), response);
  }
});

test("terminal parsing supports BOM, CRLF, event names, multiline data and final unterminated event", () => {
  const stream = '\uFEFF: keepalive\r\nevent: response.completed\r\ndata: {"response":\r\ndata: ' +
    JSON.stringify(completed) + '}';
  assert.deepEqual(terminalResponseFromEventStream(stream), completed);
});

test("failure, incomplete, malformed and truncated streams cannot become success", () => {
  const cases = [
    [sse({ type: "response.failed", response: { status: "failed", error: { code: "policy_denied", message: "Synthetic refusal." } } }), 422, "Synthetic refusal."],
    [sse({ type: "response.incomplete", response: { status: "incomplete", incomplete_details: { reason: "max_output_tokens" } } }), 422, "max_output_tokens"],
    [sse({ type: "error", error: { code: "rate_limit_exceeded", message: "Slow down." } }), 429, "Slow down."],
    [sse({ type: "error", error: '{"error":{"code":"server_error","message":"Unavailable."}}' }), 503, "Unavailable."],
    ['event: error\ndata: {"message":"Synthetic refusal."}\n\n', 422, "Synthetic refusal."],
    ['data: {invalid}\n\n', 502, "Invalid JSON"],
    ['data: [DONE]\n\n', 502, "without a terminal"],
    [sse({ type: "response.output_text.delta", delta: "partial" }), 502, "response.output_text.delta"],
    [sse({ type: "response.completed" }), 502, "no response object"],
    [sse({ type: "response.completed", response: { output: {} } }), 502, "invalid output"],
    [sse({ type: "response.completed", response: { output: [null] } }), 502, "invalid output"],
    [sse({ type: "response.completed", response: { output: [{ content: [null] }] } }), 502, "invalid output"]
  ];
  for (const [stream, status, message] of cases) {
    assert.throws(() => terminalResponseFromEventStream(stream), error =>
      error.status === status && error.message.includes(message));
  }
  assert.throws(() => terminalResponseFromEventStream(
    sse({ type: "error", message: "Do not overwrite this failure." }) +
    sse({ type: "response.completed", response: completed })
  ), /Do not overwrite/);
});

test("buffered proxy reports upstream failure before committing HTTP success", async () => {
  let currentEvent = { type: "response.failed", response: { status: "failed", error: { code: "policy_denied", message: "Synthetic upstream refusal." } } };
  const upstream = createServer(async (request, response) => {
    for await (const chunk of request) {}
    response.writeHead(200, { "content-type": "text/event-stream", "x-request-id": "upstream-123" });
    response.write(sse({ type: "response.output_text.delta", delta: "uncommitted partial text" }));
    response.end(sse(currentEvent));
  });
  await new Promise(resolve => upstream.listen(0, "127.0.0.1", resolve));
  const provider = { name: "buffered", baseUrl: `http://127.0.0.1:${upstream.address().port}` };
  assert.notEqual(proxyConfiguration(provider), proxyConfiguration({
    ...provider, requestCompatibility: { bufferResponses: true }
  }));
  const proxy = await startRequestCompatibilityProxy({ ...provider, requestCompatibility: { bufferResponses: true } });
  try {
    const send = () => fetch(`${proxy.baseUrl}/responses`, {
      method: "POST", headers: { "content-type": "application/json", authorization: `Bearer ${proxy.capability}` },
      body: JSON.stringify({ stream: true, input: "Say hello." })
    });
    const failed = await send();
    assert.equal(failed.status, 422);
    const error = (await failed.json()).error;
    assert.equal(error.code, "policy_denied");
    assert.match(error.message, /Synthetic upstream refusal/);
    assert.match(error.message, /upstream-123/);
    currentEvent = { type: "response.completed", response: completed };
    const success = await send();
    assert.equal(success.status, 200);
    assert.match(success.headers.get("content-type"), /text\/event-stream/);
    const body = await success.text();
    assert.deepEqual(terminalResponseFromEventStream(body), completed);
    assert.doesNotMatch(body, /uncommitted partial text/);
    currentEvent = { type: "response.completed", response: {
      ...completed, output: [{ type: "message", content: [{ type: "refusal", refusal: "Synthetic refusal is preserved." }] }]
    } };
    const refusal = await send();
    assert.equal(refusal.status, 422);
    const refusalError = (await refusal.json()).error;
    assert.equal(refusalError.code, "upstream_refusal");
    assert.match(refusalError.message, /Synthetic refusal is preserved/);
  } finally {
    await proxy.close();
    await new Promise(resolve => upstream.close(resolve));
  }
});

test("downstream cancellation closes the buffered upstream stream", async () => {
  let started;
  let closed;
  const upstreamStarted = new Promise(resolve => { started = resolve; });
  const upstreamClosed = new Promise(resolve => { closed = resolve; });
  const upstream = createServer(async (request, response) => {
    for await (const chunk of request) {}
    response.once("close", closed);
    response.writeHead(200, { "content-type": "text/event-stream" });
    response.write(sse({ type: "response.output_text.delta", delta: "pending" }));
    started();
  });
  await new Promise(resolve => upstream.listen(0, "127.0.0.1", resolve));
  const proxy = await startRequestCompatibilityProxy({
    name: "cancel", baseUrl: `http://127.0.0.1:${upstream.address().port}`,
    requestCompatibility: { bufferResponses: true }
  });
  let timer;
  const timeout = new Promise((_, reject) => {
    timer = setTimeout(() => reject(new Error("Upstream cancellation timed out.")), 5000);
  });
  const abort = new AbortController();
  try {
    const pending = fetch(`${proxy.baseUrl}/responses`, {
      method: "POST", headers: { "content-type": "application/json", authorization: `Bearer ${proxy.capability}` },
      body: '{"stream":true}', signal: abort.signal
    });
    const rejected = assert.rejects(pending, error => error.name === "AbortError");
    await Promise.race([upstreamStarted, timeout]);
    abort.abort();
    await rejected;
    await Promise.race([upstreamClosed, timeout]);
  } finally {
    clearTimeout(timer);
    abort.abort();
    upstream.closeAllConnections();
    await proxy.close();
    await new Promise(resolve => upstream.close(resolve));
  }
});

test("buffered proxy catches upstream socket interruption and remains healthy", async () => {
  let healthy = false;
  const upstream = createServer(async (request, response) => {
    for await (const chunk of request) {}
    response.writeHead(200, { "content-type": "text/event-stream" });
    if (healthy) {
      response.end(sse({ type: "response.completed", response: completed }));
    } else {
      response.write(sse({ type: "response.created", response: { id: "resp_cut" } }));
      setTimeout(() => response.destroy(), 10);
    }
  });
  await new Promise(resolve => upstream.listen(0, "127.0.0.1", resolve));
  const proxy = await startRequestCompatibilityProxy({
    name: "disconnect", baseUrl: `http://127.0.0.1:${upstream.address().port}`,
    requestCompatibility: { bufferResponses: true }
  });
  try {
    const send = () => fetch(`${proxy.baseUrl}/responses`, {
      method: "POST", headers: { "content-type": "application/json", authorization: `Bearer ${proxy.capability}` },
      body: '{"stream":true}'
    });
    const broken = await send();
    assert.equal(broken.status, 502);
    assert.equal((await broken.json()).error.code, "upstream_stream_interrupted");
    healthy = true;
    assert.equal((await send()).status, 200);
  } finally {
    await proxy.close();
    await new Promise(resolve => upstream.close(resolve));
  }
});
