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

test("compatibility proxies use process-local ownership when sessions start simultaneously", async () => {
  const receivedAuthorization = [];
  const upstream = createServer(async (request, response) => {
    receivedAuthorization.push(request.headers.authorization);
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
    name: "simultaneous-test",
    baseUrl: `http://127.0.0.1:${upstreamAddress.port}`,
    auth: { type: "azure-cli", resource: "https://resource.example" },
    requestCompatibility: { maxInputItemIdLength: 64, proxyPort }
  };
  const [sessionA, sessionB] = await Promise.all([
    startRequestCompatibilityProxy(provider, { getBearerToken: async () => "session-a-token" }),
    startRequestCompatibilityProxy(provider, { getBearerToken: async () => "session-b-token" })
  ]);
  try {
    assert.notEqual(sessionA.baseUrl, sessionB.baseUrl);
    assert.equal(new URL(sessionA.baseUrl).port === String(proxyPort) || new URL(sessionB.baseUrl).port === String(proxyPort), true);
    await fetch(`${sessionA.baseUrl}/responses`, { method: "POST", headers: { "content-type": "application/json" }, body: "{}" });
    await fetch(`${sessionB.baseUrl}/responses`, { method: "POST", headers: { "content-type": "application/json" }, body: "{}" });
    assert.deepEqual(new Set(receivedAuthorization), new Set(["Bearer session-a-token", "Bearer session-b-token"]));
  } finally {
    await sessionA.close();
    await sessionB.close();
    await new Promise((resolve, reject) => upstream.close(error => error ? reject(error) : resolve()));
  }
});

test("compatibility proxy falls back when the configured port is occupied by stale or unrelated work", async () => {
  const upstream = createServer((_request, response) => {
    response.writeHead(200, { "content-type": "application/json" });
    response.end('{"ok":true}');
  });
  const stale = createServer((_request, response) => {
    response.writeHead(404);
    response.end();
  });
  await new Promise(resolve => upstream.listen(0, "127.0.0.1", resolve));
  await new Promise(resolve => stale.listen(0, "127.0.0.1", resolve));
  const upstreamAddress = upstream.address();
  const staleAddress = stale.address();
  const proxy = await startRequestCompatibilityProxy({
    name: "stale-port-test",
    baseUrl: `http://127.0.0.1:${upstreamAddress.port}`,
    requestCompatibility: { maxInputItemIdLength: 64, proxyPort: staleAddress.port }
  });
  try {
    assert.notEqual(new URL(proxy.baseUrl).port, String(staleAddress.port));
    const response = await fetch(`${proxy.baseUrl}/responses`);
    assert.equal(response.status, 200);
    assert.deepEqual(await response.json(), { ok: true });
  } finally {
    await proxy.close();
    await new Promise((resolve, reject) => stale.close(error => error ? reject(error) : resolve()));
    await new Promise((resolve, reject) => upstream.close(error => error ? reject(error) : resolve()));
  }
});

test("providers sharing a configured port each receive an isolated local proxy", async () => {
  const received = [];
  const upstream = createServer(async (request, response) => {
    received.push({ url: request.url, authorization: request.headers.authorization });
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
  const providerA = {
    name: "provider-a",
    baseUrl: `http://127.0.0.1:${upstreamAddress.port}/a`,
    requestCompatibility: { maxInputItemIdLength: 64, proxyPort }
  };
  const providerB = {
    name: "provider-b",
    baseUrl: `http://127.0.0.1:${upstreamAddress.port}/b`,
    requestCompatibility: { maxInputItemIdLength: 64, proxyPort }
  };
  const [proxyA, proxyB] = await Promise.all([
    startRequestCompatibilityProxy(providerA, { getBearerToken: async () => "token-a" }),
    startRequestCompatibilityProxy(providerB, { getBearerToken: async () => "token-b" })
  ]);
  try {
    assert.notEqual(proxyA.baseUrl, proxyB.baseUrl);
    await fetch(`${proxyA.baseUrl}/responses`, { method: "POST", headers: { "content-type": "application/json" }, body: "{}" });
    await fetch(`${proxyB.baseUrl}/responses`, { method: "POST", headers: { "content-type": "application/json" }, body: "{}" });
    assert.deepEqual(received.map(item => item.url).sort(), ["/a/responses", "/b/responses"]);
    assert.deepEqual(new Set(received.map(item => item.authorization)), new Set(["Bearer token-a", "Bearer token-b"]));
  } finally {
    await proxyA.close();
    await proxyB.close();
    await new Promise((resolve, reject) => upstream.close(error => error ? reject(error) : resolve()));
  }
});
