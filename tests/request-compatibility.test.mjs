import assert from "node:assert/strict";
import { createServer, request as httpRequest } from "node:http";
import test from "node:test";
import {
  proxyCapabilityHeader,
  proxyConfiguration,
  startRequestCompatibilityProxy
} from "../extensions/BYOModels/extensions/BYOModels/request-compatibility.mjs";

function proxyHeaders(proxy, extra = {}) {
  return { ...extra, [proxyCapabilityHeader]: proxy.capability };
}

test("legacy tool compatibility is opt-in and participates in proxy ownership", async () => {
  const upstream = createServer(async (request, response) => {
    const chunks = [];
    for await (const chunk of request) chunks.push(chunk);
    response.writeHead(200, { "content-type": "application/json" });
    response.end(Buffer.concat(chunks));
  });
  await new Promise(resolve => upstream.listen(0, "127.0.0.1", resolve));
  const provider = { name: "legacy", baseUrl: `http://127.0.0.1:${upstream.address().port}` };
  assert.notEqual(proxyConfiguration(provider), proxyConfiguration({
    ...provider, requestCompatibility: { legacyTools: true }
  }));
  try {
    for (const legacyTools of [false, true]) {
      const proxy = await startRequestCompatibilityProxy({
        ...provider, requestCompatibility: { legacyTools, maxInputItemIdLength: 64 }
      });
      try {
        const tools = [{ type: "namespace", name: "mcp", tools: [
          { type: "function", name: "mcp_read", defer_loading: true, parameters: { type: "object" } }
        ] }, { type: "tool_search" }];
        const response = await fetch(`${proxy.baseUrl}/responses`, {
          method: "POST", headers: proxyHeaders(proxy, { "content-type": "application/json" }),
          body: JSON.stringify({ tools })
        });
        assert.equal(response.status, 200);
        const body = await response.json();
        if (legacyTools) {
          assert.deepEqual(body.tools.map(tool => tool.name), ["mcp_read"]);
          assert.equal(body.tools[0].defer_loading, undefined);
        } else {
          assert.deepEqual(body.tools, tools);
        }
      } finally { await proxy.close(); }
    }
  } finally { await new Promise(resolve => upstream.close(resolve)); }
});

function sendRawRequest(url, { headers, body }) {
  return new Promise((resolve, reject) => {
    const request = httpRequest(url, { method: "POST", headers }, (response) => {
      const chunks = [];
      response.on("data", chunk => chunks.push(chunk));
      response.on("end", () => resolve({
        body: Buffer.concat(chunks),
        headers: response.headers,
        status: response.statusCode
      }));
    });
    request.on("error", reject);
    request.end(body);
  });
}

test("proxy configuration normalizes header names and includes values", () => {
  const provider = {
    auth: { type: "azure-cli", resource: "https://resource.example" },
    headers: { "X-Z": "2", "x-a": "1" },
    requestCompatibility: { maxInputItemIdLength: 64, forceStreaming: true }
  };
  assert.equal(proxyConfiguration(provider), "xxJUMvZ6fjU1ZvCj3mIFY-YhweILh_zxKyVeZ3mTSAs");
  assert.equal(proxyConfiguration({
    ...provider,
    headers: { "X-A": "1", "x-z": "2" }
  }), proxyConfiguration(provider));
  assert.notEqual(proxyConfiguration({
    ...provider,
    headers: { "x-a": "changed", "x-z": "2" }
  }), proxyConfiguration(provider));

  const configuredBearer = {
    auth: { type: "bearer-token", value: "configured-token" },
    requestCompatibility: { maxInputItemIdLength: 64 }
  };
  assert.equal(proxyConfiguration(configuredBearer), "dwYzmfdNzoCy2mLHJSPbJwr8k85Kp3kK1qZyTRosLyc");
  assert.notEqual(proxyConfiguration({
    ...configuredBearer,
    auth: { ...configuredBearer.auth, value: "changed-token" }
  }), proxyConfiguration(configuredBearer));
});

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
      headers: proxyHeaders(proxy, {
        "content-type": "application/json"
      }),
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

