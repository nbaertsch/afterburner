package terminal

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
)

// ModalAction describes a host-rendered action advertised by a modal canvas.
type ModalAction struct {
	Name        string `json:"name"`
	Label       string `json:"label"`
	Key         string `json:"key,omitempty"`
	Description string `json:"description,omitempty"`
}

// ModalFrame is the structured, host-rendered content an extension sends to
// present or update a modal canvas. It intentionally excludes raw ANSI: the
// host owns rendering, focus, and terminal-mode handling.
type ModalFrame struct {
	ID       string          `json:"id"`
	Title    string          `json:"title"`
	Status   string          `json:"status"`
	Body     string          `json:"body"`
	Footer   string          `json:"footer"`
	Actions  []ModalAction   `json:"actions,omitempty"`
	Document json.RawMessage `json:"document,omitempty"`
}

// ModalEvent is sent back to the extension runtime when a human key maps to
// a modal action or close request.
type ModalEvent struct {
	Type             string `json:"type"`
	ID               string `json:"id"`
	Generation       int64  `json:"generation"`
	Sequence         int64  `json:"sequence"`
	TargetID         string `json:"targetId,omitempty"`
	DocumentRevision int64  `json:"documentRevision,omitempty"`
	ActionName       string `json:"actionName,omitempty"`
	Key              string `json:"key,omitempty"`
	Value            any    `json:"value,omitempty"`
}

const (
	modalMaxMessageBytes     = 256 * 1024
	modalMaxFieldBytes       = 64 * 1024
	modalMaxActions          = 16
	modalMaxGeneration       = 1<<53 - 1
	modalMaxDocumentNodes    = 512
	modalMaxDocumentDepth    = 16
	modalMaxDocumentChildren = 64
	modalMaxQueuedEvents     = 64

	// ModalLegacy* identifies the explicitly registered compatibility surface used
	// by older Black Box modal clients. It is not inferred from missing identity.
	ModalLegacyOwnerExtensionID = "black-box"
	ModalLegacyCanvasID         = "black-box"
	ModalLegacySurfaceID        = "black-box"
)

var (
	validModalID         = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	validModalOwnerID    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:@/-]{0,127}$`)
	validModalActionName = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,63}$`)
)

var modalDocumentKinds = map[string]struct{}{
	"dialog": {}, "application": {}, "surface": {}, "window": {}, "viewport": {},
	"stack": {}, "row": {}, "column": {}, "group": {}, "grid": {}, "statusGrid": {},
	"split": {}, "section": {}, "box": {}, "disclosure": {}, "panel": {}, "card": {},
	"scroll": {}, "toolbar": {}, "separator": {}, "spacer": {}, "breadcrumb": {},
	"contextMenu": {}, "icon": {}, "text": {}, "markdown": {}, "code": {},
	"keyValue": {}, "detail": {}, "badge": {}, "alert": {}, "toast": {},
	"progress": {}, "meter": {}, "bar": {}, "sparkline": {}, "spinner": {},
	"loading": {}, "errorBoundary": {}, "empty": {}, "list": {}, "table": {},
	"tree": {}, "timeline": {}, "tabs": {}, "log": {}, "commandPalette": {},
	"form": {}, "button": {}, "link": {}, "textInput": {}, "searchInput": {},
	"numberInput": {}, "dateInput": {}, "fileInput": {}, "passwordInput": {},
	"textArea": {}, "select": {}, "checkbox": {}, "radioGroup": {}, "toggle": {},
	"slider": {}, "actionBar": {}, "keybindingHint": {}, "pagination": {},
	"help": {}, "confirmation": {}, "prompt": {},
}

var modalInteractiveDocumentKinds = map[string]struct{}{
	"button": {}, "link": {}, "textInput": {}, "searchInput": {}, "numberInput": {},
	"dateInput": {}, "fileInput": {}, "passwordInput": {}, "textArea": {},
	"select": {}, "checkbox": {}, "radioGroup": {}, "toggle": {}, "slider": {},
	"tabs": {},
}

// ModalServer accepts structured open/update/close requests from the
// Copilot-side extension runtime over a private, connection-authenticated
// transport and drives a Broker's modal presentation. Copilot's own process,
// tools, and event emission are never paused by the server; only terminal
// *presentation* and human input ownership move to the modal while it is
// open.
type ModalServer struct {
	broker *Broker

	mu                   sync.Mutex
	registrations        map[modalIdentity]*modalRegistrationState
	authorizedClientPIDs map[uint32]struct{}
	requireClientAuth    bool
	active               map[modalIdentity]modalSession
	order                []modalIdentity
	waiters              map[modalEventKey][]chan ModalEvent
	events               map[modalEventKey][]ModalEvent
	eventSequences       map[modalEventKey]int64
	pendingControlValues map[modalEventKey]map[string]modalPendingControlValue
	renderer             ModalRenderer
	pollTimeout          time.Duration
	pendingInput         []byte
	pendingEscapeTimer   *time.Timer
	pendingEscapeDelay   time.Duration
}

// ModalRegistration is supplied by trusted launch/runtime host integration.
// Pipe callers cannot create registrations; requests are accepted only for an
// identity that was registered server-side before use.
type ModalRegistration struct {
	OwnerExtensionID string
	CanvasID         string
	SurfaceID        string
}

// ModalCapability identifies a server-registered modal surface. It is not an
// authority token: pipe authorization is bound to the trusted native client
// connection, and extension code receives only owner-scoped closures.
type ModalCapability struct {
	OwnerExtensionID string `json:"ownerExtensionId"`
	CanvasID         string `json:"canvasId"`
	SurfaceID        string `json:"surfaceId"`
	Pipe             string `json:"pipe,omitempty"`
}

type modalIdentity struct {
	ownerExtensionID string
	canvasID         string
	surfaceID        string
}

type modalRegistrationState struct {
	maxGeneration int64
}

type modalSession struct {
	identity   modalIdentity
	frame      ModalFrame
	generation int64
}

type modalEventKey struct {
	identity   modalIdentity
	generation int64
}

type modalPendingControlValue struct {
	sequence int64
	value    any
}

func printableModalText(value string, multiline bool) string {
	value = stripModalANSI(value)
	var out strings.Builder
	for _, r := range value {
		switch {
		case r == '\n' && multiline:
			out.WriteRune('\n')
		case r == '\r' && multiline:
			out.WriteRune('\n')
		case r == '\t':
			out.WriteRune('\t')
		case r < 0x20 || r == 0x7f:
			out.WriteRune(' ')
		default:
			out.WriteRune(r)
		}
	}
	return out.String()
}

func stripModalANSI(value string) string {
	var out strings.Builder
	state := 0
	for i := 0; i < len(value); i++ {
		ch := value[i]
		switch state {
		case 0:
			if ch == 0x1b {
				state = 1
				continue
			}
			out.WriteByte(ch)
		case 1:
			switch ch {
			case '[':
				state = 2
			case ']':
				state = 3
			case 'P', '^', '_':
				state = 4
			default:
				state = 0
			}
		case 2:
			if ch >= 0x40 && ch <= 0x7e {
				state = 0
			}
		case 3:
			if ch == 0x07 {
				state = 0
			} else if ch == 0x1b {
				state = 5
			}
		case 4:
			if ch == 0x1b {
				state = 5
			}
		case 5:
			if ch == '\\' {
				state = 0
			} else if ch != 0x1b {
				state = 4
			}
		}
	}
	return out.String()
}

// ModalRenderer paints the active modal frame atop the terminal that the
// Broker's ConPTY output is being written to. HideModal is called when the
// last modal closes so the host can repaint Copilot's live screen.
type ModalRenderer interface {
	ShowModal(frame ModalFrame)
	HideModal()
}

type modalScrollRenderer interface {
	ScrollModal(key string) bool
}

type modalClickRenderer interface {
	ClickModal(row, col int) (string, bool)
}

type modalActionFocusRenderer interface {
	FocusModalAction(delta int) bool
	ActivateFocusedModalAction(trigger string) (string, string, bool)
}

type modalControlEvent struct {
	Type       string
	TargetID   string
	ActionName string
	Key        string
	Value      any
}

type modalControlInputRenderer interface {
	HandleModalControlInput(key string) (*modalControlEvent, bool)
}

type modalControlValueRenderer interface {
	ApplyModalControlValue(id string, value any)
}

type modalControlClickRenderer interface {
	ClickModalControl(row, col int) (*modalControlEvent, bool)
}

func NewModalServer(broker *Broker, renderer ModalRenderer) (*ModalServer, error) {
	server := &ModalServer{
		broker:               broker,
		registrations:        make(map[modalIdentity]*modalRegistrationState),
		authorizedClientPIDs: make(map[uint32]struct{}),
		active:               make(map[modalIdentity]modalSession),
		waiters:              make(map[modalEventKey][]chan ModalEvent),
		events:               make(map[modalEventKey][]ModalEvent),
		eventSequences:       make(map[modalEventKey]int64),
		pendingControlValues: make(map[modalEventKey]map[string]modalPendingControlValue),
		renderer:             renderer,
		pollTimeout:          30 * time.Second,
		pendingEscapeDelay:   50 * time.Millisecond,
	}
	return server, nil
}

// RequireAuthenticatedClients makes wire requests fail closed unless the
// transport supplies an authorized native client process identity.
func (s *ModalServer) RequireAuthenticatedClients() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requireClientAuth = true
}

// AuthorizeClientProcess records the child runtime process that may use the
// modal control channel. Authorization is connection-bound, not provided in
// each JSON frame.
func (s *ModalServer) AuthorizeClientProcess(pid uint32) {
	if pid == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requireClientAuth = true
	s.authorizedClientPIDs[pid] = struct{}{}
}

func (s *ModalServer) clientAuthorized(pid uint32, authenticated bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.requireClientAuth {
		return true
	}
	if !authenticated || pid == 0 {
		return false
	}
	return modalClientMatchesAuthorizedPID(pid, s.authorizedClientPIDs)
}

