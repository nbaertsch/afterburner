# Black Box

Black Box is Afterburner's built-in, metadata-only session recorder. Install and manage it with:

```powershell
afterburn install black-box
afterburn enable black-box
afterburn disable black-box
afterburn uninstall black-box
```

Uninstalling preserves configuration and recorded data. Black Box stores bounded JSONL segments
under `AFTERBURNER_HOME\extension-data\black-box` and exposes `/black-box`, `/black-box-tail`,
`/black-box-tail-stop`, `/black-box-export`, `/black-box-doctor`, and the `afterburner-black-box` canvas.

Black Box is silent by default: it never writes automatic event or anomaly messages into the user
timeline. Timeline output occurs only after the user explicitly runs `/black-box`, `/black-box-tail`,
`/black-box-doctor`, or `/black-box-export`.

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
event envelope. Observer and storage errors are contained and counted instead of escaping into the
host.

The Copilot session component uses `@github/copilot-sdk` and resolves the native event log from, in
order: `AFTERBURNER_BLACK_BOX_EVENTS`, `COPILOT_SESSION_STATE_DIR\events.jsonl`, or
`COPILOT_HOME\session-state\COPILOT_AGENT_SESSION_ID\events.jsonl`.

## Tests

```powershell
npm test --prefix extensions\BlackBox
```
