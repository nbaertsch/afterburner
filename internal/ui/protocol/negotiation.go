package protocol

import (
	"fmt"
	"sort"
	"time"
)

type Capability struct {
	ID       string            `json:"id"`
	Version  string            `json:"version,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

type Quota struct {
	ID           string `json:"id"`
	Limit        int64  `json:"limit"`
	WindowMillis int64  `json:"windowMillis,omitempty"`
	Burst        int64  `json:"burst,omitempty"`
	Unit         string `json:"unit,omitempty"`
}

type EndpointMetadata struct {
	Name         string            `json:"name,omitempty"`
	Version      string            `json:"version,omitempty"`
	Locale       string            `json:"locale,omitempty"`
	Theme        string            `json:"theme,omitempty"`
	Capabilities []Capability      `json:"capabilities,omitempty"`
	Quotas       []Quota           `json:"quotas,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty"`
}

type Hello struct {
	SchemaVersion      int              `json:"schemaVersion"`
	Protocol           string           `json:"protocol"`
	Compatibility      []string         `json:"compatibility,omitempty"`
	MinimumRevision    int              `json:"minimumRevision"`
	PreferredRevision  int              `json:"preferredRevision"`
	SupportedRevisions []int            `json:"supportedRevisions"`
	RejectDowngrade    bool             `json:"rejectDowngrade,omitempty"`
	SessionID          string           `json:"sessionId,omitempty"`
	ExtensionID        string           `json:"extensionId,omitempty"`
	Epoch              uint64           `json:"epoch,omitempty"`
	Generation         uint64           `json:"generation,omitempty"`
	MaxFrameBytes      uint32           `json:"maxFrameBytes,omitempty"`
	MaxJSONDepth       int              `json:"maxJsonDepth,omitempty"`
	Endpoint           EndpointMetadata `json:"endpoint,omitempty"`
	SentAt             time.Time        `json:"sentAt"`
}

type HelloResult struct {
	SchemaVersion      int              `json:"schemaVersion"`
	Protocol           string           `json:"protocol"`
	Compatibility      []string         `json:"compatibility,omitempty"`
	Accepted           bool             `json:"accepted"`
	Revision           int              `json:"revision,omitempty"`
	MinimumRevision    int              `json:"minimumRevision,omitempty"`
	PreferredRevision  int              `json:"preferredRevision,omitempty"`
	SupportedRevisions []int            `json:"supportedRevisions,omitempty"`
	SessionID          string           `json:"sessionId,omitempty"`
	ExtensionID        string           `json:"extensionId,omitempty"`
	Epoch              uint64           `json:"epoch,omitempty"`
	Generation         uint64           `json:"generation,omitempty"`
	MaxFrameBytes      uint32           `json:"maxFrameBytes,omitempty"`
	MaxJSONDepth       int              `json:"maxJsonDepth,omitempty"`
	Endpoint           EndpointMetadata `json:"endpoint,omitempty"`
	Error              *StructuredError `json:"error,omitempty"`
}

func DefaultHello(endpoint EndpointMetadata) Hello {
	return Hello{
		SchemaVersion:      SchemaVersion,
		Protocol:           Protocol,
		Compatibility:      []string{CompatibilityRevision},
		MinimumRevision:    MinimumHostRevision,
		PreferredRevision:  ProtocolRevision,
		SupportedRevisions: []int{ProtocolRevision},
		MaxFrameBytes:      DefaultMaxFrameBytes,
		MaxJSONDepth:       DefaultMaxJSONDepth,
		Endpoint:           endpoint,
		SentAt:             time.Now().UTC(),
	}
}

func NegotiateHello(local, remote Hello) (HelloResult, error) {
	result := HelloResult{
		SchemaVersion:      SchemaVersion,
		Protocol:           Protocol,
		Compatibility:      []string{CompatibilityRevision},
		MinimumRevision:    local.MinimumRevision,
		PreferredRevision:  local.PreferredRevision,
		SupportedRevisions: append([]int(nil), local.SupportedRevisions...),
		SessionID:          remote.SessionID,
		ExtensionID:        remote.ExtensionID,
		Epoch:              remote.Epoch,
		Generation:         remote.Generation,
		MaxFrameBytes:      minNonZero(local.MaxFrameBytes, remote.MaxFrameBytes),
		MaxJSONDepth:       minNonZeroInt(local.MaxJSONDepth, remote.MaxJSONDepth),
		Endpoint:           local.Endpoint,
	}
	if local.Protocol != Protocol || remote.Protocol != Protocol {
		return reject(result, ErrorUnsupportedRevision, "unsupported protocol", false)
	}
	selected := highestCommonRevision(local, remote)
	if selected == 0 {
		return reject(result, ErrorUnsupportedRevision, "no compatible protocol revision", false)
	}
	if (local.RejectDowngrade && selected < local.PreferredRevision) || (remote.RejectDowngrade && selected < remote.PreferredRevision) {
		return reject(result, ErrorDowngradeRejected, "protocol downgrade rejected", false)
	}
	result.Accepted = true
	result.Revision = selected
	return result, nil
}

func highestCommonRevision(local, remote Hello) int {
	allowedLocal := supportedAtOrAbove(local.SupportedRevisions, maxInt(local.MinimumRevision, remote.MinimumRevision))
	allowedRemote := supportedAtOrAbove(remote.SupportedRevisions, maxInt(local.MinimumRevision, remote.MinimumRevision))
	if len(allowedLocal) == 0 || len(allowedRemote) == 0 {
		return 0
	}
	remoteSet := map[int]bool{}
	for _, revision := range allowedRemote {
		remoteSet[revision] = true
	}
	sort.Sort(sort.Reverse(sort.IntSlice(allowedLocal)))
	for _, revision := range allowedLocal {
		if remoteSet[revision] {
			return revision
		}
	}
	return 0
}

func supportedAtOrAbove(values []int, min int) []int {
	out := make([]int, 0, len(values))
	seen := map[int]bool{}
	for _, value := range values {
		if value >= min && !seen[value] {
			out = append(out, value)
			seen[value] = true
		}
	}
	return out
}

func reject(result HelloResult, code ErrorCode, message string, recoverable bool) (HelloResult, error) {
	result.Accepted = false
	result.Error = &StructuredError{Code: code, Message: message, Recoverable: recoverable}
	return result, *result.Error
}

func minNonZero(a, b uint32) uint32 {
	if a == 0 {
		return b
	}
	if b == 0 {
		return a
	}
	if a < b {
		return a
	}
	return b
}

func minNonZeroInt(a, b int) int {
	if a == 0 {
		return b
	}
	if b == 0 {
		return a
	}
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func (h Hello) Validate() error {
	if h.SchemaVersion != SchemaVersion || h.Protocol != Protocol {
		return StructuredError{Code: ErrorUnsupportedRevision, Message: "invalid hello protocol", Recoverable: false}
	}
	if h.MinimumRevision <= 0 || h.PreferredRevision <= 0 || len(h.SupportedRevisions) == 0 {
		return StructuredError{Code: ErrorUnsupportedRevision, Message: "hello revision set is empty", Recoverable: false}
	}
	if h.PreferredRevision < h.MinimumRevision {
		return StructuredError{Code: ErrorDowngradeRejected, Message: fmt.Sprintf("preferred revision %d is below minimum %d", h.PreferredRevision, h.MinimumRevision), Recoverable: false}
	}
	return nil
}
