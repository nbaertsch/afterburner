package byomodels

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProxyStartsBeforeProviderRegistration(t *testing.T) {
	var receivedID string
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		var payload struct {
			Input []struct {
				ID string `json:"id"`
			} `json:"input"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		receivedID = payload.Input[0].ID
		response.Header().Set("content-type", "application/json")
		_, _ = response.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	port := availablePort(t)
	path := writeConfig(t, upstream.URL, port)
	manager, err := Start(path)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()

	healthRequest, err := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d%s", port, healthPath), nil)
	if err != nil {
		t.Fatal(err)
	}
	healthRequest.Header.Set(proxyCapabilityHeader, manager.services[0].capability)
	health, err := http.DefaultClient.Do(healthRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer health.Body.Close()
	var status identity
	if err := json.NewDecoder(health.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	if status.Marker != healthMarker || status.Provider != "test-provider" {
		t.Fatalf("unexpected health identity: %#v", status)
	}

	longID := strings.Repeat("x", 100)
	request, err := http.NewRequest(
		http.MethodPost,
		fmt.Sprintf("http://127.0.0.1:%d/openai/v1/responses", port),
		strings.NewReader(`{"input":[{"id":"`+longID+`"}]}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("content-type", "application/json")
	request.Header.Set(proxyCapabilityHeader, manager.services[0].capability)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("status = %d, body = %s", response.StatusCode, body)
	}
	if receivedID == longID || !strings.HasPrefix(receivedID, "ab_") {
		t.Fatalf("input ID was not rewritten: %q", receivedID)
	}
}

func TestProxyRejectsUnauthorizedRequestsBeforeForwarding(t *testing.T) {
	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		upstreamCalls++
		response.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	port := availablePort(t)
	manager, err := Start(writeConfig(t, upstream.URL, port))
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()

	for _, token := range []string{"", "wrong"} {
		request, err := http.NewRequest(http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d/responses", port), strings.NewReader(`{"input":[{"id":"`+strings.Repeat("x", 100)+`"}]}`))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("content-type", "application/json")
		if token != "" {
			request.Header.Set(proxyCapabilityHeader, token)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", response.StatusCode)
		}
	}
	if upstreamCalls != 0 {
		t.Fatalf("unauthorized requests reached upstream %d time(s)", upstreamCalls)
	}

	health, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d%s", port, healthPath))
	if err != nil {
		t.Fatal(err)
	}
	defer health.Body.Close()
	var body map[string]any
	if err := json.NewDecoder(health.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["marker"] != healthMarker || body["provider"] != nil || body["upstream"] != nil || body["configuration"] != nil {
		t.Fatalf("unauthenticated health leaked details: %#v", body)
	}
}

func TestProxyFallsBackWhenPreferredPortHasUnrelatedListener(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("content-type", "application/json")
		_, _ = response.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	preferredPort := listener.Addr().(*net.TCPAddr).Port
	manager, err := Start(writeConfig(t, upstream.URL, preferredPort))
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()

	actualPort := managerPort(t, manager, 0)
	if actualPort == preferredPort {
		t.Fatalf("proxy reused occupied preferred port %d", preferredPort)
	}
	request, err := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/responses", actualPort), nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set(proxyCapabilityHeader, manager.services[0].capability)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
}

func TestProxyStartsIsolatedOwnersForSimultaneousSessions(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("content-type", "application/json")
		_, _ = response.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	port := availablePort(t)
	path := writeConfig(t, upstream.URL, port)
	owner, err := Start(path)
	if err != nil {
		t.Fatal(err)
	}
	standby, err := Start(path)
	if err != nil {
		owner.Close()
		t.Fatal(err)
	}
	defer owner.Close()
	defer standby.Close()
	ownerPort := managerPort(t, owner, 0)
	standbyPort := managerPort(t, standby, 0)
	if ownerPort == standbyPort {
		t.Fatalf("simultaneous sessions shared proxy port %d", ownerPort)
	}
	for index, endpointPort := range []int{ownerPort, standbyPort} {
		request, err := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/responses", endpointPort), nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set(proxyCapabilityHeader, []*Manager{owner, standby}[index].services[0].capability)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("status = %d from %d", response.StatusCode, endpointPort)
		}
	}
}

func TestProvidersSharingPreferredPortEachStartProxy(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("content-type", "application/json")
		_, _ = response.Write([]byte(fmt.Sprintf(`{"path":%q}`, request.URL.Path)))
	}))
	defer upstream.Close()
	port := availablePort(t)
	path := writeTwoProviderConfig(t, upstream.URL, port)
	manager, err := Start(path)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	if len(manager.services) != 2 {
		t.Fatalf("services = %d, want 2", len(manager.services))
	}
	firstPort := managerPort(t, manager, 0)
	secondPort := managerPort(t, manager, 1)
	if firstPort == secondPort {
		t.Fatalf("providers shared proxy port %d", firstPort)
	}
	for index, endpointPort := range []int{firstPort, secondPort} {
		request, err := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/responses", endpointPort), nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set(proxyCapabilityHeader, manager.services[index].capability)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		var body struct {
			Path string `json:"path"`
		}
		if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
			response.Body.Close()
			t.Fatal(err)
		}
		response.Body.Close()
		want := []string{"/a/responses", "/b/responses"}[index]
		if body.Path != want {
			t.Fatalf("path = %q, want %q", body.Path, want)
		}
	}
}

