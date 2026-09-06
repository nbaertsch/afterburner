# Afterburner

Afterburner is a native control plane and trusted runtime-extension host for GitHub Copilot CLI. The
single public command, `afterburn.exe`, owns launch isolation, exact argument forwarding,
compatibility selection, extension lifecycle, recovery, diagnostics, installation, and updates.
JavaScript remains only where code must execute inside Copilot.

## Install

Build from source:

```powershell
git clone git@github.com:nbaertsch/afterburner.git
Set-Location .\afterburner
go build -trimpath -o .\afterburn.exe .\cmd\afterburn
.\afterburn.exe core install
afterburn install
```

`core install` atomically installs the executable at `~\.afterburner\bin\afterburn.exe`, retains the
previous core for rollback, and adds that directory to the user PATH. `afterburn install` installs
and enables the BYOModels and Black Box built-ins.

Tagged GitHub Releases publish `afterburn-windows-amd64.zip`,
`afterburn-windows-arm64.zip`, `black-box.zip`, `byo-models.zip`, `checksums.txt`,
`release-manifest.json`, and `release-manifest.sig`.

Release publication is manually dispatched from `main` for a version tag that identifies the exact
current `origin/main` commit. Unsigned assets are built in a secretless job; signing occurs only in
the `release-signing` GitHub Environment, whose Ed25519 key is restricted to the `main` workflow
ref.

### Built-in extension updates

Built-in extensions are versioned in lockstep with the Afterburner core release. `afterburn update`
fetches the signed core archive plus every built-in package listed in that release manifest (for
example `black-box.zip` and `byo-models.zip`) and re-syncs the installed built-ins to the same tag
before staging the core replacement. If a pinned built-in cannot be fetched or verified, the update
fails instead of leaving a core/extension mismatch.

`afterburn install <id>` still works as a repair/bootstrap command. It fetches the signed built-in
release asset matching the running core version when possible and falls back to the copy embedded in
the running `afterburn.exe` binary via `go:embed` if the network fetch fails.

Set `AFTERBURNER_DISABLE_BUILTIN_RELEASE_FETCH=1` to force `afterburn install` to always use the
embedded built-in and skip the network fetch entirely.

For local development on a built-in extension's source (`extensions/BlackBox`,
`extensions/BYOModels`), set `AFTERBURNER_BUILTIN_SOURCE_OVERRIDE=id=path[,id2=path2]` before
running `afterburn install <id>` to materialize the extension directly from a local source
directory instead of any release asset or embedded copy. This is a development-only escape hatch:
it is never consulted for signature or trust decisions, and it still enforces that the source
directory's `afterburner.json` manifest matches the requested `id` and has `"visibility": "builtin"`.

```powershell
$env:AFTERBURNER_BUILTIN_SOURCE_OVERRIDE = "black-box=C:\path\to\afterburner\extensions\BlackBox"
afterburn install black-box
```

## Launch and passthrough

```text
afterburn                         Copilot with zero user arguments
afterburn <copilot arguments...>  Exact passthrough
afterburn run <arguments...>      Forced passthrough for command-name collisions
afterburn -- <arguments...>       Forced passthrough after --
```

Afterburner preserves argument boundaries, empty arguments, repeated flags, Unicode, standard I/O,
interactive terminal behavior, Ctrl+C, and child exit codes. It injects only the prepared runtime
version required by Copilot's loader. Normal `copilot` remains untouched.

Local interactive Windows launches automatically use the production ConPTY terminal broker. The
broker forwards Copilot input/output and hosts authenticated extension UI surfaces. Redirected and
non-interactive launches continue using the direct process path. For emergency diagnostics only,
set `AFTERBURNER_DISABLE_TERMINAL_BROKER=1` to force direct passthrough.

Recovery launch options:

```powershell
afterburn --safe-mode
afterburn --disable-extension steward-burn
```

## Extensions

Manage embedded built-ins:

```powershell
afterburn install
afterburn install byo-models black-box
afterburn enable black-box
afterburn disable black-box
afterburn uninstall black-box
```

Uninstall removes managed package registration and immutable package caches while preserving
configuration and extension data.

Manage custom local or Git extensions:

```powershell
afterburn extension install nbaertsch/steward-burn@main
afterburn extension enable steward-burn
afterburn extension update steward-burn
afterburn extension update --all
afterburn extension rollback steward-burn
afterburn extension inspect steward-burn
afterburn extension list
```

