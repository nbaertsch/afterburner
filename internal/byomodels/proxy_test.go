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

	health, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d%s", port, healthPath))
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
	response, err := http.Post(
		fmt.Sprintf("http://127.0.0.1:%d/openai/v1/responses", port),
		"application/json",
		strings.NewReader(`{"input":[{"id":"`+longID+`"}]}`),
	)
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

func TestProxyRejectsUnrelatedListener(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	path := writeConfig(t, "https://example.invalid", port)
	if _, err := Start(path); err == nil || !strings.Contains(err.Error(), "already in use") {
		t.Fatalf("Start() error = %v", err)
	}
}

func TestProxySurvivesOwnerHandoff(t *testing.T) {
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
	defer standby.Close()
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}

	endpoint := fmt.Sprintf("http://127.0.0.1:%d/responses", port)
	var response *http.Response
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
		response, err = http.Get(endpoint)
		if err == nil {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("standby did not take ownership: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
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

	response, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/responses", port))
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

func TestMissingConfigurationIsNoop(t *testing.T) {
	manager, err := Start(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
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
	response, err := http.Post(
		fmt.Sprintf("http://127.0.0.1:%d/openai/v1/responses", port),
		"application/json",
		body,
	)
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
