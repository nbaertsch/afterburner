# Afterburner UI extension author guide

Afterburner UI extensions declare their UI contract in `afterburner.json` under the optional `ui` field. Existing extensions remain valid without it.

## Manifest

```json
{
  "schemaVersion": 1,
  "id": "sample-ui",
  "displayName": "Sample UI",
  "visibility": "private",
  "requires": { "afterburner": ">=1.0.0" },
  "runtime": { "execution": "in-process", "entrypoint": "runtime/extension.mjs" },
  "ui": {
    "protocol": "afterburner.ui",
    "revision": 1,
    "surfaces": [{ "id": "sample-panel", "kind": "panel" }],
    "components": ["panel", "text", "button"],
    "capabilities": ["ui.render.components", "ui.action.invoke"]
  }
}
```

Validate locally:

```powershell
afterburn ui validate-manifest .\afterburner.json
```

Discover the host-supported UI contract before authoring or reviewing an extension:

```powershell
afterburn ui catalog
afterburn ui catalog --json
```

The catalog output is generated from the same component, surface, and capability registries enforced by validation, so it is the source of truth for manifest `ui.components`, `ui.surfaces[].kind`, and `ui.capabilities` values.

## Components and lifecycle

Use the SDK component builders from `sdk\ui` to emit versioned `afterburner.ui` component snapshots and patches. Every public component kind has both `ui.components.<kind>(...)` and a named `ui.<kind>(...)` builder. Extensions can inspect `ui.componentCatalog` for `{ kind, stability, description }` metadata and `ui.componentKinds` for the ordered kind list. Surface authors can likewise inspect `ui.surfaceCatalog`/`ui.surfaceKinds`, and enterprise policy tooling can inspect `ui.capabilityCatalog`/`ui.capabilityKinds` before declaring grants. Every node should have a stable `id`, a `kind`, JSON props, and accessibility metadata when the visible label is not obvious. Surfaces move through declared, mounted, rendering, interactive, suspended, disposing, disposed, and failed states.

Public composable design primitives include:

- Layout: `application`, `window`, `surface`, `viewport`, `stack`, `column`, `row`, `grid`, `box`, `section`, `split`, `scroll`, `disclosure`, `panel`, `card`, `separator`, `spacer`.
- Content/status: `empty`, `text`, `markdown`, `code`, `icon`, `badge`, `keyValue`, `detail`, `alert`, `progress`, `meter`, `bar`, `sparkline`, `spinner`, `loading`, `toast`, `errorBoundary`.
- Collections/data: `list`, `table`, `tree`, `timeline`, `log`, `chart`.
- Inputs/actions: `form`, `button`, `link`, `textInput`, `passwordInput`, `searchInput`, `numberInput`, `textArea`, `select`, `checkbox`, `radioGroup`, `toggle`, `slider`, `dateInput`, `fileInput`, `toolbar`, `actionBar`, `contextMenu`, `commandPalette`, `keybindingHint`, `pagination`, `help`, `confirmation`, `prompt`.
- Surfaces/media/extension points: `dialog`, `terminal`, `canvas`, `image`, `video`, `extensionOutlet`.

## Security and grants

Declare only the capabilities your surface needs. Enterprise hosts deny by default unless a runtime grant or built-in signed default applies. Validate grant behavior with:

```powershell
afterburn ui certify --extension sample-ui --surface sample-panel
```

## Fallback and testing

Authors should test narrow, monochrome, no-Unicode, and high-contrast modes. The built-in fixtures cover all components plus malformed and abuse inputs:

```powershell
afterburn ui render-fixture component-gallery
afterburn ui render-fixture all-components
afterburn ui simulate --extension sample-ui --surface sample-panel --json
```

If rich rendering fails, surfaces must degrade to sanitized plain output. Do not depend on Black Box being installed; it is an optional metadata-only observability sink.
