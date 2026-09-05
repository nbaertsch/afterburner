package terminal

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
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
	ID      string        `json:"id"`
	Title   string        `json:"title"`
	Status  string        `json:"status"`
	Body    string        `json:"body"`
	Footer  string        `json:"footer"`
	Actions []ModalAction `json:"actions,omitempty"`
}

// ModalEvent is sent back to the extension runtime when a human key maps to
// a modal action or close request.
type ModalEvent struct {
	Type       string `json:"type"`
	ID         string `json:"id"`
	Generation int64  `json:"generation"`
	ActionName string `json:"actionName,omitempty"`
	Key        string `json:"key,omitempty"`
}

const (
	modalMaxMessageBytes = 256 * 1024
	modalMaxFieldBytes   = 64 * 1024
	modalMaxActions      = 16
	modalMaxGeneration   = 1<<53 - 1

	// ModalBlackBox* identifies the enterprise Black Box live canvas registered
	// natively before any runtime extension can request a modal capability.
	ModalBlackBoxOwnerExtensionID = "black-box"
	ModalBlackBoxCanvasID         = "afterburner-black-box-live"
	ModalBlackBoxSurfaceID        = "afterburner-black-box-live"

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
	surfaces             map[string]modalIdentity
	active               map[modalIdentity]modalSession
	order                []modalIdentity
	waiters              map[modalEventKey][]chan ModalEvent
	events               map[modalEventKey][]ModalEvent
	renderer             ModalRenderer
	pollTimeout          time.Duration
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

func printableModalText(value string, multiline bool) string {
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

// ModalRenderer paints the active modal frame atop the terminal that the
// Broker's ConPTY output is being written to. HideModal is called when the
// last modal closes so the host can repaint Copilot's live screen.
type ModalRenderer interface {
	ShowModal(frame ModalFrame)
	HideModal()
}

func NewModalServer(broker *Broker, renderer ModalRenderer) (*ModalServer, error) {
	server := &ModalServer{
		broker:               broker,
		registrations:        make(map[modalIdentity]*modalRegistrationState),
		authorizedClientPIDs: make(map[uint32]struct{}),
		surfaces:             make(map[string]modalIdentity),
		active:               make(map[modalIdentity]modalSession),
		waiters:              make(map[modalEventKey][]chan ModalEvent),
		events:               make(map[modalEventKey][]ModalEvent),
		renderer:             renderer,
		pollTimeout:          30 * time.Second,
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
	if existing, exists := s.surfaces[identity.surfaceID]; exists && existing != identity {
		return ModalCapability{}, fmt.Errorf("modal surface %q is already registered", identity.surfaceID)
	}
	if _, exists := s.registrations[identity]; !exists {
		s.registrations[identity] = &modalRegistrationState{}
		s.surfaces[identity.surfaceID] = identity
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
	Operation        string        `json:"operation"`
	Type             string        `json:"type"`
	ID               string        `json:"id"`
	OwnerExtensionID string        `json:"ownerExtensionId"`
	CanvasID         string        `json:"canvasId"`
	SurfaceID        string        `json:"surfaceId"`
	Generation       *int64        `json:"generation"`
	Title            string        `json:"title"`
	Status           string        `json:"status"`
	Body             string        `json:"body"`
	Footer           string        `json:"footer"`
	Actions          []ModalAction `json:"actions"`
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
	if oversized(req.Title) || oversized(req.Status) || oversized(req.Body) || oversized(req.Footer) {
		return modalResponse{Error: "modal-field-too-large"}
	}
	if len(req.Actions) > modalMaxActions {
		return modalResponse{Error: "modal-too-many-actions"}
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

func (s *ModalServer) open(req modalRequest, identity modalIdentity) modalResponse {
	generation := *req.Generation
	frame := ModalFrame{ID: req.ID, Title: req.Title, Status: req.Status, Body: req.Body, Footer: req.Footer, Actions: copyModalActions(req.Actions)}
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
	frame := mergeModalFrame(previous.frame, req)
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
	s.events[key] = append(s.events[key], event)
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
	if len(data) == 0 {
		return 0, nil
	}
	identity, frame, generation, ok := s.topSession()
	if !ok {
		return len(data), nil
	}
	key := modalInputKey(data)
	if key == "escape" {
		s.requestClose(identity, generation, key)
		return len(data), nil
	}
	if actionName := modalActionForKey(frame, key); actionName != "" {
		s.enqueueEvent(ModalEvent{Type: "action", ID: identity.surfaceID, Generation: generation, ActionName: actionName, Key: key}, identity)
		return len(data), nil
	}
	if key == "q" {
		s.requestClose(identity, generation, key)
	}
	return len(data), nil
}

func modalInputKey(data []byte) string {
	switch string(data) {
	case "\x1b":
		return "escape"
	case "\r", "\n":
		return "enter"
	case "\t":
		return "tab"
	case "q", "Q":
		return strings.ToLower(string(data))
	default:
		if len(data) == 1 && data[0] >= 0x20 && data[0] <= 0x7e {
			return strings.ToLower(string(data))
		}
		return ""
	}
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

	mu          sync.Mutex
	active      bool
	hostAlt     bool
	activeFrame ModalFrame
}

func NewTerminalModalRenderer(writer io.Writer) *TerminalModalRenderer {
	size := Size{Cols: vtDefaultCols, Rows: vtDefaultRows}
	if file, ok := writer.(*os.File); ok {
		size = ConsoleSize(file)
	}
	return NewTerminalModalRendererWithSize(writer, size)
}

func NewTerminalModalRendererWithSize(writer io.Writer, size Size) *TerminalModalRenderer {
	return &TerminalModalRenderer{writer: writer, screen: newVTScreen(size), queryRequests: newTerminalQueryRequestSplitter()}
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
		_, _ = io.WriteString(r.writer, renderModalFrame(r.activeFrame))
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
	r.active = true
	r.activeFrame = frame
	if r.writer == nil {
		return
	}
	_, _ = io.WriteString(r.writer, renderModalFrame(frame))
}

func renderModalFrame(frame ModalFrame) string {
	var out strings.Builder
	out.WriteString("\x1b[?25l\x1b[0m\x1b[H\x1b[2J")
	out.WriteString("\x1b[1m")
	out.WriteString(printableModalText(frame.Title, false))
	out.WriteString("\x1b[0m\r\n")
	if frame.Status != "" {
		out.WriteString(printableModalText(frame.Status, false))
		out.WriteString("\r\n")
	}
	out.WriteString("\r\n")
	body := printableModalText(frame.Body, true)
	for _, line := range strings.Split(body, "\n") {
		out.WriteString(line)
		out.WriteString("\r\n")
	}
	if len(frame.Actions) > 0 {
		out.WriteString("\r\n")
		for index, action := range frame.Actions {
			if index > 0 {
				out.WriteString("  ")
			}
			if action.Key != "" {
				out.WriteString("[")
				out.WriteString(printableModalText(action.Key, false))
				out.WriteString("] ")
			}
			out.WriteString(printableModalText(action.Label, false))
		}
		out.WriteString("\r\n")
	}
	if frame.Footer != "" {
		out.WriteString("\r\n")
		out.WriteString(printableModalText(frame.Footer, false))
		out.WriteString("\r\n")
	}
	out.WriteString("\x1b[?25h")
	return out.String()
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
	if r.writer != nil {
		if r.hostAlt && !r.screen.useAlt {
			_, _ = io.WriteString(r.writer, "\x1b[?1049l")
		}
		_, _ = io.WriteString(r.writer, r.screen.Repaint())
		r.hostAlt = r.screen.useAlt
	}
}
