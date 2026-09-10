//go:build windows

package terminal

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestModalPipeListenerServesAuthenticatedRequest(t *testing.T) {
	renderer := &testModalRenderer{}
	server, err := NewModalServer(nil, renderer)
	if err != nil {
		t.Fatal(err)
	}
	registerLegacyModalForTest(t, server)
	listener, err := NewModalPipeListener(server)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	server.AuthorizeClientProcess(uint32(os.Getpid()))
	go listener.Serve()

	response := sendPipeRequest(t, listener.PipeName(), map[string]any{
		"operation": "open",
		"id":        "black-box",
		"title":     "Black Box",
		"body":      "live",
	})
	if !response.OK || response.Error != "" {
		t.Fatalf("response = %#v", response)
	}
	if server.ActiveCount() != 1 || len(renderer.shown) != 1 {
		t.Fatalf("active=%d shown=%d", server.ActiveCount(), len(renderer.shown))
	}
}

func TestModalPipeRejectsStaleSessionIdentity(t *testing.T) {
	server, err := NewModalServer(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	capability, err := server.RegisterModalCanvas(ModalRegistration{OwnerExtensionID: "owner.alpha", CanvasID: "shared", SurfaceID: "alpha-modal"})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := NewModalPipeListenerForCapability(server, capability)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	server.AuthorizeClientProcess(uint32(os.Getpid()))
	go listener.Serve()

	missing := modalRequestMap(capability, "open", 1)
	delete(missing, "sessionId")
	response := sendPipeRequest(t, listener.PipeName(), missing)
	if response.OK || response.Error != "modal-unauthorized-session" {
		t.Fatalf("missing session response = %#v", response)
	}
	request := modalRequestMap(capability, "open", 1)
	request["sessionId"] = "stale-session-stale-session-stale-session"
	response = sendPipeRequest(t, listener.PipeName(), request)
	if response.OK || response.Error != "modal-unauthorized-session" {
		t.Fatalf("stale session response = %#v", response)
	}
	response = sendPipeRequest(t, listener.PipeName(), modalRequestMap(capability, "open", 1))
	if !response.OK || server.ActiveCount() != 1 {
		t.Fatalf("fresh session response = %#v active=%d", response, server.ActiveCount())
	}
}

func TestModalPipeRejectsDirectOwnerImpersonation(t *testing.T) {
	server, err := NewModalServer(nil, nil)
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
	listener, err := NewModalPipeListenerForCapability(server, ownerACap)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go listener.Serve()

	unauthorized := sendPipeRequest(t, listener.PipeName(), modalRequestMap(ownerACap, "open", int64(1)))
	if unauthorized.OK || unauthorized.Error != "modal-unauthorized" {
		t.Fatalf("unauthenticated pipe request response = %#v", unauthorized)
	}
	server.AuthorizeClientProcess(uint32(os.Getpid()))

	cases := []struct {
		name    string
		request map[string]any
		want    string
	}{
		{"unknown-canvas", map[string]any{
			"type":             "open",
			"id":               "missing-modal",
			"ownerExtensionId": "owner.alpha",
			"canvasId":         "missing",
			"surfaceId":        "missing-modal",
			"generation":       int64(1),
		}, "modal-unknown-canvas"},
		{"canvas-collision", map[string]any{
			"type":             "open",
			"id":               ownerBCap.SurfaceID,
			"ownerExtensionId": ownerBCap.OwnerExtensionID,
			"canvasId":         ownerBCap.CanvasID,
			"surfaceId":        ownerBCap.SurfaceID,
			"sessionId":        ownerBCap.SessionID,
			"generation":       int64(1),
		}, "modal-unauthorized-surface"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			response := sendPipeRequest(t, listener.PipeName(), tt.request)
			if response.OK || response.Error != tt.want {
				t.Fatalf("response = %#v, want %s", response, tt.want)
			}
		})
	}

	response := sendPipeRequest(t, listener.PipeName(), modalRequestMap(ownerACap, "open", int64(1)))
	if !response.OK || server.ActiveCount() != 1 {
		t.Fatalf("scoped open response = %#v active=%d", response, server.ActiveCount())
	}
	response = sendPipeRequest(t, listener.PipeName(), modalRequestMap(ownerACap, "close", int64(1)))
	if !response.OK {
		t.Fatalf("scoped close response = %#v", response)
	}
	response = sendPipeRequest(t, listener.PipeName(), modalRequestMap(ownerACap, "open", int64(1)))
	if response.OK || response.Error != "modal-stale-generation" {
		t.Fatalf("stale generation response = %#v", response)
	}
}