Git sources are resolved to immutable commits. Installation is disabled by default until explicitly
enabled. Packages execute trusted JavaScript in the Copilot process and may optionally provide a
session extension from the same immutable package.

## BYOModels

BYOModels adds configured providers and model-picker reasoning/context controls. Configuration is
user-owned at:

```text
~\.afterburner\config\byomodels.json
```

Open `/model`, use left/right arrows for reasoning, press Tab to focus context, then use left/right
for `256K`, `512K`, `768K`, and `1.05M` where supported. See
[`extensions/BYOModels/README.md`](extensions/BYOModels/README.md).

## Black Box

Black Box records bounded, metadata-only runtime diagnostics under
`~\.afterburner\extension-data\black-box`. It does not copy prompts, responses, source, tool
arguments/results, or summaries. See [`extensions/BlackBox/README.md`](extensions/BlackBox/README.md).

## Enterprise UI architecture contract

The supported UI foundation is the versioned `afterburner.ui` contract under `internal\ui` with
JSON schemas in `schemas\ui-*-v1.schema.json`. These files define protocol revision constants,
component/catalog types, semantic style tokens, accessibility/localization metadata,
surface/action/data/stream lifecycles, capability and quota descriptors, error codes, SLO and
compatibility identifiers, and implementation interfaces for renderers, terminal surfaces,
reconcilers, policy/audit, SDK bridges, schema registries, and event sinks. Black Box is an optional
`afterburner.ui` observability event sink extension; hosts and renderers must function when it is
not installed or enabled.

Enterprise UI tooling is available under `afterburn ui ...` for manifest validation, fixture
rendering, surface simulation, metadata-only trace inspection, runtime grants, policy installation,
and certification reports. See [`docs\extension-ui-authoring.md`](docs/extension-ui-authoring.md)
and [`docs\enterprise-ui-admin.md`](docs/enterprise-ui-admin.md).

## Compatibility and recovery

Afterburner inventories Copilot packages across user, local-app-data, executable-associated, and
managed roots. Launch trust requires an embedded profile with exact `app.js` and `runtime.node`
hashes, unique structural source probes, and a bounded real runtime self-test.

```powershell
afterburn compatibility list
afterburn compatibility status
afterburn compatibility retry
afterburn doctor
afterburn doctor --json
afterburn doctor --bundle
afterburn repair
```

Successful runtime tuples are persisted as last-known-good state. A failed candidate falls back only
when package files and the extension-registry fingerprint still match. `repair` validates the
managed session junction and registry, reconciles session components, removes invalid fallback
state, and cleans abandoned staging directories without deleting user configuration or extension
data.

## Updates and rollback

```powershell
afterburn update --check
afterburn update
afterburn rollback core
```

Updates use GitHub Releases and private-repository authentication from `GITHUB_TOKEN`, `GH_TOKEN`,
or `gh auth token`. Before downloading or executing archives, the updater verifies an Ed25519
signature over a canonical manifest bound to this repository, tag, commit, platform, architecture,
archive SHA-256, size, and built-in extension package checksums. It syncs built-ins to that exact
release tag, verifies PE architecture and embedded version, then launches a detached replacement
helper. The current core is retained, a bounded post-update doctor runs, and validation failure
restores the previous binary automatically. If Windows reports the executable is locked, the helper
reports an actionable error asking the user to close other Afterburner sessions and retry.

## Layout

```text
~\.afterburner\
├── bin\                 installed and previous native cores
├── config\              user-owned configuration
├── extensions\          immutable installed extension packages
├── extension-data\      extension-owned durable data
├── copilot-home\        isolated Copilot state and prepared runtimes
├── diagnostics\         explicit sanitized doctor bundles
├── state\               compatibility and update transaction state
└── registry.json        extension source and enablement registry
```

The managed Copilot home shares only native session state through a validated directory junction.
Afterburner never registers its extensions in normal Copilot configuration.

## Develop and test

```powershell
go generate ./internal/assets
go test ./...
go vet -unsafeptr=false ./...
npm test
```

Windows CI also builds amd64/arm64 binaries and validates exact resume forwarding, Ctrl+C, and
the modal broker under a real ConPTY. The interactive picker harness is `tests\native-conpty.mjs`.
For installed end-to-end modal validation, run `npm run test:real-tui:black-box`; it opens a real
Afterburner/Copilot TUI, drives `/black-box-modal`, verifies the Doctor action and close/prompt
restore, and writes raw, text, machine-readable latency/result, and HTML visual-inspection captures
under `artifacts\real-tui` by default.