// RegisterModalCanvas registers a modal surface from trusted native launch or
// runtime-host code. The returned descriptor contains no bearer material.
func (s *ModalServer) RegisterModalCanvas(reg ModalRegistration) (ModalCapability, error) {
	identity, err := normalizeModalRegistration(reg)
	if err != nil {
		return ModalCapability{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.registrations[identity]; !exists {
		s.registrations[identity] = &modalRegistrationState{}
	}
	return ModalCapability{
		OwnerExtensionID: identity.ownerExtensionID,
		CanvasID:         identity.canvasID,
		SurfaceID:        identity.surfaceID,
	}, nil
}

func normalizeModalRegistration(reg ModalRegistration) (modalIdentity, error) {
	reg.OwnerExtensionID = strings.TrimSpace(reg.OwnerExtensionID)
	reg.CanvasID = strings.TrimSpace(reg.CanvasID)
	reg.SurfaceID = strings.TrimSpace(reg.SurfaceID)
	if reg.SurfaceID == "" {
		reg.SurfaceID = reg.CanvasID
	}
	if !validModalOwnerID.MatchString(reg.OwnerExtensionID) {
		return modalIdentity{}, fmt.Errorf("invalid modal owner extension id: %q", reg.OwnerExtensionID)
	}
	if !validModalID.MatchString(reg.CanvasID) {
		return modalIdentity{}, fmt.Errorf("invalid modal canvas id: %q", reg.CanvasID)
	}
	if !validModalID.MatchString(reg.SurfaceID) {
		return modalIdentity{}, fmt.Errorf("invalid modal surface id: %q", reg.SurfaceID)
	}
	return modalIdentity{ownerExtensionID: reg.OwnerExtensionID, canvasID: reg.CanvasID, surfaceID: reg.SurfaceID}, nil
}

func (s *ModalServer) authorize(req modalRequest) (modalIdentity, modalResponse) {
	identity, response := req.identity()
	if response.Error != "" {
		return modalIdentity{}, response
	}
	s.mu.Lock()
	_, registered := s.registrations[identity]
	s.mu.Unlock()
	if !registered {
		return modalIdentity{}, modalResponse{Error: "modal-unknown-canvas"}
	}
	return identity, modalResponse{}
}

type modalRequest struct {
	Operation        string          `json:"operation"`
	Type             string          `json:"type"`
	ID               string          `json:"id"`
	OwnerExtensionID string          `json:"ownerExtensionId"`
	CanvasID         string          `json:"canvasId"`
	SurfaceID        string          `json:"surfaceId"`
	Generation       *int64          `json:"generation"`
	Title            string          `json:"title"`
	Status           string          `json:"status"`
	Body             string          `json:"body"`
	Footer           string          `json:"footer"`
	Actions          []ModalAction   `json:"actions"`
	Document         json.RawMessage `json:"document,omitempty"`
	AckEventSequence *int64          `json:"ackEventSequence,omitempty"`
}

type modalResponse struct {
	OK    bool        `json:"ok"`
	Error string      `json:"error,omitempty"`
	Event *ModalEvent `json:"event,omitempty"`
}

func (r modalRequest) operation() (string, modalResponse) {
	operation := strings.TrimSpace(r.Operation)
	if operation == "" {
		return "", modalResponse{Error: "modal-invalid-operation"}
	}
	if r.Type != "" && r.Type != operation {
		return "", modalResponse{Error: "modal-invalid-operation"}
	}
	return operation, modalResponse{}
}

func (r modalRequest) identity() (modalIdentity, modalResponse) {
	owner := strings.TrimSpace(r.OwnerExtensionID)
	canvas := strings.TrimSpace(r.CanvasID)
	surface := strings.TrimSpace(r.SurfaceID)
	if owner == "" || canvas == "" || surface == "" {
		return modalIdentity{}, modalResponse{Error: "modal-invalid-identity"}
	}
	if !validModalOwnerID.MatchString(owner) || !validModalID.MatchString(canvas) || !validModalID.MatchString(surface) {
		return modalIdentity{}, modalResponse{Error: "modal-invalid-identity"}
	}
	if r.ID != surface {
		return modalIdentity{}, modalResponse{Error: "modal-identity-mismatch"}
	}
	return modalIdentity{ownerExtensionID: owner, canvasID: canvas, surfaceID: surface}, modalResponse{}
}

// HandleConnection reads exactly one JSON request from a trusted in-process
// caller, applies it, and writes exactly one JSON response. Wire transports
// must use HandleAuthenticatedConnection so authorization is bound to the
// native connection instead of bearer data in JSON.
func (s *ModalServer) HandleConnection(conn io.ReadWriter) {
	response := s.handleOne(conn, true, nil)
	encoded, err := json.Marshal(response)
	if err != nil {
		encoded = []byte(`{"ok":false,"error":"modal-encode-failed"}`)
	}
	_, _ = conn.Write(append(encoded, '\n'))
}

func (s *ModalServer) HandleAuthenticatedConnection(conn io.ReadWriter, clientPID uint32) {
	s.HandleAuthorizedSurfaceConnection(conn, clientPID, nil)
}

func (s *ModalServer) HandleAuthorizedSurfaceConnection(conn io.ReadWriter, clientPID uint32, allowedIdentity *modalIdentity) {
	response := s.handleOne(conn, s.clientAuthorized(clientPID, true), allowedIdentity)
	encoded, err := json.Marshal(response)
	if err != nil {
		encoded = []byte(`{"ok":false,"error":"modal-encode-failed"}`)
	}
	_, _ = conn.Write(append(encoded, '\n'))
}

func (s *ModalServer) handleOne(conn io.Reader, connectionAuthorized bool, allowedIdentity *modalIdentity) modalResponse {
	line, err := readModalRequestLine(conn)
	if errors.Is(err, errModalMessageTooLarge) {
		return modalResponse{Error: "modal-message-too-large"}
	}
	if err != nil {
		if line == "" {
			return modalResponse{Error: "modal-read-failed"}
		}
		return modalResponse{Error: "modal-invalid-request"}
	}
	var req modalRequest
	decoder := json.NewDecoder(strings.NewReader(line))
	decoder.DisallowUnknownFields()
	if decodeErr := decoder.Decode(&req); decodeErr != nil {
		return modalResponse{Error: "modal-invalid-request"}
	}
	if decodeErr := decoder.Decode(&struct{}{}); decodeErr != io.EOF {
		return modalResponse{Error: "modal-invalid-request"}
	}
	operation, operationResponse := req.operation()
	if operationResponse.Error != "" {
		return operationResponse
	}
	if !connectionAuthorized {
		return modalResponse{Error: "modal-unauthorized"}
	}
	if !validModalID.MatchString(req.ID) {
		return modalResponse{Error: "modal-invalid-id"}
	}
	if operation == "register" {
		return modalResponse{Error: "modal-registration-disabled"}
	}
	identity, authResponse := s.authorize(req)
	if authResponse.Error != "" {
		return authResponse
	}
	if allowedIdentity != nil && identity != *allowedIdentity {
		return modalResponse{Error: "modal-unauthorized-surface"}
	}
	if req.Generation == nil || *req.Generation < 0 || *req.Generation > modalMaxGeneration {
		return modalResponse{Error: "modal-invalid-generation"}
	}
	if req.AckEventSequence != nil && (*req.AckEventSequence < 0 || *req.AckEventSequence > modalMaxGeneration) {
		return modalResponse{Error: "modal-invalid-event-sequence"}
	}
	if oversized(req.Title) || oversized(req.Status) || oversized(req.Body) || oversized(req.Footer) || len(req.Document) > modalMaxFieldBytes {
		return modalResponse{Error: "modal-field-too-large"}
	}
	if len(req.Actions) > modalMaxActions {
		return modalResponse{Error: "modal-too-many-actions"}
	}
	if len(req.Document) > 0 {
		if err := validateModalDocument(req.Document, req.SurfaceID); err != nil {
			return modalResponse{Error: "modal-invalid-document"}
		}
	}
	seenActions := map[string]struct{}{}
	for _, action := range req.Actions {
		if !validModalActionName.MatchString(action.Name) {
			return modalResponse{Error: "modal-invalid-action"}
		}
		if _, exists := seenActions[action.Name]; exists {
			return modalResponse{Error: "modal-duplicate-action"}
		}
		seenActions[action.Name] = struct{}{}
		if oversized(action.Label) || oversized(action.Key) || oversized(action.Description) {
			return modalResponse{Error: "modal-field-too-large"}
		}
	}
	switch operation {
	case "open":
		return s.open(req, identity)
	case "update":
		return s.update(req, identity)
	case "close":
		return s.close(identity, *req.Generation)
	case "poll":
		return s.poll(identity, *req.Generation)
	default:
		return modalResponse{Error: "modal-unknown-type"}
	}
}

var errModalMessageTooLarge = errors.New("modal message too large")

func readModalRequestLine(conn io.Reader) (string, error) {
	reader := bufio.NewReaderSize(conn, 4096)
	var line strings.Builder
	for {
		fragment, err := reader.ReadString('\n')
		if line.Len()+len(fragment) > modalMaxMessageBytes {
			return "", errModalMessageTooLarge
		}
		line.WriteString(fragment)
		if err == nil {
			return line.String(), nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return line.String(), err
	}
}

func oversized(field string) bool { return len(field) > modalMaxFieldBytes }

func copyModalActions(actions []ModalAction) []ModalAction {
	if len(actions) == 0 {
		return nil
	}
	copied := make([]ModalAction, len(actions))
	copy(copied, actions)
	return copied
}

func copyModalDocument(document json.RawMessage) json.RawMessage {
	if len(document) == 0 {
		return nil
	}
	copied := make(json.RawMessage, len(document))
	copy(copied, document)
	return copied
}

func (s *ModalServer) open(req modalRequest, identity modalIdentity) modalResponse {
	generation := *req.Generation
	frame := ModalFrame{ID: req.ID, Title: req.Title, Status: req.Status, Body: req.Body, Footer: req.Footer, Actions: copyModalActions(req.Actions), Document: copyModalDocument(req.Document)}
	s.mu.Lock()
	if len(s.order) > 0 && s.order[0] != identity {
		s.mu.Unlock()
		return modalResponse{Error: "modal-already-open"}
	}
	registration := s.registrations[identity]
	previous, exists := s.active[identity]
	if exists && generation < previous.generation {
		s.mu.Unlock()
		return modalResponse{Error: "modal-stale-generation"}
	}
	if exists && generation > previous.generation {
		s.mu.Unlock()
		return modalResponse{Error: "modal-already-open"}
	}
	if !exists && registration != nil && registration.maxGeneration >= generation {
		s.mu.Unlock()
		return modalResponse{Error: "modal-stale-generation"}
	}
	if !exists {
		s.order = []modalIdentity{identity}
	}
	if registration != nil && registration.maxGeneration < generation {
		registration.maxGeneration = generation
	}
	s.active[identity] = modalSession{identity: identity, frame: frame, generation: generation}
	renderer := s.renderer
	s.mu.Unlock()
	if s.broker != nil {
		s.broker.SetOwner(OwnerModal)
	}
	if renderer != nil {
		renderer.ShowModal(frame)
	}
	return modalResponse{OK: true}
}

func (s *ModalServer) update(req modalRequest, identity modalIdentity) modalResponse {
	generation := *req.Generation
	key := modalEventKey{identity: identity, generation: generation}
	s.mu.Lock()
	previous, exists := s.active[identity]
	if !exists {
		s.mu.Unlock()
		return modalResponse{Error: "modal-not-open"}
	}
	if previous.generation != generation {
		s.mu.Unlock()
		return modalResponse{Error: "modal-stale-generation"}
	}
	if nextRevision, previousRevision := modalDocumentRevision(req.Document), modalDocumentRevision(previous.frame.Document); nextRevision > 0 && previousRevision > 0 && nextRevision <= previousRevision {
		s.mu.Unlock()
		return modalResponse{Error: "modal-stale-document"}
	}
	frame := mergeModalFrame(previous.frame, req)
	if req.AckEventSequence != nil {
		pending := s.pendingControlValues[key]
		for id, value := range pending {
			if value.sequence <= *req.AckEventSequence {
				delete(pending, id)
			}
		}
		if len(pending) == 0 {
			delete(s.pendingControlValues, key)
		}
	}
	frame.Document = modalDocumentWithPendingControlValues(frame.Document, s.pendingControlValues[key])
	s.active[identity] = modalSession{identity: identity, frame: frame, generation: generation}
	renderer := s.renderer
	s.mu.Unlock()
	if renderer != nil {
		renderer.ShowModal(frame)
	}
	return modalResponse{OK: true}
}

func mergeModalFrame(previous ModalFrame, req modalRequest) ModalFrame {
	frame := previous
	if req.Title != "" {
		frame.Title = req.Title
	}
	if req.Status != "" || previous.Status == "" {
		frame.Status = req.Status
	}
	if req.Body != "" || previous.Body == "" {
		frame.Body = req.Body
	}
	if req.Footer != "" || previous.Footer == "" {
		frame.Footer = req.Footer
	}
	if req.Actions != nil {
		frame.Actions = copyModalActions(req.Actions)
	}
	if req.Document != nil {
		frame.Document = copyModalDocument(req.Document)
	}
	return frame
}

func (s *ModalServer) poll(identity modalIdentity, generation int64) modalResponse {
	key := modalEventKey{identity: identity, generation: generation}
	waiter := make(chan ModalEvent, 1)
	s.mu.Lock()
	if active, exists := s.active[identity]; exists && active.generation != generation {
		s.mu.Unlock()
		return modalResponse{Error: "modal-stale-generation"}
	}
	if registration := s.registrations[identity]; registration != nil && registration.maxGeneration > generation {
		s.mu.Unlock()
		return modalResponse{Error: "modal-stale-generation"}
	}
	if queued := s.events[key]; len(queued) > 0 {
		event := queued[0]
		if len(queued) == 1 {
			delete(s.events, key)
		} else {
			s.events[key] = queued[1:]
		}
		s.mu.Unlock()
		return modalResponse{OK: true, Event: &event}
	}
	s.waiters[key] = append(s.waiters[key], waiter)
	s.mu.Unlock()

	timer := time.NewTimer(s.pollTimeout)
	defer timer.Stop()
	select {
	case event := <-waiter:
		return modalResponse{OK: true, Event: &event}
	case <-timer.C:
		s.removeWaiter(key, waiter)
		return modalResponse{OK: true}
	}
}

func (s *ModalServer) enqueueEvent(event ModalEvent, identity modalIdentity) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.enqueueEventLocked(event, identity)
}

func (s *ModalServer) enqueueEventLocked(event ModalEvent, identity modalIdentity) {
	key := modalEventKey{identity: identity, generation: event.Generation}
	s.eventSequences[key]++
	event.Sequence = s.eventSequences[key]
	if event.Type == "change" && event.TargetID != "" {
		pending := s.pendingControlValues[key]
		if pending == nil {
			pending = make(map[string]modalPendingControlValue)
			s.pendingControlValues[key] = pending
		}
		pending[event.TargetID] = modalPendingControlValue{sequence: event.Sequence, value: event.Value}
	}
	waiters := s.waiters[key]
	if len(waiters) > 0 {
		waiter := waiters[0]
		if len(waiters) == 1 {
			delete(s.waiters, key)
		} else {
			s.waiters[key] = waiters[1:]
		}
		waiter <- event
		return
	}
	queue := s.events[key]
	if len(queue) >= modalMaxQueuedEvents {
		copy(queue, queue[len(queue)-modalMaxQueuedEvents+1:])
		queue = queue[:modalMaxQueuedEvents-1]
	}
	s.events[key] = append(queue, event)
}

func (s *ModalServer) removeWaiter(key modalEventKey, target chan ModalEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	waiters := s.waiters[key]
	for index, waiter := range waiters {
		if waiter == target {
			s.waiters[key] = append(waiters[:index], waiters[index+1:]...)
			break
		}
	}
	if len(s.waiters[key]) == 0 {
		delete(s.waiters, key)
	}
}

func (s *ModalServer) close(identity modalIdentity, generation int64) modalResponse {
	event := &ModalEvent{Type: "closed", ID: identity.surfaceID, Generation: generation}
	closed, stale := s.closeWithEvents(identity, generation, event)
	if stale {
		return modalResponse{Error: "modal-stale-generation"}
	}
	if !closed {
		return modalResponse{Error: "modal-not-open"}
	}
	return modalResponse{OK: true}
}

func (s *ModalServer) requestClose(identity modalIdentity, generation int64, key string) {
	event := &ModalEvent{Type: "close", ID: identity.surfaceID, Generation: generation, Key: key}
	s.closeWithEvents(identity, generation, event)
}

func (s *ModalServer) closeWithEvents(identity modalIdentity, generation int64, event *ModalEvent) (bool, bool) {
	s.mu.Lock()
	active, exists := s.active[identity]
	if !exists {
		s.mu.Unlock()
		return false, false
	}
	if active.generation != generation {
		s.mu.Unlock()
		return false, true
	}
	if event != nil {
		s.enqueueEventLocked(*event, identity)
	}
	s.closeLocked(identity)
	renderer := s.renderer
	s.mu.Unlock()
	if s.broker != nil {
		s.broker.SetOwner(OwnerCopilot)
	}
	if renderer != nil {
		renderer.HideModal()
	}
	return true, false
}

func (s *ModalServer) closeLocked(identity modalIdentity) {
	delete(s.active, identity)
	for key := range s.pendingControlValues {
		if key.identity == identity {
			delete(s.pendingControlValues, key)
			delete(s.eventSequences, key)
		}
	}
	for index, existing := range s.order {
		if existing == identity {
			s.order = append(s.order[:index], s.order[index+1:]...)
			break
		}
	}
}

// HandleInput consumes human input while a modal owns the terminal. Matching
// action keys are sent back to the runtime event pump; Escape requests a
// graceful runtime close. Unhandled keys are swallowed so they cannot leak
// into Copilot while the modal is visible.
func (s *ModalServer) HandleInput(data []byte) (int, error) {
	inputLen := len(data)
	keys := s.collectInputKeys(data)
	for _, key := range keys {
		if key != "" {
			s.handleInputKey(key)
		}
	}
	return inputLen, nil
}

func (s *ModalServer) collectInputKeys(data []byte) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pendingEscapeTimer != nil {
		s.pendingEscapeTimer.Stop()
		s.pendingEscapeTimer = nil
	}
	if len(s.pendingInput) > 0 {
		combined := make([]byte, 0, len(s.pendingInput)+len(data))
		combined = append(combined, s.pendingInput...)
		combined = append(combined, data...)
		data = combined
		s.pendingInput = nil
	}
	keys := []string{}
	for len(data) > 0 {
		consumed, key, incomplete := nextModalInputKeyState(data)
		if incomplete {
			s.pendingInput = append(s.pendingInput[:0], data...)
			if len(s.pendingInput) == 1 && s.pendingInput[0] == '\x1b' {
				delay := s.pendingEscapeDelay
				if delay <= 0 {
					delay = 25 * time.Millisecond
				}
				s.pendingEscapeTimer = time.AfterFunc(delay, s.flushPendingEscape)
			}
			break
		}
		if consumed <= 0 {
			consumed = 1
		}
		if key != "" {
			keys = append(keys, key)
		}
		data = data[consumed:]
	}
	return keys
}

func (s *ModalServer) flushPendingEscape() {
	s.mu.Lock()
	if len(s.pendingInput) != 1 || s.pendingInput[0] != '\x1b' {
		s.mu.Unlock()
		return
	}
	s.pendingInput = nil
	s.pendingEscapeTimer = nil
	s.mu.Unlock()
	s.handleInputKey("escape")
}

func (s *ModalServer) handleInputKey(key string) {
	identity, frame, generation, ok := s.topSession()
	if !ok {
		return
	}
	if key == "escape" {
		s.requestClose(identity, generation, key)
		return
	}
	if controls, ok := s.renderer.(modalControlInputRenderer); ok {
		if event, consumed := controls.HandleModalControlInput(key); consumed {
			if event != nil {
				modalEvent := ModalEvent{
					Type:             event.Type,
					ID:               identity.surfaceID,
					Generation:       generation,
					TargetID:         event.TargetID,
					DocumentRevision: modalDocumentRevision(frame.Document),
					ActionName:       event.ActionName,
					Key:              event.Key,
					Value:            event.Value,
				}
				s.enqueueEvent(modalEvent, identity)
				if event.Type == "change" {
					if values, ok := s.renderer.(modalControlValueRenderer); ok {
						values.ApplyModalControlValue(event.TargetID, event.Value)
					}
				}
			}
			return
		}
	}
	if key == "tab" || key == "shift+tab" {
		if focuser, ok := s.renderer.(modalActionFocusRenderer); ok {
			delta := 1
			if key == "shift+tab" {
				delta = -1
			}
			focuser.FocusModalAction(delta)
		}
		return
	}
	if (key == "enter" || key == "space") && len(frame.Actions) > 0 {
		if focuser, ok := s.renderer.(modalActionFocusRenderer); ok {
			if actionName, eventKey, ok := focuser.ActivateFocusedModalAction(key); ok {
				s.enqueueEvent(ModalEvent{
					Type:             "action",
					ID:               identity.surfaceID,
					Generation:       generation,
					TargetID:         actionName,
					DocumentRevision: modalDocumentRevision(frame.Document),
					ActionName:       actionName,
					Key:              eventKey,
				}, identity)
				return
			}
		}
	}
	if row, col, ok := modalMouseClick(key); ok {
		if clicker, ok := s.renderer.(modalControlClickRenderer); ok {
			if event, hit := clicker.ClickModalControl(row, col); hit {
				if event != nil {
					s.enqueueEvent(ModalEvent{
						Type:             event.Type,
						ID:               identity.surfaceID,
						Generation:       generation,
						TargetID:         event.TargetID,
						DocumentRevision: modalDocumentRevision(frame.Document),
						ActionName:       event.ActionName,
						Key:              event.Key,
						Value:            event.Value,
					}, identity)
				}
				return
			}
		}
		clickKey, hit := "", false
		if clicker, ok := s.renderer.(modalClickRenderer); ok {
			clickKey, hit = clicker.ClickModal(row, col)
		}
		if !hit {
			return
		}
		key = clickKey
		if key == "escape" {
			s.requestClose(identity, generation, key)
			return
		}
	}
	if modalIsScrollKey(key) {
		if scroller, ok := s.renderer.(modalScrollRenderer); ok && scroller.ScrollModal(key) {
			return
		}
	}
	if actionName := modalActionForKey(frame, key); actionName != "" {
		s.enqueueEvent(ModalEvent{
			Type:             "action",
			ID:               identity.surfaceID,
			Generation:       generation,
			TargetID:         actionName,
			DocumentRevision: modalDocumentRevision(frame.Document),
			ActionName:       actionName,
			Key:              key,
		}, identity)
		return
	}
	if key == "q" {
		s.requestClose(identity, generation, key)
	}
}

func modalInputKey(data []byte) string {
	_, key := nextModalInputKey(data)
	return key
}

func nextModalInputKey(data []byte) (int, string) {
	consumed, key, incomplete := nextModalInputKeyState(data)
	if incomplete {
		return 0, ""
	}
	return consumed, key
}

func nextModalInputKeyState(data []byte) (int, string, bool) {
	if len(data) == 0 {
		return 0, "", false
	}
	switch data[0] {
	case '\x1b':
		return nextModalEscapeInputKey(data)
	case '\r', '\n':
		return 1, "enter", false
	case '\t':
		return 1, "tab", false
	case '\b', '\x7f':
		return 1, "backspace", false
	case ' ':
		return 1, "space", false
	default:
		if data[0] >= 0x20 && data[0] <= 0x7e {
			return 1, string(data[0]), false
		}
		if !utf8.FullRune(data) {
			return 0, "", true
		}
		if r, size := utf8.DecodeRune(data); r != utf8.RuneError || size > 1 {
			return size, string(r), false
		}
		return 1, "", false
	}
}

func nextModalEscapeInputKey(data []byte) (int, string, bool) {
	if len(data) == 1 {
		return 0, "", true
	}
	switch data[1] {
	case '[':
		if consumed, key, incomplete := consumeModalCSIInput(data); consumed > 0 || incomplete {
			return consumed, key, incomplete
		}
	case 'O':
		if len(data) < 3 {
			return 0, "", true
		}
		switch data[2] {
		case 'A':
			return 3, "up", false
		case 'B':
			return 3, "down", false
		case 'H':
			return 3, "home", false
		case 'F':
			return 3, "end", false
		}
	case ']':
		if consumed := consumeModalStringInput(data); consumed > 0 {
			return consumed, "", false
		}
		return 0, "", true
	case 'P':
		if consumed := consumeModalStringInput(data); consumed > 0 {
			return consumed, "", false
		}
		return 0, "", true
	}
	return 1, "escape", false
}

func consumeModalCSIInput(data []byte) (int, string, bool) {
	for i := 2; i < len(data); i++ {
		if data[i] >= 0x40 && data[i] <= 0x7e {
			return i + 1, modalCSIInputKey(data[2 : i+1]), false
		}
		if data[i] < 0x20 || data[i] > 0x3f {
			return 0, "", false
		}
	}
	return 0, "", true
}

func modalCSIInputKey(seq []byte) string {
	if len(seq) > 0 && seq[len(seq)-1] == '_' {
		return modalWindowsVTInputKey(seq[:len(seq)-1])
	}
	text := string(seq)
	if key := modalSGRMouseInputKey(text); key != "" {
		return key
	}
	switch text {
	case "Z":
		return "shift+tab"
	case "A":
		return "up"
	case "B":
		return "down"
	case "C":
		return "right"
	case "D":
		return "left"
	case "H", "1~", "7~":
		return "home"
	case "F", "4~", "8~":
		return "end"
	case "5~":
		return "pageup"
	case "6~":
		return "pagedown"
	}
	if len(text) >= 2 {
		final := text[len(text)-1]
		params := text[:len(text)-1]
		if strings.Contains(params, ";") {
			switch final {
			case 'A':
				return "up"
			case 'B':
				return "down"
			case 'C':
				return "right"
			case 'D':
				return "left"
			case 'H':
				return "home"
			case 'F':
				return "end"
			}
		}
	}
	return ""
}

func modalSGRMouseInputKey(text string) string {
	if len(text) < 2 || text[0] != '<' || text[len(text)-1] != 'M' {
		return ""
	}
	fields := strings.Split(strings.TrimSuffix(strings.TrimPrefix(text, "<"), "M"), ";")
	if len(fields) != 3 {
		return ""
	}
	button, err1 := strconv.Atoi(fields[0])
	col, err2 := strconv.Atoi(fields[1])
	row, err3 := strconv.Atoi(fields[2])
	if err1 != nil || err2 != nil || err3 != nil || row <= 0 || col <= 0 {
		return ""
	}
	if button&64 != 0 {
		if button&1 == 0 {
			return "up"
		}
		return "down"
	}
	if button&3 == 0 {
		return fmt.Sprintf("mouse:left:%d:%d", row, col)
	}
	return ""
}

func modalMouseClick(key string) (int, int, bool) {
	if !strings.HasPrefix(key, "mouse:left:") {
		return 0, 0, false
	}
	fields := strings.Split(strings.TrimPrefix(key, "mouse:left:"), ":")
	if len(fields) != 2 {
		return 0, 0, false
	}
	row, err1 := strconv.Atoi(fields[0])
	col, err2 := strconv.Atoi(fields[1])
	return row, col, err1 == nil && err2 == nil && row > 0 && col > 0
}

func modalWindowsVTInputKey(body []byte) string {
	fields := strings.Split(string(body), ";")
	if len(fields) >= 4 && fields[3] != "1" {
		return ""
	}
	codeField := ""
	if len(fields) >= 3 && fields[2] != "" && fields[2] != "0" {
		codeField = fields[2]
	} else if len(fields) >= 1 {
		codeField = fields[0]
	}
	code, err := strconv.Atoi(codeField)
	if err != nil {
		return ""
	}
	switch code {
	case 9:
		if len(fields) >= 5 {
			control, _ := strconv.Atoi(fields[4])
			if control&16 != 0 {
				return "shift+tab"
			}
		}
		return "tab"
	case 13:
		return "enter"
	case 27:
		return "escape"
	case 32:
		return "space"
	case 33:
		return "pageup"
	case 34:
		return "pagedown"
	case 35:
		return "end"
	case 36:
		return "home"
	case 38:
		return "up"
	case 40:
		return "down"
	case 37:
		return "left"
	case 39:
		return "right"
	}
	if code >= 0x20 && code <= 0x7e {
		return string(rune(code))
	}
	return ""
}

func modalIsScrollKey(key string) bool {
	switch key {
	case "up", "down", "pageup", "pagedown", "home", "end":
		return true
	default:
		return false
	}
}

func consumeModalStringInput(data []byte) int {
	for i := 2; i < len(data); i++ {
		switch data[i] {
		case '\a':
			return i + 1
		case '\x1b':
			if i+1 < len(data) && data[i+1] == '\\' {
				return i + 2
			}
			return 0
		}
	}
	return 0
}

func modalActionForKey(frame ModalFrame, key string) string {
	if key == "" {
		return ""
	}
	for _, action := range frame.Actions {
		if strings.EqualFold(action.Key, key) {
			return action.Name
		}
	}
	return ""
}

func (s *ModalServer) TopID() (string, bool) {
	identity, _, _, ok := s.topSession()
	return identity.surfaceID, ok
}

func (s *ModalServer) TopFrame() (string, ModalFrame, bool) {
	identity, frame, _, ok := s.topSession()
	return identity.surfaceID, frame, ok
}

func (s *ModalServer) topSession() (modalIdentity, ModalFrame, int64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.order) == 0 {
		return modalIdentity{}, ModalFrame{}, 0, false
	}
	identity := s.order[len(s.order)-1]
	session := s.active[identity]
	return identity, session.frame, session.generation, true
}