func TestModalPipeRejectsAuthorizedProcessDescendant(t *testing.T) {
	server, err := NewModalServer(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	capability, err := server.RegisterModalCanvas(ModalRegistration{OwnerExtensionID: "black-box", CanvasID: "black-box", SurfaceID: "black-box"})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := NewModalPipeListenerForCapability(server, capability)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	server.AuthorizeClientProcess(uint32(os.Getpid()))
	go listener.Serve()

	command := exec.Command(os.Args[0], "-test.run=TestModalPipeDescendantHelper", "--", listener.PipeName())
	command.Env = append(os.Environ(), "GO_WANT_MODAL_DESCENDANT_HELPER=1")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("descendant helper failed: %v\n%s", err, output)
	}
}

func TestModalPipeDescendantHelper(t *testing.T) {
	if os.Getenv("GO_WANT_MODAL_DESCENDANT_HELPER") != "1" {
		return
	}
	pipeName := os.Args[len(os.Args)-1]
	response, err := sendPipeRequestResult(pipeName, map[string]any{"operation": "open", "id": "black-box", "generation": int64(1)}, 2*time.Second)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if response.OK || response.Error != "modal-unauthorized" {
		fmt.Fprintf(os.Stderr, "response = %#v, want modal-unauthorized\n", response)
		os.Exit(3)
	}
	os.Exit(0)
}

func TestModalPipeRejectsMalformedOversizedUnknownAndUnauthorized(t *testing.T) {
	server, err := NewModalServer(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	registerLegacyModalForTest(t, server)
	listener, err := NewModalPipeListener(server)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go listener.Serve()

	unauthorized := sendPipeRaw(t, listener.PipeName(), `{"operation":"open","id":"black-box","ownerExtensionId":"black-box","canvasId":"black-box","surfaceId":"black-box","generation":1}`+"\n")
	if unauthorized.OK || unauthorized.Error != "modal-unauthorized" {
		t.Fatalf("unauthenticated response = %#v", unauthorized)
	}
	server.AuthorizeClientProcess(uint32(os.Getpid()))

	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"malformed", "not-json\n", "modal-invalid-request"},
		{"unknown", `{"operation":"bogus","id":"black-box","ownerExtensionId":"black-box","canvasId":"black-box","surfaceId":"black-box","generation":1}` + "\n", "modal-unknown-type"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			response := sendPipeRaw(t, listener.PipeName(), tt.raw)
			if response.OK || response.Error != tt.want {
				t.Fatalf("response = %#v, want %s", response, tt.want)
			}
		})
	}
}

