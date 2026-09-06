# Black Box

Black Box is Afterburner's built-in, metadata-only session recorder. Install and manage it with:

```powershell
afterburn install black-box
afterburn enable black-box
afterburn disable black-box
afterburn uninstall black-box
```

Uninstalling preserves configuration and recorded data. Black Box stores bounded JSONL segments
under `AFTERBURNER_HOME\extension-data\black-box` and exposes `/black-box`, `/black-box-modal`,
`/black-box-tail`, `/black-box-tail-stop`, `/black-box-export`, `/black-box-doctor`, and the
`afterburner-black-box` command canvas.

`/black-box-modal` requests the runtime-owned native modal surface `afterburner-black-box-live`.
When the Afterburner terminal broker is attached, the runtime opens a host-rendered overlay with
Copilot still running underneath. When the UI host, terminal broker, or required grant is absent,
Black Box prints an explicit deterministic text fallback instead of claiming that an interactive
view opened.

Black Box is silent by default: it never writes automatic event or anomaly messages into the user
timeline. Timeline output occurs only after the user explicitly runs `/black-box`, `/black-box-modal`,
`/black-box-tail`, `/black-box-doctor`, or `/black-box-export`.

## Commands and modal controls

- `/black-box` prints the current recorder, queue, storage, and anomaly status.
- `/black-box-modal` opens the live native overlay. Successful opens are intentionally silent in
  the Copilot timeline; the modal itself is the confirmation.
- `/black-box-tail [N]` starts a bounded live metadata tail; `/black-box-tail stop` or
  `/black-box-tail-stop` stops it.
- `/black-box-export [N]` writes a sanitized local export bundle.
- `/black-box-doctor` checks configuration, storage, native event tailing, and recovery health.

Inside the modal:

- `r` refreshes the live timeline.
- `d` opens the metadata-only doctor view.
- `q` or `Esc` closes the overlay and restores Copilot's current screen.
- `↑`/`↓`, `PgUp`/`PgDn`, `Home`, and `End` scroll long modal content.

Black Box never persists prompt, tool argument, tool result, assistant/system message, reasoning,
working-directory, or task-summary bodies. Native `events.jsonl` entries are represented by hashed
correlation references and durable byte ranges. Export bundles revalidate every record against the
metadata schema before writing it.

## Configuration

The optional user-owned configuration is `AFTERBURNER_HOME\config\black-box.json`. Set
`AFTERBURNER_BLACK_BOX_CONFIG` to override the path. Missing or invalid configuration falls back to
safe defaults and is reported by `/black-box-doctor`; copy `config.example.json` to customize it.
Retention is byte-only: the default is 500 MiB total with 8 MiB per-process segments.

## Runtime integration

`runtime/extension.mjs` expects Afterburner to provide `registerRuntimeObserver`. It registers a
stable `black-box` observer definition whose `onEvent` handler receives the metadata-only runtime
event envelope. When `@afterburner/ui` is available, Black Box defines the
`afterburner-black-box-live` panel surface with a virtualized/sortable/filterable timeline table,
responsive timeline/detail split, anomaly and milestone sections, storage health, progress/status,
command palette, and refresh/doctor/export/close/select/filter/sort actions. The surface declares
its UI grants in `afterburner.json`, carries keyboard, mouse, accessibility, and localization
metadata, coalesces live updates into bounded patches, and preserves selection/filter/sort across
reconnect recovery.

Black Box also registers the optional `black-box.ui.events` observability sink. It accepts only UI
platform lifecycle, performance/quota, security/policy, patch, backpressure, and recovery metadata;
prompt bodies, assistant/tool payloads, secrets, and raw paths are excluded before storage or display.
When the UI bridge, observability sink, or required grants are absent or denied, the recorder remains
operational and falls back to text status/timeline output. When `registerModalCanvas` is also
available, the live modal uses the same metadata-only enterprise layout model: an action bar, status
cards, timeline table, selected-record details, and doctor view. Native terminal overlays still receive
a deterministic text projection with refresh, doctor, and close actions.

The Copilot session component uses `@github/copilot-sdk` and resolves the native event log from, in
order: `AFTERBURNER_BLACK_BOX_EVENTS`, `COPILOT_SESSION_STATE_DIR\events.jsonl`, or
`COPILOT_HOME\session-state\COPILOT_AGENT_SESSION_ID\events.jsonl`.

## Tests

```powershell
npm test --prefix extensions\BlackBox
```

## Packaging note

The generated built-in ZIP is produced from this directory by `go generate ./internal/assets` and
is checked by CI for deterministic freshness.