func TestManagerEndpointsExposeAuthoritativeCapabilityAndSkipUnsupportedAuth(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	port := availablePort(t)
	path := filepath.Join(t.TempDir(), "byomodels.json")
	value := fmt.Sprintf(`{
  "version": 1,
  "providers": [{
    "name": "native-owner",
    "baseUrl": %q,
    "requestCompatibility": { "maxInputItemIdLength": 64, "proxyPort": %d },
    "auth": { "type": "azure-cli", "resource": "https://resource.example" }
  }, {
    "name": "js-owner",
    "baseUrl": %q,
    "requestCompatibility": { "maxInputItemIdLength": 64, "proxyPort": %d },
    "auth": { "type": "api-key-env", "environmentVariable": "TOKEN" }
  }],
  "models": []
}`, upstream.URL, port, upstream.URL, port)
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
	manager, err := Start(path)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	endpoints := manager.Endpoints()
	if len(endpoints) != 1 || endpoints[0].Provider != "native-owner" || endpoints[0].BaseURL == "" || endpoints[0].Capability == "" {
		t.Fatalf("unexpected endpoints: %#v", endpoints)
	}
}

func TestProxyDecompressesUpstreamResponses(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("accept-encoding") != "gzip" {
			t.Fatalf("accept-encoding = %q", request.Header.Get("accept-encoding"))
		}
		response.Header().Set("content-encoding", "gzip")
		writer := gzip.NewWriter(response)
		_, _ = writer.Write([]byte("streamed response"))
		_ = writer.Close()
	}))
	defer upstream.Close()

	port := availablePort(t)
	manager, err := Start(writeConfig(t, upstream.URL, port))
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()

	request, err := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/responses", port), nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set(proxyCapabilityHeader, manager.services[0].capability)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "streamed response" {
		t.Fatalf("body = %q", body)
	}
	if encoding := response.Header.Get("content-encoding"); encoding != "" {
		t.Fatalf("content-encoding = %q", encoding)
	}
}

