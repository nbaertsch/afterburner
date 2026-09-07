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
afterburn ui catalog components
afterburn ui catalog --find observability
afterburn ui catalog capabilities --find sink --json
```

The catalog output is generated from the same component, surface, and capability registries enforced by validation, so it is the source of truth for manifest `ui.components`, `ui.surfaces[].kind`, and `ui.capabilities` values. Filter by `components`, `surfaces`, or `capabilities` when reviewing one part of the contract, and add `--find <text>` to search names, stability, and descriptions.

## Components and lifecycle

Use the SDK component builders from `sdk\ui` to emit versioned `afterburner.ui` component snapshots and patches. Every public component kind has both `ui.components.<kind>(...)` and a named `ui.<kind>(...)` builder. Extensions can inspect `ui.componentCatalog` for `{ kind, stability, description }` metadata and `ui.componentKinds` for the ordered kind list. Surface authors can likewise inspect `ui.surfaceCatalog`/`ui.surfaceKinds`, and enterprise policy tooling can inspect `ui.capabilityCatalog`/`ui.capabilityKinds` before declaring grants. Every node should have a stable `id`, a `kind`, JSON props, and accessibility metadata when the visible label is not obvious. Surfaces move through declared, mounted, rendering, interactive, suspended, disposing, disposed, and failed states.

## Accessibility props

The accessibility projection honors structured `accessibility` metadata plus author-friendly prop aliases for labels (`ariaLabel`, `aria-label`), descriptions (`ariaDescription`, `aria-description`), visibility (`ariaHidden`, `aria-hidden`), modal state (`ariaModal`, `aria-modal`), boolean states (`ariaDisabled`, `ariaExpanded`, `ariaSelected`, `ariaPressed`, `ariaHasPopup`), input hints (`ariaPlaceholder`, `ariaAutoComplete`, `ariaMultiline`), live regions (`ariaLive`, `ariaAtomic`, `ariaRelevant`), ranges (`ariaValueNow`, `ariaValueMin`, `ariaValueMax`, `ariaValueText`), hierarchy (`ariaLevel`, `ariaPosInSet`, `ariaSetSize`), table/grid coordinates (`ariaSort`, `ariaRowIndex`, `ariaColIndex`, `ariaRowSpan`, `ariaColSpan`, `ariaRowCount`, `ariaColCount`), keyboard hints (`ariaKeyShortcuts`), and relationships (`ariaLabelledBy`, `ariaDescribedBy`, `ariaControls`, `ariaActiveDescendant`, `ariaErrorMessage`, `ariaDetails`, `ariaOwns`, `ariaFlowTo`). CamelCase and hyphenated ARIA spellings are accepted where applicable, and relationship/key shortcut lists may be provided as arrays or as comma/space-separated strings. Use these aliases when porting web accessibility guidance into terminal-first Afterburner component trees.

Public composable design primitives include:
- Layout: `application`, `window`, `surface`, `viewport`, `stack`, `column`, `row`, `grid`, `box`, `section`, `split`, `scroll`, `disclosure`, `panel`, `card`, `separator`, `spacer`.
- Content/status: `empty`, `text`, `markdown`, `code`, `icon`, `badge`, `keyValue`, `detail`, `alert`, `progress`, `meter`, `bar`, `sparkline`, `spinner`, `loading`, `toast`, `errorBoundary`.
- Collections/data: `list`, `table`, `tree`, `timeline`, `log`, `chart`.
- Inputs/actions: `form`, `button`, `link`, `textInput`, `passwordInput`, `searchInput`, `numberInput`, `textArea`, `select`, `checkbox`, `radioGroup`, `toggle`, `slider`, `dateInput`, `fileInput`, `toolbar`, `actionBar`, `contextMenu`, `commandPalette`, `keybindingHint`, `pagination`, `help`, `confirmation`, `prompt`.
- Surfaces/media/extension points: `dialog`, `terminal`, `canvas`, `image`, `video`, `extensionOutlet`.

Native terminal modal fallback is document-first: when a modal frame includes a `document`, the host renders the structured document before considering legacy `body` text. The terminal projection includes sections, cards, progress-like controls, tables, key/value details, and form controls (`textInput`, `passwordInput`, `searchInput`, `numberInput`, `dateInput`, `fileInput`, `textArea`, `select`, `radioGroup`, `checkbox`, `toggle`, and `slider`) as sanitized text rows. Password values are masked in the projection.

## Security and grants

Declare only the capabilities your surface needs. Enterprise hosts deny by default unless a runtime grant or built-in signed default applies. Validate grant behavior with:

```powershell
afterburn ui certify --extension sample-ui --surface sample-panel
```

## Fallback and testing

Authors should test narrow, monochrome, no-Unicode, and high-contrast modes. The built-in fixtures cover all components plus malformed and abuse inputs:

```powershell
afterburn ui render-fixture --list
afterburn ui render-fixture component-gallery --width 120 --height 40 --theme afterburner.dark --color truecolor --unicode
afterburn ui render-fixture all-components --width 40 --height 30 --theme afterburner.highContrast --color mono
afterburn ui simulate --extension sample-ui --surface sample-panel --json
```

If rich rendering fails, surfaces must degrade to sanitized plain output. Do not depend on Black Box being installed; it is an optional metadata-only observability sink.
