import assert from "node:assert/strict";
import { createServer } from "node:http";
import test from "node:test";
import { startRequestCompatibilityProxy } from "../extensions/BYOModels/extensions/BYOModels/request-compatibility.mjs";

test("compatibility proxy normalizes IDs and refreshes authentication", async () => {
  let received;
  let receivedAuthorization;
  let tokenRequestCount = 0;
  const upstream = createServer(async (request, response) => {
    const chunks = [];
    for await (const chunk of request) chunks.push(chunk);
    received = JSON.parse(Buffer.concat(chunks).toString("utf8"));
    receivedAuthorization = request.headers.authorization;
    response.writeHead(200, { "content-type": "application/json" });
    response.end('{"ok":true}');
  });
  await new Promise(resolve => upstream.listen(0, "127.0.0.1", resolve));
  const address = upstream.address();
  const proxy = await startRequestCompatibilityProxy({
    name: "test",
    baseUrl: `http://127.0.0.1:${address.port}`,
    requestCompatibility: { maxInputItemIdLength: 64 }
  }, {
    getBearerToken: async () => `fresh-token-${++tokenRequestCount}`
  });
  try {
    const oversizedId = "x".repeat(496);
    const sendRequest = () => fetch(`${proxy.baseUrl}/responses`, {
      method: "POST",
      headers: {
        "content-type": "application/json",
        authorization: "******"
      },
      body: JSON.stringify({
        input: [
          { type: "tool_search_call", id: oversizedId },
          { type: "reasoning", id: oversizedId },
          { type: "message", id: "valid-id" }
        ]
      })
    });
    await sendRequest();
    assert.equal(received.input[0].id, received.input[1].id);
    assert.ok(received.input[0].id.length <= 64);
    assert.equal(received.input[2].id, "valid-id");
    assert.equal(receivedAuthorization, "Bearer " + ["fresh", "token", "1"].join("-"));
    await sendRequest();
    assert.equal(receivedAuthorization, "Bearer " + ["fresh", "token", "2"].join("-"));
    assert.equal(tokenRequestCount, 2);
  } finally {
    await proxy.close();
    await new Promise((resolve, reject) => upstream.close(error => error ? reject(error) : resolve()));
  }
});

test("compatibility proxy adapts non-streaming Responses calls for streaming-only endpoints", async () => {
  const received = [];
  const upstream = createServer(async (request, response) => {
    const chunks = [];
    for await (const chunk of request) chunks.push(chunk);
    const payload = JSON.parse(Buffer.concat(chunks).toString("utf8"));
    received.push({ url: request.url, payload });
    response.writeHead(200, { "content-type": "text/event-stream" });
    response.end([
      'event: response.created',
      'data: {"type":"response.created","response":{"id":"response-1","status":"in_progress"}}',
      "",
      'event: response.completed',
      'data: {"type":"response.completed","response":{"id":"response-1","status":"completed","model":"wire-model","output":[]}}',
      "",
      "data: [DONE]",
      "",
      ""
    ].join("\n"));
  });
  await new Promise(resolve => upstream.listen(0, "127.0.0.1", resolve));
  const address = upstream.address();
  const proxy = await startRequestCompatibilityProxy({
    name: "streaming-only",
    baseUrl: `http://127.0.0.1:${address.port}/workspaces/default/stream/2.0/openai/v1?api-version=1`,
    requestCompatibility: { forceStreaming: true }
  });
  try {
    const response = await fetch(`${proxy.baseUrl}/responses?trace=1`, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ model: "wire-model", input: "hello" })
    });
    assert.equal(response.status, 200);
    assert.match(response.headers.get("content-type"), /^application\/json/);
    assert.deepEqual(await response.json(), {
      id: "response-1",
      status: "completed",
      model: "wire-model",
      output: []
    });
    assert.equal(
      received[0].url,
      "/workspaces/default/stream/2.0/openai/v1/responses?api-version=1&trace=1"
    );
    assert.equal(received[0].payload.model, "wire-model");
    assert.equal(received[0].payload.stream, true);
  } finally {
    await proxy.close();
    await new Promise((resolve, reject) => upstream.close(error => error ? reject(error) : resolve()));
  }
});

