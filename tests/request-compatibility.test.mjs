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
