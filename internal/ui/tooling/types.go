package tooling

import (
	"encoding/json"
	"time"

	"github.com/nbaertsch/afterburner/internal/ui/protocol"
)

const SchemaVersion = 1

var DeterministicTime = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

type Status string

const (
	StatusPass Status = "pass"
	StatusFail Status = "fail"
	StatusWarn Status = "warn"
	StatusInfo Status = "info"
)

type Check struct {
	ID       string          `json:"id"`
	Category string          `json:"category"`
	Status   Status          `json:"status"`
	Message  string          `json:"message"`
	Details  json.RawMessage `json:"details,omitempty"`
}

type Report struct {
	SchemaVersion int               `json:"schemaVersion"`
	Protocol      protocol.Revision `json:"protocol"`
	GeneratedAt   time.Time         `json:"generatedAt"`
	ExtensionID   string            `json:"extensionId,omitempty"`
	SurfaceID     string            `json:"surfaceId,omitempty"`
	Fixture       string            `json:"fixture,omitempty"`
	Summary       Summary           `json:"summary"`
	Checks        []Check           `json:"checks"`
	Artifacts     []Artifact        `json:"artifacts,omitempty"`
}

type Summary struct {
	Status Status `json:"status"`
	Pass   int    `json:"pass"`
	Fail   int    `json:"fail"`
	Warn   int    `json:"warn"`
	Info   int    `json:"info"`
}

type Artifact struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Path        string `json:"path,omitempty"`
	Description string `json:"description,omitempty"`
}

func NewReport(extensionID, surfaceID string) Report {
	return Report{SchemaVersion: SchemaVersion, Protocol: protocol.CurrentRevision(), GeneratedAt: DeterministicTime, ExtensionID: extensionID, SurfaceID: surfaceID}
}

func (r *Report) Add(id, category string, status Status, message string, details any) {
	var raw json.RawMessage
	if details != nil {
		data, err := json.Marshal(details)
		if err == nil {
			raw = data
		}
	}
	r.Checks = append(r.Checks, Check{ID: id, Category: category, Status: status, Message: message, Details: raw})
}

func (r *Report) Finalize() Report {
	var summary Summary
	for _, check := range r.Checks {
		switch check.Status {
		case StatusPass:
			summary.Pass++
		case StatusFail:
			summary.Fail++
		case StatusWarn:
			summary.Warn++
		default:
			summary.Info++
		}
	}
	if summary.Fail > 0 {
		summary.Status = StatusFail
	} else if summary.Warn > 0 {
		summary.Status = StatusWarn
	} else {
		summary.Status = StatusPass
	}
	r.Summary = summary
	return *r
}