test("compatibility proxy normalizes wire model catalog IDs to stable configured IDs", async () => {
  const upstream = createServer((_request, response) => {
    response.writeHead(200, { "content-type": "application/json" });
    response.end(JSON.stringify({
      object: "list",
      data: [
        { id: "/workspace/models/qwen.gguf", object: "model" },
        { id: "qwen-stable", object: "model" }
      ]
    }));
  });
  await new Promise(resolve => upstream.listen(0, "127.0.0.1", resolve));
  const proxy = await startRequestCompatibilityProxy({
    name: "doi",
    baseUrl: `http://127.0.0.1:${upstream.address().port}`,
    modelAliases: { "/workspace/models/qwen.gguf": "qwen-stable" },
    requestCompatibility: { forceStreaming: true }
  });
  try {
    const response = await fetch(`${proxy.baseUrl}/models`, {
      headers: proxyHeaders(proxy)
    });
    assert.equal(response.status, 200);
    assert.deepEqual(await response.json(), {
      object: "list",
      data: [{ id: "qwen-stable", object: "model" }]
    });
  } finally {
    await proxy.close();
    await new Promise((resolve, reject) => upstream.close(error => error ? reject(error) : resolve()));
  }
});

test("compatibility proxy reports an explicit upstream timeout", async () => {
  const upstream = createServer((_request, response) => {
    setTimeout(() => {
      response.writeHead(200);
      response.end();
    }, 250);
  });
  await new Promise(resolve => upstream.listen(0, "127.0.0.1", resolve));
  const proxy = await startRequestCompatibilityProxy({
    name: "slow-provider",
    baseUrl: `http://127.0.0.1:${upstream.address().port}`,
    requestCompatibility: { maxInputItemIdLength: 64 }
  }, { upstreamTimeout: 30 });
  try {
    const response = await fetch(`${proxy.baseUrl}/responses`, {
      method: "POST",
      headers: proxyHeaders(proxy, { "content-type": "application/json" }),
      body: "{}"
    });
    assert.equal(response.status, 502);
    const body = await response.json();
    assert.match(body.error.message, /slow-provider.*made no progress for 30ms/);
  } finally {
    await proxy.close();
    await new Promise(resolve => upstream.close(resolve));
  }
});

test("compatibility proxy resets its idle timeout on upstream progress", async () => {
  const upstream = createServer((_request, response) => {
    response.writeHead(200, { "content-type": "text/event-stream" });
    let remaining = 6;
    const interval = setInterval(() => {
      response.write(": ping\n\n");
      remaining--;
      if (remaining === 0) {
        clearInterval(interval);
        response.end('event: response.completed\ndata: {"type":"response.completed","response":{"status":"completed","output":[]}}\n\n');
      }
    }, 50);
  });
  await new Promise(resolve => upstream.listen(0, "127.0.0.1", resolve));
  const proxy = await startRequestCompatibilityProxy({
    name: "progress-provider",
    baseUrl: `http://127.0.0.1:${upstream.address().port}`,
    requestCompatibility: { maxInputItemIdLength: 64 }
  }, { upstreamTimeout: 200 });
  try {
    const started = Date.now();
    const response = await fetch(`${proxy.baseUrl}/responses`, {
      method: "POST",
      headers: proxyHeaders(proxy, { "content-type": "application/json" }),
      body: '{"stream":true}'
    });
    const body = await response.text();
    assert.equal(response.status, 200);
    assert.ok(Date.now() - started > 200);
    assert.match(body, /response\.completed/);
  } finally {
    await proxy.close();
    await new Promise(resolve => upstream.close(resolve));
  }
});

test("compatibility proxy rejects unauthorized callers before token acquisition or forwarding", async () => {
  let upstreamRequests = 0;
  let tokenRequests = 0;
  const upstream = createServer((_request, response) => {
    upstreamRequests++;
    response.writeHead(200, { "content-type": "application/json" });
    response.end('{"ok":true}');
  });
  await new Promise(resolve => upstream.listen(0, "127.0.0.1", resolve));
  const address = upstream.address();
  const proxy = await startRequestCompatibilityProxy({
    name: "auth-test",
    baseUrl: `http://127.0.0.1:${address.port}`,
    requestCompatibility: { maxInputItemIdLength: 64 }
  }, {
    getBearerToken: async () => {
      tokenRequests++;
      return "upstream-token";
    }
  });
  try {
    for (const headers of [{}, { [proxyCapabilityHeader]: "wrong" }, { authorization: "Bearer wrong" }]) {
      const response = await fetch(`${proxy.baseUrl}/responses`, {
        method: "POST",
        headers: { ...headers, "content-type": "application/json" },
        body: JSON.stringify({ input: [{ id: "x".repeat(100) }] })
      });
      assert.equal(response.status, 401);
    }
    const health = await fetch(`${proxy.baseUrl}${"/__afterburner/byomodels/health"}`);
    assert.deepEqual(await health.json(), { marker: "afterburner-byomodels-proxy-v1", ready: true });
    assert.equal(tokenRequests, 0);
    assert.equal(upstreamRequests, 0);

    const allowed = await fetch(`${proxy.baseUrl}/responses`, {
      method: "POST",
      headers: { authorization: `Bearer ${proxy.capability}`, "content-type": "application/json" },
      body: "{}"
    });
    assert.equal(allowed.status, 200);
    assert.equal(tokenRequests, 1);
    assert.equal(upstreamRequests, 1);
  } finally {
    await proxy.close();
    await new Promise((resolve, reject) => upstream.close(error => error ? reject(error) : resolve()));
  }
});

