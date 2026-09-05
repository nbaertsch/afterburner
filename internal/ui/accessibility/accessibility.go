package accessibility

type Role string

const (
	RoleApplication   Role = "application"
	RoleArticle       Role = "article"
	RoleBanner        Role = "banner"
	RoleButton        Role = "button"
	RoleCheckbox      Role = "checkbox"
	RoleCode          Role = "code"
	RoleColumnHeader  Role = "columnheader"
	RoleCombobox      Role = "combobox"
	RoleComplementary Role = "complementary"
	RoleContentInfo   Role = "contentinfo"
	RoleDialog        Role = "dialog"
	RoleDocument      Role = "document"
	RoleGrid          Role = "grid"
	RoleGridCell      Role = "gridcell"
	RoleGroup         Role = "group"
	RoleHeading       Role = "heading"
	RoleImage         Role = "img"
	RoleLink          Role = "link"
	RoleList          Role = "list"
	RoleListItem      Role = "listitem"
	RoleLog           Role = "log"
	RoleMain          Role = "main"
	RoleMenu          Role = "menu"
	RoleMenuItem      Role = "menuitem"
	RoleNavigation    Role = "navigation"
	RoleOption        Role = "option"
	RoleProgressBar   Role = "progressbar"
	RoleRadio         Role = "radio"
	RoleRadioGroup    Role = "radiogroup"
	RoleRegion        Role = "region"
	RoleRow           Role = "row"
	RoleRowHeader     Role = "rowheader"
	RoleSearch        Role = "search"
	RoleSeparator     Role = "separator"
	RoleStatus        Role = "status"
	RoleSwitch        Role = "switch"
	RoleTab           Role = "tab"
	RoleTabList       Role = "tablist"
	RoleTabPanel      Role = "tabpanel"
	RoleTable         Role = "table"
	RoleTextbox       Role = "textbox"
	RoleTimer         Role = "timer"
	RoleToolbar       Role = "toolbar"
	RoleTooltip       Role = "tooltip"
	RoleTree          Role = "tree"
	RoleTreeItem      Role = "treeitem"
)

type LivePoliteness string

const (
	LiveOff       LivePoliteness = "off"
	LivePolite    LivePoliteness = "polite"
	LiveAssertive LivePoliteness = "assertive"
)

type Shortcut struct {
	Key         string   `json:"key"`
	Modifiers   []string `json:"modifiers,omitempty"`
	Description string   `json:"description,omitempty"`
}

type Node struct {
	Role             Role           `json:"role,omitempty"`
	Name             string         `json:"name,omitempty"`
	Description      string         `json:"description,omitempty"`
	LabelledBy       []string       `json:"labelledBy,omitempty"`
	DescribedBy      []string       `json:"describedBy,omitempty"`
	Hidden           bool           `json:"hidden,omitempty"`
	Disabled         bool           `json:"disabled,omitempty"`
	ReadOnly         bool           `json:"readOnly,omitempty"`
	Required         bool           `json:"required,omitempty"`
	Invalid          string         `json:"invalid,omitempty"`
	Expanded         *bool          `json:"expanded,omitempty"`
	Selected         *bool          `json:"selected,omitempty"`
	Checked          *bool          `json:"checked,omitempty"`
	Current          string         `json:"current,omitempty"`
	Level            int            `json:"level,omitempty"`
	PositionInSet    int            `json:"positionInSet,omitempty"`
	SetSize          int            `json:"setSize,omitempty"`
	Live             LivePoliteness `json:"live,omitempty"`
	Atomic           bool           `json:"atomic,omitempty"`
	Relevant         []string       `json:"relevant,omitempty"`
	FocusOrder       int            `json:"focusOrder,omitempty"`
	KeyboardShortcut []Shortcut     `json:"keyboardShortcut,omitempty"`
}
