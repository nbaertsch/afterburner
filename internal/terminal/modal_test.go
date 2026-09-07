package terminal

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

type testModalRenderer struct {
	shown  []ModalFrame
	hidden int
}

func (r *testModalRenderer) ShowModal(frame ModalFrame) { r.shown = append(r.shown, frame) }
func (r *testModalRenderer) HideModal()                 { r.hidden++ }

func registerLegacyModalForTest(t *testing.T, server *ModalServer) ModalCapability {
	t.Helper()
	capability, err := server.RegisterModalCanvas(ModalRegistration{OwnerExtensionID: ModalLegacyOwnerExtensionID, CanvasID: ModalLegacyCanvasID, SurfaceID: ModalLegacySurfaceID})
	if err != nil {
		t.Fatal(err)
	}
	return capability
}

func TestModalServerAuthenticatesAndDrivesBrokerOwnership(t *testing.T) {
	process := &fakeProcess{}
	backend := &fakeBackend{process: process}
	broker := NewBroker(backend, BrokerOptions{})
	if err := broker.Start(t.Context(), Command{Path: "copilot"}); err != nil {
		t.Fatal(err)
	}
	renderer := &testModalRenderer{}
	server, err := NewModalServer(broker, renderer)
	if err != nil {
		t.Fatal(err)
	}
	registerLegacyModalForTest(t, server)

	server.RequireAuthenticatedClients()
	unauthorized := callModalServerAuthenticated(t, server, map[string]any{"operation": "open", "id": "black-box"}, 0)
	if unauthorized.OK || unauthorized.Error != "modal-unauthorized" {
		t.Fatalf("unauthorized response = %#v", unauthorized)
	}
	server.AuthorizeClientProcess(42)
	missingIdentity := callModalServerRawAuthenticated(t, server, `{"operation":"open","id":"black-box","generation":1}`+"\n", 42)
	if missingIdentity.OK || missingIdentity.Error != "modal-invalid-identity" {
		t.Fatalf("missing identity response = %#v", missingIdentity)
	}
	response := callModalServer(t, server, map[string]any{
		"operation": "open", "id": "black-box", "title": "Black Box", "body": "first",
		"actions": []map[string]any{{"name": "submit", "label": "Submit", "key": "enter"}},
	})
	if !response.OK || broker.Owner() != OwnerModal || server.ActiveCount() != 1 {
		t.Fatalf("open response=%#v owner=%s active=%d", response, broker.Owner(), server.ActiveCount())
	}
	if len(renderer.shown) != 1 || renderer.shown[0].Actions[0].Key != "enter" {
		t.Fatalf("renderer frames = %#v", renderer.shown)
	}
	if _, err := server.HandleInput([]byte("q")); err != nil {
		t.Fatal(err)
	}
	if broker.Owner() != OwnerCopilot || server.ActiveCount() != 0 || renderer.hidden != 1 {
		t.Fatalf("fallback q close owner=%s active=%d hidden=%d", broker.Owner(), server.ActiveCount(), renderer.hidden)
	}
}

