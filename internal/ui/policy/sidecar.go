package policy

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/nbaertsch/afterburner/internal/ui/capability"
)

type FilesystemMode string

type NetworkMode string

const (
	FilesystemInherit FilesystemMode = "inherit"
	FilesystemDeny    FilesystemMode = "deny"
	FilesystemRead    FilesystemMode = "read"
	FilesystemWrite   FilesystemMode = "write"
	NetworkInherit    NetworkMode    = "inherit"
	NetworkDeny       NetworkMode    = "deny"
	NetworkLoopback   NetworkMode    = "loopback"
	NetworkAllowlist  NetworkMode    = "allowlist"
)

type RestrictedEnvironmentPolicy struct {
	ClearInherited bool              `json:"clearInherited"`
	Allow          map[string]string `json:"allow,omitempty"`
	DenyNames      []string          `json:"denyNames,omitempty"`
}

type FilesystemPolicy struct {
	Mode       FilesystemMode `json:"mode"`
	ReadRoots  []string       `json:"readRoots,omitempty"`
	WriteRoots []string       `json:"writeRoots,omitempty"`
	DenyGlobs  []string       `json:"denyGlobs,omitempty"`
}

type NetworkPolicy struct {
	Mode         NetworkMode `json:"mode"`
	AllowedHosts []string    `json:"allowedHosts,omitempty"`
	DeniedPorts  []int       `json:"deniedPorts,omitempty"`
}

type ResourcePolicy struct {
	MaxProcesses     uint32                      `json:"maxProcesses,omitempty"`
	MaxProcessMemory uint64                      `json:"maxProcessMemoryBytes,omitempty"`
	MaxJobMemory     uint64                      `json:"maxJobMemoryBytes,omitempty"`
	MaxCPUTime       time.Duration               `json:"-"`
	MaxCPUTimeMillis int64                       `json:"maxCpuTimeMillis,omitempty"`
	KillOnClose      bool                        `json:"killOnClose"`
	Environment      RestrictedEnvironmentPolicy `json:"environment"`
	Filesystem       FilesystemPolicy            `json:"filesystem"`
	Network          NetworkPolicy               `json:"network"`
}

type IsolationCapabilities struct {
	Platform               string `json:"platform"`
	RestrictedEnvironment  bool   `json:"restrictedEnvironment"`
	ProcessTreeContainment bool   `json:"processTreeContainment"`
	ResourceLimits         bool   `json:"resourceLimits"`
	FilesystemBoundary     bool   `json:"filesystemBoundary"`
	NetworkBoundary        bool   `json:"networkBoundary"`
}

type EnforcementRequest struct {
	OpaqueRequestID string         `json:"opaqueRequestId"`
	ExtensionID     string         `json:"extensionId"`
	Capability      capability.ID  `json:"capability"`
	Resource        string         `json:"resource,omitempty"`
	Policy          ResourcePolicy `json:"policy"`
}

type EnforcementDecision struct {
	Allowed         bool              `json:"allowed"`
	Reason          string            `json:"reason,omitempty"`
	OpaqueRequestID string            `json:"opaqueRequestId"`
	Applied         map[string]string `json:"applied,omitempty"`
}

type SidecarEnforcer interface {
	Enforce(ctx context.Context, request EnforcementRequest) (EnforcementDecision, error)
}

type LocalDeclarationEnforcer struct{}

func localDeclarationEnforcerCapabilities() IsolationCapabilities {
	capabilities := PlatformIsolationCapabilities()
	capabilities.ProcessTreeContainment = false
	capabilities.ResourceLimits = false
	return capabilities
}