// CloseAll restores Copilot ownership and hides any active modal during
// broker shutdown or failure cleanup.
func (s *ModalServer) CloseAll() {
	s.mu.Lock()
	if len(s.order) == 0 {
		s.mu.Unlock()
		return
	}
	s.active = make(map[modalIdentity]modalSession)
	s.order = nil
	renderer := s.renderer
	s.mu.Unlock()
	if s.broker != nil {
		s.broker.SetOwner(OwnerCopilot)
	}
	if renderer != nil {
		renderer.HideModal()
	}
}

// ActiveCount reports the number of currently open modal canvases. Exposed
// for tests and diagnostics.
func (s *ModalServer) ActiveCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.order)
}

// TerminalModalRenderer paints ModalFrame content directly to a terminal
// writer. Copilot output is continuously consumed into a bounded VT screen
// model; while a modal is active the bytes are not replayed raw. Closing the
// modal clears the host frame and repaints the current modeled Copilot screen.
type TerminalModalRenderer struct {
	writer        io.Writer
	screen        *vtScreen
	queryRequests terminalSequenceSplitter

	mu                 sync.Mutex
	active             bool
	hostAlt            bool
	activeFrame        ModalFrame
	scrollOffset       int
	focusedActionIndex int
	documentControls   []modalDocumentControl
	focusedControlID   string
	controlValues      map[string]any
}

func NewTerminalModalRenderer(writer io.Writer) *TerminalModalRenderer {
	size := Size{Cols: vtDefaultCols, Rows: vtDefaultRows}
	if file, ok := writer.(*os.File); ok {
		size = ConsoleSize(file)
	}
	return NewTerminalModalRendererWithSize(writer, size)
}

func NewTerminalModalRendererWithSize(writer io.Writer, size Size) *TerminalModalRenderer {
	return &TerminalModalRenderer{
		writer:        writer,
		screen:        newVTScreen(size),
		queryRequests: newTerminalQueryRequestSplitter(),
		controlValues: make(map[string]any),
	}
}

func (r *TerminalModalRenderer) HandleOutput(output Output) {
	r.WriteCopilotOutput(output.Data)
}

func (r *TerminalModalRenderer) Snapshot() TerminalSnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.screen.Snapshot()
}

func (r *TerminalModalRenderer) Repaint() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.screen.Repaint()
}

func (r *TerminalModalRenderer) Resize(size Size) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.screen.Resize(size)
	if r.active && r.writer != nil {
		body, scrollOffset := renderModalFrameFocused(r.screen.Snapshot(), r.activeFrame, r.scrollOffset, r.focusedActionIndex)
		r.scrollOffset = scrollOffset
		_, _ = io.WriteString(r.writer, body)
	}
}

// WriteCopilotOutput must be used as the Broker's output sink instead of
// writing directly to the terminal, so output arriving while a modal is
// open updates the restoration model without interleaving with the modal.
func (r *TerminalModalRenderer) WriteCopilotOutput(data []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(data) > 0 {
		r.screen.Consume(data)
	}
	if r.writer == nil || len(data) == 0 {
		return
	}
	if !r.active {
		r.queryRequests.Reset()
		r.hostAlt = r.screen.useAlt
		_, _ = r.writer.Write(data)
		return
	}
	if queryRequests, _ := r.queryRequests.Split(data); len(queryRequests) > 0 {
		_, _ = r.writer.Write(queryRequests)
	}
}

func (r *TerminalModalRenderer) ShowModal(frame ModalFrame) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.queryRequests.Reset()
	r.hostAlt = r.screen.useAlt
	if !r.active || r.activeFrame.ID != frame.ID {
		r.scrollOffset = 0
		r.focusedActionIndex = -1
		r.focusedControlID = ""
		r.controlValues = make(map[string]any)
	}
	r.reconcileDocumentControls(frame)
	if len(r.documentControls) == 0 {
		r.focusedActionIndex = normalizeModalFocusedActionIndex(frame, r.focusedActionIndex)
	} else {
		// Document controls own keyboard focus; keep the legacy footer actions
		// visible as shortcut hints without rendering a second focus marker.
		r.focusedActionIndex = -2
	}
	r.active = true
	r.activeFrame = r.frameWithControlState(frame)
	if r.writer == nil {
		return
	}
	body, scrollOffset := renderModalFrameFocused(r.screen.Snapshot(), r.activeFrame, r.scrollOffset, r.focusedActionIndex)
	r.scrollOffset = scrollOffset
	_, _ = io.WriteString(r.writer, body)
}

