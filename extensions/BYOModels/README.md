# BYOModels

BYOModels is Afterburner's built-in model integration. It registers user-configured providers and
models with an Afterburner-managed Copilot session, then adds model-picker context-window and
reasoning controls. It is installed and upgraded as part of Afterburner; do not install it with
`copilot plugin install`.

## Setup

From an Afterburner source checkout, install or refresh the CLI, then install the built-in package:

```powershell
.\scripts\install-cli.ps1
afterburn install
```

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
they are not stored in `byomodels.json`.

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

Use the normal Afterburner upgrade path:

```powershell
afterburn extension update byomodels
afterburn extension rollback byomodels
```

Package versions are immutable and stored under `~\.afterburner\extensions\byomodels`. User
configuration remains under `~\.afterburner\config` and is not copied into, overwritten by, or
deleted with package versions. Normal `copilot` sessions do not load BYOModels.

## Compatibility

The bundled package currently supports Copilot CLI `>=1.0.83-1 <1.0.84`. Provider
`requestCompatibility.maxInputItemIdLength` enables a loopback-only proxy that shortens oversized
Responses API input-item IDs without changing message content.