func TestModalPipeOversizedRequestDoesNotExhaustListener(t *testing.T) {
	oldTimeout := modalPipeRequestReadTimeout
	modalPipeRequestReadTimeout = 100 * time.Millisecond
	t.Cleanup(func() { modalPipeRequestReadTimeout = oldTimeout })

	server, err := NewModalServer(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	registerLegacyModalForTest(t, server)
	listener, err := NewModalPipeListener(server)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	server.AuthorizeClientProcess(uint32(os.Getpid()))
	go listener.Serve()

	response := sendPipeRawAllowClosed(t, listener.PipeName(), strings.Repeat("x", modalMaxMessageBytes+1)+"\n")
	if response.Error != "modal-message-too-large" && response.Error != "modal-pipe-closed" && response.Error != "modal-read-failed" {
		t.Fatalf("oversized response = %#v", response)
	}
	response = sendPipeRequest(t, listener.PipeName(), map[string]any{"operation": "open", "id": "black-box"})
	if !response.OK {
		t.Fatalf("valid request after oversized response = %#v", response)
	}
}

func TestModalPipeSlowClientsTimeoutAndFreeInstances(t *testing.T) {
	oldTimeout := modalPipeRequestReadTimeout
	modalPipeRequestReadTimeout = 100 * time.Millisecond
	t.Cleanup(func() { modalPipeRequestReadTimeout = oldTimeout })

	server, err := NewModalServer(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	registerLegacyModalForTest(t, server)
	listener, err := NewModalPipeListener(server)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	server.AuthorizeClientProcess(uint32(os.Getpid()))
	go listener.Serve()

	clients := make([]*os.File, 0, modalPipeMaxInstance)
	for i := 0; i < modalPipeMaxInstance; i++ {
		conn := openPipeWithRetry(t, listener.PipeName(), time.Second)
		clients = append(clients, conn)
	}
	defer func() {
		for _, conn := range clients {
			_ = conn.Close()
		}
	}()

	deadline := time.Now().Add(3 * time.Second)
	var last modalResponse
	for time.Now().Before(deadline) {
		conn := openPipeWithRetry(t, listener.PipeName(), time.Second)
		request := map[string]any{"operation": "open", "id": "black-box", "generation": int64(1)}
		addLegacyModalIdentity(request)
		if err := json.NewEncoder(conn).Encode(request); err != nil {
			_ = conn.Close()
			last = modalResponse{Error: err.Error()}
			time.Sleep(25 * time.Millisecond)
			continue
		}
		last = readPipeResponse(t, conn)
		_ = conn.Close()
		if last.OK {
			return
		}
		if last.Error != "modal-read-failed" {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("valid request after slow clients response = %#v", last)
}

func TestModalPipeConcurrentPollDoesNotBlockOpen(t *testing.T) {
	server, err := NewModalServer(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	registerLegacyModalForTest(t, server)
	server.pollTimeout = 30 * time.Second
	listener, err := NewModalPipeListener(server)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	server.AuthorizeClientProcess(uint32(os.Getpid()))
	go listener.Serve()

	key := modalEventKey{identity: modalIdentity{ownerExtensionID: ModalLegacyOwnerExtensionID, canvasID: ModalLegacyCanvasID, surfaceID: ModalLegacySurfaceID}, generation: 1}
	pollDone := make(chan pipeRequestResult, 1)
	go func() {
		response, err := sendPipeRequestResult(listener.PipeName(), map[string]any{"operation": "poll", "id": "black-box"}, time.Second)
		pollDone <- pipeRequestResult{response: response, err: err}
	}()
	waitForModalWaiter(t, server, key, pollDone, 5*time.Second)

	openDone := make(chan pipeRequestResult, 1)
	go func() {
		response, err := sendPipeRequestResult(listener.PipeName(), map[string]any{"operation": "open", "id": "black-box", "title": "Black Box"}, 5*time.Second)
		openDone <- pipeRequestResult{response: response, err: err}
	}()
	select {
	case result := <-openDone:
		if result.err != nil {
			t.Fatalf("open request failed while poll was pending: %v", result.err)
		}
		if !result.response.OK {
			t.Fatalf("open response = %#v", result.response)
		}
	case result := <-pollDone:
		t.Fatalf("poll completed before open request: response=%#v err=%v", result.response, result.err)
	case <-time.After(10 * time.Second):
		t.Fatal("open request blocked behind long poll")
	}

	closeResponse := sendPipeRequest(t, listener.PipeName(), map[string]any{"operation": "close", "id": "black-box"})
	if !closeResponse.OK {
		t.Fatalf("close response = %#v", closeResponse)
	}
	select {
	case result := <-pollDone:
		if result.err != nil {
			t.Fatalf("poll request failed after close: %v", result.err)
		}
		if result.response.Event == nil || result.response.Event.Type != "closed" {
			t.Fatalf("poll response after close = %#v", result.response)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("poll did not complete after close event")
	}
}

type pipeRequestResult struct {
	response modalResponse
	err      error
}

func waitForModalWaiter(t *testing.T, server *ModalServer, key modalEventKey, done <-chan pipeRequestResult, timeout time.Duration) {
	t.Helper()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		server.mu.Lock()
		waiterCount := len(server.waiters[key])
		server.mu.Unlock()
		if waiterCount > 0 {
			return
		}
		select {
		case result := <-done:
			t.Fatalf("poll completed before registering waiter: response=%#v err=%v", result.response, result.err)
		case <-deadline.C:
			t.Fatal("poll request did not register waiter")
		case <-ticker.C:
		}
	}
}

func sendPipeRequest(t *testing.T, pipeName string, request map[string]any) modalResponse {
	t.Helper()
	response, err := sendPipeRequestResult(pipeName, request, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func sendPipeRequestResult(pipeName string, request map[string]any, timeout time.Duration) (modalResponse, error) {
	addLegacyModalIdentity(request)
	if _, ok := request["generation"]; !ok {
		request["generation"] = int64(1)
	}
	conn, err := openPipeWithRetryResult(pipeName, timeout)
	if err != nil {
		return modalResponse{}, err
	}
	defer conn.Close()
	if err := json.NewEncoder(conn).Encode(request); err != nil {
		return modalResponse{}, err
	}
	return readPipeResponseResult(conn)
}

func sendPipeRaw(t *testing.T, pipeName, raw string) modalResponse {
	t.Helper()
	conn := openPipeWithRetry(t, pipeName, time.Second)
	defer conn.Close()
	if len(raw) <= modalPipeBufferSize {
		if written, err := conn.WriteString(raw); err != nil && written == 0 {
			t.Fatal(err)
		}
		return readPipeResponse(t, conn)
	}
	writeDone := make(chan error, 1)
	go func() {
		written, err := conn.WriteString(raw)
		if err != nil && written == 0 {
			writeDone <- err
			return
		}
		writeDone <- nil
	}()
	response := readPipeResponse(t, conn)
	select {
	case err := <-writeDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(100 * time.Millisecond):
	}
	return response
}

func sendPipeRawAllowClosed(t *testing.T, pipeName, raw string) modalResponse {
	t.Helper()
	conn := openPipeWithRetry(t, pipeName, time.Second)
	defer conn.Close()
	writeDone := make(chan error, 1)
	go func() {
		written, err := conn.WriteString(raw)
		if err != nil && written == 0 {
			writeDone <- err
			return
		}
		writeDone <- nil
	}()
	var response modalResponse
	if err := json.NewDecoder(bufio.NewReader(conn)).Decode(&response); err != nil {
		response = modalResponse{Error: "modal-pipe-closed"}
	}
	select {
	case <-writeDone:
	case <-time.After(100 * time.Millisecond):
	}
	return response
}

func openPipeWithRetry(t *testing.T, pipeName string, timeout time.Duration) *os.File {
	t.Helper()
	conn, err := openPipeWithRetryResult(pipeName, timeout)
	if err != nil {
		t.Fatal(err)
	}
	return conn
}

func openPipeWithRetryResult(pipeName string, timeout time.Duration) (*os.File, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		conn, err := os.OpenFile(pipeName, os.O_RDWR, 0)
		if err == nil {
			return conn, nil
		}
		lastErr = err
		time.Sleep(10 * time.Millisecond)
	}
	return nil, fmt.Errorf("open pipe %s: %w", pipeName, lastErr)
}

func readPipeResponse(t *testing.T, conn *os.File) modalResponse {
	t.Helper()
	response, err := readPipeResponseResult(conn)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func readPipeResponseResult(conn *os.File) (modalResponse, error) {
	var response modalResponse
	if err := json.NewDecoder(bufio.NewReader(conn)).Decode(&response); err != nil {
		return modalResponse{}, err
	}
	return response, nil
}