func (r *TerminalModalRenderer) ClickModal(row, col int) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.active || r.writer == nil {
		return "", false
	}
	return modalActionKeyAt(r.activeFrame, newModalLayout(r.screen.Snapshot()), r.focusedActionIndex, row, col)
}

func (r *TerminalModalRenderer) ClickModalControl(row, col int) (*modalControlEvent, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.active || r.writer == nil || len(r.documentControls) == 0 {
		return nil, false
	}
	layout := newModalLayout(r.screen.Snapshot())
	if layout.compact || col < layout.innerLeft || col >= layout.innerLeft+layout.innerWidth {
		return nil, false
	}
	bodyStart := layout.top + 1 + layout.headerRows + layout.separatorRows
	bodyIndex := r.scrollOffset + row - bodyStart
	bodyLines := modalFrameBodyLines(r.activeFrame, layout.innerWidth)
	if bodyIndex < 0 || bodyIndex >= len(bodyLines) {
		return nil, false
	}
	controlIndex := modalDocumentControlIndexForLine(r.documentControls, bodyLines, bodyIndex)
	if controlIndex < 0 {
		return nil, false
	}
	control := r.documentControls[controlIndex]
	r.focusedControlID = control.ID
	var event *modalControlEvent
	switch control.Kind {
	case "textInput", "searchInput", "numberInput", "dateInput", "fileInput", "passwordInput", "textArea":
		event = &modalControlEvent{
			Type:     "focus",
			TargetID: control.ID,
			Key:      "mouse",
			Value:    r.controlValues[control.ID],
		}
	default:
		event, _ = r.applyDocumentControlInput(control, "enter")
	}
	r.activeFrame = r.frameWithControlState(r.activeFrame)
	r.repaintModalLocked()
	return event, true
}

func modalDocumentControlIndexForLine(controls []modalDocumentControl, lines []string, target int) int {
	searchFrom := 0
	for index, control := range controls {
		label := printableModalText(control.Label, false)
		if label == "" {
			continue
		}
		for lineIndex := searchFrom; lineIndex < len(lines); lineIndex++ {
			if strings.Contains(lines[lineIndex], label) {
				if lineIndex == target {
					return index
				}
				searchFrom = lineIndex + 1
				break
			}
		}
	}
	return -1
}

func (r *TerminalModalRenderer) FocusModalAction(delta int) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.active || r.writer == nil || len(r.activeFrame.Actions) == 0 {
		return false
	}
	count := len(r.activeFrame.Actions)
	if r.focusedActionIndex < 0 || r.focusedActionIndex >= count {
		r.focusedActionIndex = 0
	} else {
		r.focusedActionIndex = (r.focusedActionIndex + delta + count) % count
	}
	snapshot := r.screen.Snapshot()
	layout := newModalLayout(snapshot)
	bodyLines := modalFrameBodyLines(r.activeFrame, layout.innerWidth)
	maxScroll := maxInt(0, len(bodyLines)-layout.bodyRows)
	_, _ = io.WriteString(r.writer, renderModalFooterFrame(layout, r.activeFrame, maxScroll, r.focusedActionIndex))
	return true
}

func (r *TerminalModalRenderer) HandleModalControlInput(key string) (*modalControlEvent, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.active || len(r.documentControls) == 0 {
		return nil, false
	}
	if key == "tab" || key == "shift+tab" {
		delta := 1
		if key == "shift+tab" {
			delta = -1
		}
		r.focusDocumentControl(delta)
		r.repaintModalLocked()
		return nil, true
	}
	index := r.focusedDocumentControlIndex()
	if index < 0 {
		return nil, false
	}
	control := r.documentControls[index]
	event, consumed := r.applyDocumentControlInput(control, key)
	if consumed {
		r.activeFrame = r.frameWithControlState(r.activeFrame)
		r.repaintModalLocked()
	}
	return event, consumed
}

func (r *TerminalModalRenderer) ApplyModalControlValue(id string, value any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.active {
		return
	}
	for _, control := range r.documentControls {
		if control.ID == id {
			r.controlValues[id] = value
			r.activeFrame = r.frameWithControlState(r.activeFrame)
			r.repaintModalLocked()
			return
		}
	}
}

func (r *TerminalModalRenderer) focusDocumentControl(delta int) {
	index := r.focusedDocumentControlIndex()
	if index < 0 {
		if delta < 0 {
			index = len(r.documentControls) - 1
		} else {
			index = 0
		}
	} else {
		index = (index + delta + len(r.documentControls)) % len(r.documentControls)
	}
	r.focusedControlID = r.documentControls[index].ID
}

func (r *TerminalModalRenderer) focusedDocumentControlIndex() int {
	for index, control := range r.documentControls {
		if control.ID == r.focusedControlID {
			return index
		}
	}
	return -1
}

func (r *TerminalModalRenderer) applyDocumentControlInput(control modalDocumentControl, key string) (*modalControlEvent, bool) {
	event := &modalControlEvent{TargetID: control.ID, Key: key}
	switch control.Kind {
	case "button", "link":
		if key != "enter" && key != "space" {
			return nil, false
		}
		event.Type = "activate"
		event.ActionName = control.ActionName
		return event, true
	case "checkbox", "toggle":
		if key != "enter" && key != "space" {
			return nil, false
		}
		value, _ := r.controlValues[control.ID].(bool)
		value = !value
		r.controlValues[control.ID] = value
		event.Type, event.Value = "change", value
		return event, true
	case "select", "radioGroup", "tabs":
		if key != "left" && key != "right" && key != "up" && key != "down" && key != "enter" && key != "space" {
			return nil, false
		}
		value := modalNextControlOption(control, r.controlValues[control.ID], key == "left" || key == "up")
		r.controlValues[control.ID] = value
		event.Type, event.Value = "change", value
		return event, true
	case "slider":
		if key != "left" && key != "right" && key != "up" && key != "down" {
			return nil, false
		}
		value := modalNextSliderValue(control, r.controlValues[control.ID], key == "left" || key == "down")
		r.controlValues[control.ID] = value
		event.Type, event.Value = "change", value
		return event, true
	case "textInput", "searchInput", "numberInput", "dateInput", "fileInput", "passwordInput", "textArea":
		value := fmt.Sprint(r.controlValues[control.ID])
		switch key {
		case "enter":
			event.Type, event.Value = "submit", value
			return event, true
		case "space":
			value += " "
		case "backspace":
			runes := []rune(value)
			if len(runes) > 0 {
				value = string(runes[:len(runes)-1])
			}
		default:
			if !modalPrintableInputKey(key) {
				return nil, false
			}
			value += key
		}
		r.controlValues[control.ID] = value
		event.Type, event.Value = "change", value
		return event, true
	default:
		return nil, false
	}
}

func (r *TerminalModalRenderer) repaintModalLocked() {
	if r.writer == nil {
		return
	}
	body, scrollOffset := renderModalFrameFocused(r.screen.Snapshot(), r.activeFrame, r.scrollOffset, r.focusedActionIndex)
	r.scrollOffset = scrollOffset
	_, _ = io.WriteString(r.writer, body)
}

func (r *TerminalModalRenderer) ActivateFocusedModalAction(trigger string) (string, string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.active || len(r.activeFrame.Actions) == 0 {
		return "", "", false
	}
	r.focusedActionIndex = normalizeModalFocusedActionIndex(r.activeFrame, r.focusedActionIndex)
	if r.focusedActionIndex < 0 || r.focusedActionIndex >= len(r.activeFrame.Actions) {
		return "", "", false
	}
	action := r.activeFrame.Actions[r.focusedActionIndex]
	if action.Name == "" {
		return "", "", false
	}
	return action.Name, trigger, true
}

func (r *TerminalModalRenderer) ScrollModal(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.active || r.writer == nil {
		return false
	}
	snapshot := r.screen.Snapshot()
	layout := newModalLayout(snapshot)
	bodyLines := modalFrameBodyLines(r.activeFrame, layout.innerWidth)
	maxScroll := maxInt(0, len(bodyLines)-layout.bodyRows)
	if maxScroll == 0 {
		return false
	}
	oldOffset := r.scrollOffset
	page := maxInt(1, layout.bodyRows-1)
	switch key {
	case "up":
		r.scrollOffset--
	case "down":
		r.scrollOffset++
	case "pageup":
		r.scrollOffset -= page
	case "pagedown":
		r.scrollOffset += page
	case "home":
		r.scrollOffset = 0
	case "end":
		r.scrollOffset = maxScroll
	default:
		return false
	}
	r.scrollOffset = clampInt(r.scrollOffset, 0, maxScroll)
	if r.scrollOffset == oldOffset {
		return false
	}
	body, scrollOffset := renderModalScrollFrame(snapshot, layout, r.activeFrame, bodyLines, r.scrollOffset, maxScroll, r.focusedActionIndex)
	r.scrollOffset = scrollOffset
	_, _ = io.WriteString(r.writer, body)
	return true
}

func renderModalFrame(snapshot TerminalSnapshot, frame ModalFrame, scrollOffset int) (string, int) {
	return renderModalFrameFocused(snapshot, frame, scrollOffset, -1)
}

func renderModalFrameFocused(snapshot TerminalSnapshot, frame ModalFrame, scrollOffset int, focusedActionIndex int) (string, int) {
	focusedActionIndex = normalizeModalFocusedActionIndex(frame, focusedActionIndex)
	layout := newModalLayout(snapshot)
	bodyLines := modalFrameBodyLines(frame, layout.innerWidth)
	maxScroll := maxInt(0, len(bodyLines)-layout.bodyRows)
	scrollOffset = clampInt(scrollOffset, 0, maxScroll)

	styles := newModalStyles()
	var out strings.Builder
	out.Grow(modalFrameRenderCapacity(layout))
	out.WriteString("\x1b[?25l\x1b[0m\x1b[H")
	writeBackdrop(&out, snapshot, layout.cols, layout.rows, styles)
	writeModalShadow(&out, layout, styles)
	if layout.compact {
		writeCompactModalPanel(&out, layout, frame, styles)
	} else {
		writeEnterpriseModalPanel(&out, layout, frame, bodyLines, scrollOffset, maxScroll, focusedActionIndex, styles)
	}
	out.WriteString("\x1b[0m\x1b[?25h")
	return out.String(), scrollOffset
}

func renderModalScrollFrame(snapshot TerminalSnapshot, layout modalLayout, frame ModalFrame, bodyLines []string, scrollOffset, maxScroll int, focusedActionIndex int) (string, int) {
	focusedActionIndex = normalizeModalFocusedActionIndex(frame, focusedActionIndex)
	scrollOffset = clampInt(scrollOffset, 0, maxScroll)
	if layout.compact {
		return renderModalFrameFocused(snapshot, frame, scrollOffset, focusedActionIndex)
	}
	styles := newModalStyles()
	var out strings.Builder
	out.Grow(maxInt(1, layout.panelWidth*(layout.headerRows+layout.separatorRows+layout.bodyRows+layout.footerRows)*2))
	out.WriteString("\x1b[?25l\x1b[0m")
	writeModalHeader(&out, layout, frame, bodyLines, scrollOffset, maxScroll, styles)
	separatorRow := layout.top + 1 + layout.headerRows
	if layout.separatorRows > 0 {
		writeModalText(&out, separatorRow, layout.innerLeft, layout.innerWidth, styles.muted, strings.Repeat("─", layout.innerWidth))
	}
	writeModalBody(&out, layout, bodyLines, scrollOffset, styles)
	writeModalFooter(&out, layout, frame, maxScroll, focusedActionIndex, styles)
	out.WriteString("\x1b[0m\x1b[?25h")
	return out.String(), scrollOffset
}

type modalLayout struct {
	cols           int
	rows           int
	panelWidth     int
	panelHeight    int
	left           int
	top            int
	innerLeft      int
	innerWidth     int
	contentRows    int
	headerRows     int
	separatorRows  int
	footerRows     int
	bodyRows       int
	footerStartRow int
	compact        bool
}

func newModalLayout(snapshot TerminalSnapshot) modalLayout {
	cols := int(snapshot.Size.Cols)
	rows := int(snapshot.Size.Rows)
	if cols <= 0 {
		cols = vtDefaultCols
	}
	if rows <= 0 {
		rows = vtDefaultRows
	}
	panelWidth := cols - 10
	if cols < 72 {
		panelWidth = cols - 4
	}
	if cols <= 40 {
		panelWidth = cols - 2
	}
	panelWidth = clampInt(panelWidth, minInt(cols, 24), minInt(cols, 112))
	if panelWidth < 4 {
		panelWidth = cols
	}
	panelHeight := rows - 6
	if rows >= 18 {
		panelHeight = rows * 75 / 100
	}
	panelHeight = clampInt(panelHeight, minInt(rows, 8), minInt(rows, 30))
	if rows <= 8 {
		panelHeight = rows
	}
	if panelHeight < 3 {
		panelHeight = rows
	}
	left := (cols-panelWidth)/2 + 1
	top := (rows-panelHeight)/2 + 1
	if left < 1 {
		left = 1
	}
	if top < 1 {
		top = 1
	}
	innerWidth := panelWidth - 4
	if innerWidth < 1 {
		innerWidth = maxInt(1, panelWidth-2)
	}
	layout := modalLayout{
		cols:        cols,
		rows:        rows,
		panelWidth:  panelWidth,
		panelHeight: panelHeight,
		left:        left,
		top:         top,
		innerLeft:   minInt(cols, left+2),
		innerWidth:  innerWidth,
		compact:     panelWidth < 24 || panelHeight < 6,
	}
	layout.contentRows = maxInt(0, panelHeight-2)
	layout.headerRows = 2
	layout.separatorRows = 1
	layout.footerRows = 4
	layout.bodyRows = layout.contentRows - layout.headerRows - layout.separatorRows - layout.footerRows
	if layout.bodyRows < 2 {
		layout.footerRows = minInt(1, layout.footerRows)
		layout.bodyRows = layout.contentRows - layout.headerRows - layout.separatorRows - layout.footerRows
	}
	if layout.bodyRows < 1 {
		layout.bodyRows = maxInt(0, layout.contentRows-layout.headerRows)
		layout.separatorRows = 0
		layout.footerRows = 0
	}
	layout.footerStartRow = layout.top + 1 + layout.headerRows + layout.separatorRows + layout.bodyRows
	return layout
}

