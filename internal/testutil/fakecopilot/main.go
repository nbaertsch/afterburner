package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

type capture struct {
	Args            []string              `json:"args"`
	Env             map[string]string     `json:"env"`
	ProxyReady      bool                  `json:"proxyReady,omitempty"`
	Modal           *modalExerciseCapture `json:"modal,omitempty"`
	Interrupts      int                   `json:"interrupts,omitempty"`
	StdinBytes      int                   `json:"stdinBytes,omitempty"`
	StdinCtrlCBytes int                   `json:"stdinCtrlCBytes,omitempty"`
}

type modalExerciseCapture struct {
	StaleRejected  bool                `json:"staleRejected"`
	RuntimeClient  bool                `json:"runtimeClient,omitempty"`
	InvalidRequest bool                `json:"invalidRequest,omitempty"`
	Error          string              `json:"error,omitempty"`
	Opened         bool                `json:"opened"`
	Updated        bool                `json:"updated"`
	Event          *modalEvent         `json:"event,omitempty"`
	ActionEvents   []modalEvent        `json:"actionEvents,omitempty"`
	EscapeEvent    *modalEvent         `json:"escapeEvent,omitempty"`
	Reopen         *modalReopenCapture `json:"reopen,omitempty"`
	Sizes          []string            `json:"sizes,omitempty"`
}

type modalReopenCapture struct {
	Opened             bool        `json:"opened"`
	StaleCloseRejected bool        `json:"staleCloseRejected"`
	StalePollRejected  bool        `json:"stalePollRejected"`
	Event              *modalEvent `json:"event,omitempty"`
}

type modalEvent struct {
	Type             string `json:"type"`
	ID               string `json:"id"`
	Generation       int64  `json:"generation,omitempty"`
	TargetID         string `json:"targetId,omitempty"`
	DocumentRevision int64  `json:"documentRevision,omitempty"`
	ActionName       string `json:"actionName,omitempty"`
	Key              string `json:"key,omitempty"`
	Value            any    `json:"value,omitempty"`
}

type modalSurface struct {
	OwnerExtensionID string `json:"ownerExtensionId"`
	CanvasID         string `json:"canvasId"`
	SurfaceID        string `json:"surfaceId"`
	SessionID        string `json:"sessionId"`
	Pipe             string `json:"pipe"`
}

type modalResponse struct {
	OK    bool        `json:"ok"`
	Error string      `json:"error,omitempty"`
	Event *modalEvent `json:"event,omitempty"`
}

type modalBootstrap struct {
	Pipe          string         `json:"pipe"`
	SessionID     string         `json:"sessionId"`
	ModalSurfaces []modalSurface `json:"modalSurfaces"`
}

type modalInputCapture struct {
	interrupts      atomic.Int32
	stdinBytes      atomic.Int32
	stdinCtrlCBytes atomic.Int32
}

func startModalInputCapture() *modalInputCapture {
	if os.Getenv("AFTERBURNER_TEST_CAPTURE_INPUT") != "1" {
		return nil
	}
	capture := &modalInputCapture{}
	interrupts := make(chan os.Signal, 8)
	signal.Notify(interrupts, os.Interrupt)
	go func() {
		for range interrupts {
			capture.interrupts.Add(1)
		}
	}()
	go func() {
		buffer := make([]byte, 1024)
		for {
			read, err := os.Stdin.Read(buffer)
			if read > 0 {
				capture.stdinBytes.Add(int32(read))
				for _, b := range buffer[:read] {
					if b == 0x03 {
						capture.stdinCtrlCBytes.Add(1)
					}
				}
			}
			if err != nil {
				return
			}
		}
	}()
	return capture
}

func (c *modalInputCapture) apply(value *capture) {
	if c == nil {
		return
	}
	value.Interrupts = int(c.interrupts.Load())
	value.StdinBytes = int(c.stdinBytes.Load())
	value.StdinCtrlCBytes = int(c.stdinCtrlCBytes.Load())
}

