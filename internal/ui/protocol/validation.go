package protocol

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

type PayloadValidator interface {
	Validate(ctx context.Context, schemaID string, document json.RawMessage) error
}

type Handler func(context.Context, Envelope) error

type Dispatcher struct {
	Validator PayloadValidator
	Handlers  map[EnvelopeKind]Handler
}

func (d Dispatcher) Dispatch(ctx context.Context, envelope Envelope) error {
	if err := ValidateEnvelope(envelope); err != nil {
		return err
	}
	if d.Validator != nil {
		schemaID := SchemaIDForKind(envelope.Kind)
		if schemaID != "" {
			if err := d.Validator.Validate(ctx, schemaID, envelope.Payload); err != nil {
				return StructuredError{Code: ErrorInvalidEnvelope, Message: "payload failed schema validation", Recoverable: false, Target: string(envelope.Kind)}
			}
		}
	}
	handler := d.Handlers[envelope.Kind]
	if handler == nil {
		return StructuredError{Code: ErrorInvalidEnvelope, Message: "no handler registered for envelope kind", Recoverable: false, Target: string(envelope.Kind)}
	}
	return handler(ctx, envelope)
}

func ValidateEnvelope(envelope Envelope) error {
	if envelope.SchemaVersion != SchemaVersion {
		return StructuredError{Code: ErrorInvalidEnvelope, Message: "invalid schema version", Recoverable: false}
	}
	if envelope.Protocol != Protocol || envelope.Revision != ProtocolRevision {
		return StructuredError{Code: ErrorUnsupportedRevision, Message: "unsupported protocol revision", Recoverable: false}
	}
	if envelope.EffectiveMessageID() == "" {
		return StructuredError{Code: ErrorInvalidEnvelope, Message: "messageId is required", Recoverable: false, Target: "messageId"}
	}
	if envelope.Kind == "" || !isSupportedKind(envelope.Kind) {
		return StructuredError{Code: ErrorInvalidEnvelope, Message: "unsupported envelope kind", Recoverable: false, Target: "kind"}
	}
	if envelope.Source.ID == "" || envelope.Source.Kind == "" || envelope.Target.ID == "" || envelope.Target.Kind == "" {
		return StructuredError{Code: ErrorInvalidEnvelope, Message: "source and target actors are required", Recoverable: false}
	}
	if envelope.Timestamp.IsZero() {
		return StructuredError{Code: ErrorInvalidEnvelope, Message: "timestamp is required", Recoverable: false, Target: "timestamp"}
	}
	if len(envelope.Payload) == 0 || !json.Valid(envelope.Payload) {
		return StructuredError{Code: ErrorInvalidEnvelope, Message: "payload must be valid JSON", Recoverable: false, Target: "payload"}
	}
	if envelope.ContentType != "" && !strings.EqualFold(envelope.ContentType, "application/json") {
		return StructuredError{Code: ErrorInvalidEnvelope, Message: "unsupported content type", Recoverable: false, Target: "contentType"}
	}
	if envelope.Auth != nil {
		if envelope.Auth.Algorithm == "" || envelope.Auth.KeyID == "" || envelope.Auth.Nonce == "" || envelope.Auth.Timestamp.IsZero() {
			return StructuredError{Code: ErrorAuthenticationFailed, Message: "authentication header is incomplete", Recoverable: false, Target: "auth"}
		}
	}
	return nil
}

func SchemaIDForKind(kind EnvelopeKind) string {
	switch kind {
	case EnvelopeComponentSnapshot:
		return "ui-component-v1.schema.json"
	case EnvelopeComponentPatch:
		return "ui-patch-v1.schema.json"
	case EnvelopeUIEvent:
		return "ui-event-v1.schema.json"
	case EnvelopeGrantPolicy:
		return "ui-grant-policy-v1.schema.json"
	case EnvelopeHello, EnvelopeHelloResult, EnvelopeError, EnvelopeAck, EnvelopeBackpressure, EnvelopeLifecycle, EnvelopeAuditEvent, EnvelopeObservation:
		return ""
	default:
		return ""
	}
}

type JSONShapeValidator struct {
	MaxDepth int
}

