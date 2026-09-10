package byomodels

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	healthPath            = "/__afterburner/byomodels/health"
	healthMarker          = "afterburner-byomodels-proxy-v1"
	proxyCapabilityHeader = "x-afterburner-proxy-capability"
)

type config struct {
	Version   int        `json:"version"`
	Providers []provider `json:"providers"`
}

type provider struct {
	Name                 string               `json:"name"`
	BaseURL              string               `json:"baseUrl"`
	RequestCompatibility requestCompatibility `json:"requestCompatibility"`
	Auth                 auth                 `json:"auth"`
}

type requestCompatibility struct {
	MaxInputItemIDLength int  `json:"maxInputItemIdLength"`
	ProxyPort            int  `json:"proxyPort"`
	ForceStreaming       bool `json:"forceStreaming"`
}

type auth struct {
	Type     string `json:"type"`
	Resource string `json:"resource"`
}

type identity struct {
	Marker        string `json:"marker"`
	Provider      string `json:"provider"`
	Upstream      string `json:"upstream"`
	Configuration string `json:"configuration"`
}

type ProxyEndpoint struct {
	Provider      string `json:"provider"`
	BaseURL       string `json:"baseUrl"`
	Capability    string `json:"capability"`
	Configuration string `json:"configuration"`
}

type tokenValue struct {
	Token        string
	ExpiresAt    time.Time
	RefreshAfter time.Time
}

type tokenCache struct {
	mu     sync.Mutex
	values map[string]tokenValue
}

type service struct {
	mu         sync.Mutex
	provider   provider
	upstream   *url.URL
	identity   identity
	tokens     *tokenCache
	client     *http.Client
	capability string
	server     *http.Server
	listener   net.Listener
	done       chan struct{}
	closed     bool
}

type Manager struct {
	services []*service
}

func Start(configPath string) (*Manager, error) {
	data, err := os.ReadFile(configPath)
	if os.IsNotExist(err) {
		return &Manager{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read BYOModels proxy configuration: %w", err)
	}
	var value config
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, fmt.Errorf("parse BYOModels proxy configuration: %w", err)
	}
	if value.Version != 1 {
		return nil, fmt.Errorf("unsupported BYOModels configuration version: %d", value.Version)
	}

	manager := &Manager{}
	tokens := &tokenCache{values: map[string]tokenValue{}}
	for _, configured := range value.Providers {
		compatibility := configured.RequestCompatibility
		if (compatibility.MaxInputItemIDLength < 16 && !compatibility.ForceStreaming) ||
			compatibility.ProxyPort == 0 || !canNativeOwn(configured) {
			continue
		}
		if compatibility.ProxyPort < 1024 || compatibility.ProxyPort > 65535 {
			manager.Close()
			return nil, fmt.Errorf(
				"provider %q must define requestCompatibility.proxyPort between 1024 and 65535",
				configured.Name,
			)
		}
		upstream, err := url.Parse(configured.BaseURL)
		if err != nil || upstream.Scheme == "" || upstream.Host == "" {
			manager.Close()
			return nil, fmt.Errorf("provider %q has an invalid baseUrl", configured.Name)
		}
		if upstream.Path == "" {
			upstream.Path = "/"
		}
		capability, err := newCapability()
		if err != nil {
			manager.Close()
			return nil, fmt.Errorf("create BYOModels proxy capability for provider %q: %w", configured.Name, err)
		}
		current := &service{
			provider: configured,
			upstream: upstream,
			identity: identity{
				Marker:        healthMarker,
				Provider:      configured.Name,
				Upstream:      upstream.String(),
				Configuration: proxyConfiguration(configured),
			},
			tokens:     tokens,
			client:     &http.Client{},
			capability: capability,
			done:       make(chan struct{}),
		}
		if err := current.start(); err != nil {
			manager.Close()
			return nil, err
		}
		manager.services = append(manager.services, current)
	}
	return manager, nil
}

func canNativeOwn(value provider) bool {
	return value.Auth.Type == "" || value.Auth.Type == "azure-cli"
}

func newCapability() (string, error) {
	var data [32]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data[:]), nil
}

func proxyConfiguration(value provider) string {
	configuration := strings.Join([]string{
		strconv.Itoa(value.RequestCompatibility.MaxInputItemIDLength),
		strconv.FormatBool(value.RequestCompatibility.ForceStreaming),
		value.Auth.Type,
		value.Auth.Resource,
	}, "\x00")
	hash := sha256.Sum256([]byte(configuration))
	return base64.RawURLEncoding.EncodeToString(hash[:])
}