func main() {
	path := os.Getenv("AFTERBURNER_TEST_CAPTURE")
	inputCapture := startModalInputCapture()
	value := capture{
		Args: append([]string(nil), os.Args[1:]...),
		Env: map[string]string{
			"COPILOT_HOME":                         os.Getenv("COPILOT_HOME"),
			"AFTERBURNER_HOME":                     os.Getenv("AFTERBURNER_HOME"),
			"AFTERBURNER_BASE_PACKAGE":             os.Getenv("AFTERBURNER_BASE_PACKAGE"),
			"AFTERBURNER_BASE_APP_SHA256":          os.Getenv("AFTERBURNER_BASE_APP_SHA256"),
			"AFTERBURNER_BASE_RUNTIME_SHA256":      os.Getenv("AFTERBURNER_BASE_RUNTIME_SHA256"),
			"AFTERBURNER_MODAL_PIPE":               os.Getenv("AFTERBURNER_MODAL_PIPE"),
			"AFTERBURNER_MODAL_BOOTSTRAP":          os.Getenv("AFTERBURNER_MODAL_BOOTSTRAP"),
			"AFTERBURNER_MODAL_SECRET":             os.Getenv("AFTERBURNER_MODAL_SECRET"),
			"AFTERBURNER_MODAL_OWNER_EXTENSION_ID": os.Getenv("AFTERBURNER_MODAL_OWNER_EXTENSION_ID"),
			"AFTERBURNER_MODAL_CANVAS_ID":          os.Getenv("AFTERBURNER_MODAL_CANVAS_ID"),
			"AFTERBURNER_MODAL_SURFACE_ID":         os.Getenv("AFTERBURNER_MODAL_SURFACE_ID"),
			"AFTERBURNER_SESSION_ROUTE":            os.Getenv("AFTERBURNER_SESSION_ROUTE"),
		},
	}
	if proxyURL := os.Getenv("AFTERBURNER_TEST_PROXY_URL"); proxyURL != "" {
		response, err := http.Get(proxyURL + "/__afterburner/byomodels/health")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(41)
		}
		value.ProxyReady = response.StatusCode == http.StatusOK
		_ = response.Body.Close()
		if !value.ProxyReady {
			fmt.Fprintf(os.Stderr, "proxy health status = %d\n", response.StatusCode)
			os.Exit(42)
		}
	}
	inputCapture.apply(&value)
	writeCapture(path, value)
	if script := os.Getenv("AFTERBURNER_TEST_MODAL_JS_CLIENT"); script != "" {
		fmt.Println("copilot-before-modal")
		modal, err := exerciseModalRuntimeClient(script, os.Args[1:])
		value.Modal = modal
		inputCapture.apply(&value)
		writeCapture(path, value)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(43)
		}
		fmt.Println("copilot-after-modal")
		if os.Getenv("AFTERBURNER_TEST_MODAL_FORCE_EXIT") == "1" {
			os.Exit(0)
		}
	}
	if os.Getenv("AFTERBURNER_TEST_MODAL") == "1" {
		fmt.Println("copilot-before-modal")
		modal, err := exerciseModalPipe()
		value.Modal = modal
		inputCapture.apply(&value)
		writeCapture(path, value)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(43)
		}
		if os.Getenv("AFTERBURNER_TEST_MODAL_CRASH_OPEN") == "1" {
			os.Exit(44)
		}
		if os.Getenv("AFTERBURNER_TEST_MODAL_EXIT_OPEN") != "1" {
			fmt.Println("copilot-after-modal")
		}
		if os.Getenv("AFTERBURNER_TEST_MODAL_FORCE_EXIT") == "1" {
			os.Exit(0)
		}
	}
	if os.Getenv("AFTERBURNER_TEST_WAIT_FOR_INTERRUPT") == "1" {
		restoreInput := prepareInterruptInput()
		defer restoreInput()
		interrupts := make(chan os.Signal, 1)
		signal.Notify(interrupts, os.Interrupt)
		defer signal.Stop(interrupts)
		stdinCtrlC := make(chan struct{}, 1)
		go func() {
			buffer := make([]byte, 1024)
			for {
				read, err := os.Stdin.Read(buffer)
				for _, b := range buffer[:read] {
					if b == 0x03 {
						select {
						case stdinCtrlC <- struct{}{}:
						default:
						}
						return
					}
				}
				if err != nil {
					return
				}
			}
		}()
		fmt.Println("fake-copilot-ready")
		select {
		case <-interrupts:
		case <-stdinCtrlC:
		}
		fmt.Println("fake-copilot-interrupted")
		os.Exit(130)
	}
	if raw := os.Getenv("AFTERBURNER_TEST_EXIT_CODE"); raw != "" {
		code, _ := strconv.Atoi(raw)
		os.Exit(code)
	}
	if os.Getenv("COPILOT_RUNTIME_EXTENSION_SELF_TEST") == "1" {
		bootstrapPath := os.Getenv("AFTERBURNER_NATIVE_BOOTSTRAP")
		bootstrapData, err := os.ReadFile(bootstrapPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "verified extension bootstrap is unavailable")
			os.Exit(44)
		}
		var bootstrap struct {
			VerifiedExtensions []json.RawMessage `json:"verifiedExtensions"`
		}
		if json.Unmarshal(bootstrapData, &bootstrap) != nil || len(bootstrap.VerifiedExtensions) == 0 {
			fmt.Fprintln(os.Stderr, "verified extension bootstrap is empty")
			os.Exit(45)
		}
		fmt.Println(`{
		  "projection": {"hasReasoningColumn": true, "hasContextColumn": true},
		  "runtimeObservers": {"diagnostics": {}},
		  "modal": {
		    "fallbackAPIOK": true,
		    "brokerExpected": false,
		    "brokerTransportOK": false,
		    "updateBeforeOpenRejected": true,
		    "actionOK": true,
		    "diagnostics": {"registered": 1, "closed": 1}
		  }
		}`)
	}
}

