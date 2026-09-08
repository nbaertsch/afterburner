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

Never put credentials directly in the configuration file.

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

The bundled package currently supports Copilot CLI `>=1.0.83-1 <1.0.84`. Provider
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
Set `requestCompatibility.proxyPort` to a stable, provider-specific port so resumed sessions never
retain an expired ephemeral endpoint. Concurrent Afterburner sessions verify and share the same
proxy, and a standby process takes ownership when the previous owner exits.
