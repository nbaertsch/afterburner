# Afterburner

Afterburner is a native Go control plane, process launcher, and trusted runtime-extension host for GitHub Copilot CLI.

Instead of launching `copilot` directly, you run `afterburn`. Afterburner prepares an isolated managed environment, selects a verified compatibility profile for your installed Copilot package, launches the real Copilot CLI with exact argument passthrough, and hosts in-process extensions that add capabilities like custom model providers, privacy-preserving diagnostics, and a local OpenAI-compatible API.

---

## Architecture & Mental Model

```text
User / Terminal / Automation
             │
             ▼
┌────────────────────────────────────────────────────────┐
│ afterburn.exe (Native Go Control Plane)                │
│                                                        │
│ • Home & Layout Management (~/.afterburner)            │
│ • Package Discovery & Profile Compatibility Selection  │
│ • Tamper-Resistant Extension Registry                  │
│ • ConPTY Terminal Broker & Native Modal Broker         │
│ • Atomic In-Place Binary Updates & Rollback            │
│ • Process Supervision & Signal Forwarding              │
└───────────────────────────┬────────────────────────────┘
                            │ launches & supervises
                            ▼
┌────────────────────────────────────────────────────────┐
│ GitHub Copilot CLI Process                             │
│                                                        │
│ ┌────────────────────────────────────────────────────┐ │
│ │ Trusted In-Process JavaScript Runtime Host         │ │
│ │                                                    │ │
│ │ • BYOModels (Custom Providers & Context Scaling)   │ │
│ │ • Black Box (Supported Observability Layer)        │ │
│ │ • OpenAI Server (Localhost /v1 HTTP Bridge)        │ │
│ │ • Custom Extensions (.zip or Git sources)          │ │
│ └────────────────────────────────────────────────────┘ │
└────────────────────────────────────────────────────────┘
```

- **Native Go Control Plane (`afterburn.exe`):** Runs outside Copilot. Owns launch isolation, process lifecycle, signal handling, terminal brokering, immutable extension verification, self-repair, and atomic updates.
- **In-Process JavaScript Runtime Host:** Runs inside Copilot CLI. Loads verified extension packages that hook directly into Copilot's SDK and session lifecycle without altering global configuration.

---

## Quickstart

### Option A: Install from a Release Binary (Recommended)