func writeCapture(path string, value capture) {
	if path == "" {
		return
	}
	data, _ := json.Marshal(value)
	_ = os.WriteFile(path, data, 0o600)
}

func exerciseModalRuntimeClient(script string, args []string) (*modalExerciseCapture, error) {
	version := preferredRuntimeVersion(args)
	if version == "" {
		return nil, fmt.Errorf("missing --prefer-version for modal runtime client")
	}
	copilotHome := os.Getenv("COPILOT_HOME")
	if copilotHome == "" {
		return nil, fmt.Errorf("COPILOT_HOME is required for modal runtime client")
	}
	appPath := filepath.Join(copilotHome, "pkg", testRuntimePlatform(), version, "app.js")
	resultPath := filepath.Join(os.TempDir(), fmt.Sprintf("afterburner-modal-runtime-%d.json", os.Getpid()))
	_ = os.Remove(resultPath)
	cmd := exec.Command("node", script)
	cmd.Env = append(os.Environ(),
		"AFTERBURNER_TEST_MODAL_RUNTIME_APP="+appPath,
		"AFTERBURNER_TEST_MODAL_JS_RESULT="+resultPath,
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	data, readErr := os.ReadFile(resultPath)
	_ = os.Remove(resultPath)
	if readErr != nil {
		if err != nil {
			return nil, fmt.Errorf("modal runtime client failed: %w", err)
		}
		return nil, fmt.Errorf("modal runtime client did not write capture: %w", readErr)
	}
	var result modalExerciseCapture
	if decodeErr := json.Unmarshal(data, &result); decodeErr != nil {
		return nil, fmt.Errorf("decode modal runtime client capture: %w", decodeErr)
	}
	if err != nil {
		if result.InvalidRequest {
			return &result, fmt.Errorf("modal runtime client hit modal-invalid-request: %s", result.Error)
		}
		return &result, fmt.Errorf("modal runtime client failed: %w: %s", err, result.Error)
	}
	return &result, nil
}

func preferredRuntimeVersion(args []string) string {
	for index, arg := range args {
		if arg == "--prefer-version" && index+1 < len(args) {
			return args[index+1]
		}
		if value, ok := strings.CutPrefix(arg, "--prefer-version="); ok {
			return value
		}
	}
	return ""
}

func testRuntimePlatform() string {
	if runtime.GOOS == "windows" {
		return "win32-" + mapTestArch(runtime.GOARCH)
	}
	return runtime.GOOS + "-" + mapTestArch(runtime.GOARCH)
}

func mapTestArch(arch string) string {
	if arch == "amd64" {
		return "x64"
	}
	return arch
}

func exerciseModalPipe() (*modalExerciseCapture, error) {
	bootstrap, err := loadModalBootstrap()
	if err != nil {
		return nil, err
	}
	owner, canvas, surfaceID := modalTestIdentity()
	title := firstNonEmptyEnv("AFTERBURNER_TEST_MODAL_TITLE", "Afterburner Black Box Live")
	surface, err := registerModalSurface(bootstrap, owner, canvas, surfaceID)
	if err != nil {
		return nil, err
	}
	pipeName := surface.Pipe
	if surface.SessionID != "" {
		os.Setenv("AFTERBURNER_TEST_MODAL_SESSION_ID", surface.SessionID)
	} else if bootstrap.SessionID != "" {
		os.Setenv("AFTERBURNER_TEST_MODAL_SESSION_ID", bootstrap.SessionID)
	}
	const generation int64 = 1
	result := &modalExerciseCapture{}
	if response, err := sendModalRequest(pipeName, map[string]any{
		"operation":        "open",
		"id":               "forged-" + surfaceID,
		"ownerExtensionId": surface.OwnerExtensionID,
		"canvasId":         surface.CanvasID,
		"surfaceId":        "forged-" + surfaceID,
		"generation":       generation,
		"title":            "forged raw replay should not render",
		"body":             "forged modal body",
	}); err != nil {
		return result, err
	} else {
		result.StaleRejected = !response.OK && response.Error == "modal-unknown-canvas"
		if !result.StaleRejected {
			return result, fmt.Errorf("forged modal surface was accepted: %#v", response)
		}
	}
	if size := consoleSizeText(); size != "" {
		result.Sizes = append(result.Sizes, size)
	}
	actions := modalActionsForTest()
	openRequest := map[string]any{
		"operation":  "open",
		"id":         surfaceID,
		"generation": generation,
		"title":      title,
		"status":     "test modal open",
		"body":       "live metadata frame",
		"footer":     "waiting for structured close event",
		"actions":    actions,
	}
	if document := modalDocumentForTest(surfaceID, 1); document != nil {
		openRequest["document"] = document
	}
	if response, err := sendModalRequest(pipeName, openRequest); err != nil {
		return result, err
	} else if !response.OK {
		return result, fmt.Errorf("modal open failed: %s", response.Error)
	} else {
		result.Opened = true
	}
	time.Sleep(150 * time.Millisecond)
	updateRequest := map[string]any{
		"operation":  "update",
		"id":         surfaceID,
		"generation": generation,
		"status":     "test modal live update",
		"body":       "live activity update frame",
		"footer":     "waiting for structured close event",
		"actions":    actions,
	}
	if document := modalDocumentForTest(surfaceID, 2); document != nil {
		updateRequest["document"] = document
	}
	if response, err := sendModalRequest(pipeName, updateRequest); err != nil {
		return result, err
	} else if !response.OK {
		return result, fmt.Errorf("modal update failed: %s", response.Error)
	} else {
		result.Updated = true
	}
	fmt.Println("copilot-during-modal")
	time.Sleep(600 * time.Millisecond)
	if size := consoleSizeText(); size != "" {
		result.Sizes = append(result.Sizes, size)
		writeRepaintMarker(size)
	}
	if os.Getenv("AFTERBURNER_TEST_MODAL_EXIT_OPEN") == "1" || os.Getenv("AFTERBURNER_TEST_MODAL_CRASH_OPEN") == "1" {
		return result, nil
	}
	if os.Getenv("AFTERBURNER_TEST_MODAL_CONTROLS") == "1" {
		if err := exerciseModalControlRouting(pipeName, surfaceID, result, generation); err != nil {
			return result, err
		}
		return result, nil
	}
	if os.Getenv("AFTERBURNER_TEST_MODAL_ACTIONS") == "1" || os.Getenv("AFTERBURNER_TEST_MODAL_FOCUS_ACTIONS") == "1" {
		if err := exerciseModalActionRouting(pipeName, surfaceID, result, generation, actions); err != nil {
			return result, err
		}
		return result, nil
	}
	if event, err := closeAndPollModal(pipeName, surfaceID, generation); err != nil {
		return result, err
	} else {
		result.Event = event
	}
	if os.Getenv("AFTERBURNER_TEST_MODAL_REOPEN") == "1" {
		reopen, err := exerciseModalReopen(pipeName, surfaceID)
		result.Reopen = reopen
		if err != nil {
			return result, err
		}
	}
	return result, nil
}

func modalDocumentForTest(surfaceID string, revision int64) any {
	if os.Getenv("AFTERBURNER_TEST_MODAL_CONTROLS") != "1" {
		return nil
	}
	return map[string]any{
		"schemaVersion": 1,
		"protocol":      "afterburner.ui",
		"revision":      revision,
		"surfaceId":     surfaceID,
		"root": map[string]any{
			"id": "root", "kind": "dialog",
			"children": []any{
				map[string]any{"id": "name", "kind": "textInput", "props": map[string]any{"label": "Display name", "value": ""}},
				map[string]any{"id": "enabled", "kind": "checkbox", "props": map[string]any{"label": "Enabled", "checked": false}},
				map[string]any{"id": "save", "kind": "button", "props": map[string]any{"label": "Save"}, "actionBindings": map[string]any{"activate": "save"}},
			},
		},
	}
}

func exerciseModalControlRouting(pipeName, id string, result *modalExerciseCapture, generation int64) error {
	for _, expected := range []struct {
		eventType string
		targetID  string
	}{
		{"change", "name"},
		{"change", "enabled"},
		{"activate", "save"},
	} {
		event, err := pollModalEvent(pipeName, id, generation)
		if err != nil {
			return err
		}
		if event.Type != expected.eventType || event.TargetID != expected.targetID || event.DocumentRevision != 2 {
			return fmt.Errorf("modal control event = %#v, want %s/%s revision 2", event, expected.eventType, expected.targetID)
		}
		result.ActionEvents = append(result.ActionEvents, *event)
	}
	event, err := closeAndPollModal(pipeName, id, generation)
	if err != nil {
		return err
	}
	result.Event = event
	return nil
}

func modalActionsForTest() []map[string]string {
	if os.Getenv("AFTERBURNER_TEST_MODAL_ACTIONS") != "1" && os.Getenv("AFTERBURNER_TEST_MODAL_FOCUS_ACTIONS") != "1" {
		return []map[string]string{{"name": "close", "label": "Close", "key": "q"}}
	}
	return []map[string]string{
		{"name": "refresh", "label": "Refresh", "key": "r"},
		{"name": "doctor", "label": "Doctor", "key": "d"},
		{"name": "close", "label": "Close", "key": "q"},
	}
}

func exerciseModalActionRouting(pipeName, id string, result *modalExerciseCapture, generation int64, actions []map[string]string) error {
	expectedEvents := []struct {
		name string
		key  string
	}{
		{"refresh", "r"},
		{"doctor", "d"},
		{"close", "q"},
	}
	if os.Getenv("AFTERBURNER_TEST_MODAL_FOCUS_ACTIONS") == "1" {
		expectedEvents = []struct {
			name string
			key  string
		}{
			{"doctor", "enter"},
			{"refresh", "space"},
			{"close", "enter"},
		}
	}
	for _, expected := range expectedEvents {
		event, err := pollModalEvent(pipeName, id, generation)
		if err != nil {
			return err
		}
		if event.Type != "action" || event.ActionName != expected.name || event.Key != expected.key {
			return fmt.Errorf("modal action event = %#v, want %s/%s", event, expected.name, expected.key)
		}
		result.ActionEvents = append(result.ActionEvents, *event)
		switch expected.name {
		case "refresh":
			if response, err := sendModalRequest(pipeName, map[string]any{
				"operation":  "update",
				"id":         id,
				"generation": generation,
				"status":     "refresh action observed",
				"body":       "refresh action updated frame",
				"footer":     "press d for doctor",
				"actions":    actions,
			}); err != nil {
				return err
			} else if !response.OK {
				return fmt.Errorf("modal refresh update failed: %s", response.Error)
			}
		case "doctor":
			if response, err := sendModalRequest(pipeName, map[string]any{
				"operation":  "update",
				"id":         id,
				"generation": generation,
				"title":      "Afterburner Black Box Doctor",
				"status":     "doctor action observed",
				"body":       `{"healthy":true,"source":"native-modal-harness"}`,
				"footer":     "press q to close",
				"actions":    actions,
			}); err != nil {
				return err
			} else if !response.OK {
				return fmt.Errorf("modal doctor update failed: %s", response.Error)
			}
		case "close":
			closed, err := closeAndPollModal(pipeName, id, generation)
			if err != nil {
				return err
			}
			result.Event = closed
		}
	}

	const escapeGeneration int64 = 2
	if response, err := sendModalRequest(pipeName, map[string]any{
		"operation":  "open",
		"id":         id,
		"generation": escapeGeneration,
		"title":      "Afterburner Black Box Escape",
		"status":     "escape close check",
		"body":       "press Escape to close this modal",
		"footer":     "Escape closes",
		"actions":    actions,
	}); err != nil {
		return err
	} else if !response.OK {
		return fmt.Errorf("modal escape reopen failed: %s", response.Error)
	}
	event, err := pollModalEvent(pipeName, id, escapeGeneration)
	if err != nil {
		return err
	}
	if event.Type != "close" || event.Key != "escape" {
		return fmt.Errorf("modal escape event = %#v, want close/escape", event)
	}
	result.EscapeEvent = event
	return nil
}

func pollModalEvent(pipeName, id string, generation int64) (*modalEvent, error) {
	response, err := sendModalRequest(pipeName, map[string]any{
		"operation":  "poll",
		"id":         id,
		"generation": generation,
	})
	if err != nil {
		return nil, err
	}
	if !response.OK {
		return nil, fmt.Errorf("modal poll failed: %s", response.Error)
	}
	if response.Event == nil || response.Event.ID != id || response.Event.Generation != generation {
		return nil, fmt.Errorf("modal poll returned unexpected event: %#v", response.Event)
	}
	return response.Event, nil
}

func closeAndPollModal(pipeName, id string, generation int64) (*modalEvent, error) {
	if response, err := sendModalRequest(pipeName, map[string]any{
		"operation":  "close",
		"id":         id,
		"generation": generation,
	}); err != nil {
		return nil, err
	} else if !response.OK {
		return nil, fmt.Errorf("modal close failed: %s", response.Error)
	}
	response, err := sendModalRequest(pipeName, map[string]any{
		"operation":  "poll",
		"id":         id,
		"generation": generation,
	})
	if err != nil {
		return nil, err
	}
	if !response.OK {
		return nil, fmt.Errorf("modal poll failed: %s", response.Error)
	}
	if response.Event == nil || response.Event.Type != "closed" || response.Event.ID != id || response.Event.Generation != generation {
		return nil, fmt.Errorf("modal poll returned unexpected event: %#v", response.Event)
	}
	return response.Event, nil
}

func exerciseModalReopen(pipeName, id string) (*modalReopenCapture, error) {
	const staleGeneration int64 = 1
	const generation int64 = 2
	result := &modalReopenCapture{}
	if response, err := sendModalRequest(pipeName, map[string]any{
		"operation":  "open",
		"id":         id,
		"generation": generation,
		"title":      "Afterburner Black Box Reopened",
		"status":     "test modal reopened",
		"body":       "generation two frame",
		"footer":     "waiting for generation-isolated close event",
		"actions":    []map[string]string{{"name": "close", "label": "Close", "key": "q"}},
	}); err != nil {
		return result, err
	} else if !response.OK {
		return result, fmt.Errorf("modal reopen failed: %s", response.Error)
	} else {
		result.Opened = true
	}
	time.Sleep(150 * time.Millisecond)
	if response, err := sendModalRequest(pipeName, map[string]any{
		"operation":  "close",
		"id":         id,
		"generation": staleGeneration,
	}); err != nil {
		return result, err
	} else {
		result.StaleCloseRejected = !response.OK && response.Error == "modal-stale-generation"
		if !result.StaleCloseRejected {
			return result, fmt.Errorf("stale modal close was accepted: %#v", response)
		}
	}
	if response, err := sendModalRequest(pipeName, map[string]any{
		"operation":  "poll",
		"id":         id,
		"generation": staleGeneration,
	}); err != nil {
		return result, err
	} else {
		result.StalePollRejected = !response.OK && response.Error == "modal-stale-generation"
		if !result.StalePollRejected {
			return result, fmt.Errorf("stale modal poll was accepted: %#v", response)
		}
	}
	event, err := closeAndPollModal(pipeName, id, generation)
	if err != nil {
		return result, err
	}
	result.Event = event
	return result, nil
}

func sendModalRequest(pipeName string, request map[string]any) (modalResponse, error) {
	applyModalIdentity(request)
	attempts := 1
	requestType, _ := request["operation"].(string)
	if requestType == "open" || requestType == "update" {
		attempts = 3
	}
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		response, err := sendModalRequestOnce(pipeName, request)
		if err == nil {
			return response, nil
		}
		lastErr = err
		if !retryableModalPipeError(err) {
			break
		}
		time.Sleep(time.Duration(attempt+1) * 25 * time.Millisecond)
	}
	return modalResponse{}, lastErr
}

