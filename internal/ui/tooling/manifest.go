package tooling

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/nbaertsch/afterburner/internal/registry"
	"github.com/nbaertsch/afterburner/internal/ui/capability"
	"github.com/nbaertsch/afterburner/internal/ui/component"
	"github.com/nbaertsch/afterburner/internal/ui/observability"
	"github.com/nbaertsch/afterburner/internal/ui/protocol"
	"github.com/nbaertsch/afterburner/internal/ui/render"
	"github.com/nbaertsch/afterburner/internal/ui/surface"
)

var manifestIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
var uiStableIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._:-]{0,127}$`)

type UIDeclaration struct {
	Protocol      string                 `json:"protocol"`
	Revision      int                    `json:"revision"`
	Surfaces      []UISurface            `json:"surfaces,omitempty"`
	Components    []string               `json:"components,omitempty"`
	Capabilities  []string               `json:"capabilities,omitempty"`
	GrantPolicy   json.RawMessage        `json:"grantPolicy,omitempty"`
	Observability *UIObservability       `json:"observability,omitempty"`
	Extra         map[string]interface{} `json:"-"`
}

type UISurface struct {
	ID                   string   `json:"id"`
	Kind                 string   `json:"kind"`
	Title                string   `json:"title,omitempty"`
	RequiredCapabilities []string `json:"requiredCapabilities,omitempty"`
}

type UIObservability struct {
	EventSinks []observability.EventSinkDescriptor `json:"eventSinks,omitempty"`
}

type ManifestValidation struct {
	Path     string            `json:"path"`
	Valid    bool              `json:"valid"`
	Manifest registry.Manifest `json:"manifest,omitempty"`
	UI       *UIDeclaration    `json:"ui,omitempty"`
	Errors   []string          `json:"errors,omitempty"`
	Warnings []string          `json:"warnings,omitempty"`
}

func ValidateManifest(path string) ManifestValidation {
	result := ManifestValidation{Path: path}
	data, err := os.ReadFile(path)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("read manifest: %v", err))
		return result
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("parse manifest: %v", err))
		return result
	}
	for key := range top {
		switch key {
		case "$schema", "schemaVersion", "id", "name", "displayName", "description", "visibility", "requires", "runtime", "capabilities", "sessionExtension", "ui":
		default:
			result.Errors = append(result.Errors, fmt.Sprintf("unsupported manifest property %q", key))
		}
	}
	var manifest registry.Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("parse manifest: %v", err))
		return result
	}
	registry.NormalizeManifest(&manifest)
	result.Manifest = manifest
	if manifest.SchemaVersion != 1 {
		result.Errors = append(result.Errors, "schemaVersion must be 1")
	}
	if !manifestIDPattern.MatchString(manifest.ID) {
		result.Errors = append(result.Errors, "id must match ^[a-z0-9][a-z0-9-]{0,63}$")
	}
	if strings.TrimSpace(manifest.DisplayName) == "" {
		result.Errors = append(result.Errors, "displayName or legacy name is required")
	}
	if !registry.ValidVisibility(manifest.Visibility) {
		result.Errors = append(result.Errors, "visibility must be builtin, public, or private")
	}
	if strings.TrimSpace(manifest.Requires.Afterburner) == "" {
		result.Errors = append(result.Errors, "requires.afterburner is required")
	}
	if manifest.Runtime.Execution != "in-process" {
		result.Errors = append(result.Errors, "runtime.execution must be in-process")
	}
	if strings.TrimSpace(manifest.Runtime.Entrypoint) == "" {
		result.Errors = append(result.Errors, "runtime.entrypoint is required")
	} else if err := validateRelativeEntrypoint(filepath.Dir(path), manifest.Runtime.Entrypoint); err != nil {
		result.Errors = append(result.Errors, err.Error())
	}
	if manifest.SessionExtension != nil && strings.TrimSpace(manifest.SessionExtension.Entrypoint) != "" {
		if err := validateRelativeEntrypoint(filepath.Dir(path), manifest.SessionExtension.Entrypoint); err != nil {
			result.Errors = append(result.Errors, "sessionExtension."+err.Error())
		}
	}
	for _, capID := range manifest.Capabilities {
		if !manifestIDPattern.MatchString(capID) {
			result.Errors = append(result.Errors, fmt.Sprintf("capability %q must be an extension capability id", capID))
		}
	}
	if len(manifest.UI) > 0 {
		ui, errs, warnings := validateUIDeclaration(manifest.UI)
		result.UI = ui
		result.Errors = append(result.Errors, errs...)
		result.Warnings = append(result.Warnings, warnings...)
	}
	result.Valid = len(result.Errors) == 0
	return result
}

func validateUIDeclaration(raw json.RawMessage) (*UIDeclaration, []string, []string) {
	var ui UIDeclaration
	var errs, warnings []string
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&ui); err != nil {
		return nil, []string{fmt.Sprintf("ui declaration parse error: %v", err)}, nil
	}
	if ui.Protocol != protocol.Protocol {
		errs = append(errs, "ui.protocol must be afterburner.ui")
	}
	if ui.Revision != protocol.ProtocolRevision {
		errs = append(errs, fmt.Sprintf("ui.revision must be %d", protocol.ProtocolRevision))
	}
	seenSurfaces := map[string]bool{}
	for _, s := range ui.Surfaces {
		if strings.TrimSpace(s.ID) == "" {
			errs = append(errs, "ui.surfaces[].id is required")
		} else if !uiStableIDPattern.MatchString(s.ID) {
			errs = append(errs, fmt.Sprintf("ui surface id %q must be a stable lowercase id", s.ID))
		}
		if seenSurfaces[s.ID] {
			errs = append(errs, fmt.Sprintf("duplicate ui surface %q", s.ID))
		}
		seenSurfaces[s.ID] = true
		if !validSurfaceKind(s.Kind) {
			errs = append(errs, fmt.Sprintf("unsupported ui surface kind %q%s", s.Kind, suggestionSuffix(s.Kind, surfaceKindCandidates())))
		}
		for _, capID := range s.RequiredCapabilities {
			if !validUICapability(capID) {
				errs = append(errs, fmt.Sprintf("invalid surface capability %q%s", capID, suggestionSuffix(capID, uiCapabilityCandidates())))
			}
		}
	}
	supported := supportedKindSet()
	seenComponents := map[string]bool{}
	componentCandidates := SortedSupportedComponentKinds()
	for _, kind := range ui.Components {
		if seenComponents[kind] {
			errs = append(errs, fmt.Sprintf("duplicate component kind %q", kind))
		}
		seenComponents[kind] = true
		if !supported[kind] {
			errs = append(errs, fmt.Sprintf("unsupported component kind %q%s", kind, suggestionSuffix(kind, componentCandidates)))
		}
	}
	capabilityCandidates := uiCapabilityCandidates()
	for _, capID := range ui.Capabilities {
		if !validUICapability(capID) {
			errs = append(errs, fmt.Sprintf("invalid ui capability %q%s", capID, suggestionSuffix(capID, capabilityCandidates)))
		}
	}
	if len(ui.GrantPolicy) > 0 {
		var policy capability.GrantPolicy
		if err := json.Unmarshal(ui.GrantPolicy, &policy); err != nil {
			errs = append(errs, fmt.Sprintf("ui.grantPolicy is invalid JSON: %v", err))
		} else if policy.SchemaVersion != protocol.SchemaVersion || policy.Protocol != protocol.Protocol || policy.Revision != protocol.ProtocolRevision {
			errs = append(errs, "ui.grantPolicy must use current afterburner.ui protocol")
		} else {
			capabilityCandidates := uiCapabilityCandidates()
			for _, grant := range policy.Grants {
				for _, capID := range grant.Capabilities {
					if !validUICapability(string(capID)) {
						errs = append(errs, fmt.Sprintf("invalid ui grant capability %q%s", capID, suggestionSuffix(string(capID), capabilityCandidates)))
					}
				}
			}
		}
	}
	return &ui, errs, warnings
}

func validateRelativeEntrypoint(root, entrypoint string) error {
	if filepath.IsAbs(entrypoint) || strings.Contains(entrypoint, "..") {
		return fmt.Errorf("runtime.entrypoint must stay inside the extension package")
	}
	path := filepath.Join(root, filepath.FromSlash(entrypoint))
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("runtime.entrypoint %q is not readable: %v", entrypoint, err)
	}
	return nil
}

func validSurfaceKind(kind string) bool {
	for _, entry := range surface.PublicCatalog() {
		if string(entry.Kind) == kind {
			return true
		}
	}
	return false
}

func surfaceKindCandidates() []string {
	entries := surface.PublicCatalog()
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		out = append(out, string(entry.Kind))
	}
	return out
}

func validUICapability(value string) bool {
	for _, descriptor := range capability.CoreDescriptors() {
		if string(descriptor.ID) == value {
			return true
		}
	}
	return false
}

func uiCapabilityCandidates() []string {
	descriptors := capability.CoreDescriptors()
	out := make([]string, 0, len(descriptors))
	for _, descriptor := range descriptors {
		out = append(out, string(descriptor.ID))
	}
	return out
}

func suggestionSuffix(value string, candidates []string) string {
	candidate, ok := closestCatalogValue(value, candidates)
	if !ok {
		return ""
	}
	return fmt.Sprintf("; did you mean %q?", candidate)
}

func closestCatalogValue(value string, candidates []string) (string, bool) {
	if value == "" || len(candidates) == 0 {
		return "", false
	}
	best := ""
	bestDistance := len(value) + 1
	for _, candidate := range candidates {
		distance := levenshteinDistance(value, candidate)
		if distance < bestDistance || (distance == bestDistance && candidate < best) {
			best = candidate
			bestDistance = distance
		}
	}
	limit := len(value) / 3
	if limit < 2 {
		limit = 2
	}
	if bestDistance > limit {
		return "", false
	}
	return best, true
}

func levenshteinDistance(a, b string) int {
	previous := make([]int, len(b)+1)
	current := make([]int, len(b)+1)
	for j := range previous {
		previous[j] = j
	}
	for i := 1; i <= len(a); i++ {
		current[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 0
			if a[i-1] != b[j-1] {
				cost = 1
			}
			current[j] = min(previous[j]+1, current[j-1]+1, previous[j-1]+cost)
		}
		previous, current = current, previous
	}
	return previous[len(b)]
}

func supportedKindSet() map[string]bool {
	set := map[string]bool{}
	for _, kind := range render.SupportedComponentKinds() {
		set[string(kind)] = true
	}
	for _, entry := range component.PublicCatalog() {
		set[string(entry.Kind)] = true
	}
	return set
}

func SortedSupportedComponentKinds() []string {
	set := supportedKindSet()
	out := make([]string, 0, len(set))
	for kind := range set {
		out = append(out, kind)
	}
	sort.Strings(out)
	return out
}
