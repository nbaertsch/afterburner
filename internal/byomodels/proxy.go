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
	"sort"
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
	completedReuseWindow  = 30 * time.Second
)

var upstreamIdleTimeout = 5 * time.Minute

var proxyManagedHeaders = map[string]bool{
	"accept-encoding":     true,
	"authorization":       true,
	"connection":          true,
	"content-length":      true,
	"host":                true,
	"keep-alive":          true,
	"proxy-authenticate":  true,
	"proxy-authorization": true,
	"te":                  true,
	"trailer":             true,
	"transfer-encoding":   true,
	"upgrade":             true,
	proxyCapabilityHeader: true,
}

type config struct {
	Version   int        `json:"version"`
	Providers []provider `json:"providers"`
	Models    []model    `json:"models"`
}

type model struct {
	Provider  string `json:"provider"`
	ID        string `json:"id"`
	WireModel string `json:"wireModel"`
}

type provider struct {
	Name                 string               `json:"name"`
	BaseURL              string               `json:"baseUrl"`
	Headers              map[string]string    `json:"headers"`
	RequestCompatibility requestCompatibility `json:"requestCompatibility"`
	Auth                 auth                 `json:"auth"`
	ModelAliases         map[string]string    `json:"-"`
}

type requestCompatibility struct {
	MaxInputItemIDLength int  `json:"maxInputItemIdLength"`
	ProxyPort            int  `json:"proxyPort"`
	ForceStreaming       bool `json:"forceStreaming"`
	LegacyTools          bool `json:"legacyTools"`
	BufferResponses      bool `json:"bufferResponses"`
}

type auth struct {
	Type     string `json:"type"`
	Resource string `json:"resource"`
	Value    string `json:"value"`
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
	inflight   map[string]*inflightResponse
}

type inflightResponse struct {
	done       chan struct{}
	statusCode int
	headers    http.Header
	body       []byte
	err        error
}

type idleDeadline struct {
	mu      sync.Mutex
	timer   *time.Timer
	timeout time.Duration
	cancel  context.CancelCauseFunc
	version uint64
	stopped bool
	fired   bool
}

type progressReadCloser struct {
	io.ReadCloser
	deadline *idleDeadline
	ctx      context.Context
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
	var pending []*service
	for _, configured := range value.Providers {
		configured.ModelAliases = modelAliases(value.Models, configured.Name)
		if err := validateProviderHeaders(configured); err != nil {
			manager.Close()
			return nil, err
		}
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
			inflight:   map[string]*inflightResponse{},
		}
		pending = append(pending, current)
	}

	startErrors := make([]error, len(pending))
	var wait sync.WaitGroup
	wait.Add(len(pending))
	for index, current := range pending {
		go func() {
			defer wait.Done()
			startErrors[index] = current.start()
		}()
	}
	wait.Wait()
	for index, current := range pending {
		if startErrors[index] == nil {
			manager.services = append(manager.services, current)
		}
	}
	for _, err := range startErrors {
		if err != nil {
			_ = manager.Close()
			return nil, err
		}
	}
	return manager, nil
}

func modelAliases(models []model, providerName string) map[string]string {
	aliases := map[string]string{}
	for _, current := range models {
		if current.Provider == providerName && current.WireModel != "" && current.WireModel != current.ID {
			aliases[current.WireModel] = current.ID
		}
	}
	return aliases
}

func canNativeOwn(value provider) bool {
	// Schema relocation is owned by the session proxy, including when a preferred port is configured.
	return !value.RequestCompatibility.LegacyTools && !value.RequestCompatibility.BufferResponses &&
		(value.Auth.Type == "" || value.Auth.Type == "azure-cli" || value.Auth.Type == "bearer-token")
}

func newCapability() (string, error) {
	var data [32]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data[:]), nil
}

