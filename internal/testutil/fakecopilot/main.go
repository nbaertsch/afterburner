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
	Type       string `json:"type"`
	ID         string `json:"id"`
	Generation int64  `json:"generation,omitempty"`
	ActionName string `json:"actionName,omitempty"`
	Key        string `json:"key,omitempty"`
}

type modalSurface struct {
	OwnerExtensionID string `json:"ownerExtensionId"`
	CanvasID         string `json:"canvasId"`
	SurfaceID        string `json:"surfaceId"`
	Pipe             string `json:"pipe"`
}

type modalResponse struct {
	OK    bool        `json:"ok"`
	Error string      `json:"error,omitempty"`
	Event *modalEvent `json:"event,omitempty"`
}

type modalBootstrap struct {
	Pipe          string         `json:"pipe"`
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
	surface, err := registerModalSurface(bootstrap, "black-box", "black-box", "black-box")
	if err != nil {
		return nil, err
	}
	pipeName := surface.Pipe
	const generation int64 = 1
	result := &modalExerciseCapture{}
	if response, err := sendModalRequest(pipeName, map[string]any{
		"operation":        "open",
		"id":               "forged-black-box",
		"ownerExtensionId": surface.OwnerExtensionID,
		"canvasId":         surface.CanvasID,
		"surfaceId":        "forged-black-box",
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
	if response, err := sendModalRequest(pipeName, map[string]any{
		"operation":  "open",
		"id":         "black-box",
		"generation": generation,
		"title":      "Afterburner Black Box Live",
		"status":     "test modal open",
		"body":       "live metadata frame",
		"footer":     "waiting for structured close event",
		"actions":    []map[string]string{{"name": "close", "label": "Close", "key": "q"}},
	}); err != nil {
		return result, err
	} else if !response.OK {
		return result, fmt.Errorf("modal open failed: %s", response.Error)
	} else {
		result.Opened = true
	}
	time.Sleep(150 * time.Millisecond)
	if response, err := sendModalRequest(pipeName, map[string]any{
		"operation":  "update",
		"id":         "black-box",
		"generation": generation,
		"status":     "test modal live update",
		"body":       "live activity update frame",
		"footer":     "waiting for structured close event",
		"actions":    []map[string]string{{"name": "close", "label": "Close", "key": "q"}},
	}); err != nil {
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
	if event, err := closeAndPollModal(pipeName, "black-box", generation); err != nil {
		return result, err
	} else {
		result.Event = event
	}
	if os.Getenv("AFTERBURNER_TEST_MODAL_REOPEN") == "1" {
		reopen, err := exerciseModalReopen(pipeName)
		result.Reopen = reopen
		if err != nil {
			return result, err
		}
	}
	return result, nil
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

func exerciseModalReopen(pipeName string) (*modalReopenCapture, error) {
	const staleGeneration int64 = 1
	const generation int64 = 2
	result := &modalReopenCapture{}
	if response, err := sendModalRequest(pipeName, map[string]any{
		"operation":  "open",
		"id":         "black-box",
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
		"id":         "black-box",
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
		"id":         "black-box",
		"generation": staleGeneration,
	}); err != nil {
		return result, err
	} else {
		result.StalePollRejected = !response.OK && response.Error == "modal-stale-generation"
		if !result.StalePollRejected {
			return result, fmt.Errorf("stale modal poll was accepted: %#v", response)
		}
	}
	event, err := closeAndPollModal(pipeName, "black-box", generation)
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
	owner := "black-box"
	canvas := "black-box"
	surface := "black-box"
	if _, ok := request["ownerExtensionId"]; !ok {
		request["ownerExtensionId"] = owner
	}
	if _, ok := request["canvasId"]; !ok {
		request["canvasId"] = canvas
	}
	if _, ok := request["surfaceId"]; !ok {
		request["surfaceId"] = surface
	}
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