1. Download the latest release archive (`afterburn-windows-amd64.zip` or `afterburn-windows-arm64.zip`) from [GitHub Releases](https://github.com/nbaertsch/afterburner/releases).
2. Extract the archive into a folder.
3. Open a PowerShell terminal in that folder and run:

```powershell
# 1. Install the executable to ~/.afterburner/bin and add to user PATH
.\afterburn.exe core install

# 2. Download and verify signed built-in extensions (BYOModels, Black Box, OpenAI Server)
afterburn install

# 3. Verify health and environment compatibility
afterburn doctor
```

4. Open a fresh terminal (so the updated `PATH` is active) and launch Copilot:

```powershell
afterburn
```

---

### Option B: Build from Source

Prerequisites: Go 1.23+, Node.js 22+, and Git on Windows.

```powershell
git clone git@github.com:nbaertsch/afterburner.git
Set-Location .\afterburner

# Build native binary
go build -trimpath -o .\afterburn.exe .\cmd\afterburn

# Install core and default extensions
.\afterburn.exe core install
afterburn install
afterburn doctor
afterburn
```

---

### Verify Your Installation

Run:

```powershell
afterburn version         # Shows core version
afterburn doctor          # Inspects installation health and Copilot compatibility
afterburn extension list  # Lists installed and active extensions
```

---

## Daily CLI Workflows

Afterburner acts as a transparent, high-fidelity wrapper around Copilot CLI. All standard Copilot flags, arguments, prompts, and piping work identically:

```powershell
# Interactive chat session
afterburn

# Non-interactive prompt execution
afterburn -p "Explain how quicksort works in Go"

# Pass arbitrary flags to Copilot
afterburn --model gpt-5.4 --effort high

# Collision resolution: force arguments directly to Copilot
afterburn run <copilot-args...>
afterburn -- <copilot-args...>
```

### Interactive Terminal Broker

On Windows, interactive launches automatically attach to Afterburner's production ConPTY terminal broker. The broker seamlessly multiplexes Copilot's interactive terminal with authenticated native modal UI dialogs. Non-interactive and piped launches run directly without terminal overhead.

### Safe Mode & Emergency Recovery

If an extension or experimental configuration misbehaves, bypass extensions without losing data:

```powershell
# Launch Copilot in safe mode with all extensions disabled
afterburn --safe-mode

# Disable a specific extension for a single run
afterburn --disable-extension steward-burn
```

---

## Built-In Extensions (First-Party Product Pillars)

Afterburner ships with four primary first-party extensions versioned in lockstep with the core release:

### 1. OpenAI Server (`openai-server`)

Exposes your active Copilot CLI session as a local OpenAI-compatible HTTP API (`http://127.0.0.1:41425/v1`). This enables local Python scripts, LiteLLM, evaluation harnesses (such as Colosseum), and developer tools to query Copilot models.

- **Status:** Installed disabled by default to avoid unintended local port exposure.
- **Commands:** Run `afterburn enable openai-server`, then inside a session use `/openai-server`.
- **Auto-start:** Set `"enabled": true` in `~\.afterburner\config\openai-server.json`.
- **Documentation:** [**OpenAI Server Deep Dive & Quickstart**](extensions/OpenAIServer/README.md)

---

### 2. BYOModels (`byo-models`)

Enables "Bring Your Own Models" with custom OpenAI/Azure-compatible endpoints and exposes native reasoning effort and context-window scaling controls in Copilot's interactive `/model` picker.

- **Status:** Enabled by default upon `afterburn install`.
- **Config:** User-owned configuration at `~\.afterburner\config\byomodels.json`.
- **Usage:** Open `/model` in an interactive session; use arrow keys for reasoning effort and Tab to select context tiers (256K, 512K, 768K, 1.05M).
- **Features:** Header forwarding allowlists, legacy tool schema flattening, request ID tracking, and real-time streaming with pre-token error propagation.
- **Documentation:** [**BYOModels Deep Dive**](extensions/BYOModels/README.md)

---

### 3. Black Box (`black-box`)

Afterburner's supported observability layer. Captures bounded, privacy-preserving, metadata-only runtime diagnostics to help debug crashes, latency bottlenecks, and tool failures.

- **Status:** Enabled by default upon `afterburn install`.
- **Privacy Guarantee:** Never records prompts, model responses, source code, tool arguments/results, or summaries.
- **Commands:** Use `/black-box`, `/black-box-modal`, `/black-box-tail`, `/black-box-export`, and `/black-box-doctor` inside Copilot.
- **Data storage:** Stored locally in `~\.afterburner\extension-data\black-box`.
- **Documentation:** [**Black Box Deep Dive**](extensions/BlackBox/README.md)

---

### 4. Subagent Policy (`subagent-policy`)

Applies live, session-scoped controls to Copilot subagents, including model routing, required or preferred model selection, concurrency, nesting depth, disabled agents, and optional agent-factory budgets.

- **Status:** Enabled by default upon `afterburn install`.
- **Command:** Use `/subagent-policy` in an interactive session.
- **Config:** Optional policy catalog at `~\.afterburner\config\subagent-policy.json`.
- **Safety:** Policies never reduce or replace a model's configured context window.
- **Documentation:** [**Subagent Policy Guide**](extensions/SubagentPolicy/README.md)

---

## Onboarding: Connecting External Tools via OpenAI Server

A common workflow for new engineers is using Afterburner to power local evaluation harnesses, agent loops, or Python scripts:

### 1. Configure the local bridge

Create `~\.afterburner\config\openai-server.json`:

```json
{
  "enabled": true,
  "host": "127.0.0.1",
  "port": 41425,
  "requireApiKey": false
}
```

### 2. Start Afterburner

Keep a terminal running:

```powershell
afterburn
```

### 3. Query from Python

```python
from openai import OpenAI

client = OpenAI(
    base_url="http://127.0.0.1:41425/v1",
    api_key="local-token",
)

response = client.chat.completions.create(
    model="gpt-5.4",
    messages=[{"role": "user", "content": "What is the capital of France?"}],
)

print(response.choices[0].message.content)
```

### 4. Query from LiteLLM / Evaluation Runners

Point your evaluation runner or LiteLLM router to:

- **API Base:** `http://127.0.0.1:41425/v1`
- **API Key:** `local-token`
- **Model:** `openai/gpt-5.4` (or any model reported by `GET /v1/models`)

See [extensions/OpenAIServer/README.md](extensions/OpenAIServer/README.md) for full configuration, streaming examples, and troubleshooting.

---

## Extension Management

### Managing Built-In Extensions

Built-ins are fetched and verified against the current signed release:

| Task | Command |
|---|---|
| Install all built-ins | `afterburn install` |
| Install specific built-in | `afterburn install byo-models openai-server` |
| Enable built-in | `afterburn enable openai-server` |
| Disable built-in | `afterburn disable black-box` |
| Uninstall built-in | `afterburn uninstall black-box` |
| Sync with new core release | `afterburn update` |

---

### Managing Custom Extensions

You can install third-party or custom extensions from packaged ZIPs or Git repositories:

| Task | Command |
|---|---|
| Pack extension directory | `afterburn extension pack .\my-plugin .\dist\my-plugin.zip` |
| Validate manifest | `afterburn extension validate .\my-plugin` |
| Install from ZIP | `afterburn extension install .\dist\my-plugin.zip` |
| Install from Git ref | `afterburn extension install user/repo@main` |
| Enable custom extension | `afterburn extension enable my-plugin` |
| Update custom extension | `afterburn extension update my-plugin` |
| Roll back to previous version | `afterburn extension rollback my-plugin` |
| List all extensions | `afterburn extension list` |

For custom extension development, TypeScript declarations, and schemas, see the [Extension SDK Guide](sdk/README.md).

---

## Configuration Locations

All Afterburner configuration and state is organized under `~/.afterburner`:

| Component | Path / Location | Purpose |
|---|---|---|
| **Root Home** | `~/.afterburner` | Base directory for Afterburner control plane state |
| **Binaries** | `~/.afterburner\bin\afterburn.exe` | Installed native control plane executable |
| **Extension Registry** | `~/.afterburner\registry.json` | Verified extension identities, sources, and status |
| **BYOModels Config** | `~/.afterburner\config\byomodels.json` | Custom model providers, endpoints, and credentials |
| **OpenAI Server Config** | `~/.afterburner\config\openai-server.json` | Localhost bridge port, startup, and auth settings |
| **Black Box Config** | `~/.afterburner\config\black-box.json` | Observability storage retention and ring limits |
| **Subagent Policy Config** | `~/.afterburner\config\subagent-policy.json` | Session defaults, routing, concurrency, depth, and agent-factory budgets |
| **Black Box Data** | `~/.afterburner\extension-data\black-box` | Sanitized session timelines and diagnostic records |
| **Copilot Home** | `~/.afterburner\copilot-home` | Isolated loader state (session state linked via junction) |

---

## Troubleshooting & Diagnostics

### Run System Diagnostics

```powershell
# Human-readable report of core, packages, and extensions
afterburn doctor

# Machine-readable JSON output for automated health checks
afterburn doctor --json

# Export a sanitized diagnostic bundle for bug reporting
afterburn doctor --bundle
```

### Repair Corrupt or Stale State

```powershell
afterburn repair
```

Validates the managed session directory junction, verifies extension registry integrity, purges abandoned update staging directories, and reconciles state without touching user configuration or stored extension data.

### Inspect Package Compatibility

```powershell
afterburn compatibility status  # Shows selected Copilot package and profile ID
afterburn compatibility list    # Lists embedded compatibility profiles
afterburn compatibility retry   # Invalidates cache to re-run compatibility probes
```

Exact known Copilot package hashes use embedded compatibility profiles. Complete Copilot packages
newer than the newest embedded profile are accepted through a generated forward-compatibility
profile, then must pass Afterburner's runtime self-test before they replace the last-known-good
package. Built-in extensions use the same open-ended minimum-version policy.

---

## Updates & Rollback

Afterburner includes an atomic in-place update engine:

```powershell
# Check for updates without applying
afterburn update --check

# Download, verify signatures, and install latest core and built-ins
afterburn update

# Roll back to the previous core and matching extension set
afterburn rollback core
```

### Security & Release Verification

1. **Cryptographic Signatures:** Every release publishes a canonical `release-manifest.json` signed with an Ed25519 key restricted to GitHub Actions release workflows.
2. **Atomic Binary Replacement:** Windows prevents overwriting running executables. Afterburner uses a retiring replacement mechanism to stage and swap binaries atomically without terminating active sessions.
3. **Automatic Reversion:** Post-update preflight tests verify the new core; if startup fails, the previous working binary is automatically restored.

---

## Development & Testing

```powershell
# Run full Go unit tests
go test ./...

# Run static analysis
go vet -unsafeptr=false ./...

# Run JavaScript extension tests
npm test

# Verify byte synchronization across runtime mirrors
npm run check:runtime

# Run full release validation script
npm run test:release-local
```

---

## Documentation Links

- [BYOModels Guide](extensions/BYOModels/README.md)
- [Black Box Observability Guide](extensions/BlackBox/README.md)
- [OpenAI Server Bridge Guide](extensions/OpenAIServer/README.md)
- [Subagent Policy Guide](extensions/SubagentPolicy/README.md)
- [Extension Authoring SDK](sdk/README.md)
- [UI Document Schema](schemas/ui-document-v1.schema.json)