type modalStyles struct {
	backdrop lipgloss.Style
	shadow   lipgloss.Style
	panel    lipgloss.Style
	border   lipgloss.Style
	title    lipgloss.Style
	status   lipgloss.Style
	muted    lipgloss.Style
	body     lipgloss.Style
	accent   lipgloss.Style
	footer   lipgloss.Style
	actions  lipgloss.Style
}

func newModalStyles() modalStyles {
	return modalStyles{
		backdrop: lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Background(lipgloss.Color("234")).Faint(true),
		shadow:   lipgloss.NewStyle().Background(lipgloss.Color("232")),
		panel:    lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Background(lipgloss.Color("235")),
		border:   lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Background(lipgloss.Color("235")).Bold(true),
		title:    lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Background(lipgloss.Color("235")).Bold(true),
		status:   lipgloss.NewStyle().Foreground(lipgloss.Color("16")).Background(lipgloss.Color("39")).Bold(true),
		muted:    lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Background(lipgloss.Color("235")),
		body:     lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Background(lipgloss.Color("235")),
		accent:   lipgloss.NewStyle().Foreground(lipgloss.Color("220")).Background(lipgloss.Color("235")).Bold(true),
		footer:   lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Background(lipgloss.Color("235")),
		actions:  lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Background(lipgloss.Color("238")).Bold(true),
	}
}

func modalFrameRenderCapacity(layout modalLayout) int {
	cells := maxInt(1, layout.cols*layout.rows)
	overlay := maxInt(1, layout.panelWidth*layout.panelHeight)
	return cells*2 + overlay*2
}

func writeBackdrop(out *strings.Builder, snapshot TerminalSnapshot, cols, rows int, styles modalStyles) {
	for row := 0; row < rows; row++ {
		line := ""
		if row < len(snapshot.Rows) {
			line = terminalSnapshotRowString(snapshot.Rows[row])
		}
		line = modalPadLine(modalFitLine(line, cols), cols)
		out.WriteString(styles.backdrop.Render(line))
		if row < rows-1 {
			out.WriteString("\r\n")
		}
	}
	out.WriteString("\x1b[0m")
}

func terminalSnapshotRowString(row []TerminalCell) string {
	var line strings.Builder
	for _, cell := range row {
		if cell.Continuation {
			continue
		}
		if cell.Grapheme == "" {
			line.WriteByte(' ')
		} else {
			line.WriteString(cell.Grapheme)
		}
	}
	return strings.TrimRight(line.String(), " ")
}

func writeModalShadow(out *strings.Builder, layout modalLayout, styles modalStyles) {
	if layout.panelWidth < 4 || layout.panelHeight < 3 {
		return
	}
	shadowCell := styles.shadow.Render(" ")
	for row := 1; row < layout.panelHeight && layout.top+row <= layout.rows; row++ {
		col := layout.left + layout.panelWidth
		if col <= layout.cols {
			moveModalCursor(out, layout.top+row, col)
			out.WriteString(shadowCell)
		}
	}
	bottomRow := layout.top + layout.panelHeight
	if bottomRow <= layout.rows {
		moveModalCursor(out, bottomRow, minInt(layout.cols, layout.left+2))
		width := minInt(layout.panelWidth-1, layout.cols-layout.left-1)
		if width > 0 {
			out.WriteString(styles.shadow.Render(strings.Repeat(" ", width)))
		}
	}
}

func writeCompactModalPanel(out *strings.Builder, layout modalLayout, frame ModalFrame, styles modalStyles) {
	width := layout.panelWidth
	if width < 1 {
		return
	}
	if width < 8 || layout.panelHeight < 3 {
		writeModalLine(out, layout.top, layout.left, width, styles.panel, printableModalText(firstNonEmpty(frame.Title, frame.Body, "Modal"), false))
		if layout.panelHeight > 1 {
			writeModalLine(out, layout.top+1, layout.left, width, styles.body, printableModalText(frame.Body, true))
		}
		return
	}
	writeModalLine(out, layout.top, layout.left, width, styles.border, "╭"+strings.Repeat("─", width-2)+"╮")
	insideRows := layout.panelHeight - 2
	content := []string{printableModalText(firstNonEmpty(frame.Title, "Modal"), false)}
	if frame.Status != "" {
		content = append(content, printableModalText(frame.Status, false))
	}
	content = append(content, modalFrameBodyLines(frame, maxInt(1, width-4))...)
	for i := 0; i < insideRows; i++ {
		text := ""
		if i < len(content) {
			text = content[i]
		}
		writeModalLine(out, layout.top+1+i, layout.left, width, styles.panel, "│"+modalPadLine(modalFitLine(text, width-2), width-2)+"│")
	}
	writeModalLine(out, layout.top+layout.panelHeight-1, layout.left, width, styles.border, "╰"+strings.Repeat("─", width-2)+"╯")
}

func writeEnterpriseModalPanel(out *strings.Builder, layout modalLayout, frame ModalFrame, bodyLines []string, scrollOffset, maxScroll int, focusedActionIndex int, styles modalStyles) {
	writeModalLine(out, layout.top, layout.left, layout.panelWidth, styles.border, "╭"+strings.Repeat("─", layout.panelWidth-2)+"╮")
	for row := 1; row < layout.panelHeight-1; row++ {
		writeModalLine(out, layout.top+row, layout.left, layout.panelWidth, styles.panel, "│"+strings.Repeat(" ", layout.panelWidth-2)+"│")
	}
	writeModalLine(out, layout.top+layout.panelHeight-1, layout.left, layout.panelWidth, styles.border, "╰"+strings.Repeat("─", layout.panelWidth-2)+"╯")

	writeModalHeader(out, layout, frame, bodyLines, scrollOffset, maxScroll, styles)
	separatorRow := layout.top + 1 + layout.headerRows
	if layout.separatorRows > 0 {
		writeModalText(out, separatorRow, layout.innerLeft, layout.innerWidth, styles.muted, strings.Repeat("─", layout.innerWidth))
	}
	writeModalBody(out, layout, bodyLines, scrollOffset, styles)
	writeModalFooter(out, layout, frame, maxScroll, focusedActionIndex, styles)
}

func renderModalFooterFrame(layout modalLayout, frame ModalFrame, maxScroll int, focusedActionIndex int) string {
	styles := newModalStyles()
	var out strings.Builder
	out.Grow(maxInt(1, layout.panelWidth*layout.footerRows*2))
	out.WriteString("\x1b[?25l\x1b[0m")
	writeModalFooter(&out, layout, frame, maxScroll, focusedActionIndex, styles)
	out.WriteString("\x1b[0m\x1b[?25h")
	return out.String()
}

func writeModalHeader(out *strings.Builder, layout modalLayout, frame ModalFrame, bodyLines []string, scrollOffset, maxScroll int, styles modalStyles) {
	title := printableModalText(firstNonEmpty(frame.Title, "Modal"), false)
	status := printableModalText(frame.Status, false)
	if status == "" {
		status = "Native overlay"
	}
	statusText := " " + modalFitLine(status, maxInt(1, minInt(28, layout.innerWidth/3))) + " "
	statusBadge := styles.status.Render(statusText)
	statusWidth := lipgloss.Width(statusText)
	titleWidth := maxInt(1, layout.innerWidth-statusWidth-2)
	titleText := "◆ " + modalFitLine(title, maxInt(1, titleWidth-2))
	space := maxInt(1, layout.innerWidth-lipgloss.Width(titleText)-statusWidth)
	moveModalCursor(out, layout.top+1, layout.innerLeft)
	out.WriteString(styles.title.Render(titleText))
	out.WriteString(styles.panel.Render(strings.Repeat(" ", space)))
	out.WriteString(statusBadge)

	subtitle := "Native Afterburner modal overlay"
	if maxScroll > 0 {
		end := minInt(len(bodyLines), scrollOffset+layout.bodyRows)
		subtitle = fmt.Sprintf("Native Afterburner modal overlay • lines %d-%d of %d", scrollOffset+1, end, len(bodyLines))
	}
	writeModalText(out, layout.top+2, layout.innerLeft, layout.innerWidth, styles.muted, subtitle)
}

func writeModalBody(out *strings.Builder, layout modalLayout, bodyLines []string, scrollOffset int, styles modalStyles) {
	bodyStart := layout.top + 1 + layout.headerRows + layout.separatorRows
	if layout.bodyRows <= 0 {
		return
	}
	if len(bodyLines) == 0 {
		bodyLines = []string{"No live details yet."}
	}
	for row := 0; row < layout.bodyRows; row++ {
		index := scrollOffset + row
		text := ""
		style := styles.body
		if index < len(bodyLines) {
			text = bodyLines[index]
		}
		if row == 0 && scrollOffset > 0 {
			text = "↑ " + text
			style = styles.accent
		}
		if row == layout.bodyRows-1 && scrollOffset+layout.bodyRows < len(bodyLines) {
			remaining := len(bodyLines) - (scrollOffset + layout.bodyRows)
			text = fmt.Sprintf("… %d more line%s (↓/PgDn)", remaining, pluralSuffix(remaining))
			style = styles.accent
		}
		writeModalText(out, bodyStart+row, layout.innerLeft, layout.innerWidth, style, text)
	}
}

func writeModalFooter(out *strings.Builder, layout modalLayout, frame ModalFrame, maxScroll int, focusedActionIndex int, styles modalStyles) {
	if layout.footerRows <= 0 {
		return
	}
	row := layout.footerStartRow
	if row < layout.top+layout.panelHeight-1 {
		writeModalText(out, row, layout.innerLeft, layout.innerWidth, styles.muted, strings.Repeat("─", layout.innerWidth))
		row++
	}
	if frame.Footer != "" && row < layout.top+layout.panelHeight-1 {
		writeModalText(out, row, layout.innerLeft, layout.innerWidth, styles.footer, printableModalText(frame.Footer, false))
		row++
	}
	if row < layout.top+layout.panelHeight-1 {
		if actions := modalActionsText(frame, focusedActionIndex); actions != "" {
			writeModalText(out, row, layout.innerLeft, layout.innerWidth, styles.actions, actions)
			row++
		}
	}
	if row < layout.top+layout.panelHeight-1 {
		writeModalText(out, row, layout.innerLeft, layout.innerWidth, styles.footer, modalControlHints(frame, maxScroll))
	}
}

func writeModalLine(out *strings.Builder, row, col, width int, style lipgloss.Style, text string) {
	moveModalCursor(out, row, col)
	out.WriteString(style.Render(modalPadLine(modalFitLine(text, width), width)))
}

func writeModalText(out *strings.Builder, row, col, width int, style lipgloss.Style, text string) {
	moveModalCursor(out, row, col)
	out.WriteString(style.Render(modalPadLine(modalFitLine(printableModalText(text, false), width), width)))
}

func moveModalCursor(out *strings.Builder, row, col int) {
	fmt.Fprintf(out, "\x1b[%d;%dH", row, col)
}

func modalControlHints(frame ModalFrame, maxScroll int) string {
	parts := []string{"[Esc] Close"}
	if maxScroll > 0 {
		parts = append(parts, "[↑/↓ PgUp/PgDn Home/End] Scroll")
	}
	if len(frame.Actions) > 1 {
		parts = append(parts, "[Tab/Shift+Tab] Focus")
	}
	if len(frame.Actions) > 0 {
		parts = append(parts, "[Enter/Space] Activate")
	}
	return strings.Join(parts, "  ")
}

func modalActionsText(frame ModalFrame, focusedActionIndex int) string {
	focusedActionIndex = normalizeModalFocusedActionIndex(frame, focusedActionIndex)
	parts := make([]string, 0, len(frame.Actions)+1)
	for index, action := range frame.Actions {
		parts = append(parts, modalActionLabel(action, index == focusedActionIndex))
	}
	return strings.Join(parts, "  ")
}

func modalActionLabel(action ModalAction, focused bool) string {
	label := printableModalText(action.Label, false)
	if label == "" {
		label = printableModalText(action.Name, false)
	}
	if action.Key != "" {
		label = "[" + printableModalText(action.Key, false) + "] " + label
	}
	if action.Description != "" {
		label += " — " + printableModalText(action.Description, false)
	}
	if focused {
		label = "▶ " + label + " ◀"
	}
	return label
}

func normalizeModalFocusedActionIndex(frame ModalFrame, focusedActionIndex int) int {
	if len(frame.Actions) == 0 {
		return -1
	}
	if focusedActionIndex == -2 {
		return -1
	}
	if focusedActionIndex < 0 {
		return 0
	}
	if focusedActionIndex >= len(frame.Actions) {
		return len(frame.Actions) - 1
	}
	return focusedActionIndex
}

func modalActionKeyAt(frame ModalFrame, layout modalLayout, focusedActionIndex int, row, col int) (string, bool) {
	actionRow, ok := modalActionRow(frame, layout)
	if !ok || row != actionRow || col < layout.innerLeft || col >= layout.innerLeft+layout.innerWidth {
		return "", false
	}
	cursor := layout.innerLeft
	focusedActionIndex = normalizeModalFocusedActionIndex(frame, focusedActionIndex)
	for index, action := range frame.Actions {
		label := modalActionLabel(action, index == focusedActionIndex)
		width := lipgloss.Width(label)
		if col >= cursor && col < cursor+width {
			return firstNonEmpty(action.Key, action.Name), true
		}
		cursor += width + 2
	}
	closeLabel := "q/Esc Close"
	if col >= cursor && col < cursor+lipgloss.Width(closeLabel) {
		return "escape", true
	}
	return "", false
}

func modalActionRow(frame ModalFrame, layout modalLayout) (int, bool) {
	if layout.footerRows <= 0 || layout.compact || len(frame.Actions) == 0 {
		return 0, false
	}
	row := layout.footerStartRow
	if row < layout.top+layout.panelHeight-1 {
		row++
	}
	if frame.Footer != "" && row < layout.top+layout.panelHeight-1 {
		row++
	}
	if row < layout.top+layout.panelHeight-1 {
		return row, true
	}
	return 0, false
}

func modalFrameBodyLines(frame ModalFrame, width int) []string {
	if lines := modalDocumentBodyLines(frame, width); len(lines) > 0 {
		return lines
	}
	return modalBodyLines(frame.Body, width)
}

// RenderUIDocumentPreview validates and projects a native UI document without
// opening a terminal session.
func RenderUIDocumentPreview(document []byte, width int) ([]string, error) {
	if err := validateModalDocument(document, ""); err != nil {
		return nil, err
	}
	if width < 20 {
		width = 20
	}
	return modalDocumentBodyLines(ModalFrame{Document: json.RawMessage(document)}, width), nil
}

