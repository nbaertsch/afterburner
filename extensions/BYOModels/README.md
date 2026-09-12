# BYOModels

BYOModels is Afterburner's built-in model integration. It registers user-configured providers and
models with an Afterburner-managed Copilot session, then adds model-picker context-window and
reasoning controls plus the `afterburner-byomodels` status canvas. It is installed and upgraded as
part of Afterburner; do not install it with `copilot plugin install`.

## Setup

Build and install the native CLI, then install this built-in package:

```powershell
go build -o .\afterburn.exe .\cmd\afterburn
.\afterburn.exe core install
afterburn install byo-models
```

Running `afterburn install` without IDs installs every built-in, including BYOModels and Black Box.

`afterburn install` creates an example configuration only when no user configuration exists. It does
not overwrite an existing file. Start the managed session with:

```powershell
afterburn
```

Open `/model` to select a configured model. Use left/right arrows to change reasoning effort, Tab to
focus the context-window control, and left/right arrows to change context size.

Validated capability metadata is cached outside the configuration file. On later launches,
BYOModels registers that metadata immediately and refreshes the live Copilot model catalog in the
background after extension activation has resolved, so provider discovery and its RPC are not on
the UI/plugin-readiness critical path. The session log reports the immediate registration count
before the detached refresh is scheduled. A cache is accepted only
when its model identity still matches the current configuration. Models with complete
token limits and boolean vision/reasoning support configured directly in `models` can also register
immediately. An explicit `"reasoningEffort": false` is authoritative: the custom model does not
inherit reasoning controls or a default reasoning level from its `modelId` capability source.
Discovery and registration have bounded RPC deadlines; failures are reported in the
session log while already validated registrations remain available.

## Configuration

The canonical user-owned configuration is:

```text
~\.afterburner\config\byomodels.json
```

Copy `extensions\BYOModels\extensions\BYOModels\models.example.json` there when configuring
manually. `AFTERBURNER_BYOMODELS_CONFIG` may override the path for automation or alternate profiles.

The file contains:

- `version`: currently `1`.
- `contextWindowOptions`: picker choices in tokens.
- `providers`: endpoint, wire API, authentication, and optional request compatibility.
- `models`: picker identity, display name, upstream capability model, and wire deployment.

A model's stable picker ID is `<provider>/<id>`. `modelId` identifies the built-in Copilot model whose
live capabilities are projected onto the custom model. `wireModel` is the deployment/model name sent
to the configured provider.

## Authentication

Supported provider authentication forms are:

### Azure CLI

```json
"auth": {
  "type": "azure-cli",
  "resource": "https://cognitiveservices.azure.com/"
}
```

Sign in with Azure CLI before launching Afterburner. Tokens are acquired and refreshed at runtime;
they are not stored in `byomodels.json`. The `resource` may be any Entra resource accepted by
`az account get-access-token`; it is not limited to Azure Cognitive Services. Providers without a
request-compatibility proxy use the Copilot SDK's per-request bearer-token callback, which preserves
path-prefixed OpenAI-compatible base URLs.

### API key environment variable

```json
"auth": {
  "type": "api-key-env",
  "environmentVariable": "MY_MODEL_API_KEY"
}
```

### Bearer-token environment variable

```json
"auth": {
  "type": "bearer-token-env",
  "environmentVariable": "MY_MODEL_BEARER_TOKEN"
}
```

### Bearer token in configuration

```json
"auth": {
  "type": "bearer-token",
  "value": "YOUR_BEARER_TOKEN"
}
```

Use this form when the token must be stored in `byomodels.json`. Restrict the file to the current
user because it now contains a credential. Environment-backed auth remains preferable on shared
systems.

## Updates and safety

Built-ins update with the installed Afterburner core:

```powershell
afterburn update
afterburn install byo-models
```

Package versions are immutable and stored under `~\.afterburner\extensions\byo-models`. User
configuration remains under `~\.afterburner\config` and is not copied into, overwritten by, or
deleted with package versions. Normal `copilot` sessions do not load BYOModels.

## Compatibility