func (LocalDeclarationEnforcer) Enforce(ctx context.Context, request EnforcementRequest) (EnforcementDecision, error) {
	if err := ctx.Err(); err != nil {
		return EnforcementDecision{}, err
	}
	if request.OpaqueRequestID == "" || request.ExtensionID == "" || request.Capability == "" {
		return EnforcementDecision{Allowed: false, OpaqueRequestID: request.OpaqueRequestID, Reason: "missing enforcement identity"}, nil
	}
	capabilities := localDeclarationEnforcerCapabilities()
	if err := request.Policy.Validate(); err != nil {
		return EnforcementDecision{Allowed: false, OpaqueRequestID: request.OpaqueRequestID, Reason: err.Error(), Applied: capabilities.Map()}, nil
	}
	missing := request.Policy.MissingIsolationCapabilities(capabilities)
	if len(missing) > 0 {
		return EnforcementDecision{Allowed: false, OpaqueRequestID: request.OpaqueRequestID, Reason: "isolation capability unavailable: " + strings.Join(missing, ", "), Applied: capabilities.Map()}, nil
	}
	applied := capabilities.Map()
	applied["filesystem"] = string(request.Policy.Filesystem.Mode)
	applied["network"] = string(request.Policy.Network.Mode)
	return EnforcementDecision{Allowed: true, OpaqueRequestID: request.OpaqueRequestID, Reason: "enforced", Applied: applied}, nil
}

func (p *ResourcePolicy) NormalizeDurations() {
	if p.MaxCPUTime == 0 && p.MaxCPUTimeMillis > 0 {
		p.MaxCPUTime = time.Duration(p.MaxCPUTimeMillis) * time.Millisecond
	}
	if p.MaxCPUTime != 0 {
		p.MaxCPUTimeMillis = p.MaxCPUTime.Milliseconds()
	}
}

func (p ResourcePolicy) Validate() error {
	if !validFilesystemMode(p.Filesystem.Mode) {
		return fmt.Errorf("valid filesystem policy mode is required")
	}
	if !validNetworkMode(p.Network.Mode) {
		return fmt.Errorf("valid network policy mode is required")
	}
	return nil
}

func (p ResourcePolicy) MissingIsolationCapabilities(capabilities IsolationCapabilities) []string {
	p.NormalizeDurations()
	var missing []string
	if p.Environment.ClearInherited && !capabilities.RestrictedEnvironment {
		missing = append(missing, "environment.restricted")
	}
	if (p.KillOnClose || p.MaxProcesses > 0) && !capabilities.ProcessTreeContainment {
		missing = append(missing, "process.jobObject")
	}
	if (p.MaxProcessMemory > 0 || p.MaxJobMemory > 0 || p.MaxCPUTime > 0) && !capabilities.ResourceLimits {
		missing = append(missing, "process.resourceLimits")
	}
	if p.Filesystem.Mode != FilesystemInherit && !capabilities.FilesystemBoundary {
		missing = append(missing, "filesystem."+string(p.Filesystem.Mode))
	}
	if p.Network.Mode != NetworkInherit && !capabilities.NetworkBoundary {
		missing = append(missing, "network."+string(p.Network.Mode))
	}
	return missing
}

func (c IsolationCapabilities) Map() map[string]string {
	return map[string]string{
		"platform":               c.Platform,
		"restrictedEnvironment":  fmt.Sprint(c.RestrictedEnvironment),
		"processTreeContainment": fmt.Sprint(c.ProcessTreeContainment),
		"resourceLimits":         fmt.Sprint(c.ResourceLimits),
		"filesystemBoundary":     fmt.Sprint(c.FilesystemBoundary),
		"networkBoundary":        fmt.Sprint(c.NetworkBoundary),
	}
}

func validFilesystemMode(mode FilesystemMode) bool {
	switch mode {
	case FilesystemInherit, FilesystemDeny, FilesystemRead, FilesystemWrite:
		return true
	default:
		return false
	}
}

func validNetworkMode(mode NetworkMode) bool {
	switch mode {
	case NetworkInherit, NetworkDeny, NetworkLoopback, NetworkAllowlist:
		return true
	default:
		return false
	}
}
