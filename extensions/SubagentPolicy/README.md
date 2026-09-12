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
offers the built-in `luna-three` preset (Luna for `explore`, concurrency 3, depth 1).

## Compatibility and failure behavior

This is a version-anchored MOD for Copilot CLI `1.0.84-4`. Both native source anchors must occur
exactly once. Startup fails closed if either anchor is absent or duplicated; the extension never
guesses at a changed minified layout.

The extension has only the `application-source-transform` capability. It has no session command,
session extension, modal surface, or user-facing IPC channel.

## Configuration

```json
{
  "policies": {
    "luna-three": {
      "displayName": "Luna Three",
      "description": "Route exploration through Luna with bounded parallelism",
      "maxConcurrency": 3,
      "maxDepth": 1,
      "resultExposure": "summary",
      "agents": {
        "explore": {
          "model": "colosseum-prod/gpt-5-6-luna",
          "modelPolicy": "required"
        }
      }
    }
  }
}
```

Model IDs are passed directly to Copilot's native subagent settings state. The native model picker
continues to provide the authoritative model catalog and normal per-agent editing workflow.
