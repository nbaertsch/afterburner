package capability

import "time"

type ID string

const (
	RenderComponents      ID = "ui.render.components"
	RenderTerminal        ID = "ui.render.terminal"
	SurfaceTerminal       ID = "ui.surface.terminal"
	SurfaceModal          ID = "ui.surface.modal"
	SurfacePanel          ID = "ui.surface.panel"
	SurfaceInline         ID = "ui.surface.inline"
	SurfaceStatusLine     ID = "ui.surface.statusLine"
	SurfaceCommandPalette ID = "ui.surface.commandPalette"
	SurfaceOverlay        ID = "ui.surface.overlay"
	ActionInvoke          ID = "ui.action.invoke"
	DataRead              ID = "ui.data.read"
	DataWrite             ID = "ui.data.write"
	StreamRead            ID = "ui.stream.read"
	StreamWrite           ID = "ui.stream.write"
	ThemeRead             ID = "ui.theme.read"
	ThemeWrite            ID = "ui.theme.write"
	LocalizationRead      ID = "ui.localization.read"
	AccessibilityInspect  ID = "ui.accessibility.inspect"
	PolicyEvaluate        ID = "ui.policy.evaluate"
	AuditWrite            ID = "ui.audit.write"
	ObservabilitySink     ID = "ui.observability.sink"
	BlackBoxEventSink     ID = "ui.observability.black-box.sink"
)

type Stability string

const (
	StabilityStable       Stability = "stable"
	StabilityExperimental Stability = "experimental"
	StabilityDeprecated   Stability = "deprecated"
)

type Scope string

const (
	ScopeHost      Scope = "host"
	ScopeExtension Scope = "extension"
	ScopeSession   Scope = "session"
	ScopeSurface   Scope = "surface"
	ScopeUser      Scope = "user"
)

type Descriptor struct {
	ID             ID                `json:"id"`
	Version        string            `json:"version"`
	Stability      Stability         `json:"stability"`
	Description    string            `json:"description,omitempty"`
	Scopes         []Scope           `json:"scopes,omitempty"`
	RequiredGrants []GrantDescriptor `json:"requiredGrants,omitempty"`
	Quotas         []QuotaDescriptor `json:"quotas,omitempty"`
	SLOs           []SLODescriptor   `json:"slos,omitempty"`
	Metadata       map[string]string `json:"metadata,omitempty"`
}

type GrantDescriptor struct {
	ID          string   `json:"id"`
	Capability  ID       `json:"capability"`
	Scope       Scope    `json:"scope"`
	Resources   []string `json:"resources,omitempty"`
	Required    bool     `json:"required"`
	Description string   `json:"description,omitempty"`
}

type QuotaDescriptor struct {
	ID          string        `json:"id"`
	Scope       Scope         `json:"scope"`
	Limit       int64         `json:"limit"`
	Window      time.Duration `json:"window"`
	Burst       int64         `json:"burst,omitempty"`
	Unit        string        `json:"unit"`
	Description string        `json:"description,omitempty"`
}

type SLODescriptor struct {
	ID          string `json:"id"`
	Target      string `json:"target"`
	Window      string `json:"window"`
	Description string `json:"description,omitempty"`
}

type PolicyEffect string

const (
	PolicyAllow PolicyEffect = "allow"
	PolicyDeny  PolicyEffect = "deny"
	PolicyAudit PolicyEffect = "audit"
)

type GrantPolicy struct {
	SchemaVersion int         `json:"schemaVersion"`
	Protocol      string      `json:"protocol"`
	Revision      int         `json:"revision"`
	ExtensionID   string      `json:"extensionId"`
	DenyByDefault bool        `json:"denyByDefault"`
	Grants        []GrantRule `json:"grants"`
	Audit         []AuditRule `json:"audit,omitempty"`
	Quotas        []QuotaRule `json:"quotas,omitempty"`
}

type GrantRule struct {
	ID           string       `json:"id"`
	Effect       PolicyEffect `json:"effect"`
	Capabilities []ID         `json:"capabilities"`
	Resources    []string     `json:"resources,omitempty"`
	Conditions   []Condition  `json:"conditions,omitempty"`
}

type AuditRule struct {
	ID        string   `json:"id"`
	Events    []string `json:"events"`
	SinkIDs   []string `json:"sinkIds,omitempty"`
	Redaction string   `json:"redaction,omitempty"`
}