func modalBodyLines(text string, width int) []string {
	lines := wrapModalText(printableModalText(text, true), width)
	if len(lines) == 0 {
		return []string{"No live details yet."}
	}
	return lines
}

type modalDocumentTree struct {
	SchemaVersion int               `json:"schemaVersion,omitempty"`
	Protocol      string            `json:"protocol,omitempty"`
	Revision      int64             `json:"revision,omitempty"`
	SurfaceID     string            `json:"surfaceId,omitempty"`
	Locale        string            `json:"locale,omitempty"`
	Root          modalDocumentNode `json:"root"`
}

type modalDocumentNode struct {
	ID             string              `json:"id"`
	Kind           string              `json:"kind"`
	Props          map[string]any      `json:"props,omitempty"`
	Accessibility  map[string]any      `json:"accessibility,omitempty"`
	ActionBindings map[string]any      `json:"actionBindings,omitempty"`
	Metadata       map[string]any      `json:"metadata,omitempty"`
	Children       []modalDocumentNode `json:"children,omitempty"`
}

type modalDocumentControl struct {
	ID         string
	Kind       string
	Label      string
	ActionName string
	Options    []any
	Min        float64
	Max        float64
	Step       float64
}

func (r *TerminalModalRenderer) reconcileDocumentControls(frame ModalFrame) {
	controls, initialValues := modalControlsFromDocument(frame.Document)
	r.documentControls = controls
	nextValues := make(map[string]any, len(controls))
	for _, control := range controls {
		nextValues[control.ID] = initialValues[control.ID]
	}
	r.controlValues = nextValues
	if r.focusedDocumentControlIndex() < 0 {
		r.focusedControlID = ""
	}
}

func modalControlsFromDocument(document json.RawMessage) ([]modalDocumentControl, map[string]any) {
	var tree modalDocumentTree
	if len(document) == 0 || json.Unmarshal(document, &tree) != nil {
		return nil, map[string]any{}
	}
	controls := []modalDocumentControl{}
	values := make(map[string]any)
	var walk func(modalDocumentNode)
	walk = func(node modalDocumentNode) {
		if _, interactive := modalInteractiveDocumentKinds[node.Kind]; interactive && node.ID != "" {
			control := modalDocumentControl{
				ID:         node.ID,
				Kind:       node.Kind,
				Label:      modalDocumentControlLabel(node),
				ActionName: firstNonEmpty(modalStringProp(node.ActionBindings, "activate"), modalStringProp(node.Props, "actionId")),
				Options:    modalControlOptions(node),
				Min:        modalNumberProp(node.Props, "min", 0),
				Max:        modalNumberProp(node.Props, "max", 100),
				Step:       modalNumberProp(node.Props, "step", 1),
			}
			controls = append(controls, control)
			values[node.ID] = modalInitialControlValue(node)
		}
		for _, child := range node.Children {
			walk(child)
		}
	}
	walk(tree.Root)
	return controls, values
}

func modalDocumentControlLabel(node modalDocumentNode) string {
	return firstNonEmpty(
		modalStringProp(node.Props, "label"),
		modalStringProp(node.Props, "title"),
		modalStringProp(node.Props, "name"),
		modalStringProp(node.Props, "text"),
		node.ID,
	)
}

func modalInitialControlValue(node modalDocumentNode) any {
	switch node.Kind {
	case "checkbox", "toggle":
		return modalBoolProp(node.Props, "checked") || modalBoolProp(node.Props, "selected") || modalBoolProp(node.Props, "value")
	case "select", "radioGroup":
		return firstNonEmpty(modalStringProp(node.Props, "value"), modalStringProp(node.Props, "selected"), modalStringProp(node.Props, "selectedValue"))
	case "tabs":
		return firstNonEmpty(modalStringProp(node.Props, "active"), modalStringProp(node.Props, "activeId"), modalStringProp(node.Props, "value"))
	case "slider":
		return modalNumberProp(node.Props, "value", modalNumberProp(node.Props, "min", 0))
	default:
		return modalStringProp(node.Props, "value")
	}
}

func modalControlOptions(node modalDocumentNode) []any {
	key := "options"
	if node.Kind == "tabs" {
		key = "items"
		if _, exists := node.Props[key]; !exists {
			key = "tabs"
		}
	}
	options, _ := node.Props[key].([]any)
	return options
}

func modalControlOptionValue(option any) any {
	if entry, ok := option.(map[string]any); ok {
		for _, key := range []string{"value", "id", "label", "title"} {
			if value, exists := entry[key]; exists {
				return value
			}
		}
	}
	return option
}

func modalNextControlOption(control modalDocumentControl, current any, reverse bool) any {
	if len(control.Options) == 0 {
		return current
	}
	index := -1
	for candidate, option := range control.Options {
		if fmt.Sprint(modalControlOptionValue(option)) == fmt.Sprint(current) {
			index = candidate
			break
		}
	}
	if reverse {
		if index < 0 {
			index = 0
		}
		index = (index - 1 + len(control.Options)) % len(control.Options)
	} else {
		index = (index + 1) % len(control.Options)
	}
	return modalControlOptionValue(control.Options[index])
}

func modalNextSliderValue(control modalDocumentControl, current any, reverse bool) float64 {
	value, ok := modalNumber(current)
	if !ok {
		value = control.Min
	}
	step := control.Step
	if step <= 0 {
		step = 1
	}
	if reverse {
		value -= step
	} else {
		value += step
	}
	if value < control.Min {
		value = control.Min
	}
	if control.Max >= control.Min && value > control.Max {
		value = control.Max
	}
	return value
}

func modalPrintableInputKey(key string) bool {
	if utf8.RuneCountInString(key) != 1 {
		return false
	}
	r, _ := utf8.DecodeRuneInString(key)
	return !unicode.IsControl(r)
}

func (r *TerminalModalRenderer) frameWithControlState(frame ModalFrame) ModalFrame {
	if len(frame.Document) == 0 || len(r.documentControls) == 0 {
		return frame
	}
	var tree modalDocumentTree
	if json.Unmarshal(frame.Document, &tree) != nil {
		return frame
	}
	applyModalControlState(&tree.Root, r.controlValues, r.focusedControlID)
	document, err := json.Marshal(tree)
	if err == nil {
		frame.Document = document
	}
	return frame
}

func applyModalControlState(node *modalDocumentNode, values map[string]any, focusedID string) {
	if node.Props == nil {
		node.Props = make(map[string]any)
	}
	delete(node.Props, "_focused")
	if node.ID == focusedID {
		node.Props["_focused"] = true
	}
	if value, exists := values[node.ID]; exists {
		switch node.Kind {
		case "checkbox", "toggle":
			node.Props["checked"] = value
			node.Props["value"] = value
		case "tabs":
			node.Props["active"] = value
		case "select", "radioGroup":
			node.Props["value"] = value
		default:
			node.Props["value"] = value
		}
	}
	for index := range node.Children {
		applyModalControlState(&node.Children[index], values, focusedID)
	}
}

func modalDocumentWithPendingControlValues(document json.RawMessage, pending map[string]modalPendingControlValue) json.RawMessage {
	if len(document) == 0 || len(pending) == 0 {
		return document
	}
	var tree modalDocumentTree
	if json.Unmarshal(document, &tree) != nil {
		return document
	}
	values := make(map[string]any, len(pending))
	for id, value := range pending {
		values[id] = value.value
	}
	applyModalControlValues(&tree.Root, values)
	updated, err := json.Marshal(tree)
	if err != nil {
		return document
	}
	return updated
}

func applyModalControlValues(node *modalDocumentNode, values map[string]any) {
	if value, exists := values[node.ID]; exists {
		if node.Props == nil {
			node.Props = make(map[string]any)
		}
		switch node.Kind {
		case "checkbox", "toggle":
			node.Props["checked"] = value
			node.Props["value"] = value
		case "tabs":
			node.Props["active"] = value
		default:
			node.Props["value"] = value
		}
	}
	for index := range node.Children {
		applyModalControlValues(&node.Children[index], values)
	}
}

func validateModalDocument(document json.RawMessage, expectedSurfaceID string) error {
	var tree modalDocumentTree
	decoder := json.NewDecoder(strings.NewReader(string(document)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&tree); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("trailing document data")
	}
	versioned := tree.SchemaVersion != 0 || tree.Protocol != "" || tree.Revision != 0 || tree.SurfaceID != "" || tree.Locale != ""
	if versioned {
		if tree.SchemaVersion != 1 || tree.Protocol != "afterburner.ui" || tree.Revision < 1 || !validModalID.MatchString(tree.SurfaceID) || len(tree.Locale) > 64 || strings.ContainsRune(tree.Locale, '\x1b') {
			return errors.New("invalid document envelope")
		}
		if expectedSurfaceID != "" && tree.SurfaceID != expectedSurfaceID {
			return errors.New("document surface mismatch")
		}
	}
	if tree.Root.Kind != "dialog" {
		return errors.New("document root must be dialog")
	}
	state := modalDocumentValidationState{ids: make(map[string]struct{}), requireInteractiveIDs: versioned}
	return validateModalDocumentNode(tree.Root, &state, 1)
}

func modalDocumentRevision(document json.RawMessage) int64 {
	if len(document) == 0 {
		return 0
	}
	var envelope struct {
		Revision int64 `json:"revision"`
	}
	if err := json.Unmarshal(document, &envelope); err != nil || envelope.Revision < 1 {
		return 0
	}
	return envelope.Revision
}

type modalDocumentValidationState struct {
	nodes                 int
	ids                   map[string]struct{}
	requireInteractiveIDs bool
}

func validateModalDocumentNode(node modalDocumentNode, state *modalDocumentValidationState, depth int) error {
	state.nodes++
	if state.nodes > modalMaxDocumentNodes || depth > modalMaxDocumentDepth || len(node.Children) > modalMaxDocumentChildren {
		return errors.New("document structural limit exceeded")
	}
	if _, ok := modalDocumentKinds[node.Kind]; !ok {
		return errors.New("unsupported document component")
	}
	if node.ID != "" {
		if !validModalID.MatchString(node.ID) {
			return errors.New("invalid document component id")
		}
		if _, exists := state.ids[node.ID]; exists {
			return errors.New("duplicate document component id")
		}
		state.ids[node.ID] = struct{}{}
	} else if state.requireInteractiveIDs {
		if _, interactive := modalInteractiveDocumentKinds[node.Kind]; interactive {
			return errors.New("interactive document component requires id")
		}
	}
	for _, values := range []map[string]any{node.Props, node.Accessibility, node.ActionBindings, node.Metadata} {
		if modalJSONContainsEscape(values) {
			return errors.New("document contains terminal escape")
		}
	}
	for _, child := range node.Children {
		if err := validateModalDocumentNode(child, state, depth+1); err != nil {
			return err
		}
	}
	return nil
}

func modalJSONContainsEscape(value any) bool {
	switch typed := value.(type) {
	case string:
		return strings.ContainsRune(typed, '\x1b')
	case []any:
		for _, entry := range typed {
			if modalJSONContainsEscape(entry) {
				return true
			}
		}
	case map[string]any:
		for _, entry := range typed {
			if modalJSONContainsEscape(entry) {
				return true
			}
		}
	}
	return false
}

func modalDocumentBodyLines(frame ModalFrame, width int) []string {
	if len(frame.Document) == 0 {
		return nil
	}
	var tree modalDocumentTree
	if err := json.Unmarshal(frame.Document, &tree); err != nil || strings.TrimSpace(tree.Root.Kind) == "" {
		return nil
	}
	var raw []string
	if shortcut := modalShortcutSummary(frame.Actions); shortcut != "" {
		raw = append(raw, shortcut)
	}
	if frame.Footer != "" {
		raw = append(raw, printableModalText(frame.Footer, true))
	}
	appendModalDocumentNodeLines(&raw, tree.Root, "", width)
	return compactModalBodyLines(raw, width)
}

func modalShortcutSummary(actions []ModalAction) string {
	if len(actions) == 0 {
		return ""
	}
	parts := make([]string, 0, len(actions))
	for _, action := range actions {
		if strings.EqualFold(action.Name, "close") {
			continue
		}
		label := printableModalText(firstNonEmpty(action.Label, action.Name), false)
		key := printableModalText(action.Key, false)
		if key != "" {
			label = key + " " + label
		}
		parts = append(parts, label)
	}
	parts = append(parts, "q/Esc Close")
	return "Shortcuts: " + strings.Join(parts, " · ") + " · ↑/↓ PgUp/PgDn Home/End Scroll"
}