func (manager *Manager) Close() error {
	var result error
	for _, current := range manager.services {
		if err := current.close(); err != nil {
			result = errors.Join(result, err)
		}
	}
	manager.services = nil
	return result
}

func (manager *Manager) Endpoints() []ProxyEndpoint {
	endpoints := make([]ProxyEndpoint, 0, len(manager.services))
	for _, current := range manager.services {
		address, ok := current.listener.Addr().(*net.TCPAddr)
		if !ok {
			continue
		}
		endpoints = append(endpoints, ProxyEndpoint{
			Provider:      current.provider.Name,
			BaseURL:       "http://127.0.0.1:" + strconv.Itoa(address.Port),
			Capability:    current.capability,
			Configuration: current.identity.Configuration,
		})
	}
	return endpoints
}

func (current *service) start() error {
	preferred := net.JoinHostPort("127.0.0.1", strconv.Itoa(current.provider.RequestCompatibility.ProxyPort))
	listener, err := net.Listen("tcp", preferred)
	if err == nil {
		current.activate(listener)
		return nil
	}
	if !errors.Is(err, os.ErrExist) && !isAddressInUse(err) {
		return fmt.Errorf("bind BYOModels proxy for provider %q: %w", current.provider.Name, err)
	}
	fallback, fallbackErr := net.Listen("tcp", "127.0.0.1:0")
	if fallbackErr != nil {
		return fmt.Errorf(
			"bind BYOModels fallback proxy for provider %q after preferred port %d was unavailable: %w",
			current.provider.Name,
			current.provider.RequestCompatibility.ProxyPort,
			fallbackErr,
		)
	}
	current.activate(fallback)
	return nil
}

func (current *service) serve(listener net.Listener) {
	server := &http.Server{
		Handler:           http.HandlerFunc(current.handle),
		ReadHeaderTimeout: 10 * time.Second,
	}
	current.listener = listener
	current.server = server
	go func() {
		_ = server.Serve(listener)
		close(current.done)
	}()
}

func (current *service) activate(listener net.Listener) bool {
	current.mu.Lock()
	defer current.mu.Unlock()
	if current.closed {
		_ = listener.Close()
		close(current.done)
		return false
	}
	current.serve(listener)
	return true
}

func (current *service) close() error {
	current.mu.Lock()
	if current.closed {
		done := current.done
		current.mu.Unlock()
		<-done
		return nil
	}
	current.closed = true
	server := current.server
	done := current.done
	current.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := server.Shutdown(ctx)
	<-done
	if err != nil {
		return fmt.Errorf("stop BYOModels proxy for provider %q: %w", current.provider.Name, err)
	}
	return nil
}

func (current *service) handle(response http.ResponseWriter, request *http.Request) {
	authorized := current.authorized(request)
	if request.Method == http.MethodGet && request.URL.Path == healthPath {
		response.Header().Set("content-type", "application/json")
		if authorized {
			_ = json.NewEncoder(response).Encode(current.identity)
		} else {
			_ = json.NewEncoder(response).Encode(map[string]any{"marker": healthMarker, "ready": true})
		}
		return
	}
	if !authorized {
		writeUnauthorized(response)
		return
	}

	body, err := io.ReadAll(request.Body)
	if err != nil {
		writeProxyError(response, err)
		return
	}
	if len(body) > 0 && strings.Contains(request.Header.Get("content-type"), "application/json") {
		body, err = rewriteInputItemIDs(body, current.provider.RequestCompatibility.MaxInputItemIDLength)
		if err != nil {
			writeProxyError(response, err)
			return
		}
	}
	translatedStreamingResponse := false
	if current.provider.RequestCompatibility.ForceStreaming &&
		request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/responses") {
		body, translatedStreamingResponse, err = forceStreaming(body)
		if err != nil {
			writeProxyError(response, err)
			return
		}
	}
	target := upstreamTarget(current.upstream, request.URL)
	upstreamRequest, err := http.NewRequestWithContext(request.Context(), request.Method, target.String(), bytes.NewReader(body))
	if err != nil {
		writeProxyError(response, err)
		return
	}
	copyHeaders(upstreamRequest.Header, request.Header)
	removeHopByHopHeaders(upstreamRequest.Header)
	upstreamRequest.Header.Del("host")
	upstreamRequest.Header.Del("content-length")
	upstreamRequest.Header.Del("accept-encoding")
	upstreamRequest.Header.Del(proxyCapabilityHeader)
	if current.authorizationBearer(request) == current.capability {
		upstreamRequest.Header.Del("authorization")
	}
	if current.provider.Auth.Type == "azure-cli" {
		token, tokenErr := current.tokens.get(request.Context(), current.provider.Auth.Resource)
		if tokenErr != nil {
			writeProxyError(response, tokenErr)
			return
		}
		upstreamRequest.Header.Set("authorization", "Bearer "+token)
	}

	upstreamResponse, err := current.client.Do(upstreamRequest)
	if err != nil {
		writeProxyError(response, err)
		return
	}
	defer upstreamResponse.Body.Close()
	copyHeaders(response.Header(), upstreamResponse.Header)
	removeHopByHopHeaders(response.Header())
	response.Header().Del("content-length")
	response.Header().Del("content-encoding")
	if translatedStreamingResponse && upstreamResponse.StatusCode >= 200 &&
		upstreamResponse.StatusCode < 300 &&
		strings.Contains(upstreamResponse.Header.Get("content-type"), "text/event-stream") {
		completed, streamErr := completedResponseFromEventStream(upstreamResponse.Body)
		if streamErr != nil {
			writeProxyError(response, streamErr)
			return
		}
		response.Header().Set("content-type", "application/json")
		response.WriteHeader(upstreamResponse.StatusCode)
		_, _ = response.Write(completed)
		return
	}
	response.WriteHeader(upstreamResponse.StatusCode)
	_, _ = io.Copy(response, upstreamResponse.Body)
}