type QuotaRule struct {
	ID         string `json:"id"`
	QuotaID    string `json:"quotaId"`
	HardFail   bool   `json:"hardFail"`
	AuditEvent string `json:"auditEvent,omitempty"`
}

type Condition struct {
	Type     string            `json:"type"`
	Operator string            `json:"operator"`
	Values   map[string]string `json:"values,omitempty"`
}

func CoreDescriptors() []Descriptor {
	return []Descriptor{
		{ID: RenderComponents, Version: "1", Stability: StabilityStable, Description: "Render versioned component trees and patches.", Scopes: []Scope{ScopeHost, ScopeExtension, ScopeSurface}},
		{ID: RenderTerminal, Version: "1", Stability: StabilityStable, Description: "Render terminal-backed component surfaces.", Scopes: []Scope{ScopeHost, ScopeSurface}},
		{ID: SurfaceTerminal, Version: "1", Stability: StabilityStable, Description: "Create and manage terminal surfaces.", Scopes: []Scope{ScopeHost, ScopeSession}},
		{ID: SurfaceModal, Version: "1", Stability: StabilityStable, Description: "Create and manage modal surfaces.", Scopes: []Scope{ScopeHost, ScopeExtension}},
		{ID: SurfacePanel, Version: "1", Stability: StabilityStable, Description: "Create and manage persistent panel surfaces.", Scopes: []Scope{ScopeHost, ScopeExtension}},
		{ID: SurfaceInline, Version: "1", Stability: StabilityStable, Description: "Create and manage inline embedded surfaces.", Scopes: []Scope{ScopeHost, ScopeExtension}},
		{ID: SurfaceStatusLine, Version: "1", Stability: StabilityStable, Description: "Create and manage compact status-line surfaces.", Scopes: []Scope{ScopeHost, ScopeExtension}},
		{ID: SurfaceCommandPalette, Version: "1", Stability: StabilityStable, Description: "Create and manage command-palette surfaces.", Scopes: []Scope{ScopeHost, ScopeExtension}},
		{ID: SurfaceOverlay, Version: "1", Stability: StabilityStable, Description: "Create and manage overlay surfaces.", Scopes: []Scope{ScopeHost, ScopeExtension}},
		{ID: ActionInvoke, Version: "1", Stability: StabilityStable, Description: "Invoke declared UI actions.", Scopes: []Scope{ScopeExtension, ScopeSurface}},
		{ID: DataRead, Version: "1", Stability: StabilityStable, Description: "Read UI data sources.", Scopes: []Scope{ScopeExtension, ScopeSession}},
		{ID: DataWrite, Version: "1", Stability: StabilityStable, Description: "Mutate UI data sources.", Scopes: []Scope{ScopeExtension, ScopeSession}},
		{ID: StreamRead, Version: "1", Stability: StabilityStable, Description: "Read UI stream frames.", Scopes: []Scope{ScopeExtension, ScopeSession}},
		{ID: StreamWrite, Version: "1", Stability: StabilityStable, Description: "Write UI stream frames.", Scopes: []Scope{ScopeExtension, ScopeSession}},
		{ID: ThemeRead, Version: "1", Stability: StabilityStable, Description: "Read semantic theme tokens.", Scopes: []Scope{ScopeExtension, ScopeUser}},
		{ID: ThemeWrite, Version: "1", Stability: StabilityStable, Description: "Provide semantic theme tokens.", Scopes: []Scope{ScopeExtension, ScopeUser}},
		{ID: LocalizationRead, Version: "1", Stability: StabilityStable, Description: "Read localized message bundles.", Scopes: []Scope{ScopeExtension, ScopeUser}},
		{ID: AccessibilityInspect, Version: "1", Stability: StabilityStable, Description: "Inspect accessibility metadata.", Scopes: []Scope{ScopeHost, ScopeExtension}},
		{ID: PolicyEvaluate, Version: "1", Stability: StabilityStable, Description: "Evaluate UI grant policy decisions.", Scopes: []Scope{ScopeHost}},
		{ID: AuditWrite, Version: "1", Stability: StabilityStable, Description: "Write policy and lifecycle audit records.", Scopes: []Scope{ScopeHost, ScopeExtension}},
		{ID: ObservabilitySink, Version: "1", Stability: StabilityStable, Description: "Receive optional UI observability events.", Scopes: []Scope{ScopeExtension, ScopeSession}},
		{ID: BlackBoxEventSink, Version: "1", Stability: StabilityStable, Description: "Receive optional Black Box UI observability events.", Scopes: []Scope{ScopeExtension, ScopeSession}},
	}
}