func appendModalDocumentNodeLines(lines *[]string, node modalDocumentNode, context string, width int) {
	kind := strings.TrimSpace(node.Kind)
	switch kind {
	case "dialog", "application", "row", "column", "stack", "group", "toolbar":
		appendModalDocumentChildren(lines, node.Children, context, width)
	case "actionBar":
		appendModalLine(lines, modalDocumentActionBarLine(node))
	case "surface", "window", "viewport", "split", "scroll":
		appendModalContainer(lines, node, context, width)
	case "breadcrumb":
		appendModalLine(lines, modalBreadcrumbLine(node))
	case "contextMenu":
		appendModalContextMenuLines(lines, node, width)
	case "section", "box", "disclosure":
		appendModalSection(lines, firstNonEmpty(modalStringProp(node.Props, "title"), modalStringProp(node.Props, "label"), strings.Title(kind)))
		appendModalDocumentChildren(lines, node.Children, context, width)
	case "separator":
		appendModalSeparator(lines, node)
	case "spacer":
		appendModalLine(lines, "")
	case "icon":
		appendModalLine(lines, modalIconLine(node))
	case "badge":
		appendModalLine(lines, modalBadgeLine(node))
	case "form":
		appendModalSection(lines, firstNonEmpty(modalStringProp(node.Props, "title"), modalStringProp(node.Props, "label"), "Form"))
		appendModalDocumentChildren(lines, node.Children, context, width)
	case "statusGrid", "grid":
		label := modalStringProp(node.Props, "label")
		if strings.Contains(strings.ToLower(label), "status cards") {
			label = "Status cards"
		}
		appendModalSection(lines, firstNonEmpty(label, "Status cards"))
		appendModalDocumentChildren(lines, node.Children, context, width)
	case "card":
		appendModalCardLine(lines, node)
	case "progress", "meter", "bar", "slider":
		label := firstNonEmpty(modalStringProp(node.Props, "label"), "Progress")
		if modalHasLinePrefix(*lines, label+":") {
			return
		}
		status := modalStringProp(node.Props, "status")
		if status == "" {
			status = fmt.Sprint(node.Props["value"])
		}
		appendModalLine(lines, label+": "+modalProgressBar(firstNonEmpty(status, fmt.Sprint(node.Props["value"])))+" "+status)
	case "sparkline":
		label := firstNonEmpty(modalStringProp(node.Props, "label"), "Signal trend")
		if modalHasLinePrefix(*lines, label+":") {
			return
		}
		appendModalLine(lines, label+": "+modalSparklineProp(node.Props["values"]))
	case "alert", "toast":
		appendModalLine(lines, modalStatusLine(node, "⚠"))
	case "empty":
		appendModalLine(lines, firstNonEmpty(modalStringProp(node.Props, "message"), modalStringProp(node.Props, "label"), "No content."))
	case "spinner", "loading":
		appendModalLine(lines, modalStatusLine(node, "⏳"))
	case "errorBoundary":
		appendModalLine(lines, modalStatusLine(node, "✕"))
	case "image", "video", "chart", "terminal", "canvas", "extensionOutlet":
		appendModalLine(lines, modalEmbeddedSurfaceLine(node))
	case "panel":
		title := modalStringProp(node.Props, "title")
		if strings.EqualFold(title, "Details") {
			title = "Selected event"
		}
		appendModalSection(lines, title)
		appendModalDocumentChildren(lines, node.Children, title, width)
	case "list", "tree":
		appendModalListLines(lines, node)
	case "timeline", "log":
		appendModalTimelineLines(lines, node)
	case "tabs":
		appendModalLine(lines, modalControlFocusPrefix(node)+modalTabsLine(node))
	case "commandPalette":
		appendModalLine(lines, modalCommandPaletteLine(node))
	case "keybindingHint":
		appendModalLine(lines, modalKeybindingHintLine(node))
	case "table":
		appendModalTableLines(lines, node, context, width)
	case "markdown":
		appendModalLine(lines, modalStringProp(node.Props, "markdown"))
	case "code":
		appendModalLine(lines, modalStringProp(node.Props, "code"))
	case "text":
		appendModalLine(lines, modalStringProp(node.Props, "value"))
	case "keyValue", "detail":
		appendModalFieldLine(lines, firstNonEmpty(modalStringProp(node.Props, "label"), modalStringProp(node.Props, "key")), modalStringProp(node.Props, "value"))
	case "textInput", "searchInput", "numberInput", "dateInput", "fileInput", "passwordInput":
		appendModalInputLine(lines, node)
	case "textArea":
		appendModalTextAreaLines(lines, node)
	case "select", "radioGroup":
		appendModalFieldLine(lines, modalControlFocusPrefix(node)+firstNonEmpty(modalStringProp(node.Props, "label"), "Selection"), modalChoiceText(node.Props))
	case "checkbox":
		appendModalLine(lines, modalCheckboxLine(node, "☐", "☑"))
	case "toggle":
		appendModalLine(lines, modalCheckboxLine(node, "○", "●"))
	case "button":
		appendModalLine(lines, modalActionControlLine(node, "Button"))
	case "link":
		appendModalLine(lines, modalActionControlLine(node, "Link"))
	case "pagination":
		appendModalLine(lines, modalPaginationLine(node))
	case "help":
		appendModalLine(lines, modalHelpLine(node))
	case "confirmation", "prompt":
		appendModalLine(lines, modalPromptLine(node))
	default:
		if title := firstNonEmpty(modalStringProp(node.Props, "title"), modalStringProp(node.Props, "label"), modalStringProp(node.Props, "message")); title != "" {
			appendModalLine(lines, title)
		}
		appendModalDocumentChildren(lines, node.Children, context, width)
	}
}

func appendModalDocumentChildren(lines *[]string, children []modalDocumentNode, context string, width int) {
	for _, child := range children {
		appendModalDocumentNodeLines(lines, child, context, width)
	}
}

func appendModalContainer(lines *[]string, node modalDocumentNode, context string, width int) {
	if title := firstNonEmpty(modalStringProp(node.Props, "title"), modalStringProp(node.Props, "label")); title != "" {
		appendModalSection(lines, title)
	}
	appendModalDocumentChildren(lines, node.Children, context, width)
}

func modalBreadcrumbLine(node modalDocumentNode) string {
	items, _ := node.Props["items"].([]any)
	parts := make([]string, 0, len(items))
	for _, item := range items {
		if label := modalValueSummary(item); label != "" {
			parts = append(parts, label)
		}
	}
	if len(parts) == 0 {
		return "Breadcrumb: " + firstNonEmpty(modalStringProp(node.Props, "label"), modalStringProp(node.Props, "value"), "root")
	}
	return "Breadcrumb: " + strings.Join(parts, " › ")
}

func appendModalContextMenuLines(lines *[]string, node modalDocumentNode, width int) {
	appendModalSection(lines, firstNonEmpty(modalStringProp(node.Props, "title"), modalStringProp(node.Props, "label"), "Context menu"))
	items, _ := node.Props["items"].([]any)
	for _, item := range items {
		entry, _ := item.(map[string]any)
		if entry == nil {
			appendModalLine(lines, "  • "+modalValueSummary(item))
			continue
		}
		appendModalLine(lines, "  • "+modalActionControlLine(modalDocumentNode{Kind: "button", Props: entry}, "Menu item"))
	}
	appendModalDocumentChildren(lines, node.Children, "Context menu", width)
}

func modalDocumentActionBarLine(node modalDocumentNode) string {
	label := firstNonEmpty(modalStringProp(node.Props, "label"), "Actions")
	parts := make([]string, 0, len(node.Children))
	for _, child := range node.Children {
		if child.Kind == "button" || child.Kind == "link" {
			parts = append(parts, modalActionControlLine(child, "Action"))
		}
	}
	if len(parts) == 0 {
		return label + ": none"
	}
	return label + ": " + strings.Join(parts, "  ")
}

func appendModalSeparator(lines *[]string, node modalDocumentNode) {
	label := firstNonEmpty(modalStringProp(node.Props, "label"), modalStringProp(node.Props, "title"))
	if label == "" {
		appendModalLine(lines, "────────")
		return
	}
	appendModalLine(lines, "── "+label+" "+strings.Repeat("─", 6))
}

func modalIconLine(node modalDocumentNode) string {
	icon := firstNonEmpty(modalStringProp(node.Props, "icon"), modalStringProp(node.Props, "name"), "•")
	label := firstNonEmpty(modalStringProp(node.Props, "label"), modalStringProp(node.Props, "title"), modalStringProp(node.Props, "description"))
	if label == "" {
		return icon
	}
	return icon + " " + label
}

func modalBadgeLine(node modalDocumentNode) string {
	label := firstNonEmpty(modalStringProp(node.Props, "label"), modalStringProp(node.Props, "text"), modalStringProp(node.Props, "value"), "Badge")
	tone := modalStringProp(node.Props, "tone")
	if tone == "" {
		return "• " + label
	}
	return modalToneGlyph(tone) + " " + label
}

func appendModalCardLine(lines *[]string, node modalDocumentNode) {
	parts := []string{modalStringProp(node.Props, "title")}
	for _, child := range node.Children {
		if child.Kind == "text" {
			parts = append(parts, modalStringProp(child.Props, "value"))
		}
	}
	cleaned := nonEmptyModalParts(parts)
	if len(cleaned) == 0 {
		return
	}
	if len(cleaned) >= 2 {
		cleaned[1] = modalToneGlyph(firstNonEmpty(modalStringProp(node.Props, "tone"), cleaned[1])) + " " + cleaned[1]
	}
	appendModalLine(lines, "  • "+strings.Join(cleaned, "  │  "))
}

func modalStatusLine(node modalDocumentNode, marker string) string {
	message := firstNonEmpty(modalStringProp(node.Props, "message"), modalStringProp(node.Props, "title"), modalStringProp(node.Props, "label"), modalStringProp(node.Props, "value"), "Status")
	return marker + " " + printableModalText(message, false)
}

func modalEmbeddedSurfaceLine(node modalDocumentNode) string {
	label := firstNonEmpty(modalStringProp(node.Props, "label"), modalStringProp(node.Props, "title"), node.ID, strings.Title(node.Kind))
	summary := firstNonEmpty(modalStringProp(node.Props, "alt"), modalStringProp(node.Props, "description"), modalStringProp(node.Props, "caption"), modalStringProp(node.Props, "source"), "available in rich UI")
	return strings.Title(node.Kind) + ": " + printableModalText(label, false) + " — " + printableModalText(summary, false)
}

func modalActionControlLine(node modalDocumentNode, fallback string) string {
	label := firstNonEmpty(modalStringProp(node.Props, "label"), modalStringProp(node.Props, "title"), modalStringProp(node.Props, "text"), node.ID, fallback)
	key := firstNonEmpty(modalStringProp(node.Props, "key"), modalStringProp(node.Props, "keybinding"), modalStringProp(node.Props, "shortcut"))
	prefix := modalControlFocusPrefix(node)
	if key != "" {
		return prefix + "[" + printableModalText(key, false) + "] " + printableModalText(label, false)
	}
	return prefix + "[" + printableModalText(label, false) + "]"
}

func modalPaginationLine(node modalDocumentNode) string {
	label := firstNonEmpty(modalStringProp(node.Props, "label"), "Page")
	current := firstNonEmpty(modalStringProp(node.Props, "page"), modalStringProp(node.Props, "current"), modalStringProp(node.Props, "value"), "?")
	total := firstNonEmpty(modalStringProp(node.Props, "totalPages"), modalStringProp(node.Props, "total"), modalStringProp(node.Props, "max"), "?")
	return fmt.Sprintf("%s: %s/%s", label, current, total)
}

func modalHelpLine(node modalDocumentNode) string {
	return "Help: " + firstNonEmpty(modalStringProp(node.Props, "text"), modalStringProp(node.Props, "label"), modalStringProp(node.Props, "description"), "No help text provided.")
}

func modalPromptLine(node modalDocumentNode) string {
	label := firstNonEmpty(modalStringProp(node.Props, "label"), modalStringProp(node.Props, "title"), strings.Title(node.Kind))
	message := firstNonEmpty(modalStringProp(node.Props, "message"), modalStringProp(node.Props, "description"), modalStringProp(node.Props, "value"))
	if message == "" {
		return label
	}
	return label + ": " + message
}

func appendModalInputLine(lines *[]string, node modalDocumentNode) {
	label := firstNonEmpty(modalStringProp(node.Props, "label"), modalStringProp(node.Props, "name"), modalStringProp(node.Props, "id"), "Input")
	value := modalStringProp(node.Props, "value")
	if node.Kind == "passwordInput" && value != "" {
		value = strings.Repeat("•", minInt(8, utf8.RuneCountInString(value)))
	}
	if value == "" {
		value = firstNonEmpty(modalStringProp(node.Props, "placeholder"), "empty")
		value = "‹" + value + "›"
	}
	appendModalFieldLine(lines, modalControlFocusPrefix(node)+label, value)
}

func appendModalTextAreaLines(lines *[]string, node modalDocumentNode) {
	label := modalControlFocusPrefix(node) + firstNonEmpty(modalStringProp(node.Props, "label"), modalStringProp(node.Props, "name"), "Text")
	appendModalLine(lines, label+":")
	for _, line := range wrapModalText(modalStringProp(node.Props, "value"), 72) {
		appendModalLine(lines, "  "+line)
	}
}

func appendModalFieldLine(lines *[]string, label, value string) {
	label = strings.TrimSpace(printableModalText(label, false))
	value = strings.TrimSpace(printableModalText(value, false))
	if label == "" && value == "" {
		return
	}
	if label == "" {
		appendModalLine(lines, value)
		return
	}
	if value == "" {
		value = "empty"
	}
	appendModalLine(lines, label+": "+value)
}

func modalCheckboxLine(node modalDocumentNode, off, on string) string {
	label := firstNonEmpty(modalStringProp(node.Props, "label"), modalStringProp(node.Props, "name"), node.ID, "Option")
	state := off
	if modalBoolProp(node.Props, "checked") || modalBoolProp(node.Props, "selected") || modalBoolProp(node.Props, "value") {
		state = on
	}
	return modalControlFocusPrefix(node) + state + " " + label
}

func modalControlFocusPrefix(node modalDocumentNode) string {
	if modalBoolProp(node.Props, "_focused") {
		return "▶ "
	}
	return ""
}

func modalChoiceText(props map[string]any) string {
	selected := firstNonEmpty(modalStringProp(props, "value"), modalStringProp(props, "selected"), modalStringProp(props, "selectedValue"))
	options, _ := props["options"].([]any)
	for _, option := range options {
		entry, _ := option.(map[string]any)
		value := modalStringProp(entry, "value")
		label := firstNonEmpty(modalStringProp(entry, "label"), value)
		if modalBoolProp(entry, "selected") || (selected != "" && value == selected) || (selected != "" && label == selected) {
			return firstNonEmpty(label, selected)
		}
	}
	return firstNonEmpty(selected, modalStringProp(props, "placeholder"), "none")
}

func appendModalListLines(lines *[]string, node modalDocumentNode) {
	appendModalSection(lines, firstNonEmpty(modalStringProp(node.Props, "title"), modalStringProp(node.Props, "label"), strings.Title(node.Kind)))
	items, _ := node.Props["items"].([]any)
	if len(items) == 0 && len(node.Children) > 0 {
		for _, child := range node.Children {
			appendModalLine(lines, "  • "+modalNodeSummary(child))
		}
		return
	}
	for index, item := range items {
		if index >= 12 {
			appendModalLine(lines, fmt.Sprintf("  … %d more item(s)", len(items)-index))
			break
		}
		appendModalLine(lines, "  • "+modalValueSummary(item))
	}
}

func appendModalTimelineLines(lines *[]string, node modalDocumentNode) {
	appendModalSection(lines, firstNonEmpty(modalStringProp(node.Props, "title"), modalStringProp(node.Props, "label"), strings.Title(node.Kind)))
	items, _ := node.Props["items"].([]any)
	if len(items) == 0 && len(node.Children) > 0 {
		for _, child := range node.Children {
			appendModalLine(lines, "  • "+modalNodeSummary(child))
		}
		return
	}
	for index, item := range items {
		if index >= 12 {
			appendModalLine(lines, fmt.Sprintf("  … %d more event(s)", len(items)-index))
			break
		}
		entry, _ := item.(map[string]any)
		if entry == nil {
			appendModalLine(lines, "  • "+modalValueSummary(item))
			continue
		}
		timeText := firstNonEmpty(modalStringProp(entry, "time"), modalStringProp(entry, "timestamp"))
		label := firstNonEmpty(modalStringProp(entry, "label"), modalStringProp(entry, "title"), modalStringProp(entry, "message"), modalStringProp(entry, "value"))
		appendModalLine(lines, "  • "+strings.TrimSpace(strings.Join(nonEmptyModalParts([]string{timeText, label}), " — ")))
	}
}

