# Subagent Policy

Subagent Policy is a first-party Afterburner extension that applies configurable, session-scoped
controls to GitHub Copilot CLI subagent dispatch.

## Goals

- Select a named policy for the current Copilot session.
- Route each built-in subagent type to a configured model with a preferred or required policy.
- Limit concurrent subagents and nesting depth.
- Disable selected subagent types.
- Control how much subagent activity is surfaced in the main session.
- Keep the active policy session-local unless the user explicitly saves a default.
- Make active and queued work visible without recording prompts, responses, code, or tool payloads.

## Non-goals

- Alter model context windows.
- Rewrite Copilot prompts or subagent results.
- Silently cancel work when a limit is reached.
- Replace Copilot's scheduler. Limits use Copilot's live subagent settings; per-model admission is
  enforced separately by model-serving infrastructure when configured.

## User workflow

Run `/subagent-policy` in an Afterburner-managed Copilot session. The native modal shows the
current policy and available policies. Keyboard shortcuts select and apply a policy:

| Key | Policy | Concurrency | Depth | Result exposure |
|---|---|---:|---:|---|
| `1` | Solo | 1 | 1 | Final only |
| `2` | Conservative | 2 | 1 | Final only |
| `3` | Balanced | 4 | 1 | Status and final |
| `4` | Burst | 6 | 1 | Status and final |
| `r` | Reload | - | - | Reload configuration |
| `x` | Clear | - | - | Clear the session override |
| `q` / Escape | Close | - | - | Close without changing policy |

Applying a policy changes only the current live session. `defaultPolicy` selects the policy applied
when the extension initializes in each new session. The extension never edits Copilot's global
`/subagents` settings.

## Configuration

Path: `~\.afterburner\config\subagent-policy.json`

```json
{
  "version": 1,
  "defaultPolicy": "balanced",
  "policies": {
    "balanced": {
      "displayName": "Balanced",
      "description": "Four workers with shallow delegation.",
      "maxConcurrency": 4,
      "maxDepth": 1,
      "resultExposure": "status-and-final",
      "disabledSubagents": [],
      "agents": {
        "explore": {
          "model": "doi/qwen38-blackfrost",
          "modelPolicy": "required",
          "effortLevel": "low",
          "contextTier": "default"
        },
        "task": {
          "model": "doi/qwen38-blackfrost",
          "modelPolicy": "required",
          "effortLevel": "low",
          "contextTier": "default"
        },
        "general-purpose": {
          "model": "doi/qwen38-blackfrost",
          "modelPolicy": "required",
          "effortLevel": "medium",
          "contextTier": "default"
        },
        "research": {
          "model": "doi/qwen38-flash-next-abliterated",
          "modelPolicy": "required",
          "effortLevel": "high",
          "contextTier": "long_context"
        }
      }
    }
  }
}
```

### Policy fields

| Field | Type | Required | Contract |
|---|---|---|---|
| `displayName` | string | yes | Non-empty operator-facing name. |
| `description` | string | yes | Non-empty explanation of intended use. |
| `maxConcurrency` | integer | yes | `1..128`; excess work queues in Copilot. |
| `maxDepth` | integer | yes | `1..128`; prevents uncontrolled nested delegation. |
| `resultExposure` | enum | yes | `final-only`, `status-and-final`, or `detailed`. |
| `disabledSubagents` | string array | no | Agent names Copilot may not dispatch. |
| `agents` | object | no | Per-agent live settings keyed by Copilot agent type. |

Each agent entry may define `model`, `modelPolicy` (`preferred` or `required`), `effortLevel`,
`contextTier` (`inherit`, `default`, or `long_context`), and `autoInvoke`.

Unknown fields, duplicate disabled-agent names, invalid ranges, invalid enums, and malformed agent
entries are rejected. An invalid user configuration does not become a partial policy.

## Built-in policies

When no configuration exists, the extension provides:

- **Solo:** disables built-in delegated workers and leaves only direct main-agent work.
- **Conservative:** two concurrent workers, depth one, final results only.
- **Balanced:** four concurrent workers, depth one, status and final results.
- **Burst:** six concurrent workers, depth one, status and final results.

Built-ins inherit the parent model unless the user configuration replaces them with explicit model
routing. This avoids assuming a provider exists.

## Runtime architecture

1. The extension joins the current Copilot session.
2. It loads and validates configuration atomically.
3. It registers `/subagent-policy`.
4. Applying a policy calls `session.tools.updateSubagentSettings` with:
   - `maxConcurrency`
   - `maxDepth`
   - `disabledSubagents`
   - per-agent model, model policy, effort, context, and auto-invocation settings
5. Clearing calls the API with `settings: null`.
6. A route-scoped IPC state file supplies the trusted native modal. It contains policy metadata and
   status only.

`resultExposure` controls extension observability:

- `final-only`: no subagent progress is projected by this extension.
- `status-and-final`: aggregate active/queued/completed state may be shown.
- `detailed`: agent names, states, and timing may be shown.

Copilot still receives the subagent's normal final result. The extension does not redact or mutate
Copilot's internal parent-child result transport.

## Failure behavior

- Configuration errors are explicit in session logs and the modal.
- Applying an invalid or unknown policy fails without changing the current policy.
- SDK update failures leave the previous policy active and are reported.
- Closing the modal never clears or changes the policy.
- Limits queue work; the extension does not turn capacity pressure into cancellation.

## Testing

Implementation is complete only when:

1. Unit tests cover defaults, configuration validation, SDK projection, clearing, failed apply, and
   session-local state.
2. IPC tests cover route isolation, stale request recovery, and action acknowledgement.
3. Existing Go and JavaScript suites pass.
4. Release packaging includes the extension.
5. Real ConPTY acceptance installs an isolated package, starts real Copilot, opens
   `/subagent-policy`, applies multiple policies using actual keys, observes durable state changes,
   closes and reopens the modal, and verifies a fresh session receives only `defaultPolicy`.
6. Agentic acceptance launches real subagent work under the applied policy and verifies the
   configured concurrency/model settings through session events or SDK-observable state.