func loadModalBootstrap() (modalBootstrap, error) {
	path := os.Getenv("AFTERBURNER_MODAL_BOOTSTRAP")
	if path == "" {
		return modalBootstrap{}, fmt.Errorf("modal bootstrap environment was not provided")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return modalBootstrap{}, fmt.Errorf("read modal bootstrap: %w", err)
	}
	var bootstrap modalBootstrap
	if err := json.Unmarshal(data, &bootstrap); err != nil {
		return modalBootstrap{}, fmt.Errorf("decode modal bootstrap: %w", err)
	}
	if len(bootstrap.ModalSurfaces) == 0 {
		return modalBootstrap{}, fmt.Errorf("modal bootstrap missing preissued surface")
	}
	return bootstrap, nil
}

func registerModalSurface(bootstrap modalBootstrap, owner, canvas, surfaceID string) (modalSurface, error) {
	for _, candidate := range bootstrap.ModalSurfaces {
		if candidate.OwnerExtensionID == owner && candidate.CanvasID == canvas && candidate.SurfaceID == surfaceID && candidate.Pipe != "" {
			return candidate, nil
		}
	}
	return modalSurface{}, fmt.Errorf("modal surface for %s/%s/%s was not preissued", owner, canvas, surfaceID)
}

func applyModalIdentity(request map[string]any) {
	owner, canvas, surface := modalTestIdentity()
	if _, ok := request["ownerExtensionId"]; !ok {
		request["ownerExtensionId"] = owner
	}
	if _, ok := request["canvasId"]; !ok {
		request["canvasId"] = canvas
	}
	if _, ok := request["surfaceId"]; !ok {
		request["surfaceId"] = surface
	}
	if sessionID := os.Getenv("AFTERBURNER_TEST_MODAL_SESSION_ID"); sessionID != "" {
		if _, ok := request["sessionId"]; !ok {
			request["sessionId"] = sessionID
		}
	}
}

