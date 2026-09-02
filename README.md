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
  -> Afterburner applies registered source transforms to a generated sidecar
  -> the transformed app is imported beside the original versioned app.js
```

Afterburner discovers enabled trusted extensions from `~/.afterburner/registry.json`. An extension
opts in through `afterburner.json` and exports:

```js
export async function activate({
  runtime,
  pluginRoot,
  registerModelPickerAdapter,
  registerAppSourceTransform
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

An Afterburner package may also declare a `sessionExtension.entrypoint`. Afterburn registers that
entrypoint directly from the same identity-addressed package path for the managed Copilot session.
This is one extension and one lifecycle: no second plugin package or Copilot direct-install cache is
created.

`registerAppSourceTransform(transform)` is the generic escape hatch for UI behavior that is not
exposed by `runtime.node`. Transforms receive the original bundled application source and run before
the application is imported. They should fail closed when expected source anchors are absent.

Runtime extensions can also register bounded metadata observers:

```js
const unregister = registerRuntimeObserver({
  id: "example-observer",
  eventTypes: ["model.selection.applied", "task.transition"],
  onEvent: async event => {
    // schemaVersion, sequence, timestamp, type, and sanitized metadata only
  }
});
```

The host removes body-bearing fields such as prompts, messages, source, tool arguments/results,
credentials, and summaries before delivery. Each observer has an isolated bounded queue, bootstrap
replay, drop counters, and failure containment. `getRuntimeObserverDiagnostics()` returns aggregate
observer health without exposing captured content.

## Install and upgrade from source

Clone or update the source checkout, bootstrap the standalone command, and let Afterburner install
its built-in components:

```powershell
git clone git@github.com:nbaertsch/afterburner.git
Set-Location .\afterburner
.\scripts\install-cli.ps1
afterburn install
afterburn
```

For an existing checkout:

```powershell
git pull --ff-only
.\scripts\install-cli.ps1
afterburn install
```

`install-cli.ps1` refreshes the core application files and PATH shim. `afterburn install` installs
and enables every built-in extension. Built-ins can also be managed selectively:

```powershell
afterburn install byo-models
afterburn install black-box
afterburn enable black-box
afterburn disable black-box
afterburn uninstall black-box
```

`uninstall` removes package registration and immutable package caches, but preserves user-owned
configuration and extension data. No built-in requires a direct `copilot plugin install`.

Normal `copilot` invocations remain untouched and load no Afterburner runtime code. Use `afterburn`
whenever you explicitly want an Afterburner-managed Copilot session. The launcher prepares the
side-by-side runtime package and invokes Copilot with `--prefer-version 9999.0.0-afterburner`.

## Afterburner extensions

Afterburner extensions are explicitly identified by `afterburner.json`. They execute trusted
JavaScript inside the Copilot process and are not normal isolated Copilot extensions.

```powershell
afterburn extension install nbaertsch/steward-burn@main
afterburn extension inspect steward-burn
afterburn extension enable steward-burn
afterburn extension update steward-burn
afterburn extension update --all
afterburn extension rollback steward-burn
afterburn extension list
```

Git is the package transport. Public repositories, private repositories accessible through the
user's existing Git credentials, SSH URLs, HTTPS URLs, and local paths are supported. Installation
does not enable an extension automatically. The resolved commit and source are recorded under `~\.afterburner`, and activation happens only
after an explicit enable command.

Updates resolve the configured source/ref into a new immutable commit- or content-addressed package,
validate it before activation, preserve enablement, atomically switch the registry, and retain the
previous package for rollback. User configuration under `~\.afterburner\config` and durable state
under `~\.afterburner\extension-data` are never copied from or deleted with package versions.

## Installation layout

```text
~\.afterburner\
├── app\                 Installed application files
├── bin\                 The afterburn launcher
├── config\              User-owned configuration
│   ├── byomodels.json   BYOModels configuration (name retained across ID migration)
│   └── black-box.json   Black Box recording and byte-retention settings
├── extensions\          Installed extension packages
├── extension-data\      Extension-owned durable state
├── copilot-home\        Isolated managed Copilot state
└── registry.json        Extension source and enablement registry
```

Installers may create missing example configuration, but never overwrite an existing user-owned
file during installation or upgrade.

## Built-in extensions

### BYOModels

`extensions/BYOModels` provides first-class custom model registration and picker controls. Its public
built-in ID is `byo-models`. After installing it, configure:

```text
~\.afterburner\config\byomodels.json
```

The installer creates an example only when this file is absent and never overwrites user
configuration. Provider endpoints, deployments, and authentication references belong in this
user-owned file—not in the installed package or repository. Credentials must remain in Azure CLI or
environment variables.

See [`extensions/BYOModels/README.md`](extensions/BYOModels/README.md) for the complete schema,
authentication options, model mapping, picker controls, upgrade procedure, and compatibility notes.

### Black Box

`extensions/BlackBox` records bounded operational metadata for extension, model, context, tool,
agent/task, and UI activity. It enriches Copilot's native session event storage rather than copying
prompt, response, source, tool argument/result, or summary bodies. Data is stored under
`~\.afterburner\extension-data\black-box` and rotated strictly by configurable byte volume.

Commands include `/black-box`, `/black-box-tail`, `/black-box-tail-stop`, `/black-box-export`, and `/black-box-doctor`.
See [`extensions/BlackBox/README.md`](extensions/BlackBox/README.md) for storage and export details.

## Test

```powershell
npm test
```

The integration test covers the tracked BYOModels package and its four context sizes. Interactive
TUI testing guidance lives in `.github/skills/copilot-tui-validation/SKILL.md`.

## Compatibility

The runtime bridge uses experimental/private Copilot seams and may require updates when Copilot CLI
changes. Afterburner dynamically selects the newest real package containing both `app.js` and the
platform `runtime.node`. The original `app.js` remains untouched; generated transformed sidecars are
recreated at startup.