func TestProxyAdaptsNonStreamingResponsesForStreamingOnlyEndpoints(t *testing.T) {
	var receivedURL string
	var received struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		receivedURL = request.URL.String()
		if err := json.NewDecoder(request.Body).Decode(&received); err != nil {
			t.Fatal(err)
		}
		response.Header().Set("content-type", "text/event-stream")
		_, _ = response.Write([]byte(
			"event: response.created\n" +
				"data: {\"type\":\"response.created\",\"response\":{\"id\":\"response-1\",\"status\":\"in_progress\"}}\n\n" +
				"event: response.completed\n" +
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"response-1\",\"status\":\"completed\",\"model\":\"wire-model\",\"output\":[]}}\n\n" +
				"data: [DONE]\n\n",
		))
	}))
	defer upstream.Close()

	port := availablePort(t)
	path := writeStreamingConfig(
		t,
		upstream.URL+"/workspaces/default/stream/2.0/openai/v1?api-version=1",
		port,
	)
	manager, err := Start(path)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()

	request, err := http.NewRequest(
		http.MethodPost,
		fmt.Sprintf("http://127.0.0.1:%d/responses?trace=1", port),
		strings.NewReader(`{"model":"wire-model","input":"hello"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("content-type", "application/json")
	request.Header.Set(proxyCapabilityHeader, manager.services[0].capability)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.StatusCode, body)
	}
	if contentType := response.Header.Get("content-type"); contentType != "application/json" {
		t.Fatalf("content-type = %q", contentType)
	}
	if receivedURL != "/workspaces/default/stream/2.0/openai/v1/responses?api-version=1&trace=1" {
		t.Fatalf("upstream URL = %q", receivedURL)
	}
	if received.Model != "wire-model" || !received.Stream {
		t.Fatalf("upstream request = %#v", received)
	}
	var completed struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Model  string `json:"model"`
	}
	if err := json.Unmarshal(body, &completed); err != nil {
		t.Fatal(err)
	}
	if completed.ID != "response-1" || completed.Status != "completed" ||
		completed.Model != "wire-model" {
		t.Fatalf("completed response = %#v", completed)
	}
}

func TestProxyPreservesCallerRequestedStreamingResponse(t *testing.T) {
	const eventStream = "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n" +
		"data: [DONE]\n\n"
	var receivedStream bool
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		var payload struct {
			Stream bool `json:"stream"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		receivedStream = payload.Stream
		response.Header().Set("content-type", "text/event-stream")
		_, _ = response.Write([]byte(eventStream))
	}))
	defer upstream.Close()

	port := availablePort(t)
	manager, err := Start(writeStreamingConfig(t, upstream.URL, port))
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()

	request, err := http.NewRequest(
		http.MethodPost,
		fmt.Sprintf("http://127.0.0.1:%d/responses", port),
		strings.NewReader(`{"model":"wire-model","stream":true,"input":"hello"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("content-type", "application/json")
	request.Header.Set(proxyCapabilityHeader, manager.services[0].capability)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.StatusCode, body)
	}
	if contentType := response.Header.Get("content-type"); contentType != "text/event-stream" {
		t.Fatalf("content-type = %q", contentType)
	}
	if string(body) != eventStream {
		t.Fatalf("body = %q", body)
	}
	if !receivedStream {
		t.Fatal("upstream stream flag was false")
	}
}

func TestMissingConfigurationIsNoop(t *testing.T) {
	manager, err := Start(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
}

func writeStreamingConfig(t *testing.T, upstream string, port int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "byomodels.json")
	value := fmt.Sprintf(`{
  "version": 1,
  "providers": [{
    "name": "test-provider",
    "baseUrl": %q,
    "requestCompatibility": {
      "forceStreaming": true,
      "proxyPort": %d
    }
  }],
  "models": []
}`, upstream, port)
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func availablePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

func writeConfig(t *testing.T, upstream string, port int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "byomodels.json")
	value := fmt.Sprintf(`{
  "version": 1,
  "providers": [{
    "name": "test-provider",
    "baseUrl": %q,
    "requestCompatibility": {
      "maxInputItemIdLength": 64,
      "proxyPort": %d
    }
  }],
  "models": []
}`, upstream, port)
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeTwoProviderConfig(t *testing.T, upstream string, port int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "byomodels.json")
	value := fmt.Sprintf(`{
  "version": 1,
  "providers": [{
    "name": "provider-a",
    "baseUrl": %q,
    "requestCompatibility": {
      "maxInputItemIdLength": 64,
      "proxyPort": %d
    }
  }, {
    "name": "provider-b",
    "baseUrl": %q,
    "requestCompatibility": {
      "maxInputItemIdLength": 64,
      "proxyPort": %d
    }
  }],
  "models": []
}`, upstream+"/a", port, upstream+"/b", port)
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func managerPort(t *testing.T, manager *Manager, index int) int {
	t.Helper()
	if index >= len(manager.services) {
		t.Fatalf("service index %d out of range", index)
	}
	address, ok := manager.services[index].listener.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("unexpected listener address %v", manager.services[index].listener.Addr())
	}
	return address.Port
}

func TestExpirationTime(t *testing.T) {
	want := time.Unix(1234, 0)
	if got := expirationTime("1234"); !got.Equal(want) {
		t.Fatalf("expirationTime() = %s, want %s", got, want)
	}
}

func TestLiveAzureProxy(t *testing.T) {
	configPath := os.Getenv("AFTERBURNER_LIVE_BYOMODELS_CONFIG")
	if configPath == "" {
		t.Skip("set AFTERBURNER_LIVE_BYOMODELS_CONFIG to run the live Azure proxy test")
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var value config
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	if len(value.Providers) == 0 {
		t.Fatal("live configuration has no providers")
	}
	port := availablePort(t)
	value.Providers = value.Providers[:1]
	value.Providers[0].RequestCompatibility.ProxyPort = port
	liveConfig, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "byomodels.json")
	if err := os.WriteFile(path, liveConfig, 0o600); err != nil {
		t.Fatal(err)
	}

	manager, err := Start(path)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	body := bytes.NewBufferString(
		`{"model":"gpt-5.6-sol","input":"Reply with exactly NATIVE_PROXY_OK","max_output_tokens":32}`,
	)
	request, err := http.NewRequest(http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d/openai/v1/responses", port), body)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("content-type", "application/json")
	request.Header.Set(proxyCapabilityHeader, manager.services[0].capability)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.StatusCode, responseBody)
	}
	if !bytes.Contains(responseBody, []byte("NATIVE_PROXY_OK")) {
		t.Fatalf("response did not contain expected output: %s", responseBody)
	}
}