func (v JSONShapeValidator) Validate(_ context.Context, schemaID string, document json.RawMessage) error {
	if schemaID == "" {
		return nil
	}
	if !json.Valid(document) {
		return StructuredError{Code: ErrorInvalidJSON, Message: "payload JSON is malformed", Recoverable: false}
	}
	if err := ValidateJSONDepth(document, v.MaxDepth); err != nil {
		return err
	}
	object, err := decodeObject(document, "payload")
	if err != nil {
		return err
	}
	switch schemaID {
	case "ui-component-v1.schema.json":
		return validateComponentPayload(object)
	case "ui-patch-v1.schema.json":
		return validatePatchPayload(object)
	case "ui-event-v1.schema.json":
		return validateEventPayload(object)
	case "ui-grant-policy-v1.schema.json":
		return validateGrantPolicyPayload(object)
	default:
		return nil
	}
}

func decodeObject(document json.RawMessage, target string) (map[string]any, error) {
	var object map[string]any
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.UseNumber()
	if err := decoder.Decode(&object); err != nil {
		return nil, StructuredError{Code: ErrorInvalidJSON, Message: fmt.Sprintf("%s must be a JSON object", target), Recoverable: false, Target: target}
	}
	var trailing any
	if err := decoder.Decode(&trailing); !isEOF(err) {
		return nil, StructuredError{Code: ErrorInvalidJSON, Message: fmt.Sprintf("%s contains trailing JSON", target), Recoverable: false, Target: target}
	}
	if object == nil {
		return nil, StructuredError{Code: ErrorInvalidJSON, Message: fmt.Sprintf("%s must be a JSON object", target), Recoverable: false, Target: target}
	}
	return object, nil
}

func validateComponentPayload(object map[string]any) error {
	if err := validateObjectShape(object, fields("root", "revision", "surfaceId", "themeId", "locale", "capabilities"), []string{"root", "revision", "surfaceId"}, "payload"); err != nil {
		return err
	}
	root, ok := object["root"].(map[string]any)
	if !ok {
		return invalidType("root", "object")
	}
	if err := validateComponentNode(root, "root"); err != nil {
		return err
	}
	if err := requireInteger(object, "revision", 0, false); err != nil {
		return err
	}
	if err := requireString(object, "surfaceId", 1, 0, false); err != nil {
		return err
	}
	if err := requireString(object, "themeId", 0, 0, true); err != nil {
		return err
	}
	if err := requireString(object, "locale", 0, 0, true); err != nil {
		return err
	}
	return validateStringArray(object, "capabilities", true, true)
}

func validateComponentNode(node map[string]any, target string) error {
	allowed := fields("id", "kind", "key", "version", "props", "children", "style", "accessibility", "localization", "dataBindings", "actionBindings", "extensionSlots", "compatibility", "metadata")
	if err := validateObjectShape(node, allowed, []string{"id", "kind"}, target); err != nil {
		return err
	}
	if err := requireString(node, "id", 1, 128, false); err != nil {
		return err
	}
	kind, err := requireStringValue(node, "kind", 1, 0, false)
	if err != nil {
		return err
	}
	if !componentKinds[kind] {
		return StructuredError{Code: ErrorInvalidEnvelope, Message: fmt.Sprintf("payload field %s has unsupported component kind %q", target+".kind", kind), Recoverable: false, Target: target + ".kind"}
	}
	if err := requireString(node, "key", 0, 0, true); err != nil {
		return err
	}
	if err := requireInteger(node, "version", 0, true); err != nil {
		return err
	}
	for _, name := range []string{"props", "style", "accessibility", "localization", "metadata"} {
		if value, exists := node[name]; exists {
			if _, ok := value.(map[string]any); !ok {
				return invalidType(target+"."+name, "object")
			}
		}
	}
	if value, exists := node["children"]; exists {
		children, ok := value.([]any)
		if !ok {
			return invalidType(target+".children", "array")
		}
		for index, child := range children {
			childObject, ok := child.(map[string]any)
			if !ok {
				return invalidType(fmt.Sprintf("%s.children[%d]", target, index), "object")
			}
			if err := validateComponentNode(childObject, fmt.Sprintf("%s.children[%d]", target, index)); err != nil {
				return err
			}
		}
	}
	if err := validateStringArray(node, "dataBindings", true, true); err != nil {
		return err
	}
	if err := validateStringArray(node, "compatibility", true, true); err != nil {
		return err
	}
	if value, exists := node["actionBindings"]; exists {
		bindings, ok := value.(map[string]any)
		if !ok {
			return invalidType(target+".actionBindings", "object")
		}
		for key, binding := range bindings {
			if _, ok := binding.(string); !ok {
				return invalidType(target+".actionBindings."+key, "string")
			}
		}
	}
	if value, exists := node["extensionSlots"]; exists {
		slots, ok := value.([]any)
		if !ok {
			return invalidType(target+".extensionSlots", "array")
		}
		for index, slot := range slots {
			if _, ok := slot.(map[string]any); !ok {
				return invalidType(fmt.Sprintf("%s.extensionSlots[%d]", target, index), "object")
			}
		}
	}
	return nil
}

