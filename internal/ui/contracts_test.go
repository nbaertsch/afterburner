package ui_test

import (
	"encoding/json"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/nbaertsch/afterburner/internal/ui/audit"
	"github.com/nbaertsch/afterburner/internal/ui/bridge"
	"github.com/nbaertsch/afterburner/internal/ui/capability"
	"github.com/nbaertsch/afterburner/internal/ui/component"
	"github.com/nbaertsch/afterburner/internal/ui/diagnostics"
	uierrors "github.com/nbaertsch/afterburner/internal/ui/errors"
	"github.com/nbaertsch/afterburner/internal/ui/observability"
	"github.com/nbaertsch/afterburner/internal/ui/policy"
	"github.com/nbaertsch/afterburner/internal/ui/protocol"
	"github.com/nbaertsch/afterburner/internal/ui/style"
	"github.com/nbaertsch/afterburner/internal/ui/surface"
)

func TestProtocolConstants(t *testing.T) {
	revision := protocol.CurrentRevision()
	if revision.Protocol != protocol.Protocol || revision.Revision != protocol.ProtocolRevision || revision.MinimumHost != protocol.MinimumHostRevision {
		t.Fatalf("unexpected revision: %#v", revision)
	}
	if revision.CompatibilityID != protocol.CompatibilityRevision {
		t.Fatalf("compatibility ID = %q", revision.CompatibilityID)
	}
	if !contains(revision.SLOIDs, protocol.SLOFirstFrameLatencyP95) || !contains(revision.SLOIDs, protocol.SLOAccessibilityConformance) {
		t.Fatalf("missing required SLO IDs: %#v", revision.SLOIDs)
	}
}

func TestBlackBoxIsOptionalObservabilitySink(t *testing.T) {
	sink := observability.BlackBoxSinkDescriptor()
	if sink.ExtensionID != observability.BlackBoxExtensionID || sink.Requirement != observability.SinkOptional {
		t.Fatalf("black box sink is not optional: %#v", sink)
	}
	if sink.Capability != capability.BlackBoxEventSink || sink.FailureBehavior != "ignore-when-absent" {
		t.Fatalf("black box sink has incompatible behavior: %#v", sink)
	}
	if !sink.Redaction.MetadataOnly {
		t.Fatal("black box sink must be metadata-only")
	}
}

func TestPublicContractsGolden(t *testing.T) {
	actual := publicContractSnapshot(t)
	goldenPath := filepath.Join("testdata", "golden", "public-contracts.golden.json")
	expected, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatal(err)
	}
	expectedText := strings.ReplaceAll(string(expected), "\r\n", "\n")
	if expectedText != actual {
		t.Fatalf("public UI contract golden mismatch\n--- expected\n%s\n--- actual\n%s", expectedText, actual)
	}
}

func TestSchemaFixturesValidateAndUnmarshal(t *testing.T) {
	cases := []struct {
		schema  string
		fixture string
		target  any
	}{
		{"ui-envelope-v1.schema.json", "ui-envelope-v1.valid.json", &protocol.Envelope{}},
		{"ui-component-v1.schema.json", "ui-component-v1.valid.json", &component.Tree{}},
		{"ui-event-v1.schema.json", "ui-event-v1.valid.json", &bridge.Event{}},
		{"ui-patch-v1.schema.json", "ui-patch-v1.valid.json", &bridge.Patch{}},
		{"ui-grant-policy-v1.schema.json", "ui-grant-policy-v1.valid.json", &capability.GrantPolicy{}},
		{"ui-grant-record-v1.schema.json", "ui-grant-record-v1.valid.json", &policy.GrantEvent{}},
		{"ui-audit-event-v1.schema.json", "ui-audit-event-v1.valid.json", &audit.Event{}},
		{"ui-diagnostics-bundle-v1.schema.json", "ui-diagnostics-bundle-v1.valid.json", &diagnostics.BundleManifest{}},
		{"ui-enterprise-policy-v1.schema.json", "ui-enterprise-policy-v1.valid.json", &policy.EnterprisePolicy{}},
		{"extension-ui-v1.schema.json", "extension-ui-v1.valid.json", &map[string]any{}},
	}
	for _, tc := range cases {
		t.Run(tc.schema, func(t *testing.T) {
			schema := readJSON(t, filepath.Join("..", "..", "schemas", tc.schema))
			fixtureBytes, err := os.ReadFile(filepath.Join("testdata", "fixtures", tc.fixture))
			if err != nil {
				t.Fatal(err)
			}
			var document any
			if err := json.Unmarshal(fixtureBytes, &document); err != nil {
				t.Fatalf("fixture JSON is invalid: %v", err)
			}
			validator := schemaValidator{root: schema}
			if err := validator.validate(schema, document, "$."); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(fixtureBytes, tc.target); err != nil {
				t.Fatalf("fixture does not unmarshal into contract type: %v", err)
			}
		})
	}
}

