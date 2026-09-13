# Subagent Policy

Subagent Policy extends Copilot's existing native `/subagents` screen. It does not register another
slash command, modal, canvas, or IPC UI.

## Control plane

`/subagents` is the sole user-facing control plane. The native model catalog, agent list, keyboard
interaction, and per-agent model controls remain intact. Afterburner adds policy presets that apply:

- per-agent model routing;
- maximum concurrent subagents;
- maximum nesting depth; and
- result-exposure metadata.

Configuration is read from `~\.afterburner\config\subagent-policy.json`. If absent, the extension
offers the built-in `three-workers` preset (concurrency 3, depth 1, no forced model).

## Compatibility and failure behavior

This is a version-anchored MOD for Copilot CLI `1.0.84-4`. Both native source anchors must occur
exactly once. Startup fails closed if either anchor is absent or duplicated; the extension never
guesses at a changed minified layout.

Policy application also fails closed unless every explicit model is present in Copilot's live,
merged model catalog. That catalog includes native models and models registered by BYOModels.
Unavailable policy rows are visibly marked and cannot silently fall back to the session model.

The extension has only the `application-source-transform` capability. It has no session command,
session extension, modal surface, or user-facing IPC channel.

## Configuration

```json
{
  "policies": {
    "three-local-workers": {
      "displayName": "Three Local Workers",
      "description": "Route built-in workers through an installed BYOModels model",
      "maxConcurrency": 3,
      "maxDepth": 1,
      "resultExposure": "status-and-final",
      "disabledSubagents": [],
      "agents": {
        "explore": {
          "model": "my-provider/my-model",
          "modelPolicy": "required"
        }
      }
    }
  }
}
```

Use the exact ID displayed by Copilot's native model picker. Native IDs such as `gpt-5.6-sol` and
provider-qualified BYOModels IDs such as `my-provider/my-model` are both supported when present in
the live catalog. A syntactically valid but unregistered ID is rejected before settings are changed.
