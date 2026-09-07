# Copilot OpenAI Bridge

Copilot OpenAI Bridge exposes the active Afterburner-managed Copilot session through a localhost OpenAI-compatible HTTP API when the extension is enabled and explicitly started.

## Install for local development

```powershell
afterburn extension install .\extensions\CopilotOpenAI
afterburn extension enable copilot-openai
afterburn
```

Inside the Copilot session, use the single management command:

```text
/copilot-openai
```

That command opens the Copilot OpenAI Bridge management panel, starts or reuses the localhost bridge, refreshes status, and prints sanitized diagnostics. The default URL is `http://127.0.0.1:41425`. Set `AFTERBURNER_COPILOT_OPENAI_PORT` or create `~\.afterburner\config\copilot-openai.json` to choose another port.

## Config

```json
{
  "enabled": false,
  "host": "127.0.0.1",
  "port": 41425,
  "requireApiKey": true,
  "apiKey": "local-development-token"
}
```

`enabled: true` starts the bridge automatically when the session extension loads. Without it, use `/copilot-openai` to open the management panel and start or reuse the bridge. The bridge only binds to localhost. If `requireApiKey` is true, clients must send an `Authorization: Bearer <token>` header. If the live SDK does not expose a lower-level chat RPC, `/v1/chat/completions` uses the active session's `sendAndWait` API.

## Endpoints

- `GET /v1/models` returns OpenAI model objects derived from `session.rpc.model.list()`.
- `POST /v1/chat/completions` forwards to the first chat completion RPC exposed by the active Copilot session, or to `session.sendAndWait` when no lower-level chat RPC is available, and normalizes responses to OpenAI chat completion JSON.
- `POST /v1/chat/completions` with `stream: true` emits OpenAI-style `text/event-stream` chunks and a final `[DONE]` marker.
- `GET /__afterburner/copilot-openai/health` returns sanitized bridge health.

## Security and privacy

The bridge never binds to a public interface, never logs prompts/responses/authorization headers, redacts token-shaped errors, and hashes session identifiers in health output. Diagnostics are metadata-only.

## Validation

```powershell
npm test --prefix extensions\CopilotOpenAI
```