func upstreamTarget(upstream, requestURL *url.URL) *url.URL {
	target := *upstream
	target.Path = strings.TrimSuffix(upstream.Path, "/") + "/" +
		strings.TrimPrefix(requestURL.Path, "/")
	switch {
	case upstream.RawQuery == "":
		target.RawQuery = requestURL.RawQuery
	case requestURL.RawQuery == "":
		target.RawQuery = upstream.RawQuery
	default:
		target.RawQuery = upstream.RawQuery + "&" + requestURL.RawQuery
	}
	target.Fragment = ""
	return &target
}

func forceStreaming(body []byte) ([]byte, bool, error) {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, false, err
	}
	if stream, ok := payload["stream"].(bool); ok && stream {
		return body, false, nil
	}
	payload["stream"] = true
	rewritten, err := json.Marshal(payload)
	if err != nil {
		return nil, false, err
	}
	return rewritten, true, nil
}

func completedResponseFromEventStream(body io.Reader) ([]byte, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), 64*1024*1024)
	var data []string
	var completed json.RawMessage
	flush := func() error {
		if len(data) == 0 {
			return nil
		}
		raw := strings.Join(data, "\n")
		data = data[:0]
		if raw == "[DONE]" {
			return nil
		}
		var event struct {
			Type     string          `json:"type"`
			Response json.RawMessage `json:"response"`
			Error    struct {
				Message string `json:"message"`
			} `json:"error"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal([]byte(raw), &event); err != nil {
			return fmt.Errorf("parse upstream streaming response event: %w", err)
		}
		switch event.Type {
		case "response.completed", "response.failed":
			if len(event.Response) > 0 && string(event.Response) != "null" {
				completed = append(completed[:0], event.Response...)
			}
		case "error":
			message := event.Error.Message
			if message == "" {
				message = event.Message
			}
			if message == "" {
				message = "upstream streaming response failed"
			}
			return errors.New(message)
		}
		return nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := flush(); err != nil {
				return nil, err
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimLeft(line[len("data:"):], " \t"))
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read upstream streaming response: %w", err)
	}
	if err := flush(); err != nil {
		return nil, err
	}
	if len(completed) == 0 {
		return nil, errors.New("upstream streaming response did not include a terminal response event")
	}
	return completed, nil
}

func rewriteInputItemIDs(body []byte, maximumLength int) ([]byte, error) {
	if maximumLength < 16 {
		return body, nil
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	input, ok := payload["input"].([]any)
	if !ok {
		return body, nil
	}
	changed := false
	for _, raw := range input {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		id, ok := item["id"].(string)
		if !ok || len(id) <= maximumLength {
			continue
		}
		hash := sha256.Sum256([]byte(id))
		item["id"] = "ab_" + base64.RawURLEncoding.EncodeToString(hash[:])
		changed = true
	}
	if !changed {
		return body, nil
	}
	return json.Marshal(payload)
}

func (cache *tokenCache) get(ctx context.Context, resource string) (string, error) {
	if resource == "" {
		return "", errors.New("Azure CLI authentication requires auth.resource")
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	now := time.Now()
	if value, ok := cache.values[resource]; ok && now.Before(value.RefreshAfter) {
		return value.Token, nil
	}
	value, err := acquireAzureCLIToken(ctx, resource)
	if err != nil {
		return "", err
	}
	cache.values[resource] = value
	return value.Token, nil
}

func acquireAzureCLIToken(ctx context.Context, resource string) (tokenValue, error) {
	executable := "az"
	args := []string{
		"account", "get-access-token", "--resource", resource, "--output", "json",
	}
	if runtime.GOOS == "windows" {
		executable = "az.cmd"
		commandInterpreter := os.Getenv("COMSPEC")
		if commandInterpreter == "" {
			commandInterpreter = "cmd.exe"
		}
		args = append([]string{"/d", "/s", "/c", executable}, args...)
		executable = commandInterpreter
	}
	command := exec.CommandContext(ctx, executable, args...)
	output, err := command.Output()
	if err != nil {
		return tokenValue{}, fmt.Errorf("acquire Azure CLI token: %w", err)
	}
	var result struct {
		AccessToken        string      `json:"accessToken"`
		ExpiresOn          interface{} `json:"expires_on"`
		ExpiresOnTimestamp interface{} `json:"expiresOnTimestamp"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		return tokenValue{}, fmt.Errorf("parse Azure CLI token: %w", err)
	}
	token := strings.TrimSpace(result.AccessToken)
	if token == "" {
		return tokenValue{}, errors.New("Azure CLI returned an empty Foundry access token")
	}
	expiresAt := expirationTime(result.ExpiresOn, result.ExpiresOnTimestamp)
	if expiresAt.IsZero() {
		expiresAt = jwtExpiration(token)
	}
	now := time.Now()
	if expiresAt.Before(now.Add(time.Minute)) {
		return tokenValue{}, errors.New("Azure CLI returned a Foundry access token that expires in less than one minute")
	}
	return tokenValue{
		Token:        token,
		ExpiresAt:    expiresAt,
		RefreshAfter: maxTime(now, expiresAt.Add(-5*time.Minute)),
	}, nil
}