test("compatibility proxy adapts non-streaming Responses calls for streaming-only endpoints", async () => {
  const received = [];
  const upstream = createServer(async (request, response) => {
    assert.equal(request.headers["x-ms-scp-use-cell"], "true");
    assert.equal(request.headers["x-client-hop"], undefined);
    assert.equal(request.headers["x-copilot-internal"], undefined);
    assert.equal(request.headers["proxy-authorization"], undefined);
    assert.equal(request.headers.te, undefined);
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
    headers: { "x-ms-scp-use-cell": "true" },
    requestCompatibility: { forceStreaming: true }
  });
  try {
    const response = await sendRawRequest(`${proxy.baseUrl}/responses?trace=1`, {
      headers: proxyHeaders(proxy, {
        "content-type": "application/json",
        connection: "x-client-hop",
        "proxy-authorization": "secret",
        te: "trailers",
        "x-client-hop": "must-not-forward",
        "x-copilot-internal": "must-not-forward",
        "x-ms-scp-use-cell": "false"
      }),
      body: JSON.stringify({ model: "wire-model", input: "hello" })
    });
    assert.equal(response.status, 200);
    assert.match(response.headers["content-type"], /^application\/json/);
    assert.deepEqual(JSON.parse(response.body.toString("utf8")), {
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
      headers: proxyHeaders(proxy, { "content-type": "application/json" }),
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
    await fetch(`${sessionA.baseUrl}/responses`, { method: "POST", headers: proxyHeaders(sessionA, { "content-type": "application/json" }), body: "{}" });
    await fetch(`${sessionB.baseUrl}/responses`, { method: "POST", headers: proxyHeaders(sessionB, { "content-type": "application/json" }), body: "{}" });
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
    const response = await fetch(`${proxy.baseUrl}/responses`, { headers: proxyHeaders(proxy) });
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
    await fetch(`${proxyA.baseUrl}/responses`, { method: "POST", headers: proxyHeaders(proxyA, { "content-type": "application/json" }), body: "{}" });
    await fetch(`${proxyB.baseUrl}/responses`, { method: "POST", headers: proxyHeaders(proxyB, { "content-type": "application/json" }), body: "{}" });
    assert.deepEqual(received.map(item => item.url).sort(), ["/a/responses", "/b/responses"]);
    assert.deepEqual(new Set(received.map(item => item.authorization)), new Set(["Bearer token-a", "Bearer token-b"]));
  } finally {
    await proxyA.close();
    await proxyB.close();
    await new Promise((resolve, reject) => upstream.close(error => error ? reject(error) : resolve()));
  }
});

test("compatibility proxy maps upstream 429 deployment-unhealthy to 422 with clear message", async () => {
  const upstream = createServer(async (request, response) => {
    response.writeHead(429, { "content-type": "application/json" });
    response.end('{"error":"No healthy deployment found for the request."}');
  });
  await new Promise(resolve => upstream.listen(0, "127.0.0.1", resolve));
  const proxy = await startRequestCompatibilityProxy({
    name: "unhealthy-test",
    baseUrl: `http://127.0.0.1:${upstream.address().port}`,
    requestCompatibility: { forceStreaming: true }
  });
  try {
    const response = await fetch(`${proxy.baseUrl}/responses`, {
      method: "POST",
      headers: proxyHeaders(proxy, { "content-type": "application/json" }),
      body: JSON.stringify({ model: "test-model", input: "hello" })
    });
    assert.equal(response.status, 422);
    const data = await response.json();
    assert.equal(data.error.code, "upstream_deployment_unhealthy");
    assert.match(data.error.message, /test-model/);
    assert.match(data.error.message, /unhealthy or unavailable/);
  } finally {
    await proxy.close();
    await new Promise((resolve, reject) => upstream.close(error => error ? reject(error) : resolve()));
  }
});
