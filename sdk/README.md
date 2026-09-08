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