func expirationTime(values ...interface{}) time.Time {
	for _, raw := range values {
		var seconds int64
		switch value := raw.(type) {
		case float64:
			seconds = int64(value)
		case string:
			seconds, _ = strconv.ParseInt(value, 10, 64)
		}
		if seconds > 0 {
			return time.Unix(seconds, 0)
		}
	}
	return time.Time{}
}

func jwtExpiration(token string) time.Time {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return time.Time{}
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}
	}
	var claims struct {
		Expires float64 `json:"exp"`
	}
	if json.Unmarshal(data, &claims) != nil || claims.Expires <= 0 {
		return time.Time{}
	}
	return time.Unix(int64(claims.Expires), 0)
}

func maxTime(left, right time.Time) time.Time {
	if left.After(right) {
		return left
	}
	return right
}

func copyHeaders(destination, source http.Header) {
	for name, values := range source {
		for _, value := range values {
			destination.Add(name, value)
		}
	}
}

func removeHopByHopHeaders(headers http.Header) {
	for _, name := range []string{
		"connection", "keep-alive", "proxy-authenticate", "proxy-authorization",
		"te", "trailer", "transfer-encoding", "upgrade",
	} {
		headers.Del(name)
	}
}

func (current *service) authorized(request *http.Request) bool {
	return secureEqual(request.Header.Get(proxyCapabilityHeader), current.capability) ||
		secureEqual(current.authorizationBearer(request), current.capability)
}

func (current *service) authorizationBearer(request *http.Request) string {
	fields := strings.Fields(request.Header.Get("authorization"))
	if len(fields) != 2 || !strings.EqualFold(fields[0], "Bearer") {
		return ""
	}
	return fields[1]
}

func secureEqual(left, right string) bool {
	return left != "" && right != "" && len(left) == len(right) && subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}

func writeUnauthorized(response http.ResponseWriter) {
	response.Header().Set("content-type", "application/json")
	response.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(response).Encode(map[string]any{
		"error": map[string]string{"message": "unauthorized BYOModels compatibility proxy request"},
	})
}

func writeProxyError(response http.ResponseWriter, err error) {
	response.Header().Set("content-type", "application/json")
	response.WriteHeader(http.StatusBadGateway)
	_ = json.NewEncoder(response).Encode(map[string]any{
		"error": map[string]string{
			"message": "BYOModels request compatibility proxy failed: " + err.Error(),
		},
	})
}

func isAddressInUse(err error) bool {
	return errors.Is(err, syscall.EADDRINUSE) || errors.Is(err, syscall.Errno(10048))
}
