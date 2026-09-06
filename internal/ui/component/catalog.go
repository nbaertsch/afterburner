package component

import (
	"encoding/json"

	"github.com/nbaertsch/afterburner/internal/ui/accessibility"
	"github.com/nbaertsch/afterburner/internal/ui/localization"
	"github.com/nbaertsch/afterburner/internal/ui/style"
)

type Kind string

const (
	KindApplication     Kind = "application"
	KindWindow          Kind = "window"
	KindSurface         Kind = "surface"
	KindViewport        Kind = "viewport"
	KindStack           Kind = "stack"
	KindRow             Kind = "row"
	KindGrid            Kind = "grid"
	KindStatusGrid      Kind = "statusGrid"
	KindPanel           Kind = "panel"
	KindCard            Kind = "card"
	KindSeparator       Kind = "separator"
	KindSpacer          Kind = "spacer"
	KindEmpty           Kind = "empty"
	KindText            Kind = "text"
	KindMarkdown        Kind = "markdown"
	KindCode            Kind = "code"
	KindIcon            Kind = "icon"
	KindBadge           Kind = "badge"
	KindKeyValue        Kind = "keyValue"
	KindDetail          Kind = "detail"
	KindAlert           Kind = "alert"
	KindButton          Kind = "button"
	KindLink            Kind = "link"
	KindTextInput       Kind = "textInput"
	KindTextArea        Kind = "textArea"
	KindSelect          Kind = "select"
	KindCheckbox        Kind = "checkbox"
	KindRadioGroup      Kind = "radioGroup"
	KindToggle          Kind = "toggle"
	KindSlider          Kind = "slider"
	KindProgress        Kind = "progress"
	KindSparkline       Kind = "sparkline"
	KindSpinner         Kind = "spinner"
	KindList            Kind = "list"
	KindTable           Kind = "table"
	KindTree            Kind = "tree"
	KindForm            Kind = "form"
	KindToolbar         Kind = "toolbar"
	KindActionBar       Kind = "actionBar"
	KindTabs            Kind = "tabs"
	KindBreadcrumb      Kind = "breadcrumb"
	KindDialog          Kind = "dialog"
	KindToast           Kind = "toast"
	KindTerminal        Kind = "terminal"
	KindCanvas          Kind = "canvas"
	KindImage           Kind = "image"
	KindVideo           Kind = "video"
	KindChart           Kind = "chart"
	KindCommandPalette  Kind = "commandPalette"
	KindKeybindingHint  Kind = "keybindingHint"
	KindExtensionOutlet Kind = "extensionOutlet"
)

type Stability string

const (
	StabilityStable       Stability = "stable"
	StabilityExperimental Stability = "experimental"
	StabilityDeprecated   Stability = "deprecated"
)

type CatalogEntry struct {
	Kind        Kind      `json:"kind"`
	Stability   Stability `json:"stability"`
	Description string    `json:"description"`
}

type Node struct {
	ID             string                     `json:"id"`
	Kind           Kind                       `json:"kind"`
	Key            string                     `json:"key,omitempty"`
	Version        uint64                     `json:"version,omitempty"`
	Props          json.RawMessage            `json:"props,omitempty"`
	Children       []Node                     `json:"children,omitempty"`
	Style          *style.ComponentStyle      `json:"style,omitempty"`
	Accessibility  *accessibility.Node        `json:"accessibility,omitempty"`
	Localization   *localization.Reference    `json:"localization,omitempty"`
	DataBindings   []string                   `json:"dataBindings,omitempty"`
	ActionBindings map[string]string          `json:"actionBindings,omitempty"`
	ExtensionSlots []ExtensionSlot            `json:"extensionSlots,omitempty"`
	Compatibility  []string                   `json:"compatibility,omitempty"`
	Metadata       map[string]json.RawMessage `json:"metadata,omitempty"`
}

