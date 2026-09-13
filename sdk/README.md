# Afterburner extension UI SDK

The UI SDK defines the versioned document contract used by verified Afterburner extensions. The
runtime injects the matching builders and `registerSurface` function as `api.ui`; extensions do not
open pipes or hold native capability tokens.

```js
export async function activate(api) {
  const { components: ui } = api.ui;
  api.ui.registerSurface({
    id: "settings",
    kind: "modal",
    open: () => ({
      document: api.ui.createDocument(
        ui.dialog({ title: "Settings" }, [
          ui.textInput({ label: "Display name" }, [], { id: "display-name" }),
          ui.checkbox({ label: "Enabled", checked: true }, [], { id: "enabled" })
        ], { id: "settings-root" }),
        { surfaceId: "settings" }
      )
    }),
    onEvent(event) {
      if (event.type === "change") {
        // Persist extension-owned state and call controls.update() to render it.
      }
    }
  });
}
```

Declare every surface in `afterburner.json` under `ui.surfaces`. Interactive components require
stable IDs. Documents are bounded, plain JSON snapshots; terminal escape sequences, unknown
component kinds, duplicate IDs, and undeclared surfaces are rejected. The machine-readable
document contract is `schemas/ui-document-v1.schema.json` in the Afterburner repository.

Prefer native controls that match the interaction: use `radioGroup` or `select` for arrow-key
selection, buttons for discrete commands, and inputs for editable values. `actionBindings` describe
semantic actions on nodes, but stateful surfaces must also implement the surface-level `onEvent`
callback. Handle `change` to render a preview and `activate`/`submit` to perform work through the
provided `controls.update()` or `controls.close()` API. Keep top-level `actions` for global keyboard
accelerators such as reload, clear, and close—not as a duplicate compatibility-frame wall for every
choice already represented by a native control.

Validate terminal UI changes in a real ConPTY. Reconstruct the semantic viewport after every input,
assert focus, selected value, and application state separately, and generate a PNG/report for human
inspection. Searching the accumulated stripped output is insufficient because stale frames can make
selection and focus assertions pass when the visible screen is wrong.