func modalTabsLine(node modalDocumentNode) string {
	items, _ := node.Props["items"].([]any)
	if len(items) == 0 {
		items, _ = node.Props["tabs"].([]any)
	}
	active := firstNonEmpty(modalStringProp(node.Props, "active"), modalStringProp(node.Props, "activeId"), modalStringProp(node.Props, "value"))
	parts := make([]string, 0, len(items))
	for _, item := range items {
		entry, _ := item.(map[string]any)
		label := modalValueSummary(item)
		if entry != nil {
			value := firstNonEmpty(modalStringProp(entry, "id"), modalStringProp(entry, "value"), label)
			label = firstNonEmpty(modalStringProp(entry, "label"), modalStringProp(entry, "title"), value)
			if modalBoolProp(entry, "active") || (active != "" && value == active) || (active != "" && label == active) {
				label = "[" + label + "]"
			}
		}
		parts = append(parts, label)
	}
	return "Tabs: " + strings.Join(parts, "  ")
}

func modalCommandPaletteLine(node modalDocumentNode) string {
	label := firstNonEmpty(modalStringProp(node.Props, "label"), modalStringProp(node.Props, "title"), "Command palette")
	query := firstNonEmpty(modalStringProp(node.Props, "query"), modalStringProp(node.Props, "value"), modalStringProp(node.Props, "placeholder"))
	if query == "" {
		return label + ": ready"
	}
	return label + ": ‹" + printableModalText(query, false) + "›"
}

func modalKeybindingHintLine(node modalDocumentNode) string {
	key := firstNonEmpty(modalStringProp(node.Props, "key"), modalStringProp(node.Props, "keybinding"), modalStringProp(node.Props, "shortcut"))
	label := firstNonEmpty(modalStringProp(node.Props, "label"), modalStringProp(node.Props, "description"), modalStringProp(node.Props, "action"))
	if key == "" {
		return label
	}
	if label == "" {
		return "[" + printableModalText(key, false) + "]"
	}
	return "[" + printableModalText(key, false) + "] " + printableModalText(label, false)
}

func modalNodeSummary(node modalDocumentNode) string {
	return firstNonEmpty(modalStringProp(node.Props, "label"), modalStringProp(node.Props, "title"), modalStringProp(node.Props, "message"), modalStringProp(node.Props, "value"), node.ID, node.Kind)
}

func modalValueSummary(value any) string {
	entry, _ := value.(map[string]any)
	if entry == nil {
		return printableModalText(fmt.Sprint(value), false)
	}
	return firstNonEmpty(modalStringProp(entry, "label"), modalStringProp(entry, "title"), modalStringProp(entry, "message"), modalStringProp(entry, "value"), modalStringProp(entry, "id"))
}

func appendModalTableLines(lines *[]string, node modalDocumentNode, context string, width int) {
	label := modalStringProp(node.Props, "label")
	if strings.EqualFold(context, "Metadata timeline") && strings.EqualFold(label, "Timeline table") {
		label = "Metadata timeline table"
	}
	appendModalLine(lines, firstNonEmpty(label, "Table"))
	columns, _ := node.Props["columns"].([]any)
	rows, _ := node.Props["rows"].([]any)
	columnIDs := make([]string, 0, len(columns))
	headers := make([]string, 0, len(columns))
	for _, column := range columns {
		entry, _ := column.(map[string]any)
		id := modalStringProp(entry, "id")
		if id == "" {
			continue
		}
		columnIDs = append(columnIDs, id)
		header := firstNonEmpty(modalStringProp(entry, "title"), id)
		if strings.EqualFold(header, "Timestamp") {
			header = "Time"
		}
		headers = append(headers, header)
	}
	tableRows := make([][]string, 0, minInt(len(rows), 10))
	for index, row := range rows {
		if index >= 10 {
			break
		}
		entry, _ := row.(map[string]any)
		cellValues, _ := entry["cells"].(map[string]any)
		if cellValues == nil {
			cellValues = entry
		}
		cells := make([]string, 0, len(columnIDs))
		for _, id := range columnIDs {
			cells = append(cells, modalTableCellValue(id, cellValues[id]))
		}
		tableRows = append(tableRows, cells)
	}
	columnWidths := modalTableColumnWidths(headers, tableRows, width)
	if len(headers) > 0 {
		appendModalLine(lines, modalTableRow(headers, columnWidths))
		appendModalLine(lines, modalTableDivider(columnWidths))
	}
	for _, cells := range tableRows {
		appendModalLine(lines, modalTableRow(cells, columnWidths))
	}
	if len(rows) > len(tableRows) {
		appendModalLine(lines, fmt.Sprintf("… %d more row(s)", len(rows)-len(tableRows)))
	}
}

func modalPriorityBodyLines(body string) []string {
	if strings.TrimSpace(body) == "" {
		return nil
	}
	bodyLines := strings.Split(printableModalText(body, true), "\n")
	var lines []string
	captureSelected := false
	captureResult := false
	for _, line := range bodyLines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			if captureSelected {
				captureSelected = false
			}
			if captureResult {
				captureResult = false
			}
			continue
		}
		if strings.HasPrefix(trimmed, "Storage usage:") {
			appendModalLine(&lines, modalProgressText(trimmed))
			continue
		}
		if strings.HasPrefix(trimmed, "Signal trend:") {
			appendModalLine(&lines, "Signal trend: "+strings.TrimSpace(strings.TrimPrefix(trimmed, "Signal trend:")))
			continue
		}
		if strings.HasPrefix(trimmed, "Needs attention:") {
			appendModalLine(&lines, "⚠ "+trimmed)
			continue
		}
		if strings.EqualFold(trimmed, "Doctor") || strings.EqualFold(trimmed, "Export") {
			appendModalSection(&lines, trimmed)
			captureResult = true
			continue
		}
		if strings.EqualFold(trimmed, "Selected event") {
			appendModalSection(&lines, trimmed)
			captureSelected = true
			continue
		}
		if captureResult {
			if strings.EqualFold(trimmed, "Selected event") {
				captureResult = false
			}
			appendModalLine(&lines, "  "+trimmed)
			continue
		}
		if captureSelected {
			if strings.HasPrefix(trimmed, "Metadata timeline") {
				captureSelected = false
				continue
			}
			appendModalLine(&lines, "  "+trimmed)
		}
	}
	return lines
}

func appendModalSection(lines *[]string, title string) {
	title = strings.TrimSpace(title)
	if title == "" {
		return
	}
	appendModalLine(lines, "")
	appendModalLine(lines, "▌ "+title)
}

func modalToneGlyph(value string) string {
	lower := strings.ToLower(value)
	switch {
	case strings.Contains(lower, "success"), strings.Contains(lower, "enabled"), strings.Contains(lower, "healthy"):
		return "✓"
	case strings.Contains(lower, "warning"), strings.Contains(lower, "failed"), strings.Contains(lower, "error"), strings.Contains(lower, "dropped"), strings.Contains(lower, "disabled"):
		return "!"
	default:
		return "•"
	}
}

func modalHasLinePrefix(lines []string, prefix string) bool {
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), prefix) {
			return true
		}
	}
	return false
}

func modalProgressText(line string) string {
	percent := modalFirstPercent(line)
	if percent < 0 {
		return line
	}
	return line + "  " + modalProgressBar(percent)
}

func modalFirstPercent(line string) int {
	for index := 0; index < len(line); index++ {
		if line[index] < '0' || line[index] > '9' {
			continue
		}
		end := index
		for end < len(line) && line[end] >= '0' && line[end] <= '9' {
			end++
		}
		if end < len(line) && line[end] == '%' {
			value, err := strconv.Atoi(line[index:end])
			if err == nil {
				return clampInt(value, 0, 100)
			}
		}
		index = end
	}
	return -1
}

func modalProgressBar(value any) string {
	percent := 0
	switch typed := value.(type) {
	case float64:
		percent = int(typed)
	case int:
		percent = typed
	case string:
		percent = modalFirstPercent(typed + "%")
	}
	percent = clampInt(percent, 0, 100)
	filled := percent * 10 / 100
	if percent > 0 && filled == 0 {
		filled = 1
	}
	return strings.Repeat("█", filled) + strings.Repeat("░", 10-filled)
}

func modalTableColumnWidths(headers []string, rows [][]string, available int) []int {
	if len(headers) == 0 {
		return nil
	}
	separatorWidth := maxInt(0, len(headers)-1) * lipgloss.Width(" │ ")
	available = maxInt(len(headers), available-separatorWidth)
	widths := make([]int, len(headers))
	for i, header := range headers {
		widths[i] = clampInt(lipgloss.Width(header), 3, 24)
	}
	for _, row := range rows {
		for i, cell := range row {
			if i >= len(widths) {
				break
			}
			widths[i] = minInt(32, maxInt(widths[i], lipgloss.Width(cell)))
		}
	}
	for modalWidthSum(widths) > available {
		largest := 0
		for i := range widths {
			if widths[i] > widths[largest] {
				largest = i
			}
		}
		if widths[largest] <= 3 {
			break
		}
		widths[largest]--
	}
	return widths
}

func modalWidthSum(values []int) int {
	total := 0
	for _, value := range values {
		total += value
	}
	return total
}

func modalTableRow(cells []string, widths []int) string {
	parts := make([]string, 0, len(cells))
	for i, cell := range cells {
		width := 12
		if i < len(widths) {
			width = widths[i]
		}
		parts = append(parts, modalFitCell(cell, width))
	}
	return strings.Join(parts, " │ ")
}

func modalTableDivider(widths []int) string {
	parts := make([]string, 0, len(widths))
	for _, width := range widths {
		parts = append(parts, strings.Repeat("─", width))
	}
	return strings.Join(parts, "─┼─")
}

func modalTableCellValue(id string, value any) string {
	text := fmt.Sprint(value)
	if text == "<nil>" {
		return ""
	}
	if strings.EqualFold(id, "timestamp") && len(text) >= len("2006-01-02T15:04:05") {
		text = strings.Replace(text[5:19], "T", " ", 1)
	}
	return text
}

func modalFitCell(value string, width int) string {
	value = strings.TrimSpace(value)
	for lipgloss.Width(value) > width && value != "" {
		runes := []rune(value)
		value = string(runes[:len(runes)-1])
	}
	if lipgloss.Width(value) == width {
		return value
	}
	return value + strings.Repeat(" ", maxInt(0, width-lipgloss.Width(value)))
}

func modalStringProp(props map[string]any, key string) string {
	if props == nil {
		return ""
	}
	value, ok := props[key]
	if !ok || value == nil {
		return ""
	}
	return fmt.Sprint(value)
}

func modalNumberProp(props map[string]any, key string, fallback float64) float64 {
	if value, ok := modalNumber(props[key]); ok {
		return value
	}
	return fallback
}

func modalNumber(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case json.Number:
		number, err := typed.Float64()
		return number, err == nil
	default:
		number, err := strconv.ParseFloat(fmt.Sprint(value), 64)
		return number, err == nil
	}
}

func modalBoolProp(props map[string]any, key string) bool {
	if props == nil {
		return false
	}
	switch value := props[key].(type) {
	case bool:
		return value
	case string:
		return strings.EqualFold(value, "true") || strings.EqualFold(value, "yes") || value == "1"
	case float64:
		return value != 0
	default:
		return false
	}
}

func modalSparklineProp(value any) string {
	values, _ := value.([]any)
	if len(values) == 0 {
		return "▁"
	}
	bars := []string{"▁", "▃", "▆", "█"}
	var out strings.Builder
	for _, entry := range values {
		level, _ := entry.(float64)
		index := clampInt(int(level), 0, len(bars)-1)
		out.WriteString(bars[index])
	}
	return out.String()
}

func appendModalLine(lines *[]string, line string) {
	line = strings.TrimRight(printableModalText(line, true), " ")
	if line == "" && (len(*lines) == 0 || (*lines)[len(*lines)-1] == "") {
		return
	}
	*lines = append(*lines, line)
}

func nonEmptyModalParts(parts []string) []string {
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			result = append(result, part)
		}
	}
	return result
}

func compactModalBodyLines(raw []string, width int) []string {
	text := strings.TrimSpace(strings.Join(raw, "\n"))
	if text == "" {
		return nil
	}
	return wrapModalText(text, width)
}

func wrapModalText(text string, width int) []string {
	if width <= 0 {
		return nil
	}
	text = strings.ReplaceAll(text, "	", "    ")
	var lines []string
	for _, raw := range strings.Split(text, "\n") {
		raw = strings.TrimRight(raw, " ")
		if raw == "" {
			lines = append(lines, "")
			continue
		}
		line := ""
		for _, word := range strings.Fields(raw) {
			if line == "" {
				for lipgloss.Width(word) > width {
					prefix, rest := splitModalWidth(word, width)
					lines = append(lines, prefix)
					word = rest
				}
				line = word
				continue
			}
			if lipgloss.Width(line)+1+lipgloss.Width(word) <= width {
				line += " " + word
				continue
			}
			lines = append(lines, line)
			line = ""
			for lipgloss.Width(word) > width {
				prefix, rest := splitModalWidth(word, width)
				lines = append(lines, prefix)
				word = rest
			}
			line = word
		}
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func fitModalLine(text string, width int) string {
	return modalFitLine(text, width)
}

func modalFitLine(text string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(text) <= width {
		return text
	}
	if width == 1 {
		return "…"
	}
	prefix, _ := splitModalWidth(text, width-1)
	return prefix + "…"
}

func modalPadLine(text string, width int) string {
	visible := lipgloss.Width(text)
	if visible >= width {
		return text
	}
	return text + strings.Repeat(" ", width-visible)
}

func splitModalWidth(text string, width int) (string, string) {
	if width <= 0 {
		return "", text
	}
	var out strings.Builder
	used := 0
	for index, r := range text {
		w := lipgloss.Width(string(r))
		if used+w > width {
			return out.String(), text[index:]
		}
		out.WriteRune(r)
		used += w
	}
	return out.String(), ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func pluralSuffix(value int) string {
	if value == 1 {
		return ""
	}
	return "s"
}

func (r *TerminalModalRenderer) HideModal() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.active {
		return
	}
	r.queryRequests.Reset()
	r.active = false
	r.activeFrame = ModalFrame{}
	r.scrollOffset = 0
	r.documentControls = nil
	r.focusedControlID = ""
	r.controlValues = make(map[string]any)
	if r.writer != nil {
		if r.hostAlt && !r.screen.useAlt {
			_, _ = io.WriteString(r.writer, "\x1b[?1049l")
		}
		_, _ = io.WriteString(r.writer, r.screen.Repaint())
		r.hostAlt = r.screen.useAlt
	}
}