func TestExtensionSchemaKeepsExistingManifestsCompatibleAndAddsUI(t *testing.T) {
	schema := readJSON(t, filepath.Join("..", "..", "schemas", "extension-v1.schema.json"))
	props := schema["properties"].(map[string]any)
	if _, ok := props["ui"]; !ok {
		t.Fatal("extension schema is missing optional ui declaration")
	}
	required := toStringSet(schema["required"].([]any))
	if required["ui"] {
		t.Fatal("ui must remain optional for existing manifests")
	}
	for _, manifestPath := range []string{
		filepath.Join("..", "..", "extensions", "BlackBox", "afterburner.json"),
		filepath.Join("..", "..", "extensions", "BYOModels", "afterburner.json"),
	} {
		manifest := readJSON(t, manifestPath)
		validator := schemaValidator{root: schema, allowExternalRefs: true}
		if err := validator.validate(schema, manifest, "$."+manifestPath); err != nil {
			t.Fatal(err)
		}
	}
}

func TestUIDependencyBoundaries(t *testing.T) {
	disallowed := []string{
		"github.com/nbaertsch/afterburner/internal/terminal",
		"github.com/nbaertsch/afterburner/internal/runtimepkg",
		"github.com/nbaertsch/afterburner/internal/extensions",
		"github.com/nbaertsch/afterburner/extensions",
	}
	root := "."
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imported := range file.Imports {
			pathValue := strings.Trim(imported.Path.Value, "\"")
			for _, denied := range disallowed {
				if pathValue == denied || strings.HasPrefix(pathValue, denied+"/") {
					return fmt.Errorf("%s imports forbidden runtime dependency %s", path, pathValue)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func publicContractSnapshot(t *testing.T) string {
	t.Helper()
	componentKinds := make([]string, 0, len(component.PublicCatalog()))
	for _, entry := range component.PublicCatalog() {
		componentKinds = append(componentKinds, string(entry.Kind))
	}
	capabilities := make([]string, 0, len(capability.CoreDescriptors()))
	for _, descriptor := range capability.CoreDescriptors() {
		capabilities = append(capabilities, string(descriptor.ID))
	}
	tokens := make([]string, 0, len(style.SemanticTokenIDs()))
	for _, token := range style.SemanticTokenIDs() {
		tokens = append(tokens, string(token))
	}
	errorCodes := make([]string, 0, len(uierrors.PublicCodes()))
	for _, code := range uierrors.PublicCodes() {
		errorCodes = append(errorCodes, string(code))
	}
	snapshot := struct {
		Protocol       protocol.Revision `json:"protocol"`
		ComponentKinds []string          `json:"componentKinds"`
		Capabilities   []string          `json:"capabilities"`
		Tokens         []string          `json:"tokens"`
		ErrorCodes     []string          `json:"errorCodes"`
		EnvelopeKinds  []string          `json:"envelopeKinds"`
		BlackBoxSink   struct {
			ID              string `json:"id"`
			ExtensionID     string `json:"extensionId"`
			Requirement     string `json:"requirement"`
			FailureBehavior string `json:"failureBehavior"`
		} `json:"blackBoxSink"`
	}{
		Protocol:       protocol.CurrentRevision(),
		ComponentKinds: componentKinds,
		Capabilities:   capabilities,
		Tokens:         tokens,
		ErrorCodes:     errorCodes,
	}
	for _, kind := range protocol.SupportedEnvelopeKinds() {
		snapshot.EnvelopeKinds = append(snapshot.EnvelopeKinds, string(kind))
	}
	sink := observability.BlackBoxSinkDescriptor()
	snapshot.BlackBoxSink.ID = sink.ID
	snapshot.BlackBoxSink.ExtensionID = sink.ExtensionID
	snapshot.BlackBoxSink.Requirement = string(sink.Requirement)
	snapshot.BlackBoxSink.FailureBehavior = sink.FailureBehavior
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return string(append(data, '\n'))
}

type schemaValidator struct {
	root              map[string]any
	allowExternalRefs bool
}

func (v schemaValidator) validate(schema any, document any, path string) error {
	if boolean, ok := schema.(bool); ok {
		if boolean {
			return nil
		}
		return fmt.Errorf("%s is disallowed by false schema", path)
	}
	schemaMap, ok := schema.(map[string]any)
	if !ok {
		return fmt.Errorf("%s schema is not an object", path)
	}
	if ref, ok := schemaMap["$ref"].(string); ok {
		resolved, err := v.resolve(ref)
		if err != nil {
			return err
		}
		return v.validate(resolved, document, path)
	}
	if constant, ok := schemaMap["const"]; ok && !reflect.DeepEqual(normalizeJSONNumber(document), normalizeJSONNumber(constant)) {
		return fmt.Errorf("%s = %#v, want const %#v", path, document, constant)
	}
	if enumValues, ok := schemaMap["enum"].([]any); ok {
		matched := false
		for _, enumValue := range enumValues {
			if reflect.DeepEqual(normalizeJSONNumber(document), normalizeJSONNumber(enumValue)) {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("%s = %#v, not in enum %#v", path, document, enumValues)
		}
	}
	if typeName, ok := schemaMap["type"].(string); ok {
		if err := validateType(typeName, document, path); err != nil {
			return err
		}
	}
	if properties, ok := schemaMap["properties"].(map[string]any); ok {
		object, ok := document.(map[string]any)
		if !ok {
			return fmt.Errorf("%s must be an object", path)
		}
		if required, ok := schemaMap["required"].([]any); ok {
			for _, name := range required {
				if _, exists := object[name.(string)]; !exists {
					return fmt.Errorf("%s missing required property %q", path, name)
				}
			}
		}
		if additional, ok := schemaMap["additionalProperties"].(bool); ok && !additional {
			for name := range object {
				if _, exists := properties[name]; !exists {
					return fmt.Errorf("%s has additional property %q", path, name)
				}
			}
		}
		for name, propertySchema := range properties {
			if value, exists := object[name]; exists {
				if err := v.validate(propertySchema, value, path+name+"."); err != nil {
					return err
				}
			}
		}
	}
	if items, ok := schemaMap["items"]; ok {
		array, ok := document.([]any)
		if !ok {
			return fmt.Errorf("%s must be an array", path)
		}
		if minItems, ok := schemaMap["minItems"].(float64); ok && len(array) < int(minItems) {
			return fmt.Errorf("%s has %d items, want at least %d", path, len(array), int(minItems))
		}
		for index, item := range array {
			if err := v.validate(items, item, fmt.Sprintf("%s[%d].", path, index)); err != nil {
				return err
			}
		}
	}
	if minLength, ok := schemaMap["minLength"].(float64); ok {
		value, _ := document.(string)
		if len(value) < int(minLength) {
			return fmt.Errorf("%s length %d, want at least %d", path, len(value), int(minLength))
		}
	}
	if minimum, ok := schemaMap["minimum"].(float64); ok {
		value, ok := document.(float64)
		if !ok || value < minimum {
			return fmt.Errorf("%s = %#v, want minimum %v", path, document, minimum)
		}
	}
	return nil
}

func (v schemaValidator) resolve(ref string) (any, error) {
	if !strings.HasPrefix(ref, "#/$defs/") {
		if v.allowExternalRefs {
			return true, nil
		}
		return nil, fmt.Errorf("unsupported external ref %q", ref)
	}
	defs, ok := v.root["$defs"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("schema has no $defs for %q", ref)
	}
	name := strings.TrimPrefix(ref, "#/$defs/")
	resolved, ok := defs[name]
	if !ok {
		return nil, fmt.Errorf("schema missing definition %q", name)
	}
	return resolved, nil
}

func validateType(typeName string, document any, path string) error {
	switch typeName {
	case "object":
		if _, ok := document.(map[string]any); !ok {
			return fmt.Errorf("%s must be an object", path)
		}
	case "array":
		if _, ok := document.([]any); !ok {
			return fmt.Errorf("%s must be an array", path)
		}
	case "string":
		if _, ok := document.(string); !ok {
			return fmt.Errorf("%s must be a string", path)
		}
	case "integer":
		value, ok := document.(float64)
		if !ok || value != float64(int64(value)) {
			return fmt.Errorf("%s must be an integer", path)
		}
	case "boolean":
		if _, ok := document.(bool); !ok {
			return fmt.Errorf("%s must be a boolean", path)
		}
	default:
		return fmt.Errorf("unsupported schema type %q at %s", typeName, path)
	}
	return nil
}

func normalizeJSONNumber(value any) any {
	if number, ok := value.(float64); ok && number == float64(int64(number)) {
		return int64(number)
	}
	return value
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return value
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func toStringSet(values []any) map[string]bool {
	set := map[string]bool{}
	for _, value := range values {
		set[value.(string)] = true
	}
	return set
}

func TestSemanticCatalogsDoNotContainDuplicates(t *testing.T) {
	assertUnique(t, publicContractSnapshotIDs(componentKinds()))
	assertUnique(t, publicContractSnapshotIDs(tokenIDs()))
	assertUnique(t, publicContractSnapshotIDs(capabilityIDs()))
}

func componentKinds() []fmt.Stringer {
	entries := component.PublicCatalog()
	values := make([]fmt.Stringer, 0, len(entries))
	for _, entry := range entries {
		values = append(values, stringer(entry.Kind))
	}
	return values
}

func tokenIDs() []fmt.Stringer {
	ids := style.SemanticTokenIDs()
	values := make([]fmt.Stringer, 0, len(ids))
	for _, id := range ids {
		values = append(values, stringer(id))
	}
	return values
}

func capabilityIDs() []fmt.Stringer {
	descriptors := capability.CoreDescriptors()
	values := make([]fmt.Stringer, 0, len(descriptors))
	for _, descriptor := range descriptors {
		values = append(values, stringer(descriptor.ID))
	}
	return values
}

func publicContractSnapshotIDs(values []fmt.Stringer) []string {
	ids := make([]string, 0, len(values))
	for _, value := range values {
		ids = append(ids, value.String())
	}
	return ids
}

func assertUnique(t *testing.T, values []string) {
	t.Helper()
	seen := map[string]bool{}
	for _, value := range values {
		if seen[value] {
			t.Fatalf("duplicate public contract ID %q in %v", value, values)
		}
		seen[value] = true
	}
	sorted := append([]string(nil), values...)
	sort.Strings(sorted)
	if len(sorted) == 0 {
		t.Fatal("empty public contract catalog")
	}
}

type stringer string

func (s stringer) String() string { return string(s) }

func TestSurfaceDurationsRemainTyped(t *testing.T) {
	descriptor := surface.ActionDescriptor{ID: "submit", Title: "Submit", Effect: surface.ActionExecute, Timeout: time.Second}
	if descriptor.Timeout != time.Second {
		t.Fatalf("duration contract lost type: %#v", descriptor)
	}
}