func TestModalServerBlackBoxLiveIDIsRegistered(t *testing.T) {
	server, err := NewModalServer(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	capability, err := server.RegisterModalCanvas(ModalRegistration{
		OwnerExtensionID: ModalBlackBoxOwnerExtensionID,
		CanvasID:         ModalBlackBoxCanvasID,
		SurfaceID:        ModalBlackBoxSurfaceID,
	})
	if err != nil {
		t.Fatal(err)
	}
	response := callModalServer(t, server, modalRequestMap(capability, "open", int64(1)))
	if !response.OK || server.ActiveCount() != 1 {
		t.Fatalf("Black Box live open response=%#v active=%d", response, server.ActiveCount())
	}
}

func TestModalServerScopedCapabilityRejectsImpersonation(t *testing.T) {
	renderer := &testModalRenderer{}
	server, err := NewModalServer(nil, renderer)
	if err != nil {
		t.Fatal(err)
	}
	ownerACap, err := server.RegisterModalCanvas(ModalRegistration{OwnerExtensionID: "owner.alpha", CanvasID: "shared", SurfaceID: "alpha-modal"})
	if err != nil {
		t.Fatal(err)
	}
	ownerBCap, err := server.RegisterModalCanvas(ModalRegistration{OwnerExtensionID: "owner.beta", CanvasID: "shared", SurfaceID: "beta-modal"})
	if err != nil {
		t.Fatal(err)
	}
	impersonation := callModalServer(t, server, map[string]any{
		"operation":        "register",
		"id":               ModalBlackBoxSurfaceID,
		"ownerExtensionId": "attacker",
		"canvasId":         ModalBlackBoxCanvasID,
		"surfaceId":        ModalBlackBoxSurfaceID,
	})
	if impersonation.OK || impersonation.Error != "modal-registration-disabled" {
		t.Fatalf("Black Box registration response = %#v", impersonation)
	}
	withOldRegistrationToken := callModalServerRaw(t, server, fmt.Sprintf(`{"operation":"register","id":"%s","ownerExtensionId":"attacker","canvasId":"%s","surfaceId":"%s","registrationToken":"old"}`+"\n", ModalBlackBoxSurfaceID, ModalBlackBoxCanvasID, ModalBlackBoxSurfaceID))
	if withOldRegistrationToken.OK || withOldRegistrationToken.Error != "modal-invalid-request" {
		t.Fatalf("legacy registration token response = %#v", withOldRegistrationToken)
	}

	server.RequireAuthenticatedClients()
	wireImpersonation := callModalServerAuthenticated(t, server, modalRequestMap(ownerBCap, "open", int64(1)), 0)
	if wireImpersonation.OK || wireImpersonation.Error != "modal-unauthorized" {
		t.Fatalf("unauthenticated wire impersonation response = %#v", wireImpersonation)
	}
	unknown := callModalServer(t, server, map[string]any{
		"type":             "open",
		"id":               "missing-modal",
		"ownerExtensionId": "owner.alpha",
		"canvasId":         "missing",
		"surfaceId":        "missing-modal",
	})
	if unknown.OK || unknown.Error != "modal-unknown-canvas" {
		t.Fatalf("unknown canvas response = %#v", unknown)
	}
	collision := callModalServer(t, server, map[string]any{
		"type":             "open",
		"id":               "alpha-modal",
		"ownerExtensionId": ownerBCap.OwnerExtensionID,
		"canvasId":         ownerBCap.CanvasID,
		"surfaceId":        "alpha-modal",
	})
	if collision.OK || collision.Error != "modal-unknown-canvas" {
		t.Fatalf("canvas collision response = %#v", collision)
	}
	identityMismatch := callModalServer(t, server, map[string]any{
		"type":             "open",
		"id":               ownerACap.SurfaceID,
		"ownerExtensionId": ownerACap.OwnerExtensionID,
		"canvasId":         ownerACap.CanvasID,
		"surfaceId":        ownerBCap.SurfaceID,
	})
	if identityMismatch.OK || identityMismatch.Error != "modal-identity-mismatch" {
		t.Fatalf("identity mismatch response = %#v", identityMismatch)
	}
	open := callModalServer(t, server, modalRequestMap(ownerACap, "open", int64(1)))
	if !open.OK || server.ActiveCount() != 1 {
		t.Fatalf("scoped open response = %#v active=%d", open, server.ActiveCount())
	}
}

func TestModalServerDefaultEscapeDelayStaysResponsive(t *testing.T) {
	server, err := NewModalServer(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if server.pendingEscapeDelay > 50*time.Millisecond {
		t.Fatalf("default escape delay should stay responsive, got %s", server.pendingEscapeDelay)
	}
}

func TestModalServerRejectsStaleReopenGeneration(t *testing.T) {
	server, err := NewModalServer(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	capability, err := server.RegisterModalCanvas(ModalRegistration{OwnerExtensionID: "owner.alpha", CanvasID: "panel", SurfaceID: "alpha-panel"})
	if err != nil {
		t.Fatal(err)
	}
	response := callModalServer(t, server, modalRequestMap(capability, "open", int64(3)))
	if !response.OK {
		t.Fatalf("open response = %#v", response)
	}
	response = callModalServer(t, server, modalRequestMap(capability, "close", int64(3)))
	if !response.OK {
		t.Fatalf("close response = %#v", response)
	}
	response = callModalServer(t, server, modalRequestMap(capability, "open", int64(2)))
	if response.OK || response.Error != "modal-stale-generation" {
		t.Fatalf("stale reopen response = %#v", response)
	}
	response = callModalServer(t, server, modalRequestMap(capability, "open", int64(4)))
	if !response.OK {
		t.Fatalf("fresh reopen response = %#v", response)
	}
	response = callModalServer(t, server, modalRequestMap(capability, "poll", int64(3)))
	if response.OK || response.Error != "modal-stale-generation" {
		t.Fatalf("stale queued poll response = %#v", response)
	}
}

func TestTerminalModalRendererRestoresCurrentScreenWithoutRawReplay(t *testing.T) {
	var output bytes.Buffer
	renderer := NewTerminalModalRendererWithSize(&output, Size{Cols: 20, Rows: 4})
	renderer.WriteCopilotOutput([]byte("before"))
	renderer.ShowModal(ModalFrame{Title: "Modal", Body: "body"})
	renderer.WriteCopilotOutput([]byte("\rraw-only\r\x1b[Kcurrent"))
	renderer.HideModal()
	text := output.String()
	for _, want := range []string{"before", "Modal", "body", "current"} {
		if !strings.Contains(text, want) {
			t.Fatalf("output missing %q: %q", want, text)
		}
	}
	if strings.Contains(text, "raw-only") {
		t.Fatalf("modal close replayed raw overwritten output: %q", text)
	}
	if strings.Contains(text, "\x1b[?1049h") || strings.Contains(text, "\x1b[?1049l") {
		t.Fatalf("renderer should not depend on alternate-screen replay: %q", text)
	}
}

func TestTerminalModalRendererSanitizesExtensionANSI(t *testing.T) {
	var output bytes.Buffer
	renderer := NewTerminalModalRendererWithSize(&output, Size{Cols: 20, Rows: 4})
	renderer.ShowModal(ModalFrame{Title: "Title\x1b[31m", Body: "Body\x1b[32m"})
	text := output.String()
	if strings.Contains(text, "Title\x1b[31m") || strings.Contains(text, "Body\x1b[32m") || strings.Contains(text, "[31m") || strings.Contains(text, "[32m") {
		t.Fatalf("extension ANSI was rendered verbatim: %q", text)
	}
}

func TestTerminalModalRendererEnterpriseOverlayChrome(t *testing.T) {
	var output bytes.Buffer
	renderer := NewTerminalModalRendererWithSize(&output, Size{Cols: 100, Rows: 30})
	renderer.WriteCopilotOutput([]byte("copilot backdrop"))
	renderer.ShowModal(ModalFrame{
		Title:  "Enterprise Review",
		Status: "Verified",
		Body:   "line one\nline two",
		Footer: "metadata-only host rendering",
		Actions: []ModalAction{
			{Name: "approve", Label: "Approve", Key: "enter"},
			{Name: "dismiss", Label: "Dismiss", Key: "escape"},
		},
	})
	text := output.String()
	for _, want := range []string{"copilot backdrop", "╭", "╰", "◆ Enterprise Review", "Verified", "Native Afterburner modal overlay", "line one", "metadata-only host rendering", "[enter] Approve"} {
		if !strings.Contains(text, want) {
			t.Fatalf("enterprise overlay missing %q: %q", want, text)
		}
	}
	if strings.Contains(text, "\x1b[?1049h") || strings.Contains(text, "\x1b[?1049l") {
		t.Fatalf("enterprise overlay should not use alternate screen: %q", text)
	}
}

func TestTerminalModalRendererProjectsDocumentBody(t *testing.T) {
	var output bytes.Buffer
	renderer := NewTerminalModalRendererWithSize(&output, Size{Cols: 120, Rows: 34})
	renderer.ShowModal(ModalFrame{
		Title:  "Document Modal",
		Status: "Structured",
		Body:   "legacy body should not render",
		Footer: "metadata-only fallback /black-box-tail",
		Actions: []ModalAction{
			{Name: "refresh", Label: "Refresh", Key: "r"},
			{Name: "export", Label: "Export", Key: "e"},
		},
		Document: json.RawMessage(`{"root":{"kind":"dialog","children":[{"kind":"statusGrid","props":{"label":"Black Box modal status cards"},"children":[{"kind":"card","props":{"title":"Recorder"},"children":[{"kind":"text","props":{"value":"Enabled"}},{"kind":"text","props":{"value":"metadata"}}]}]},{"kind":"progress","props":{"label":"Storage usage","status":"42% used"}},{"kind":"sparkline","props":{"label":"Signal trend","values":[0,1,2,3]}},{"kind":"panel","props":{"title":"Details"},"children":[{"kind":"markdown","props":{"markdown":"Record abc"}}]},{"kind":"panel","props":{"title":"Metadata timeline"},"children":[{"kind":"table","props":{"label":"Timeline table","columns":[{"id":"time","title":"Time"},{"id":"kind","title":"Kind"}],"rows":[{"time":"now","kind":"event"}]}}]}]}}`),
	})
	text := output.String()
	for _, want := range []string{"Document Modal", "Shortcuts: r Refresh · e Export", "metadata-only fallback", "▌ Status cards", "• Recorder │ ✓ Enabled │ metadata", "Storage usage: ████░░░░░░ 42% used", "Signal trend: ▁▃▆█", "▌ Selected event", "Record abc", "Metadata timeline table", "Time │ Kind", "now │ event"} {
		if !strings.Contains(text, want) {
			t.Fatalf("projected document missing %q: %q", want, text)
		}
	}
	if strings.Contains(text, "legacy body should not render") {
		t.Fatalf("document projection should take precedence over legacy body: %q", text)
	}
}

func TestTerminalModalRendererProjectsResponsiveDocumentTable(t *testing.T) {
	frame := ModalFrame{
		Title:    "Narrow Table",
		Status:   "Responsive",
		Document: json.RawMessage(`{"root":{"kind":"dialog","children":[{"kind":"table","props":{"label":"Deployments","columns":[{"id":"service","title":"Service"},{"id":"status","title":"Status"},{"id":"summary","title":"Summary"}],"rows":[{"service":"api-with-a-very-long-name","status":"healthy","summary":"serving production traffic without errors"}]}}]}}`),
	}
	lines := modalFrameBodyLines(frame, 56)
	text := strings.Join(lines, "\n")
	for _, want := range []string{"Deployments", "Service", "Status", "Summary", "api-with-a-very", "serving production"} {
		if !strings.Contains(text, want) {
			t.Fatalf("responsive table missing %q: %q", want, text)
		}
	}
	for _, line := range lines {
		if utf8.RuneCountInString(line) > 56 {
			t.Fatalf("table row overflowed modal width: %q", line)
		}
	}
}

func TestTerminalModalRendererDoesNotDuplicateCloseHintWithoutActions(t *testing.T) {
	var output bytes.Buffer
	renderer := NewTerminalModalRendererWithSize(&output, Size{Cols: 80, Rows: 18})
	renderer.ShowModal(ModalFrame{Title: "Notice", Body: "No actions here."})
	text := output.String()
	if count := strings.Count(text, "[Esc] Close"); count != 1 {
		t.Fatalf("expected one close hint, got %d: %q", count, text)
	}
	layout := newModalLayout(renderer.Snapshot())
	if _, ok := modalActionRow(ModalFrame{}, layout); ok {
		t.Fatalf("empty action list should not expose a clickable action row")
	}
}

func TestTerminalModalRendererProjectsDocumentFormControls(t *testing.T) {
	var output bytes.Buffer
	renderer := NewTerminalModalRendererWithSize(&output, Size{Cols: 120, Rows: 34})
	renderer.ShowModal(ModalFrame{
		Title:    "Form Modal",
		Status:   "Inputs",
		Document: json.RawMessage(`{"root":{"kind":"dialog","children":[{"kind":"form","props":{"title":"Deployment settings"},"children":[{"kind":"textInput","props":{"label":"Name","value":"production"}},{"kind":"passwordInput","props":{"label":"Token","value":"secret-value"}},{"kind":"searchInput","props":{"label":"Search","placeholder":"filter services"}},{"kind":"textArea","props":{"label":"Notes","value":"first line\nsecond line"}},{"kind":"select","props":{"label":"Region","value":"west","options":[{"value":"east","label":"US East"},{"value":"west","label":"US West"}]}},{"kind":"checkbox","props":{"label":"Enable audit","checked":true}},{"kind":"toggle","props":{"label":"Dry run","value":false}},{"kind":"slider","props":{"label":"Rollout","value":35,"status":"35%"}}]}]}}`),
	})
	text := output.String()
	for _, want := range []string{"▌ Deployment settings", "Name: production", "Token: ••••••••", "Search: ‹filter services›", "Notes:", "first line", "second line", "Region: US West", "☑ Enable audit", "○ Dry run", "Rollout: ███░░░░░░░ 35%"} {
		if !strings.Contains(text, want) {
			t.Fatalf("projected form control missing %q: %q", want, text)
		}
	}
	if strings.Contains(text, "secret-value") {
		t.Fatalf("password input leaked raw value: %q", text)
	}
}

func TestTerminalModalRendererProjectsDocumentCollectionsAndNavigation(t *testing.T) {
	var output bytes.Buffer
	renderer := NewTerminalModalRendererWithSize(&output, Size{Cols: 120, Rows: 34})
	renderer.ShowModal(ModalFrame{
		Title:    "Collection Modal",
		Status:   "Document",
		Document: json.RawMessage(`{"root":{"kind":"dialog","children":[{"kind":"tabs","props":{"active":"logs","items":[{"id":"overview","label":"Overview"},{"id":"logs","label":"Logs"}]}},{"kind":"commandPalette","props":{"label":"Command palette","placeholder":"type a command"}},{"kind":"keybindingHint","props":{"key":"Ctrl+K","label":"Open commands"}},{"kind":"list","props":{"label":"Deployments","items":[{"label":"api-service"},{"label":"worker-service"}]}},{"kind":"timeline","props":{"label":"Recent activity","items":[{"timestamp":"12:00","message":"deployed api"},{"timestamp":"12:03","message":"health check passed"}]}},{"kind":"log","props":{"label":"Audit log","items":["operator approved","rollout completed"]}}]}}`),
	})
	text := output.String()
	for _, want := range []string{"Tabs: Overview [Logs]", "Command palette: ‹type a command›", "[Ctrl+K] Open commands", "▌ Deployments", "• api-service", "• worker-service", "▌ Recent activity", "12:00 — deployed api", "12:03 — health check passed", "▌ Audit log", "operator approved", "rollout completed"} {
		if !strings.Contains(text, want) {
			t.Fatalf("projected collection/navigation node missing %q: %q", want, text)
		}
	}
}

func TestTerminalModalRendererProjectsCompactDocumentActionBar(t *testing.T) {
	var output bytes.Buffer
	renderer := NewTerminalModalRendererWithSize(&output, Size{Cols: 120, Rows: 34})
	renderer.ShowModal(ModalFrame{
		Title:    "Action Bar",
		Status:   "Compact",
		Document: json.RawMessage(`{"root":{"kind":"dialog","children":[{"kind":"actionBar","props":{"label":"Primary actions"},"children":[{"kind":"button","props":{"label":"Refresh","keybinding":"r"}},{"kind":"button","props":{"label":"Doctor","keybinding":"d"}},{"kind":"button","props":{"label":"Close","keybinding":"q"}}]}]}}`),
	})
	text := output.String()
	if !strings.Contains(text, "Primary actions: [r] Refresh [d] Doctor [q] Close") {
		t.Fatalf("compact action bar missing: %q", text)
	}
}

func TestTerminalModalRendererProjectsDocumentActionControls(t *testing.T) {
	var output bytes.Buffer
	renderer := NewTerminalModalRendererWithSize(&output, Size{Cols: 120, Rows: 34})
	renderer.ShowModal(ModalFrame{
		Title:    "Document Controls",
		Status:   "Actions",
		Document: json.RawMessage(`{"root":{"kind":"dialog","children":[{"kind":"button","props":{"label":"Approve","keybinding":"a"}},{"kind":"link","props":{"label":"Open docs","shortcut":"o"}},{"kind":"pagination","props":{"label":"Results","page":2,"totalPages":5}},{"kind":"help","props":{"description":"Use arrows to move through rows."}},{"kind":"confirmation","props":{"title":"Confirm deploy","message":"Deploy to production?"}},{"kind":"prompt","props":{"label":"Search prompt","value":"service:"}}]}}`),
	})
	text := output.String()
	for _, want := range []string{"[a] Approve", "[o] Open docs", "Results: 2/5", "Help: Use arrows to move through rows.", "Confirm deploy: Deploy to production?", "Search prompt: service:"} {
		if !strings.Contains(text, want) {
			t.Fatalf("projected action/control missing %q: %q", want, text)
		}
	}
}

func TestTerminalModalRendererProjectsDocumentMediaAndStatus(t *testing.T) {
	var output bytes.Buffer
	renderer := NewTerminalModalRendererWithSize(&output, Size{Cols: 120, Rows: 34})
	renderer.ShowModal(ModalFrame{
		Title:    "Media Modal",
		Status:   "Fallbacks",
		Document: json.RawMessage(`{"root":{"kind":"dialog","children":[{"kind":"toast","props":{"message":"Saved successfully"}},{"kind":"loading","props":{"label":"Syncing state"}},{"kind":"errorBoundary","props":{"title":"Preview failed"}},{"kind":"empty","props":{"message":"Nothing selected"}},{"kind":"chart","props":{"title":"Latency chart","description":"p95 trend"}},{"kind":"image","props":{"label":"Architecture diagram","alt":"system layout"}},{"kind":"video","props":{"label":"Demo recording","caption":"modal walkthrough"}},{"kind":"terminal","props":{"label":"Build output","description":"last run"}},{"kind":"canvas","props":{"label":"Nested canvas","description":"rich-only surface"}},{"kind":"extensionOutlet","props":{"label":"Plugin slot","description":"third-party content"}}]}}`),
	})
	text := output.String()
	for _, want := range []string{"⚠ Saved successfully", "⏳ Syncing state", "✕ Preview failed", "Nothing selected", "Chart: Latency chart — p95 trend", "Image: Architecture diagram — system layout", "Video: Demo recording — modal walkthrough", "Terminal: Build output — last run", "Canvas: Nested canvas — rich-only surface", "ExtensionOutlet: Plugin slot — third-party content"} {
		if !strings.Contains(text, want) {
			t.Fatalf("projected media/status node missing %q: %q", want, text)
		}
	}
}

func TestTerminalModalRendererProjectsDocumentContainerPrimitives(t *testing.T) {
	var output bytes.Buffer
	renderer := NewTerminalModalRendererWithSize(&output, Size{Cols: 120, Rows: 34})
	renderer.ShowModal(ModalFrame{
		Title:    "Container Modal",
		Status:   "Layout",
		Document: json.RawMessage(`{"root":{"kind":"dialog","children":[{"kind":"surface","props":{"title":"Ops surface"},"children":[{"kind":"viewport","props":{"label":"Primary viewport"},"children":[{"kind":"split","props":{"label":"Two pane"},"children":[{"kind":"scroll","props":{"label":"Scrollable region"},"children":[{"kind":"text","props":{"value":"Inside scroll"}}]}]}]}]},{"kind":"breadcrumb","props":{"items":[{"label":"Home"},{"label":"Deployments"},{"label":"API"}]}},{"kind":"contextMenu","props":{"label":"Row menu","items":[{"label":"Retry","key":"r"},{"label":"Cancel","key":"c"}]}}]}}`),
	})
	text := output.String()
	for _, want := range []string{"▌ Ops surface", "▌ Primary viewport", "▌ Two pane", "▌ Scrollable region", "Inside scroll", "Breadcrumb: Home › Deployments › API", "▌ Row menu", "[r] Retry", "[c] Cancel"} {
		if !strings.Contains(text, want) {
			t.Fatalf("projected container primitive missing %q: %q", want, text)
		}
	}
}

func TestTerminalModalRendererProjectsDocumentChromePrimitives(t *testing.T) {
	var output bytes.Buffer
	renderer := NewTerminalModalRendererWithSize(&output, Size{Cols: 120, Rows: 34})
	renderer.ShowModal(ModalFrame{
		Title:    "Chrome Modal",
		Status:   "Document",
		Document: json.RawMessage(`{"root":{"kind":"dialog","children":[{"kind":"section","props":{"title":"Overview"},"children":[{"kind":"badge","props":{"label":"Healthy","tone":"success"}},{"kind":"icon","props":{"icon":"◆","label":"Native surface"}}]},{"kind":"separator","props":{"label":"Next"}},{"kind":"spacer"},{"kind":"disclosure","props":{"label":"Advanced"},"children":[{"kind":"text","props":{"value":"Hidden details are visible in terminal fallback."}}]}]}}`),
	})
	text := output.String()
	for _, want := range []string{"▌ Overview", "✓ Healthy", "◆ Native surface", "── Next", "▌ Advanced", "Hidden details are visible in terminal fallback."} {
		if !strings.Contains(text, want) {
			t.Fatalf("projected chrome primitive missing %q: %q", want, text)
		}
	}
}

func TestModalServerScrollsOverflowWithoutExtensionAction(t *testing.T) {
	var output bytes.Buffer
	renderer := NewTerminalModalRendererWithSize(&output, Size{Cols: 80, Rows: 18})
	server, err := NewModalServer(nil, renderer)
	if err != nil {
		t.Fatal(err)
	}
	registerLegacyModalForTest(t, server)
	server.pollTimeout = 20 * time.Millisecond
	var body strings.Builder
	for i := 1; i <= 20; i++ {
		fmt.Fprintf(&body, "line %02d\n", i)
	}
	response := callModalServer(t, server, map[string]any{
		"type": "open", "id": "black-box", "title": "Scrollable", "body": body.String(),
		"actions": []map[string]any{{"name": "down-action", "label": "Down action", "key": "down"}},
	})
	if !response.OK {
		t.Fatalf("open response = %#v", response)
	}
	output.Reset()
	if _, err := server.HandleInput([]byte("\x1b[B")); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, want := range []string{"line 02", "lines 2-", "[↑/↓ PgUp/PgDn Home/End] Scroll"} {
		if !strings.Contains(text, want) {
			t.Fatalf("scrolled overlay missing %q: %q", want, text)
		}
	}
	response = callModalServer(t, server, map[string]any{"type": "poll", "id": "black-box"})
	if response.Event != nil {
		t.Fatalf("scroll key leaked as extension action: %#v", response)
	}
}

func TestModalServerRoutesMouseWheelAndActionClicks(t *testing.T) {
	var output bytes.Buffer
	renderer := NewTerminalModalRendererWithSize(&output, Size{Cols: 80, Rows: 18})
	server, err := NewModalServer(nil, renderer)
	if err != nil {
		t.Fatal(err)
	}
	registerLegacyModalForTest(t, server)
	server.pollTimeout = 20 * time.Millisecond
	var body strings.Builder
	for i := 1; i <= 20; i++ {
		fmt.Fprintf(&body, "line %02d\n", i)
	}
	response := callModalServer(t, server, map[string]any{
		"type": "open", "id": "black-box", "title": "Clickable", "body": body.String(),
		"actions": []map[string]any{{"name": "refresh", "label": "Refresh", "key": "r"}},
	})
	if !response.OK {
		t.Fatalf("open response = %#v", response)
	}
	output.Reset()
	if _, err := server.HandleInput([]byte("\x1b[<65;10;10M")); err != nil {
		t.Fatal(err)
	}
	if text := output.String(); !strings.Contains(text, "lines 2-") || !strings.Contains(text, "line 02") {
		t.Fatalf("mouse wheel down did not scroll modal: %q", text)
	}
	layout := newModalLayout(renderer.Snapshot())
	actionRow, ok := modalActionRow(ModalFrame{Actions: []ModalAction{{Name: "refresh", Label: "Refresh", Key: "r"}}}, layout)
	if !ok {
		t.Fatal("action row should be hittable")
	}
	if _, err := server.HandleInput([]byte(fmt.Sprintf("\x1b[<0;%d;%dM", layout.innerLeft+1, actionRow))); err != nil {
		t.Fatal(err)
	}
	response = callModalServer(t, server, map[string]any{"type": "poll", "id": "black-box"})
	if response.Event == nil || response.Event.Type != "action" || response.Event.ActionName != "refresh" || response.Event.Key != "r" {
		t.Fatalf("mouse click should invoke action: %#v", response)
	}
}

func TestModalServerScrollsSplitAndModifiedArrowSequences(t *testing.T) {
	var output bytes.Buffer
	renderer := NewTerminalModalRendererWithSize(&output, Size{Cols: 80, Rows: 18})
	server, err := NewModalServer(nil, renderer)
	if err != nil {
		t.Fatal(err)
	}
	registerLegacyModalForTest(t, server)
	server.pendingEscapeDelay = 50 * time.Millisecond
	server.pollTimeout = 20 * time.Millisecond
	var body strings.Builder
	for i := 1; i <= 20; i++ {
		fmt.Fprintf(&body, "line %02d\n", i)
	}
	response := callModalServer(t, server, map[string]any{
		"type": "open", "id": "black-box", "title": "Scrollable", "body": body.String(),
	})
	if !response.OK {
		t.Fatalf("open response = %#v", response)
	}
	output.Reset()
	if _, err := server.HandleInput([]byte("\x1b")); err != nil {
		t.Fatal(err)
	}
	if _, err := server.HandleInput([]byte("[B")); err != nil {
		t.Fatal(err)
	}
	if text := output.String(); !strings.Contains(text, "lines 2-") || !strings.Contains(text, "line 02") {
		t.Fatalf("split down arrow did not scroll modal: %q", text)
	}
	response = callModalServer(t, server, map[string]any{"type": "poll", "id": "black-box"})
	if response.Event != nil || server.ActiveCount() != 1 {
		t.Fatalf("split arrow leaked close/action event response=%#v active=%d", response, server.ActiveCount())
	}
	output.Reset()
	if _, err := server.HandleInput([]byte("\x1b[1;5A")); err != nil {
		t.Fatal(err)
	}
	if text := output.String(); !strings.Contains(text, "lines 1-") || !strings.Contains(text, "line 01") {
		t.Fatalf("modified up arrow did not scroll modal homeward: %q", text)
	}
	output.Reset()
	if _, err := server.HandleInput([]byte("\x1b[40;0;0;1;0;1_")); err != nil {
		t.Fatal(err)
	}
	if text := output.String(); !strings.Contains(text, "lines 2-") || !strings.Contains(text, "line 02") {
		t.Fatalf("Windows VT down arrow did not scroll modal: %q", text)
	}
	output.Reset()
	if _, err := server.HandleInput([]byte("\x1b[38;0;0;1;0;1_")); err != nil {
		t.Fatal(err)
	}
	if text := output.String(); !strings.Contains(text, "lines 1-") || !strings.Contains(text, "line 01") {
		t.Fatalf("Windows VT up arrow did not scroll modal homeward: %q", text)
	}
	output.Reset()
	if _, err := server.HandleInput([]byte("\x1b[34;0;0;1;0;1_")); err != nil {
		t.Fatal(err)
	}
	if text := output.String(); !strings.Contains(text, "line 04") || !strings.Contains(text, "lines 4-") {
		t.Fatalf("Windows VT page down did not scroll modal by page: %q", text)
	}
	output.Reset()
	if _, err := server.HandleInput([]byte("\x1b[33;0;0;1;0;1_")); err != nil {
		t.Fatal(err)
	}
	if text := output.String(); !strings.Contains(text, "line 01") || !strings.Contains(text, "lines 1-") {
		t.Fatalf("Windows VT page up did not scroll modal homeward: %q", text)
	}
	output.Reset()
	if _, err := server.HandleInput([]byte("\x1b[35;0;0;1;0;1_")); err != nil {
		t.Fatal(err)
	}
	if text := output.String(); !strings.Contains(text, "line 18") || !strings.Contains(text, "lines 18-") {
		t.Fatalf("Windows VT end did not scroll modal to bottom: %q", text)
	}
	output.Reset()
	if _, err := server.HandleInput([]byte("\x1b[36;0;0;1;0;1_")); err != nil {
		t.Fatal(err)
	}
	if text := output.String(); !strings.Contains(text, "line 01") || !strings.Contains(text, "lines 1-") {
		t.Fatalf("Windows VT home did not scroll modal to top: %q", text)
	}
}

func TestTerminalModalRendererForwardsQueriesDuringModal(t *testing.T) {
	var output bytes.Buffer
	renderer := NewTerminalModalRendererWithSize(&output, Size{Cols: 20, Rows: 4})
	renderer.ShowModal(ModalFrame{Title: "Modal", Body: "body"})
	renderer.WriteCopilotOutput([]byte("ordinary\x1b[31mred\x1b[6n\x1b[c\x1b[?25$p\x1b]10;?\x1b\\\x1bP$q q\x1b\\hidden"))
	text := output.String()
	for _, want := range []string{"\x1b[6n", "\x1b[c", "\x1b[?25$p", "\x1b]10;?\x1b\\", "\x1bP$q q\x1b\\"} {
		if !strings.Contains(text, want) {
			t.Fatalf("modal output did not forward terminal query %q: %q", want, text)
		}
	}
	for _, blocked := range []string{"ordinary", "red", "hidden", "\x1b[31m"} {
		if strings.Contains(text, blocked) {
			t.Fatalf("modal output leaked suppressed Copilot rendering %q: %q", blocked, text)
		}
	}
}

func TestTerminalModalRendererForwardsSplitQueriesDuringModal(t *testing.T) {
	var output bytes.Buffer
	renderer := NewTerminalModalRendererWithSize(&output, Size{Cols: 20, Rows: 4})
	renderer.ShowModal(ModalFrame{Title: "Modal", Body: "body"})
	output.Reset()

	renderer.WriteCopilotOutput([]byte("ordinary\x1b["))
	renderer.WriteCopilotOutput([]byte("6"))
	renderer.WriteCopilotOutput([]byte("n\x1b]10"))
	renderer.WriteCopilotOutput([]byte(";?\x1b\\\x1bP$q q"))
	renderer.WriteCopilotOutput([]byte("\x1b\\hidden\x1b[31"))
	renderer.WriteCopilotOutput([]byte("m"))
	text := output.String()
	for _, want := range []string{"\x1b[6n", "\x1b]10;?\x1b\\", "\x1bP$q q\x1b\\"} {
		if !strings.Contains(text, want) {
			t.Fatalf("modal output did not forward split terminal query %q: %q", want, text)
		}
	}
	for _, blocked := range []string{"ordinary", "hidden", "\x1b[31m"} {
		if strings.Contains(text, blocked) {
			t.Fatalf("split modal output leaked suppressed Copilot rendering %q: %q", blocked, text)
		}
	}
}

func TestTerminalModalRendererResizeRepaintsActiveModal(t *testing.T) {
	var output bytes.Buffer
	renderer := NewTerminalModalRendererWithSize(&output, Size{Cols: 20, Rows: 4})
	renderer.ShowModal(ModalFrame{Title: "Modal", Body: "body"})
	renderer.Resize(Size{Cols: 10, Rows: 2})
	if count := strings.Count(output.String(), "Modal"); count != 2 {
		t.Fatalf("active modal was not repainted on resize, title count=%d output=%q", count, output.String())
	}
	renderer.WriteCopilotOutput([]byte("1234567890"))
	renderer.Resize(Size{Cols: 5, Rows: 2})
	renderer.HideModal()
	if !strings.Contains(output.String(), "12345") {
		t.Fatalf("resized screen did not restore modeled Copilot output: %q", output.String())
	}
}

func TestModalActionAndCloseEvents(t *testing.T) {
	renderer := &testModalRenderer{}
	server, err := NewModalServer(nil, renderer)
	if err != nil {
		t.Fatal(err)
	}
	registerLegacyModalForTest(t, server)
	server.pollTimeout = 20 * time.Millisecond
	response := callModalServer(t, server, map[string]any{
		"type": "open", "id": "black-box", "title": "Black Box",
		"actions": []map[string]any{{"name": "submit", "label": "Submit", "key": "enter"}, {"name": "quit", "label": "Quit", "key": "q"}},
	})
	if !response.OK {
		t.Fatalf("open response = %#v", response)
	}
	if _, err := server.HandleInput([]byte("\r")); err != nil {
		t.Fatal(err)
	}
	response = callModalServer(t, server, map[string]any{"type": "poll", "id": "black-box"})
	if response.Event == nil || response.Event.Type != "action" || response.Event.ID != "black-box" || response.Event.Generation != 1 || response.Event.ActionName != "submit" || response.Event.Key != "enter" {
		t.Fatalf("action poll response = %#v", response)
	}
	if _, err := server.HandleInput([]byte("q")); err != nil {
		t.Fatal(err)
	}
	response = callModalServer(t, server, map[string]any{"type": "poll", "id": "black-box"})
	if response.Event == nil || response.Event.Type != "action" || response.Event.ID != "black-box" || response.Event.Generation != 1 || response.Event.ActionName != "quit" || response.Event.Key != "q" {
		t.Fatalf("q action poll response = %#v", response)
	}
	if server.ActiveCount() != 1 {
		t.Fatalf("q action closed modal unexpectedly")
	}
	server.pendingEscapeDelay = time.Millisecond
	if _, err := server.HandleInput([]byte("\x1b")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	response = callModalServer(t, server, map[string]any{"type": "poll", "id": "black-box"})
	if response.Event == nil || response.Event.Type != "close" || response.Event.ID != "black-box" || response.Event.Generation != 1 || response.Event.Key != "escape" {
		t.Fatalf("close poll response = %#v", response)
	}
	response = callModalServer(t, server, map[string]any{"type": "poll", "id": "black-box"})
	if response.Event != nil {
		t.Fatalf("close generated duplicate event: %#v", response)
	}
	if server.ActiveCount() != 0 || renderer.hidden != 1 {
		t.Fatalf("active=%d hidden=%d", server.ActiveCount(), renderer.hidden)
	}
}

func TestModalServerFocusesAndActivatesModalActionBar(t *testing.T) {
	var output bytes.Buffer
	renderer := NewTerminalModalRendererWithSize(&output, Size{Cols: 96, Rows: 24})
	server, err := NewModalServer(nil, renderer)
	if err != nil {
		t.Fatal(err)
	}
	registerLegacyModalForTest(t, server)
	server.pollTimeout = 20 * time.Millisecond
	response := callModalServer(t, server, map[string]any{
		"type": "open", "id": "black-box", "title": "Actions", "body": "Use the action bar.",
		"actions": []map[string]any{
			{"name": "refresh", "label": "Refresh", "key": "r"},
			{"name": "details", "label": "Details", "key": "d"},
		},
	})
	if !response.OK {
		t.Fatalf("open response = %#v", response)
	}
	if text := output.String(); !strings.Contains(text, "▶ [r] Refresh ◀") {
		t.Fatalf("initial action focus not visible: %q", text)
	}

	output.Reset()
	if _, err := server.HandleInput([]byte("\t")); err != nil {
		t.Fatal(err)
	}
	if text := output.String(); !strings.Contains(text, "▶ [d] Details ◀") || strings.Contains(text, "▶ [r] Refresh ◀") {
		t.Fatalf("tab did not move visible action focus: %q", text)
	}
	if _, err := server.HandleInput([]byte("\r")); err != nil {
		t.Fatal(err)
	}
	response = callModalServer(t, server, map[string]any{"type": "poll", "id": "black-box"})
	if response.Event == nil || response.Event.Type != "action" || response.Event.ActionName != "details" || response.Event.Key != "enter" {
		t.Fatalf("focused enter action response = %#v", response)
	}

	output.Reset()
	if _, err := server.HandleInput([]byte("\x1b[Z")); err != nil {
		t.Fatal(err)
	}
	if text := output.String(); !strings.Contains(text, "▶ [r] Refresh ◀") || strings.Contains(text, "▶ [d] Details ◀") {
		t.Fatalf("CSI shift-tab did not move visible action focus: %q", text)
	}
	if _, err := server.HandleInput([]byte("\t")); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if _, err := server.HandleInput([]byte("\x1b[9;15;0;1;16;1_")); err != nil {
		t.Fatal(err)
	}
	if text := output.String(); !strings.Contains(text, "▶ [r] Refresh ◀") || strings.Contains(text, "▶ [d] Details ◀") {
		t.Fatalf("Windows VT shift-tab did not move visible action focus: %q", text)
	}
	if _, err := server.HandleInput([]byte("\x1b[32;57;32;1;0;1_")); err != nil {
		t.Fatal(err)
	}
	response = callModalServer(t, server, map[string]any{"type": "poll", "id": "black-box"})
	if response.Event == nil || response.Event.Type != "action" || response.Event.ActionName != "refresh" || response.Event.Key != "space" {
		t.Fatalf("focused space action response = %#v", response)
	}
	if server.ActiveCount() != 1 {
		t.Fatalf("focused action activation closed modal unexpectedly")
	}
}

func TestModalServerRoutesBatchedActionKeysAndEscape(t *testing.T) {
	renderer := &testModalRenderer{}
	server, err := NewModalServer(nil, renderer)
	if err != nil {
		t.Fatal(err)
	}
	registerLegacyModalForTest(t, server)
	server.pollTimeout = 20 * time.Millisecond
	response := callModalServer(t, server, map[string]any{
		"type": "open", "id": "black-box", "title": "Black Box",
		"actions": []map[string]any{
			{"name": "refresh", "label": "Refresh", "key": "r"},
			{"name": "doctor", "label": "Doctor", "key": "d"},
			{"name": "close", "label": "Close", "key": "q"},
		},
	})
	if !response.OK {
		t.Fatalf("open response = %#v", response)
	}
	if _, err := server.HandleInput([]byte("rdq\x1b[12;34R")); err != nil {
		t.Fatal(err)
	}
	for _, want := range []struct {
		name string
		key  string
	}{
		{"refresh", "r"},
		{"doctor", "d"},
		{"close", "q"},
	} {
		response = callModalServer(t, server, map[string]any{"type": "poll", "id": "black-box"})
		if response.Event == nil || response.Event.Type != "action" || response.Event.ActionName != want.name || response.Event.Key != want.key {
			t.Fatalf("action poll response = %#v, want %s/%s", response, want.name, want.key)
		}
	}
	if server.ActiveCount() != 1 {
		t.Fatalf("action keys closed modal unexpectedly")
	}
	if _, err := server.HandleInput([]byte("\x1bq")); err != nil {
		t.Fatal(err)
	}
	response = callModalServer(t, server, map[string]any{"type": "poll", "id": "black-box"})
	if response.Event == nil || response.Event.Type != "close" || response.Event.Key != "escape" {
		t.Fatalf("batched escape close response = %#v", response)
	}
	if server.ActiveCount() != 0 || renderer.hidden != 1 {
		t.Fatalf("active=%d hidden=%d", server.ActiveCount(), renderer.hidden)
	}
}
func TestModalServerRoutesWindowsVTKeyEvents(t *testing.T) {
	renderer := &testModalRenderer{}
	server, err := NewModalServer(nil, renderer)
	if err != nil {
		t.Fatal(err)
	}
	registerLegacyModalForTest(t, server)
	server.pollTimeout = 20 * time.Millisecond
	response := callModalServer(t, server, map[string]any{
		"type": "open", "id": "black-box", "title": "Black Box",
		"actions": []map[string]any{
			{"name": "refresh", "label": "Refresh", "key": "r"},
			{"name": "doctor", "label": "Doctor", "key": "d"},
			{"name": "close", "label": "Close", "key": "q"},
		},
	})
	if !response.OK {
		t.Fatalf("open response = %#v", response)
	}
	if _, err := server.HandleInput([]byte("\x1b[82;19;114;1;0;1_\x1b[82;19;114;0;0;1_\x1b[68;32;100;1;0;1_\x1b[81;16;113;1;0;1_")); err != nil {
		t.Fatal(err)
	}
	for _, want := range []struct {
		name string
		key  string
	}{
		{"refresh", "r"},
		{"doctor", "d"},
		{"close", "q"},
	} {
		response = callModalServer(t, server, map[string]any{"type": "poll", "id": "black-box"})
		if response.Event == nil || response.Event.Type != "action" || response.Event.ActionName != want.name || response.Event.Key != want.key {
			t.Fatalf("Windows VT action response = %#v, want %s/%s", response, want.name, want.key)
		}
	}
	if _, err := server.HandleInput([]byte("\x1b[27;1;0;1;0;1_\x1b[27;1;0;0;0;1_")); err != nil {
		t.Fatal(err)
	}
	response = callModalServer(t, server, map[string]any{"type": "poll", "id": "black-box"})
	if response.Event == nil || response.Event.Type != "close" || response.Event.Key != "escape" {
		t.Fatalf("Windows VT escape response = %#v", response)
	}
}

func TestModalServerGenerationProtocolAndReopenIsolation(t *testing.T) {
	renderer := &testModalRenderer{}
	server, err := NewModalServer(nil, renderer)
	if err != nil {
		t.Fatal(err)
	}
	registerLegacyModalForTest(t, server)
	server.pollTimeout = 20 * time.Millisecond

	response := callModalServer(t, server, map[string]any{"type": "open", "id": "black-box", "generation": int64(7), "title": "first"})
	if !response.OK {
		t.Fatalf("open gen 7 response = %#v", response)
	}
	response = callModalServer(t, server, map[string]any{"type": "update", "id": "black-box", "generation": int64(6), "body": "stale"})
	if response.OK || response.Error != "modal-stale-generation" || renderer.shown[len(renderer.shown)-1].Body == "stale" {
		t.Fatalf("stale update response=%#v frames=%#v", response, renderer.shown)
	}
	if _, err := server.HandleInput([]byte("q")); err != nil {
		t.Fatal(err)
	}
	response = callModalServer(t, server, map[string]any{"type": "poll", "id": "black-box", "generation": int64(7)})
	if response.Event == nil || response.Event.Type != "close" || response.Event.ID != "black-box" || response.Event.Generation != 7 || response.Event.Key != "q" {
		t.Fatalf("gen 7 close event = %#v", response)
	}

	response = callModalServer(t, server, map[string]any{"type": "open", "id": "black-box", "generation": int64(8), "title": "second"})
	if !response.OK || server.ActiveCount() != 1 {
		t.Fatalf("open gen 8 response = %#v active=%d", response, server.ActiveCount())
	}
	response = callModalServer(t, server, map[string]any{"type": "close", "id": "black-box", "generation": int64(7)})
	if response.OK || response.Error != "modal-stale-generation" || server.ActiveCount() != 1 {
		t.Fatalf("stale close response=%#v active=%d", response, server.ActiveCount())
	}
	response = callModalServer(t, server, map[string]any{"type": "poll", "id": "black-box", "generation": int64(7)})
	if response.OK || response.Error != "modal-stale-generation" {
		t.Fatalf("stale poll response = %#v", response)
	}
	response = callModalServer(t, server, map[string]any{"type": "close", "id": "black-box", "generation": int64(8)})
	if !response.OK {
		t.Fatalf("close gen 8 response = %#v", response)
	}
	response = callModalServer(t, server, map[string]any{"type": "poll", "id": "black-box", "generation": int64(8)})
	if response.Event == nil || response.Event.Type != "closed" || response.Event.ID != "black-box" || response.Event.Generation != 8 {
		t.Fatalf("closed event = %#v", response)
	}
}

func TestModalServerRejectsMalformedOversizedUnknownAndSecondOpen(t *testing.T) {
	server, err := NewModalServer(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	registerLegacyModalForTest(t, server)
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"malformed", "{not-json}\n", "modal-invalid-request"},
		{"unknown-field", `{"operation":"open","id":"black-box","ownerExtensionId":"black-box","canvasId":"black-box","surfaceId":"black-box","generation":1,"extra":true}` + "\n", "modal-invalid-request"},
		{"unknown-type", `{"operation":"bogus","id":"black-box","ownerExtensionId":"black-box","canvasId":"black-box","surfaceId":"black-box","generation":1}` + "\n", "modal-unknown-type"},
		{"missing-operation", `{"id":"black-box","ownerExtensionId":"black-box","canvasId":"black-box","surfaceId":"black-box","generation":1}` + "\n", "modal-invalid-operation"},
		{"missing-generation", `{"operation":"open","id":"black-box","ownerExtensionId":"black-box","canvasId":"black-box","surfaceId":"black-box"}` + "\n", "modal-invalid-generation"},
		{"oversized-field", fmt.Sprintf(`{"operation":"open","id":"black-box","ownerExtensionId":"black-box","canvasId":"black-box","surfaceId":"black-box","generation":1,"title":%q}`+"\n", strings.Repeat("x", modalMaxFieldBytes+1)), "modal-field-too-large"},
		{"too-many-actions", fmt.Sprintf(`{"operation":"open","id":"black-box","ownerExtensionId":"black-box","canvasId":"black-box","surfaceId":"black-box","generation":1,"actions":%s}`+"\n", tooManyActionsJSON(t)), "modal-too-many-actions"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			response := callModalServerRaw(t, server, tt.raw)
			if response.OK || response.Error != tt.want {
				t.Fatalf("response = %#v, want error %s", response, tt.want)
			}
		})
	}
	response := callModalServer(t, server, map[string]any{"type": "open", "id": "black-box"})
	if !response.OK {
		t.Fatalf("open response = %#v", response)
	}
	response = callModalServer(t, server, map[string]any{"operation": "open", "id": "other-box", "ownerExtensionId": ModalLegacyOwnerExtensionID, "canvasId": "other-box", "surfaceId": "other-box"})
	if response.OK || response.Error != "modal-unknown-canvas" {
		t.Fatalf("unregistered canvas response = %#v", response)
	}
}

func TestModalServerCloseAllRestoresOwnerAndCleansActiveModal(t *testing.T) {
	process := &fakeProcess{}
	backend := &fakeBackend{process: process}
	broker := NewBroker(backend, BrokerOptions{})
	if err := broker.Start(t.Context(), Command{Path: "copilot"}); err != nil {
		t.Fatal(err)
	}
	renderer := &testModalRenderer{}
	server, err := NewModalServer(broker, renderer)
	if err != nil {
		t.Fatal(err)
	}
	registerLegacyModalForTest(t, server)
	response := callModalServer(t, server, map[string]any{"type": "open", "id": "black-box"})
	if !response.OK {
		t.Fatalf("open response = %#v", response)
	}
	server.CloseAll()
	if broker.Owner() != OwnerCopilot || server.ActiveCount() != 0 || renderer.hidden != 1 {
		t.Fatalf("owner=%s active=%d hidden=%d", broker.Owner(), server.ActiveCount(), renderer.hidden)
	}
}

func TestModalServerRejectsOversizedFrame(t *testing.T) {
	server, err := NewModalServer(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	response := callModalServerRaw(t, server, strings.Repeat("x", modalMaxMessageBytes+1)+"\n")
	if response.OK || response.Error != "modal-message-too-large" {
		t.Fatalf("response = %#v", response)
	}
}

func TestVTScreenModelsCurrentScreen(t *testing.T) {
	screen := newVTScreen(Size{Cols: 10, Rows: 3})
	screen.Consume([]byte("one\r\ntwo\r\nthree\r\nfour"))
	repaint := screen.Repaint()
	if strings.Contains(repaint, "one") || !strings.Contains(repaint, "two") || !strings.Contains(repaint, "four") {
		t.Fatalf("unexpected repaint after scroll: %q", repaint)
	}
	screen.Consume([]byte("\x1b[2J\x1b[Hfresh"))
	repaint = screen.Repaint()
	if strings.Contains(repaint, "two") || !strings.Contains(repaint, "fresh") {
		t.Fatalf("unexpected repaint after clear: %q", repaint)
	}
}

func TestVTScreenFullWidthEraseAtRightEdge(t *testing.T) {
	for _, seq := range []string{"\x1b[1J", "\x1b[1K", "\x1b[J", "\x1b[K", "\x1b[1X", "\x1b[1P", "\x1b[1@"} {
		t.Run(fmt.Sprintf("%q", seq), func(t *testing.T) {
			screen := newVTScreen(Size{Cols: 5, Rows: 2})
			screen.Consume([]byte("abc界\x1b[5G" + seq + "z"))
			repaint := screen.Repaint()
			if !strings.Contains(repaint, "z") {
				t.Fatalf("screen did not continue after full-width edge erase %q: %q", seq, repaint)
			}
		})
	}
}

func TestVTScreenFullWidthContinuationBoundaries(t *testing.T) {
	t.Run("erase", func(t *testing.T) {
		screen := newVTScreen(Size{Cols: 6, Rows: 1})
		screen.Consume([]byte("a界bc\x1b[3G\x1b[1X"))
		assertVTRowCells(t, screen, []rune{'a', ' ', ' ', 'b', 'c', ' '})
	})
	t.Run("delete", func(t *testing.T) {
		screen := newVTScreen(Size{Cols: 6, Rows: 1})
		screen.Consume([]byte("a界bc\x1b[3G\x1b[1P"))
		assertVTRowCells(t, screen, []rune{'a', 'b', 'c', ' ', ' ', ' '})
	})
	t.Run("insert", func(t *testing.T) {
		screen := newVTScreen(Size{Cols: 6, Rows: 1})
		screen.Consume([]byte("a界bc\x1b[3G\x1b[1@"))
		assertVTRowCells(t, screen, []rune{'a', ' ', '界', vtWideContinuation, 'b', 'c'})
	})
	t.Run("right-edge-cursor-continuation", func(t *testing.T) {
		screen := newVTScreen(Size{Cols: 5, Rows: 1})
		screen.Consume([]byte("abc界\x1b[5G\x1b[1Xz"))
		assertVTRowCells(t, screen, []rune{'a', 'b', 'c', ' ', 'z'})
	})
}

func assertVTRowCells(t *testing.T, screen *vtScreen, want []rune) {
	t.Helper()
	got := screen.active().cells[0]
	if len(got) != len(want) {
		t.Fatalf("row width = %d, want %d", len(got), len(want))
	}
	for i := range got {
		if vtCellRune(got[i]) != want[i] {
			t.Fatalf("row cells = %q, want %q", formatVTCellRow(got), formatRuneRow(want))
		}
	}
}

func vtCellRune(cell vtCell) rune {
	if cell.continuation {
		return vtWideContinuation
	}
	r, _ := utf8.DecodeRuneInString(cell.cluster)
	return r
}

func formatVTCellRow(row []vtCell) string {
	var out strings.Builder
	for _, cell := range row {
		if cell.continuation {
			out.WriteRune('·')
			continue
		}
		out.WriteString(cell.cluster)
	}
	return out.String()
}

func formatRuneRow(row []rune) string {
	var out strings.Builder
	for _, r := range row {
		if r == vtWideContinuation {
			out.WriteRune('·')
			continue
		}
		out.WriteRune(r)
	}
	return out.String()
}

func modalRequestMap(capability ModalCapability, requestType string, generation int64) map[string]any {
	return map[string]any{
		"operation":        requestType,
		"id":               capability.SurfaceID,
		"ownerExtensionId": capability.OwnerExtensionID,
		"canvasId":         capability.CanvasID,
		"surfaceId":        capability.SurfaceID,
		"generation":       generation,
	}
}

func addLegacyModalIdentity(request map[string]any) {
	if _, ok := request["operation"]; !ok {
		if requestType, ok := request["type"]; ok {
			request["operation"] = requestType
		}
	}
	if request["id"] == ModalLegacySurfaceID {
		if _, ok := request["ownerExtensionId"]; !ok {
			request["ownerExtensionId"] = ModalLegacyOwnerExtensionID
		}
		if _, ok := request["canvasId"]; !ok {
			request["canvasId"] = ModalLegacyCanvasID
		}
		if _, ok := request["surfaceId"]; !ok {
			request["surfaceId"] = ModalLegacySurfaceID
		}
	}
}

func callModalServer(t *testing.T, server *ModalServer, request map[string]any) modalResponse {
	t.Helper()
	addLegacyModalIdentity(request)
	if _, ok := request["generation"]; !ok {
		request["generation"] = int64(1)
	}
	var conn bytes.Buffer
	if err := json.NewEncoder(&conn).Encode(request); err != nil {
		t.Fatal(err)
	}
	return decodeModalResponse(t, server, &conn)
}

func callModalServerAuthenticated(t *testing.T, server *ModalServer, request map[string]any, pid uint32) modalResponse {
	t.Helper()
	addLegacyModalIdentity(request)
	if _, ok := request["generation"]; !ok {
		request["generation"] = int64(1)
	}
	var conn bytes.Buffer
	if err := json.NewEncoder(&conn).Encode(request); err != nil {
		t.Fatal(err)
	}
	server.HandleAuthenticatedConnection(&conn, pid)
	var response modalResponse
	if err := json.NewDecoder(&conn).Decode(&response); err != nil {
		t.Fatal(err)
	}
	return response
}

func callModalServerRaw(t *testing.T, server *ModalServer, raw string) modalResponse {
	t.Helper()
	conn := bytes.NewBufferString(raw)
	return decodeModalResponse(t, server, conn)
}

func callModalServerRawAuthenticated(t *testing.T, server *ModalServer, raw string, pid uint32) modalResponse {
	t.Helper()
	conn := bytes.NewBufferString(raw)
	server.HandleAuthenticatedConnection(conn, pid)
	var response modalResponse
	if err := json.NewDecoder(conn).Decode(&response); err != nil {
		t.Fatal(err)
	}
	return response
}

func decodeModalResponse(t *testing.T, server *ModalServer, conn *bytes.Buffer) modalResponse {
	t.Helper()
	server.HandleConnection(conn)
	var response modalResponse
	if err := json.NewDecoder(conn).Decode(&response); err != nil {
		t.Fatal(err)
	}
	return response
}

func tooManyActionsJSON(t *testing.T) string {
	t.Helper()
	actions := make([]map[string]string, modalMaxActions+1)
	for i := range actions {
		actions[i] = map[string]string{"name": fmt.Sprintf("a%d", i), "label": "A"}
	}
	data, err := json.Marshal(actions)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