func validatePatchPayload(object map[string]any) error {
	allowed := fields("schemaVersion", "protocol", "revision", "surfaceId", "baseRevision", "nextRevision", "operations")
	if err := validateObjectShape(object, allowed, []string{"schemaVersion", "protocol", "revision", "surfaceId", "baseRevision", "nextRevision", "operations"}, "payload"); err != nil {
		return err
	}
	if err := requireConstInteger(object, "schemaVersion", SchemaVersion); err != nil {
		return err
	}
	if err := requireConstString(object, "protocol", Protocol); err != nil {
		return err
	}
	if err := requireConstInteger(object, "revision", ProtocolRevision); err != nil {
		return err
	}
	if err := requireString(object, "surfaceId", 1, 0, false); err != nil {
		return err
	}
	if err := requireInteger(object, "baseRevision", 0, false); err != nil {
		return err
	}
	if err := requireInteger(object, "nextRevision", 0, false); err != nil {
		return err
	}
	operations, ok := object["operations"].([]any)
	if !ok {
		return invalidType("operations", "array")
	}
	if len(operations) == 0 {
		return StructuredError{Code: ErrorInvalidEnvelope, Message: "payload field operations must not be empty", Recoverable: false, Target: "operations"}
	}
	for index, operation := range operations {
		operationObject, ok := operation.(map[string]any)
		if !ok {
			return invalidType(fmt.Sprintf("operations[%d]", index), "object")
		}
		if err := validatePatchOperation(operationObject, fmt.Sprintf("operations[%d]", index)); err != nil {
			return err
		}
	}
	return nil
}

func validatePatchOperation(object map[string]any, target string) error {
	allowed := fields("op", "path", "from", "value", "guard", "reason")
	if err := validateObjectShape(object, allowed, []string{"op", "path"}, target); err != nil {
		return err
	}
	op, err := requireStringValue(object, "op", 1, 0, false)
	if err != nil {
		return err
	}
	if !patchOperations[op] {
		return StructuredError{Code: ErrorInvalidEnvelope, Message: fmt.Sprintf("payload field %s.op has unsupported operation %q", target, op), Recoverable: false, Target: target + ".op"}
	}
	if err := requireString(object, "path", 1, 0, false); err != nil {
		return err
	}
	if err := requireString(object, "from", 0, 0, true); err != nil {
		return err
	}
	return requireString(object, "reason", 0, 0, true)
}

