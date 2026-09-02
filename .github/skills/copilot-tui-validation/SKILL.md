---
name: copilot-tui-validation
description: Validate Copilot CLI and other interactive terminal UIs through Windows ConPTY using node-pty. Use for picker, dialog, keyboard-navigation, restart, and ANSI-rendering tests.
---

# Copilot TUI validation

Use a real pseudo-terminal when behavior depends on TTY detection, keyboard input, focus, terminal
dimensions, or incremental ANSI rendering. Redirected stdin/stdout is not equivalent to the TUI.

## Harness

1. Install `node-pty` in a disposable directory.
2. Spawn the exact `copilot.exe` path with `node-pty` and `name: "xterm-256color"`.
3. Use at least 120 columns; picker tables change behavior at narrow widths.
4. Remove inherited attachment variables before spawning:
   `COPILOT_AGENT_SESSION_ID`, `COPILOT_LOADER_PID`, and `COPILOT_SUPERVISED`.
5. Capture the complete `onData` stream. Keep raw output until the test is finished.
6. Drive state from semantic output, not fixed sleeps alone:
   - send Escape when `Restore interrupted sessions` appears;
   - approve extension permissions when `wants elevated permissions` appears;
   - open `/model` only after the expected registration message appears.
7. Allow search/filter highlights to settle before sending navigation keys. A Tab sent immediately
   after typing a filter can arrive before the highlighted row updates.
8. Send terminal keys as actual control characters:
   - Enter: `"\r"`
   - Escape: `"\x1b"`
   - Tab: `"\t"`
   - Down: `"\x1b[B"`
   - Right: `"\x1b[C"`
   Do not double-escape them as `"\\x1b[B"`.
9. Strip CSI and OSC sequences only for assertions. Assert durable text and state transitions rather
   than exact full-screen snapshots.
10. Terminate only the pseudo-terminal child created by the harness. Never kill processes by name.

## Cold-start validation

Set `COPILOT_RUNTIME_EXTENSION_DEBUG=1` and require these messages before testing plugin behavior:

```text
[runtime-extension-host] loaded from ...
[runtime-extension-host] registered picker adapter ...
[runtime-extension-host] activated '<plugin>'
```

This distinguishes stale/resumed sessions from a process that loaded the current runtime host.

## Picker validation

For model picker changes, verify all three layers:

1. **Projection:** expected columns, row text, navigation fields, and every option.
2. **Interaction:** send the real key repeatedly and observe every state, allowing highlight changes
   to settle first.
3. **Application:** select the row and inspect the model-change result or subsequent session events;
   rendering alone does not prove the runtime limit was applied.

For Afterburner context adapters, a complete assertion covers `256K`, `512K`, `768K`, and `1.05M`,
plus the corresponding runtime capability override.

## Cleanup

Delete the disposable npm project and captured logs after validation. Do not leave interrupted test
sessions or broad process cleanup commands behind.