type Tree struct {
	Root         Node     `json:"root"`
	Revision     uint64   `json:"revision"`
	SurfaceID    string   `json:"surfaceId"`
	ThemeID      string   `json:"themeId,omitempty"`
	Locale       string   `json:"locale,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
}

type ExtensionSlot struct {
	ID                    string   `json:"id"`
	AllowedComponentKinds []Kind   `json:"allowedComponentKinds,omitempty"`
	RequiredCapabilities  []string `json:"requiredCapabilities,omitempty"`
}

func PublicCatalog() []CatalogEntry {
	return []CatalogEntry{
		{KindApplication, StabilityStable, "Top-level application composition root."},
		{KindWindow, StabilityStable, "Window-level container for host-managed UI."},
		{KindSurface, StabilityStable, "Mount point bound to a surface descriptor."},
		{KindViewport, StabilityStable, "Scrollable viewport."},
		{KindStack, StabilityStable, "One-dimensional vertical layout."},
		{KindRow, StabilityStable, "One-dimensional horizontal layout."},
		{KindGrid, StabilityStable, "Two-dimensional layout."},
		{KindStatusGrid, StabilityStable, "Dashboard-style status grid for health and metrics."},
		{KindPanel, StabilityStable, "Grouped content panel."},
		{KindCard, StabilityStable, "Elevated content region."},
		{KindSeparator, StabilityStable, "Visual or semantic separator."},
		{KindSpacer, StabilityStable, "Intentional empty layout space."},
		{KindEmpty, StabilityStable, "Purposeful empty-state message."},
		{KindText, StabilityStable, "Plain text content."},
		{KindMarkdown, StabilityStable, "Sanitized Markdown content."},
		{KindCode, StabilityStable, "Code block or inline code content."},
		{KindIcon, StabilityStable, "Decorative or semantic icon."},
		{KindBadge, StabilityStable, "Compact status label."},
		{KindKeyValue, StabilityStable, "Compact key/value facts and metadata."},
		{KindDetail, StabilityStable, "Detailed record inspection content."},
		{KindAlert, StabilityStable, "Prominent status, warning, or error callout."},
		{KindButton, StabilityStable, "User-invoked action control."},
		{KindLink, StabilityStable, "Navigation or external reference."},
		{KindTextInput, StabilityStable, "Single-line text input."},
		{KindTextArea, StabilityStable, "Multi-line text input."},
		{KindSelect, StabilityStable, "Single or multi-select input."},
		{KindCheckbox, StabilityStable, "Boolean checkbox input."},
		{KindRadioGroup, StabilityStable, "Exclusive option group."},
		{KindToggle, StabilityStable, "Binary switch control."},
		{KindSlider, StabilityStable, "Continuous or stepped numeric input."},
		{KindProgress, StabilityStable, "Progress indicator."},
		{KindSparkline, StabilityStable, "Compact inline trend visualization."},
		{KindSpinner, StabilityStable, "Indeterminate progress indicator."},
		{KindList, StabilityStable, "Linear collection."},
		{KindTable, StabilityStable, "Tabular data collection."},
		{KindTree, StabilityStable, "Hierarchical data collection."},
		{KindForm, StabilityStable, "Validated input group."},
		{KindToolbar, StabilityStable, "Action strip."},
		{KindActionBar, StabilityStable, "Primary command/action strip with keyboard affordances."},
		{KindTabs, StabilityStable, "Tabbed content switcher."},
		{KindBreadcrumb, StabilityStable, "Navigation path."},
		{KindDialog, StabilityStable, "Modal or non-modal dialog."},
		{KindToast, StabilityStable, "Transient notification."},
		{KindTerminal, StabilityStable, "Terminal surface projection."},
		{KindCanvas, StabilityStable, "Extension-owned canvas."},
		{KindImage, StabilityStable, "Image content."},
		{KindVideo, StabilityStable, "Video content."},
		{KindChart, StabilityStable, "Data visualization."},
		{KindCommandPalette, StabilityStable, "Command discovery and invocation UI."},
		{KindKeybindingHint, StabilityStable, "Keyboard shortcut hint."},
		{KindExtensionOutlet, StabilityStable, "Policy-gated extension insertion point."},
	}
}