test("compatibility proxy preserves caller-requested streaming responses", async () => {
  let upstreamStream;
  const eventStream = [
    'data: {"type":"response.output_text.delta","delta":"hello"}',
    "",
    "data: [DONE]",
    "",
    ""
  ].join("\n");
  const upstream = createServer(async (request, response) => {
    const chunks = [];
    for await (const chunk of request) chunks.push(chunk);
    upstreamStream = JSON.parse(Buffer.concat(chunks).toString("utf8")).stream;
    response.writeHead(200, { "content-type": "text/event-stream" });
    response.end(eventStream);
  });
  await new Promise(resolve => upstream.listen(0, "127.0.0.1", resolve));
  const address = upstream.address();
  const proxy = await startRequestCompatibilityProxy({
    name: "streaming-passthrough",
    baseUrl: `http://127.0.0.1:${address.port}`,
    requestCompatibility: { forceStreaming: true }
  });
  try {
    const response = await fetch(`${proxy.baseUrl}/responses`, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ model: "wire-model", stream: true, input: "hello" })
    });
    assert.equal(response.status, 200);
    assert.match(response.headers.get("content-type"), /^text\/event-stream/);
    assert.equal(await response.text(), eventStream);
    assert.equal(upstreamStream, true);
  } finally {
    await proxy.close();
    await new Promise((resolve, reject) => upstream.close(error => error ? reject(error) : resolve()));
  }
});

test("stable compatibility proxy survives owner handoff between sessions", async () => {
  const upstream = createServer((_request, response) => {
    response.writeHead(200, { "content-type": "application/json" });
    response.end('{"ok":true}');
  });
  await new Promise(resolve => upstream.listen(0, "127.0.0.1", resolve));
  const upstreamAddress = upstream.address();
  const reservation = createServer();
  await new Promise(resolve => reservation.listen(0, "127.0.0.1", resolve));
  const reservedAddress = reservation.address();
  const proxyPort = reservedAddress.port;
  await new Promise((resolve, reject) => reservation.close(error => error ? reject(error) : resolve()));
  const provider = {
    name: "stable-test",
    baseUrl: `http://127.0.0.1:${upstreamAddress.port}`,
    requestCompatibility: { maxInputItemIdLength: 64, proxyPort }
  };
  const owner = await startRequestCompatibilityProxy(provider);
  const standby = await startRequestCompatibilityProxy(provider, { standbyRetryMs: 10 });
  try {
    assert.equal(owner.baseUrl, standby.baseUrl);
    await owner.close();
    let response;
    for (let attempt = 0; attempt < 50; attempt++) {
      try {
        response = await fetch(`${standby.baseUrl}/responses`);
        if (response.ok) break;
      } catch {}
      await new Promise(resolve => setTimeout(resolve, 10));
    }
    assert.equal(response?.status, 200);
  } finally {
    await standby.close();
    await new Promise((resolve, reject) => upstream.close(error => error ? reject(error) : resolve()));
  }
});

test("stable compatibility proxy rejects an unrelated listener", async () => {
  const unrelated = createServer((_request, response) => {
    response.writeHead(404);
    response.end();
  });
  await new Promise(resolve => unrelated.listen(0, "127.0.0.1", resolve));
  const address = unrelated.address();
  try {
    await assert.rejects(() => startRequestCompatibilityProxy({
      name: "collision-test",
      baseUrl: "https://example.invalid",
      requestCompatibility: { maxInputItemIdLength: 64, proxyPort: address.port }
    }), error => error?.code === "EADDRINUSE");
  } finally {
    await new Promise((resolve, reject) => unrelated.close(error => error ? reject(error) : resolve()));
  }
});

test("stable compatibility proxy rejects a listener with different translation settings", async () => {
  const upstream = createServer((_request, response) => {
    response.writeHead(200, { "content-type": "application/json" });
    response.end('{"ok":true}');
  });
  await new Promise(resolve => upstream.listen(0, "127.0.0.1", resolve));
  const upstreamAddress = upstream.address();
  const reservation = createServer();
  await new Promise(resolve => reservation.listen(0, "127.0.0.1", resolve));
  const reservedAddress = reservation.address();
  const proxyPort = reservedAddress.port;
  await new Promise((resolve, reject) => reservation.close(error => error ? reject(error) : resolve()));
  const owner = await startRequestCompatibilityProxy({
    name: "settings-test",
    baseUrl: `http://127.0.0.1:${upstreamAddress.port}`,
    requestCompatibility: { maxInputItemIdLength: 64, proxyPort }
  });
  try {
    await assert.rejects(() => startRequestCompatibilityProxy({
      name: "settings-test",
      baseUrl: `http://127.0.0.1:${upstreamAddress.port}`,
      requestCompatibility: { maxInputItemIdLength: 64, forceStreaming: true, proxyPort }
    }), error => error?.code === "EADDRINUSE");
  } finally {
    await owner.close();
    await new Promise((resolve, reject) => upstream.close(error => error ? reject(error) : resolve()));
  }
});