func validateEventPayload(object map[string]any) error {
	allowed := fields("schemaVersion", "protocol", "revision", "id", "type", "surfaceId", "componentId", "actionId", "phase", "timestamp", "payload", "trusted", "preventDefault", "stopPropagation")
	if err := validateObjectShape(object, allowed, []string{"schemaVersion", "protocol", "revision", "id", "type", "surfaceId", "timestamp", "trusted"}, "payload"); err != nil {
		return err
	}
	if err := requireConstInteger(object, "schemaVersion", SchemaVersion); err != nil {
		return err
	}
	if err := requireConstString(object, "protocol", Protocol); err != nil {
		return err
	}
	if err := requireConstInteger(object, "revision", ProtocolRevision); err != nil {
		return err
	}
	for _, field := range []string{"id", "type", "surfaceId"} {
		if err := requireString(object, field, 1, 128, false); err != nil {
			return err
		}
	}
	for _, field := range []string{"componentId", "actionId"} {
		if err := requireString(object, field, 0, 128, true); err != nil {
			return err
		}
	}
	if phase, err := requireStringValue(object, "phase", 0, 0, true); err != nil {
		return err
	} else if phase != "" && !eventPhases[phase] {
		return StructuredError{Code: ErrorInvalidEnvelope, Message: fmt.Sprintf("payload field phase has unsupported value %q", phase), Recoverable: false, Target: "phase"}
	}
	timestamp, err := requireStringValue(object, "timestamp", 1, 0, false)
	if err != nil {
		return err
	}
	if _, err := time.Parse(time.RFC3339Nano, timestamp); err != nil {
		return StructuredError{Code: ErrorInvalidEnvelope, Message: "payload field timestamp must be date-time", Recoverable: false, Target: "timestamp"}
	}
	if err := requireBool(object, "trusted", false); err != nil {
		return err
	}
	if err := requireBool(object, "preventDefault", true); err != nil {
		return err
	}
	return requireBool(object, "stopPropagation", true)
}

func validateGrantPolicyPayload(object map[string]any) error {
	allowed := fields("schemaVersion", "protocol", "revision", "extensionId", "denyByDefault", "grants", "audit", "quotas", "keyEpoch", "acceptedKeyEpochs", "rotationOverlapMillis")
	if err := validateObjectShape(object, allowed, []string{"schemaVersion", "protocol", "revision", "extensionId", "denyByDefault", "grants"}, "payload"); err != nil {
		return err
	}
	if err := requireConstInteger(object, "schemaVersion", SchemaVersion); err != nil {
		return err
	}
	if err := requireConstString(object, "protocol", Protocol); err != nil {
		return err
	}
	if err := requireConstInteger(object, "revision", ProtocolRevision); err != nil {
		return err
	}
	if err := requireString(object, "extensionId", 1, 64, false); err != nil {
		return err
	}
	if err := requireBool(object, "denyByDefault", false); err != nil {
		return err
	}
	if _, ok := object["grants"].([]any); !ok {
		return invalidType("grants", "array")
	}
	return nil
}

func validateObjectShape(object map[string]any, allowed map[string]bool, required []string, target string) error {
	for _, field := range required {
		if _, ok := object[field]; !ok {
			return StructuredError{Code: ErrorInvalidEnvelope, Message: fmt.Sprintf("payload missing required field %s", field), Recoverable: false, Target: field}
		}
	}
	for field := range object {
		if !allowed[field] {
			return StructuredError{Code: ErrorInvalidEnvelope, Message: fmt.Sprintf("payload field %s is not allowed", field), Recoverable: false, Target: targetField(target, field)}
		}
	}
	return nil
}

func requireConstInteger(object map[string]any, field string, want int) error {
	got, err := integerValue(object[field])
	if err != nil {
		return invalidType(field, "integer")
	}
	if got != int64(want) {
		return StructuredError{Code: ErrorInvalidEnvelope, Message: fmt.Sprintf("payload field %s must be %d", field, want), Recoverable: false, Target: field}
	}
	return nil
}

func requireConstString(object map[string]any, field string, want string) error {
	got, ok := object[field].(string)
	if !ok {
		return invalidType(field, "string")
	}
	if got != want {
		return StructuredError{Code: ErrorInvalidEnvelope, Message: fmt.Sprintf("payload field %s must be %q", field, want), Recoverable: false, Target: field}
	}
	return nil
}

func requireInteger(object map[string]any, field string, minimum int64, optional bool) error {
	value, exists := object[field]
	if !exists {
		if optional {
			return nil
		}
		return StructuredError{Code: ErrorInvalidEnvelope, Message: fmt.Sprintf("payload missing required field %s", field), Recoverable: false, Target: field}
	}
	integer, err := integerValue(value)
	if err != nil {
		return invalidType(field, "integer")
	}
	if integer < minimum {
		return StructuredError{Code: ErrorInvalidEnvelope, Message: fmt.Sprintf("payload field %s must be at least %d", field, minimum), Recoverable: false, Target: field}
	}
	return nil
}