func modalTestIdentity() (string, string, string) {
	owner := firstNonEmptyEnv("AFTERBURNER_TEST_MODAL_OWNER", "black-box")
	canvas := firstNonEmptyEnv("AFTERBURNER_TEST_MODAL_CANVAS", owner)
	surface := firstNonEmptyEnv("AFTERBURNER_TEST_MODAL_SURFACE", canvas)
	return owner, canvas, surface
}

func firstNonEmptyEnv(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func sendModalRequestOnce(pipeName string, request map[string]any) (modalResponse, error) {
	file, err := os.OpenFile(pipeName, os.O_RDWR, 0)
	if err != nil {
		return modalResponse{}, err
	}
	defer file.Close()
	if err := json.NewEncoder(file).Encode(request); err != nil {
		return modalResponse{}, err
	}
	var response modalResponse
	if err := json.NewDecoder(bufio.NewReader(file)).Decode(&response); err != nil {
		return modalResponse{}, err
	}
	return response, nil
}

func retryableModalPipeError(err error) bool {
	message := err.Error()
	return strings.Contains(message, "No process is on the other end of the pipe") || strings.Contains(message, "The pipe is being closed")
}

func consoleSizeText() string {
	if os.Getenv("AFTERBURNER_TEST_REPORT_SIZE") != "1" || runtime.GOOS != "windows" {
		return ""
	}
	command := exec.Command("cmd.exe", "/d", "/c", "mode con")
	output, err := command.Output()
	if err != nil {
		return ""
	}
	text := string(output)
	cols := regexp.MustCompile(`(?i)Columns:\s*(\d+)`).FindStringSubmatch(text)
	rows := regexp.MustCompile(`(?i)Lines:\s*(\d+)`).FindStringSubmatch(text)
	if len(cols) < 2 || len(rows) < 2 {
		return strings.TrimSpace(text)
	}
	return cols[1] + "x" + rows[1]
}

func writeRepaintMarker(size string) {
	if os.Getenv("AFTERBURNER_TEST_MODAL_REPAINT_MARKER") != "1" {
		return
	}
	_, rowsText, ok := strings.Cut(size, "x")
	if !ok {
		return
	}
	rows, err := strconv.Atoi(rowsText)
	if err != nil || rows <= 0 {
		return
	}
	fmt.Printf("\x1b[%d;1Hrepaint-row-%s\x1b[1;1H", rows, size)
}