The bundled package supports Copilot CLI `>=1.0.83-1`. Known package hashes use exact profiles;
newer complete packages use a generated forward-compatibility profile and must pass Afterburner's
runtime self-test before becoming the last-known-good package. Provider
`requestCompatibility.maxInputItemIdLength` enables a loopback-only proxy that shortens oversized
Responses API input-item IDs without changing message content.
`requestCompatibility.forceStreaming` supports endpoints that reject non-streaming Responses API
requests: the proxy requests SSE upstream and converts the terminal response event back to the JSON
response expected by Copilot. Configure it as:

```json
"requestCompatibility": {
  "forceStreaming": true,
  "proxyPort": 61952
}
```

The proxy preserves path-prefixed base URLs and query parameters.
Proxy listener binding is concurrent across providers. Listener setup, Azure CLI authentication,
capability RPCs, and upstream requests use explicit deadlines; timeout failures are returned or
logged with the provider/operation name rather than silently falling back to missing models.
For Restricted gateways with a JSON depth limit of 16 and no native namespace/tool-search support,
also set `"legacyTools": true` in `requestCompatibility`. This eagerly exposes namespaced functions
under their existing callable names and relocates deep tool schemas into shallow `$defs` references,
preserving their constraints. Existing native tool-search history is removed because all functions
are now supplied directly. Function calls and results remain intact. This compatibility mode is
owned by the session proxy even when a preferred port is configured. Other providers are unchanged.
Schemas with nested resource IDs, duplicate flattened function names, or non-schema data that still
exceeds the depth limit fail explicitly rather than silently losing constraints or tools.

For clients that discard Responses SSE failure events, set `"bufferResponses": true` as well.
The session proxy validates the entire upstream stream before sending response headers. Upstream
errors, failed responses, and incomplete responses become explicit HTTP errors with the provider
message and available upstream request ID; they are never turned into successful model responses.
Error codes also appear in the displayed message. Error extraction preserves nonblank `message`,
`title`, and `code` fields across nested or JSON-encoded error wrappers, including `error: null`.
If no recognized message is available, the diagnostic explicitly says so rather than guessing the
cause. These diagnostics do not retain raw upstream response bodies.
Deterministic failures use HTTP 422, rate limits use 429, and server/transport failures use 5xx.
Successful terminal response objects and tool calls are preserved. Refusal blocks are surfaced
as explicit `upstream_refusal` errors with their original text, rather than empty successful replies.
This opt-in mode delays visible output until the upstream response finishes and limits buffering to
64 MiB. Cancellation closes the upstream request. Providers without this setting retain live streaming.
Restricted's recommended compatibility settings are:

```json
"requestCompatibility": {
  "forceStreaming": true,
  "legacyTools": true,
  "bufferResponses": true
}
```

From the repository root, run `npm run test:real-responses-client -- <copilot-sdk-directory> <copilot.exe>`
to exercise the production proxy with the actual native client and a local synthetic provider.
It verifies explicit failures, incomplete responses, refusals, successful text, and a complete tool
round trip without contacting any configured provider.
An optional final `proxy-module` path tests an installed release's `request-compatibility.mjs`
instead of the repository copy.

Configured provider `headers` are applied by the owning proxy to every upstream request; they do
not depend on the Copilot SDK forwarding them through the loopback hop. Keep credentials in
`auth`; transport, authorization, and Afterburner capability headers are proxy-managed and cannot
be configured through `headers`.
Set `requestCompatibility.proxyPort` to a preferred stable, provider-specific port so the first
session normally receives a predictable loopback endpoint. The port is not a global singleton:
if the preferred port is already occupied by another Afterburner session, another provider in the
same config, or stale unrelated work, the session binds its own ephemeral loopback proxy and
registers models with that process-local endpoint instead of sharing authentication state or failing
startup.

Compatibility proxy invariants:

- every Copilot process that registers BYOModels owns the proxies in its model registrations;
- IPC is loopback-only and authenticated upstream credentials stay inside the owning process;
- a configured `proxyPort` is a preferred address, never a reason to drop configured models;
- cleanup closes only proxies owned by the current process; no process kills or global port cleanup;
- multi-session acceptance requires all concurrent sessions to register all configured models even
  when they prefer the same port.