func proxyConfiguration(value provider) string {
	configuredHeaders := make([]string, 0, len(value.Headers))
	for name, configuredValue := range value.Headers {
		configuredHeaders = append(
			configuredHeaders,
			strings.ToLower(strings.TrimSpace(name))+":"+configuredValue,
		)
	}
	sort.Strings(configuredHeaders)
	configuration := strings.Join([]string{
		strconv.Itoa(value.RequestCompatibility.MaxInputItemIDLength),
		strconv.FormatBool(value.RequestCompatibility.ForceStreaming),
		value.Auth.Type,
		value.Auth.Resource,
		strings.Join(configuredHeaders, "\n"),
	}, "\x00")
	if value.Auth.Type == "bearer-token" {
		configuration += "\x00" + value.Auth.Value
	}
	if value.RequestCompatibility.LegacyTools {
		configuration += "\x00legacyTools"
	}
	if value.RequestCompatibility.BufferResponses {
		configuration += "\x00bufferResponses"
	}
	if len(value.ModelAliases) > 0 {
		aliases := make([]string, 0, len(value.ModelAliases))
		for wireModel, id := range value.ModelAliases {
			aliases = append(aliases, wireModel+":"+id)
		}
		sort.Strings(aliases)
		configuration += "\x00modelAliases\n" + strings.Join(aliases, "\n")
	}
	hash := sha256.Sum256([]byte(configuration))
	return base64.RawURLEncoding.EncodeToString(hash[:])
}

func validateProviderHeaders(value provider) error {
	if value.Auth.Type == "bearer-token" && strings.TrimSpace(value.Auth.Value) == "" {
		return fmt.Errorf("provider %q must define a non-empty auth.value", value.Name)
	}
	names := map[string]bool{}
	for name, configuredValue := range value.Headers {
		normalized := strings.ToLower(strings.TrimSpace(name))
		if !validHeaderName(normalized) {
			return fmt.Errorf("provider %q has invalid configured header name %q", value.Name, name)
		}
		if names[normalized] {
			return fmt.Errorf("provider %q has duplicate configured header %q", value.Name, normalized)
		}
		if proxyManagedHeaders[normalized] {
			return fmt.Errorf("provider %q cannot configure proxy-managed header %q", value.Name, normalized)
		}
		if !validHeaderValue(configuredValue) {
			return fmt.Errorf("provider %q has an invalid value for configured header %q", value.Name, normalized)
		}
		names[normalized] = true
	}
	return nil
}

func validHeaderName(value string) bool {
	if value == "" {
		return false
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		if (character >= 'a' && character <= 'z') ||
			(character >= '0' && character <= '9') ||
			strings.ContainsRune("!#$%&'*+-.^_`|~", rune(character)) {
			continue
		}
		return false
	}
	return true
}

func validHeaderValue(value string) bool {
	for index := 0; index < len(value); index++ {
		character := value[index]
		if (character < 0x20 && character != '\t') || character == 0x7f {
			return false
		}
	}
	return true
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
	if request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/responses") {
		current.handleSharedResponse(response, request, body)
		return
	}
	current.proxy(response, request, body, request.Context())
}

func (current *service) handleSharedResponse(response http.ResponseWriter, request *http.Request, body []byte) {
	hash := sha256.New()
	_, _ = hash.Write([]byte(request.Method))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(request.URL.RequestURI()))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(body)
	key := base64.RawURLEncoding.EncodeToString(hash.Sum(nil))

	current.mu.Lock()
	shared, exists := current.inflight[key]
	if !exists {
		shared = &inflightResponse{done: make(chan struct{})}
		current.inflight[key] = shared
	}
	current.mu.Unlock()

	if !exists {
		go func() {
			recorder := newBufferedResponse()
			current.proxy(recorder, request.Clone(context.Background()), body, context.Background())
			shared.statusCode, shared.headers, shared.body, shared.err = recorder.result()
			close(shared.done)
			time.AfterFunc(completedReuseWindow, func() {
				current.mu.Lock()
				if current.inflight[key] == shared {
					delete(current.inflight, key)
				}
				current.mu.Unlock()
			})
		}()
	}

	select {
	case <-request.Context().Done():
		return
	case <-shared.done:
		if shared.err != nil {
			writeProxyError(response, shared.err)
			return
		}
		copyHeaders(response.Header(), shared.headers)
		response.WriteHeader(shared.statusCode)
		_, _ = response.Write(shared.body)
	}
}

type bufferedResponse struct {
	header     http.Header
	statusCode int
	body       bytes.Buffer
}

func newBufferedResponse() *bufferedResponse {
	return &bufferedResponse{header: make(http.Header), statusCode: http.StatusOK}
}

func (current *bufferedResponse) Header() http.Header {
	return current.header
}

func (current *bufferedResponse) WriteHeader(statusCode int) {
	if current.statusCode == http.StatusOK {
		current.statusCode = statusCode
	}
}

func (current *bufferedResponse) Write(data []byte) (int, error) {
	return current.body.Write(data)
}

func (current *bufferedResponse) Flush() {}

func (current *bufferedResponse) result() (int, http.Header, []byte, error) {
	return current.statusCode, current.header.Clone(), bytes.Clone(current.body.Bytes()), nil
}

