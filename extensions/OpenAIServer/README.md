# OpenAI Server

OpenAI Server exposes the active Afterburner-managed Copilot session through a localhost OpenAI-compatible HTTP API when the extension is enabled and explicitly started.

## Install

OpenAI Server is a first-party built-in extension. It is installed disabled by default so users opt in before exposing a localhost API:

```powershell
afterburn install openai-server
afterburn enable openai-server
afterburn
```

Inside the Copilot session, use the management command:

```text
/openai-server
```

`/copilot-openai` remains as a deprecated compatibility alias for existing workflows. The command opens the interactive OpenAI Server management menu. The menu provides Start, Stop, Status, and Doctor actions and starts or reuses the localhost server when opened. Menu activation, acknowledgements, actions, action acknowledgements, shared-port reuse, and bridge UI state require the host-issued `AFTERBURNER_SESSION_ROUTE` capability; commands without it fail closed with a local diagnostic instead of writing unscoped live IPC. The default URL is `http://127.0.0.1:41425`. Set `AFTERBURNER_OPENAI_SERVER_PORT` or create `~\.afterburner\config\openai-server.json` to choose another port. Legacy `AFTERBURNER_COPILOT_OPENAI_*` environment variables and `~\.afterburner\config\copilot-openai.json` are still honored when the canonical values are absent, but legacy unscoped modal/action/state files are ignored as live cross-session work.

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

`enabled: true` starts the server automatically when the session extension loads. Without it, use `/openai-server` to open the management panel and start or reuse the server. The server only binds to localhost. If `requireApiKey` is true, clients must send an `Authorization: Bearer <token>` header. If the live SDK does not expose a lower-level chat RPC, `/v1/chat/completions` uses the active session's `sendAndWait` API.

## Endpoints

- `GET /v1/models` returns OpenAI model objects derived from `session.rpc.model.list()`.
- `POST /v1/chat/completions` forwards to the first chat completion RPC exposed by the active Copilot session, or to `session.sendAndWait` when no lower-level chat RPC is available, and normalizes responses to OpenAI chat completion JSON.
- `POST /v1/chat/completions` with `stream: true` emits OpenAI-style `text/event-stream` chunks and a final `[DONE]` marker.
- `GET /__afterburner/openai-server/health` returns sanitized server health. `GET /__afterburner/copilot-openai/health` remains available for compatibility with older clients and shared-port detection.

## Security and privacy

The server never binds to a public interface, never logs prompts/responses/authorization headers, redacts token-shaped errors, and hashes session identifiers in health output. Diagnostics are metadata-only.

## Validation

```powershell
npm test --prefix extensions\OpenAIServer
```
