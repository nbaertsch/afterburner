import assert from "node:assert/strict";
import { createServer } from "node:http";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { pathToFileURL } from "node:url";

if (!process.argv[2] || !process.argv[3]) {
    throw new Error("Usage: node tests\\real-responses-client.mjs <copilot-sdk-directory> <copilot.exe> [proxy-module]");
}
const { startRequestCompatibilityProxy } = await import(process.argv[4]
    ? pathToFileURL(resolve(process.argv[4]))
    : new URL("../extensions/BYOModels/extensions/BYOModels/request-compatibility.mjs", import.meta.url));
const { CopilotClient, RuntimeConnection } = await import(pathToFileURL(join(resolve(process.argv[2]), "index.js")));
const root = mkdtempSync(join(tmpdir(), "afterburn-responses-client-"));
const completed = output => ({
    id: "resp_test", object: "response", status: "completed", model: "gpt-5.4", error: null, output
});
const message = text => ({ type: "message", id: "msg_test", role: "assistant", status: "completed",
    content: [{ type: "output_text", text, annotations: [] }] });
let scenario;
let calls = 0;
let toolCalls = 0;
const upstream = createServer(async (request, response) => {
    const chunks = [];
    for await (const chunk of request) chunks.push(chunk);
    const input = JSON.parse(Buffer.concat(chunks));
    calls++;
    let event;
    switch (scenario) {
        case "error":
            event = { type: "error", message: "Synthetic provider denial.", code: "policy_denied" };
            break;
        case "null-error":
            event = { type: "error", error: null, message: "Synthetic provider denial.", code: "policy_denied" };
            break;
        case "failed-null-error":
            event = { type: "response.failed", response: {
                status: "failed", error: null, message: "Synthetic provider denial.", code: "policy_denied"
            } };
            break;
        case "failed":
            event = { type: "response.failed", response: { status: "failed", error: {
                code: "policy_denied", message: "Synthetic provider denial."
            } } };
            break;
        case "incomplete":
            event = { type: "response.incomplete", response: {
                status: "incomplete", incomplete_details: { reason: "max_output_tokens" }
            } };
            break;
        case "refusal":
            event = { type: "response.completed", response: completed([{
                type: "message", id: "msg_test", role: "assistant", status: "completed",
                content: [{ type: "refusal", refusal: "Synthetic provider denial." }]
            }]) };
            break;
        case "tool":
            for (const output of input.input?.filter(item => item.type === "function_call_output") ?? []) {
                assert.equal(output.call_id, "call_test");
                assert.equal(output.output, "3");
            }
            event = { type: "response.completed", response: completed(
                input.input?.some(item => item.type === "function_call_output")
                    ? [message("TOOL_OK 3")]
                    : [{ type: "function_call", id: "fc_test", call_id: "call_test", name: "add", arguments: '{"a":1,"b":2}', status: "completed" }]
            ) };
            break;
        default:
            event = { type: "response.completed", response: completed([message("HELLO_OK")]) };
    }
    response.writeHead(200, { "content-type": "text/event-stream", "x-request-id": "synthetic-upstream-request" });
    response.end(`event: ${event.type}\ndata: ${JSON.stringify(event)}\n\n`);
});
await new Promise(resolve => upstream.listen(0, "127.0.0.1", resolve));
const proxy = await startRequestCompatibilityProxy({
    name: "native-client-test", baseUrl: `http://127.0.0.1:${upstream.address().port}`,
    requestCompatibility: { bufferResponses: true }
});
const env = { ...process.env };
for (const key of Object.keys(env)) {
    if (/^(AFTERBURNER_|COPILOT_AGENT_SESSION_ID|COPILOT_LOADER_PID|COPILOT_SUPERVISED|COPILOT_HOME)/.test(key)) delete env[key];
}
const client = new CopilotClient({
    mode: "empty", baseDirectory: root, workingDirectory: root, env,
    connection: RuntimeConnection.forStdio({ path: resolve(process.argv[3]) })
});
try {
    await client.start();
    for (scenario of ["error", "null-error", "failed", "failed-null-error", "incomplete", "refusal", "completed", "tool"]) {
        calls = 0;
        toolCalls = 0;
        const session = await client.createSession({
            model: "gpt-5.4", availableTools: scenario === "tool" ? ["add"] : [], streaming: true,
            provider: { type: "openai", wireApi: "responses", baseUrl: proxy.baseUrl, bearerToken: proxy.capability },
            tools: scenario === "tool" ? [{
                name: "add", description: "Add two numbers.",
                parameters: { type: "object", properties: { a: { type: "number" }, b: { type: "number" } }, required: ["a", "b"] },
                handler: ({ a, b }) => { toolCalls++; return String(a + b); }
            }] : [],
            onPermissionRequest: async () => ({ kind: scenario === "tool" ? "approve-once" : "reject" })
        });
        try {
            if (["completed", "tool"].includes(scenario)) {
                const reply = await session.sendAndWait({ prompt: "Say hello." }, 45000);
                assert.equal(reply?.data?.content, scenario === "tool" ? "TOOL_OK 3" : "HELLO_OK");
                assert.equal(toolCalls, scenario === "tool" ? 1 : 0);
                assert.equal(calls, scenario === "tool" ? 2 : 1);
            } else {
                await assert.rejects(session.sendAndWait({ prompt: "Say hello." }, 45000), error => {
                    assert.match(error.message, scenario === "incomplete" ? /max_output_tokens/ : /Synthetic provider denial/);
                    assert.match(error.message, /synthetic-upstream-request/);
                    if (scenario === "null-error" || scenario === "failed-null-error") {
                        assert.match(error.message, /\[policy_denied\]/);
                    }
                    assert.doesNotMatch(error.message, /retried 5 times|without a completed response/);
                    return true;
                });
                assert.equal(calls, 1);
            }
            console.log(`native-responses-client: ${scenario} passed (${calls} upstream request(s))`);
        } finally {
            await session.disconnect();
        }
    }
} finally {
    await client.stop();
    await proxy.close();
    upstream.closeAllConnections();
    await new Promise(resolve => upstream.close(resolve));
    rmSync(root, { recursive: true, force: true });
}