func (current *service) proxy(
	response http.ResponseWriter,
	request *http.Request,
	body []byte,
	parent context.Context,
) {
	requestContext, cancel := context.WithCancelCause(parent)
	deadline := newIdleDeadline(upstreamIdleTimeout, cancel)
	defer deadline.stop()
	defer cancel(nil)

	var err error
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
	upstreamRequest, err := http.NewRequestWithContext(requestContext, request.Method, target.String(), bytes.NewReader(body))
	if err != nil {
		writeProxyError(response, err)
		return
	}
	copyClientRequestHeaders(upstreamRequest.Header, request.Header)
	if current.authorizationBearer(request) == current.capability {
		upstreamRequest.Header.Del("authorization")
	}
	removeHopByHopHeaders(upstreamRequest.Header)
	upstreamRequest.Header.Del("host")
	upstreamRequest.Header.Del("content-length")
	upstreamRequest.Header.Del("accept-encoding")
	upstreamRequest.Header.Del(proxyCapabilityHeader)
	for name, value := range current.provider.Headers {
		upstreamRequest.Header.Set(strings.TrimSpace(name), value)
	}
	if current.provider.Auth.Type == "azure-cli" {
		token, tokenErr := current.tokens.get(requestContext, current.provider.Auth.Resource)
		if tokenErr != nil {
			writeProxyError(response, tokenErr)
			return
		}
		upstreamRequest.Header.Set("authorization", "Bearer "+token)
	} else if current.provider.Auth.Type == "bearer-token" {
		upstreamRequest.Header.Set("authorization", "Bearer "+strings.TrimSpace(current.provider.Auth.Value))
	}

	upstreamResponse, err := current.client.Do(upstreamRequest)
	if err != nil {
		if cause := context.Cause(requestContext); cause != nil {
			err = cause
		}
		writeProxyError(response, err)
		return
	}
	deadline.touch()
	upstreamResponse.Body = &progressReadCloser{
		ReadCloser: upstreamResponse.Body,
		deadline:   deadline,
		ctx:        requestContext,
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
	if upstreamResponse.StatusCode == http.StatusTooManyRequests {
		bodyBytes, _ := io.ReadAll(upstreamResponse.Body)
		if bytes.Contains(bytes.ToLower(bodyBytes), []byte("no healthy deployment")) {
			response.Header().Set("content-type", "application/json")
			response.WriteHeader(http.StatusUnprocessableEntity)
			payload := map[string]any{
				"error": map[string]string{
					"code":    "upstream_deployment_unhealthy",
					"message": fmt.Sprintf("BYOModels %q [upstream_deployment_unhealthy]: Upstream deployment is currently unhealthy or unavailable.", current.provider.Name),
				},
			}
			encoded, _ := json.Marshal(payload)
			_, _ = response.Write(encoded)
			return
		}
		response.WriteHeader(upstreamResponse.StatusCode)
		_, _ = response.Write(bodyBytes)
		return
	}
	if strings.Contains(upstreamResponse.Header.Get("content-type"), "text/event-stream") &&
		request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/responses") {
		if err := streamResponsesWithPreTokenValidation(response, upstreamResponse.Body, upstreamResponse.StatusCode, current.provider.Name); err != nil {
			writeProxyError(response, err)
		}
		return
	}
	if request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/models") &&
		upstreamResponse.StatusCode >= 200 && upstreamResponse.StatusCode < 300 &&
		strings.Contains(upstreamResponse.Header.Get("content-type"), "application/json") &&
		len(current.provider.ModelAliases) > 0 {
		bodyBytes, readErr := io.ReadAll(upstreamResponse.Body)
		if readErr != nil {
			writeProxyError(response, readErr)
			return
		}
		rewritten, rewriteErr := rewriteModelCatalog(bodyBytes, current.provider.ModelAliases)
		if rewriteErr != nil {
			writeProxyError(response, rewriteErr)
			return
		}
		response.WriteHeader(upstreamResponse.StatusCode)
		_, _ = response.Write(rewritten)
		return
	}
	response.WriteHeader(upstreamResponse.StatusCode)
	_, _ = io.Copy(response, upstreamResponse.Body)
}

func newIdleDeadline(timeout time.Duration, cancel context.CancelCauseFunc) *idleDeadline {
	current := &idleDeadline{timeout: timeout, cancel: cancel}
	current.armLocked()
	return current
}