func requireString(object map[string]any, field string, minLength, maxLength int, optional bool) error {
	_, err := requireStringValue(object, field, minLength, maxLength, optional)
	return err
}

func requireStringValue(object map[string]any, field string, minLength, maxLength int, optional bool) (string, error) {
	value, exists := object[field]
	if !exists {
		if optional {
			return "", nil
		}
		return "", StructuredError{Code: ErrorInvalidEnvelope, Message: fmt.Sprintf("payload missing required field %s", field), Recoverable: false, Target: field}
	}
	stringValue, ok := value.(string)
	if !ok {
		return "", invalidType(field, "string")
	}
	if len(stringValue) < minLength {
		return "", StructuredError{Code: ErrorInvalidEnvelope, Message: fmt.Sprintf("payload field %s is too short", field), Recoverable: false, Target: field}
	}
	if maxLength > 0 && len(stringValue) > maxLength {
		return "", StructuredError{Code: ErrorInvalidEnvelope, Message: fmt.Sprintf("payload field %s is too long", field), Recoverable: false, Target: field}
	}
	return stringValue, nil
}

func requireBool(object map[string]any, field string, optional bool) error {
	value, exists := object[field]
	if !exists {
		if optional {
			return nil
		}
		return StructuredError{Code: ErrorInvalidEnvelope, Message: fmt.Sprintf("payload missing required field %s", field), Recoverable: false, Target: field}
	}
	if _, ok := value.(bool); !ok {
		return invalidType(field, "boolean")
	}
	return nil
}

func validateStringArray(object map[string]any, field string, optional bool, unique bool) error {
	value, exists := object[field]
	if !exists {
		if optional {
			return nil
		}
		return StructuredError{Code: ErrorInvalidEnvelope, Message: fmt.Sprintf("payload missing required field %s", field), Recoverable: false, Target: field}
	}
	array, ok := value.([]any)
	if !ok {
		return invalidType(field, "array")
	}
	seen := map[string]bool{}
	for index, item := range array {
		stringValue, ok := item.(string)
		if !ok {
			return invalidType(fmt.Sprintf("%s[%d]", field, index), "string")
		}
		if unique && seen[stringValue] {
			return StructuredError{Code: ErrorInvalidEnvelope, Message: fmt.Sprintf("payload field %s contains duplicate value", field), Recoverable: false, Target: field}
		}
		seen[stringValue] = true
	}
	return nil
}

func integerValue(value any) (int64, error) {
	number, ok := value.(json.Number)
	if !ok {
		return 0, fmt.Errorf("not a number")
	}
	integer, err := number.Int64()
	if err != nil {
		return 0, err
	}
	if number.String() != fmt.Sprintf("%d", integer) {
		return 0, fmt.Errorf("not an integer")
	}
	return integer, nil
}

func invalidType(field, want string) StructuredError {
	return StructuredError{Code: ErrorInvalidEnvelope, Message: fmt.Sprintf("payload field %s must be a %s", field, want), Recoverable: false, Target: field}
}

func fields(names ...string) map[string]bool {
	result := make(map[string]bool, len(names))
	for _, name := range names {
		result[name] = true
	}
	return result
}

func targetField(target, field string) string {
	if target == "" || target == "payload" {
		return field
	}
	return target + "." + field
}

func isEOF(err error) bool { return err == io.EOF }

var componentKinds = fields(
	"application", "window", "surface", "viewport", "stack", "row", "grid", "panel", "card", "separator", "spacer",
	"text", "markdown", "code", "icon", "badge", "button", "link", "textInput", "textArea", "select", "checkbox",
	"radioGroup", "toggle", "slider", "progress", "spinner", "list", "table", "tree", "form", "toolbar", "tabs",
	"breadcrumb", "dialog", "toast", "terminal", "canvas", "image", "video", "chart", "commandPalette", "keybindingHint", "extensionOutlet",
)

var patchOperations = fields("add", "remove", "replace", "move", "copy", "test", "setProps", "bindData", "bindAction")
var eventPhases = fields("capture", "target", "bubble")

func isSupportedKind(kind EnvelopeKind) bool {
	for _, supported := range SupportedTransportEnvelopeKinds() {
		if kind == supported {
			return true
		}
	}
	return false
}
