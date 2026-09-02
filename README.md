# Copilot Afterburner

Afterburner is a generic pre-initialization capability layer for GitHub Copilot CLI. It is loaded by
Copilot's existing versioned-package loader, patches supported runtime seams in memory, activates
runtime contributions from installed plugins, and then imports the original unmodified `app.js`.

It exists for capabilities that normal Copilot extensions cannot currently express, such as
picker-facing metadata and model-selection adapters.

## Architecture

```text
copilot.exe
  -> package loader selects 9999.0.0-afterburner/app.js
  -> Afterburner loads the original runtime.node
  -> Afterburner activates enabled plugin runtime/extension.mjs files
  -> Afterburner installs registered adapters
  -> original versioned app.js is imported unchanged
```

Afterburner discovers enabled plugins from `~/.copilot/config.json`. A plugin opts in by exporting:

```js
export async function activate({
  runtime,
  pluginRoot,
  registerModelPickerAdapter
}) {
  registerModelPickerAdapter({
    matches: (selectionId) => selectionId.startsWith("example/"),
    upstreamModelId: () => "gpt-5.6-sol",
    selectionIds: () => ["example/model"],
    supportedReasoningEfforts: () => ["none", "low", "medium", "high"],
    defaultReasoningEffort: () => "medium",
    maxContextWindowTokens: () => 1050000,
    maxOutputTokens: () => 128000,
    contextWindowOptions: () => [256000, 512000, 768000, 1050000]
  });
}
```

Runtime contributions are intentionally separate from normal extension subprocesses. They execute
inside the Copilot process and must be treated as trusted code.

## Install

```powershell
npm install
npm run install
```

Restart Copilot after installing. `copilot version` should continue to report the original CLI
version because Afterburner delegates to the installed package.

## Test

```powershell
npm test
```

The tracked `extensions/byok-models` plugin is provider-agnostic; its checked-in `models.json`
currently configures the production Colosseum Foundry deployments. It supports Azure CLI tokens,
API keys from environment variables, and bearer tokens from environment variables. Machine-private
extensions can be placed under the gitignored `extensions/local/` directory.

The current integration test expects the tracked BYOK plugin and checks its four context sizes.
Interactive TUI testing guidance lives in
`.github/skills/copilot-tui-validation/SKILL.md`.

## Compatibility

The runtime bridge uses experimental/private Copilot seams and may require updates when Copilot CLI
changes. Afterburner dynamically selects the newest real package containing both `app.js` and the
platform `runtime.node`; it does not modify that package on disk.