func (current *idleDeadline) touch() {
	current.mu.Lock()
	defer current.mu.Unlock()
	if current.fired || current.stopped {
		return
	}
	current.version++
	current.timer.Stop()
	current.armLocked()
}

func (current *idleDeadline) stop() {
	current.mu.Lock()
	defer current.mu.Unlock()
	if current.fired || current.stopped {
		return
	}
	current.stopped = true
	current.version++
	current.timer.Stop()
}

func (current *idleDeadline) armLocked() {
	version := current.version
	current.timer = time.AfterFunc(current.timeout, func() {
		current.expire(version)
	})
}

func (current *idleDeadline) expire(version uint64) {
	current.mu.Lock()
	if current.fired || current.stopped || version != current.version {
		current.mu.Unlock()
		return
	}
	current.fired = true
	current.mu.Unlock()
	current.cancel(fmt.Errorf("upstream made no progress for %s", current.timeout))
}

func (current *progressReadCloser) Read(buffer []byte) (int, error) {
	count, err := current.ReadCloser.Read(buffer)
	if count > 0 {
		current.deadline.touch()
	}
	if err != nil {
		if cause := context.Cause(current.ctx); cause != nil {
			return count, cause
		}
	}
	return count, err
}

func rewriteModelCatalog(body []byte, aliases map[string]string) ([]byte, error) {
	var catalog map[string]any
	if err := json.Unmarshal(body, &catalog); err != nil {
		return nil, fmt.Errorf("decode upstream model catalog: %w", err)
	}
	data, ok := catalog["data"].([]any)
	if !ok {
		return nil, errors.New("decode upstream model catalog: data must be an array")
	}
	seen := map[string]bool{}
	rewritten := make([]any, 0, len(data))
	for _, rawEntry := range data {
		entry, ok := rawEntry.(map[string]any)
		if !ok {
			rewritten = append(rewritten, rawEntry)
			continue
		}
		id, _ := entry["id"].(string)
		if alias := aliases[id]; alias != "" {
			entry["id"] = alias
			id = alias
		}
		if id != "" && seen[id] {
			continue
		}
		if id != "" {
			seen[id] = true
		}
		rewritten = append(rewritten, entry)
	}
	catalog["data"] = rewritten
	return json.Marshal(catalog)
}

func streamResponsesWithPreTokenValidation(w http.ResponseWriter, upstreamBody io.ReadCloser, statusCode int, providerName string) error {
	bufReader := bufio.NewReader(upstreamBody)
	var initial []byte
	for {
		line, err := bufReader.ReadBytes('\n')
		initial = append(initial, line...)
		if len(line) == 0 || bytes.Equal(line, []byte("\n")) || bytes.Equal(line, []byte("\r\n")) {
			break
		}
		if err != nil {
			break
		}
	}
	trimmed := strings.TrimSpace(string(initial))
	if strings.HasPrefix(trimmed, "event: error") || strings.Contains(trimmed, `"type":"error"`) ||
		strings.Contains(trimmed, `"type":"response.failed"`) {
		var event struct {
			Type  string `json:"type"`
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
			Message string `json:"message"`
			Code    string `json:"code"`
		}
		for _, line := range strings.Split(trimmed, "\n") {
			if strings.HasPrefix(line, "data:") {
				_ = json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &event)
			}
		}
		msg := event.Error.Message
		if msg == "" {
			msg = event.Message
		}
		if msg == "" {
			msg = "Upstream Responses streaming error"
		}
		code := event.Error.Code
		if code == "" {
			code = event.Code
		}
		if code == "" {
			code = "upstream_error"
		}
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		payload := map[string]any{
			"error": map[string]string{
				"code":    code,
				"message": fmt.Sprintf("BYOModels %q [%s]: %s", providerName, code, msg),
			},
		}
		encoded, _ := json.Marshal(payload)
		_, _ = w.Write(encoded)
		return nil
	}

	w.WriteHeader(statusCode)
	if len(initial) > 0 {
		if _, err := w.Write(initial); err != nil {
			return err
		}
	}
	_, err := io.Copy(w, bufReader)
	return err
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

func copyClientRequestHeaders(destination, source http.Header) {
	for _, name := range []string{"accept", "content-type"} {
		for _, value := range source.Values(name) {
			destination.Add(name, value)
		}
	}
}

func removeHopByHopHeaders(headers http.Header) {
	for _, value := range headers.Values("connection") {
		for _, name := range strings.Split(value, ",") {
			headers.Del(strings.TrimSpace(name))
		}
	}
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
