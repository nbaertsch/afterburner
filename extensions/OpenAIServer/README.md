# OpenAI Server

OpenAI Server is a first-party Afterburner extension that exposes your active GitHub Copilot CLI session as a local OpenAI-compatible HTTP API (`/v1`).

It allows external tools—such as Python scripts, LiteLLM, evaluation harnesses, and AI agent frameworks—to query Copilot and leverage its models through standard OpenAI client libraries.

---

## What it does

```text
┌───────────────────────────────────────┐
│ Your Tool / Script                    │
│ (Python openai SDK, LiteLLM, curl)    │
└──────────────────┬────────────────────┘
                   │ POST http://127.0.0.1:41425/v1/chat/completions
                   ▼
┌───────────────────────────────────────┐
│ OpenAI Server (Localhost Bridge)      │
└──────────────────┬────────────────────┘
                   │ session.sendAndWait() / chat RPC
                   ▼
┌───────────────────────────────────────┐
│ Active GitHub Copilot CLI Session     │
│ (Managed by afterburn.exe)            │
└───────────────────────────────────────┘
```

> **Important:** OpenAI Server is an in-session bridge, not a detached standalone service. It runs inside an active Afterburner-managed Copilot CLI session. Keep your `afterburn` terminal open while calling the API from other tools.

---

## Quickstart (For Onboarding & New Engineers)

### 1. Enable the extension

OpenAI Server is installed disabled by default because it exposes a localhost network port. Enable it once:

```powershell
afterburn install openai-server
afterburn enable openai-server
```

### 2. Configure automatic startup (Recommended)

Create `~\.afterburner\config\openai-server.json` to start the server automatically whenever you run `afterburn`:

```powershell
New-Item -ItemType Directory -Force $HOME\.afterburner\config
@'
{
  "enabled": true,
  "host": "127.0.0.1",
  "port": 41425,
  "requireApiKey": false
}
'@ | Set-Content $HOME\.afterburner\config\openai-server.json
```

*Note: If you set `"requireApiKey": true`, provide `"apiKey": "your-secret-token"` and include `Authorization: Bearer your-secret-token` in your requests.*

### 3. Start Afterburner

Launch Copilot in a terminal and keep it running:

```powershell
afterburn
```

You can verify the bridge is active from inside the session by running:

```text
/openai-server
```

This opens the interactive management menu where you can view server status, run diagnostics, or start/stop the bridge.

---

## Verifying the Server

### Check server health

From another terminal:

```powershell
curl.exe http://127.0.0.1:41425/__afterburner/openai-server/health
```

Expected response:

```json
{
  "marker": "afterburner-openai-server-v1",
  "protocolVersion": 1,
  "active": true,
  "url": "http://127.0.0.1:41425"
}
```

### List available models

```powershell
curl.exe http://127.0.0.1:41425/v1/models
```

Returns the active catalog of Copilot and registered BYOModels.

### Test chat completions with `curl`

```powershell
curl.exe http://127.0.0.1:41425/v1/chat/completions `
  -H "Content-Type: application/json" `
  -d '{
    "model": "gpt-5.4",
    "messages": [
      { "role": "user", "content": "Reply with exactly: HELLO_FROM_OPENAI_SERVER" }
    ]
  }'
```

---

## Integrating with External Tools

### Python (using the official `openai` package)

```python
from openai import OpenAI

client = OpenAI(
    base_url="http://127.0.0.1:41425/v1",
    api_key="local-token",  # Any string if requireApiKey is false
)

response = client.chat.completions.create(
    model="gpt-5.4",  # Or any model reported by /v1/models
    messages=[
        {"role": "system", "content": "You are a concise assistant."},
        {"role": "user", "content": "What is 40 + 2?"},
    ],
)

print(response.choices[0].message.content)
```

### Python Streaming

```python
from openai import OpenAI

client = OpenAI(
    base_url="http://127.0.0.1:41425/v1",
    api_key="local-token",
)

stream = client.chat.completions.create(
    model="gpt-5.4",
    messages=[{"role": "user", "content": "Write a short poem about coding."}],
    stream=True,
)

for chunk in stream:
    content = chunk.choices[0].delta.content or ""
    print(content, end="", flush=True)
print()
```

### LiteLLM / Evaluation Runners (e.g. Colosseum)

Configure your tool or rubric profile to point to the local server:

```yaml
llm:
  model: openai/gpt-5.4
  api_base: http://127.0.0.1:41425/v1
  auth:
    type: api_key
    env: LOCAL_LLM_API_KEY
  allow_unmapped_models: true
```

Set the environment variable:

```powershell
$env:LOCAL_LLM_API_KEY = "local-token"
```

---

## Configuration Reference

Configuration file path: `~\.afterburner\config\openai-server.json`

| Field | Type | Default | Description |
|---|---|---|---|
| `enabled` | boolean | `false` | When `true`, automatically starts the server upon session startup. |
| `host` | string | `"127.0.0.1"` | Bind host. For security, only `"127.0.0.1"` and `"localhost"` are supported. |
| `port` | number | `41425` | Local TCP port for the HTTP server. |
| `requireApiKey` | boolean | `false` | When `true`, requests must supply a valid `Authorization: Bearer <key>`. |
| `apiKey` | string | `undefined` | Static API key to require when `requireApiKey` is `true`. Auto-generated if omitted. |
| `maxBodyBytes` | number | `1048576` | Maximum request body size in bytes (1 KiB to 16 MiB). |

### Environment variable overrides

- `AFTERBURNER_OPENAI_SERVER_PORT`: Overrides the listening port for the current launch.
- `AFTERBURNER_OPENAI_SERVER_API_KEY`: Overrides the API key and enforces authentication.
- `AFTERBURNER_OPENAI_SERVER_CONFIG`: Points to a custom configuration JSON file.

*Legacy compatibility:* `copilot-openai.json`, `AFTERBURNER_COPILOT_OPENAI_*`, and the `/copilot-openai` command alias remain supported for existing automated workflows.

---

## Endpoints Reference

- **`GET /v1/models`**: Returns an OpenAI model list object containing models available in the current Copilot session.
- **`POST /v1/chat/completions`**: Executes a chat completion. Supports both standard JSON responses and chunked Server-Sent Events (`stream: true`).
- **`GET /__afterburner/openai-server/health`**: Returns JSON health status, active port, session route fingerprint, and request/error counters.

---

## Troubleshooting

| Symptom | Cause | Solution |
|---|---|---|
| `Connection refused` | No active `afterburn` session running | Launch `afterburn` in a separate terminal and keep it open. |
| Server not listening | Extension disabled or `"enabled": false` | Run `afterburn enable openai-server`, set `"enabled": true` in `openai-server.json`, or run `/openai-server` in session. |
| `401 Unauthorized` | API key required but missing or wrong | Send `Authorization: Bearer <key>` matching `"apiKey"` in `openai-server.json`. |
| `Address already in use` | Another process is on port `41425` | Set a different `"port"` in `openai-server.json` (e.g. `41426`). |
| Configuration changes not applied | Server was already running | Restart the `afterburn` terminal session to reload configuration. |
| Tool times out | Model generation took longer than client timeout | Increase client timeout or check upstream model provider connectivity. |

---

## Validation & Tests

To execute the unit and integration test suite:

```powershell
npm test --prefix extensions\OpenAIServer
```
