package launch

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nbaertsch/afterburner/internal/platform"
	"github.com/nbaertsch/afterburner/internal/registry"
	"github.com/nbaertsch/afterburner/internal/terminal"
)

type Options struct {
	Executable        string
	Args              []string
	Env               []string
	Stdin             io.Reader
	Stdout            io.Writer
	Stderr            io.Writer
	ExtensionRegistry *registry.Registry
}

func FindCopilot() (string, error) {
	if explicit := os.Getenv("AFTERBURNER_COPILOT_EXECUTABLE"); explicit != "" {
		return validateExecutable(explicit)
	}
	path, err := exec.LookPath("copilot")
	if err != nil {
		return "", fmt.Errorf("find Copilot CLI: %w", err)
	}
	return validateExecutable(path)
}

func Run(ctx context.Context, opts Options) (int, error) {
	if shouldUseBroker(opts) {
		return runWithBroker(ctx, opts)
	}
	return runDirect(ctx, opts)
}

func runDirect(ctx context.Context, opts Options) (int, error) {
	childEnv, cleanupBootstrap, err := PrepareNativeEnvironment(opts.Env, opts.ExtensionRegistry)
	if err != nil {
		return 1, err
	}
	defer cleanupBootstrap()

	cmd := exec.CommandContext(ctx, opts.Executable, opts.Args...)
	cmd.Env = childEnv
	cmd.Stdin = opts.Stdin
	cmd.Stdout = opts.Stdout
	cmd.Stderr = opts.Stderr
	platform.ConfigureChild(cmd)
	if err := cmd.Start(); err != nil {
		return 1, fmt.Errorf("start Copilot CLI: %w", err)
	}
	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt)
	done := make(chan struct{})
	go func() {
		select {
		case <-interrupts:
			_ = platform.InterruptProcess(cmd.Process.Pid)
		case <-done:
		}
	}()
	err = cmd.Wait()
	close(done)
	signal.Stop(interrupts)
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode(), nil
		}
		return 1, fmt.Errorf("wait for Copilot CLI: %w", err)
	}
	return 0, nil
}

func PrepareNativeEnvironment(env []string, extensionRegistry *registry.Registry) ([]string, func(), error) {
	bootstrapPath, cleanupBootstrap, err := writeNativeBootstrap(
		nil,
		nativeIdentityAssertions(extensionRegistry),
	)
	if err != nil {
		return nil, nil, err
	}
	return withNativeEnv(env, bootstrapPath), cleanupBootstrap, nil
}

func runWithBroker(ctx context.Context, opts Options) (int, error) {
	stdin := opts.Stdin.(*os.File)
	stdout := opts.Stdout.(*os.File)
	hostMode, err := terminal.PrepareHost(stdin, stdout)
	if err != nil {
		return 1, fmt.Errorf("prepare terminal host: %w", err)
	}
	defer hostMode.Restore()
	restoreInterruptInput, err := preserveInterruptInputBytes(stdin)
	if err != nil {
		return 1, fmt.Errorf("prepare interrupt input: %w", err)
	}
	defer restoreInterruptInput()

	size := terminal.ConsoleSize(stdout)
	renderer := terminal.NewTerminalModalRendererWithSize(stdout, size)
	var modalServer *terminal.ModalServer
	router := terminal.InputRouterFunc(func(owner terminal.Owner, data []byte, process terminal.Process) (int, error) {
		if owner == terminal.OwnerModal {
			if modalServer != nil {
				return handleModalInput(modalServer, data)
			}
			return len(data), nil
		}
		return process.WriteInput(data)
	})
	broker := terminal.NewBroker(terminal.NewConPTYBackend(), terminal.BrokerOptions{
		OutputSink:  terminal.OutputHandlerFunc(func(output terminal.Output) { renderer.WriteCopilotOutput(output.Data) }),
		InputRouter: router,
	})
	modalServer, err = terminal.NewModalServer(broker, renderer)
	if err != nil {
		return 1, err
	}
	modalSurfaces, err := preissueModalCapabilities(modalServer, opts.ExtensionRegistry)
	if err != nil {
		return 1, fmt.Errorf("register native UI surfaces: %w", err)
	}
	startModalPipeServers, modalPipeClose, err := prepareModalPipes(modalServer, modalSurfaces)
	if err != nil {
		return 1, fmt.Errorf("start modal pipes: %w", err)
	}
	cleanup := brokerCleanup(modalPipeClose, modalServer.CloseAll, broker.Close)
	defer cleanup()

	bootstrapPath, cleanupBootstrap, err := writeNativeBootstrap(modalSurfaces, nativeIdentityAssertions(opts.ExtensionRegistry))
	if err != nil {
		return 1, err
	}
	defer cleanupBootstrap()

	childEnv := withModalEnv(opts.Env, bootstrapPath)
	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt)
	defer signal.Stop(interrupts)

	if err := broker.Start(ctx, terminal.Command{
		Path: opts.Executable,
		Args: opts.Args,
		Env:  childEnv,
		Size: size,
	}); err != nil {
		return 1, fmt.Errorf("start terminal broker: %w", err)
	}
	modalServer.AuthorizeClientProcess(broker.ProcessID())
	startModalPipeServers()

	inputDone := make(chan struct{})
	var inputOnce sync.Once
	closeInputDone := func() { inputOnce.Do(func() { close(inputDone) }) }
	go func() {
		defer closeInputDone()
		buffer := make([]byte, 32*1024)
		for {
			read, readErr := stdin.Read(buffer)
			if read > 0 {
				if writeErr := writeBrokerInput(broker, buffer[:read]); writeErr != nil {
					return
				}
			}
			if readErr != nil {
				return
			}
		}
	}()

	waitDone := make(chan struct{})
	var status terminal.ExitStatus
	var waitErr error
	go func() {
		status, waitErr = broker.Wait()
		close(waitDone)
	}()

	resizeTicker := time.NewTicker(200 * time.Millisecond)
	defer resizeTicker.Stop()
	lastSize := size
	for {
		select {
		case <-interrupts:
			_ = interruptBrokerIfCopilotOwner(broker)
		case <-inputDone:
			inputDone = nil
		case <-resizeTicker.C:
			if next := terminal.ConsoleSize(stdout); next != lastSize {
				lastSize = next
				resizeBrokerAndRenderer(broker, renderer, next)
			}
		case <-ctx.Done():
			_ = broker.Close()
			<-waitDone
			if waitErr != nil {
				return normalizeExitCode(status.Code, waitErr), waitErr
			}
			return normalizeExitCode(status.Code, ctx.Err()), ctx.Err()
		case <-waitDone:
			modalServer.CloseAll()
			return normalizeExitCode(status.Code, waitErr), waitErr
		}
	}
}

func shouldUseBroker(opts Options) bool {
	if !brokerFeatureEnabled() {
		return false
	}
	stdin, stdinOK := opts.Stdin.(*os.File)
	stdout, stdoutOK := opts.Stdout.(*os.File)
	if !stdinOK || !stdoutOK {
		return false
	}
	return terminal.IsTerminal(stdin) && terminal.IsTerminal(stdout)
}

func brokerFeatureEnabled() bool {
	switch strings.ToLower(os.Getenv("AFTERBURNER_DISABLE_TERMINAL_BROKER")) {
	case "1", "true", "yes", "on":
		return false
	default:
		return true
	}
}

func writeBrokerInput(broker *terminal.Broker, data []byte) error {
	if broker.Owner() == terminal.OwnerModal {
		_, err := broker.WriteInput(data)
		return err
	}
	start := 0
	for index, b := range data {
		if b != 0x03 {
			continue
		}
		if index > start {
			if _, err := broker.WriteInput(data[start:index]); err != nil {
				return err
			}
		}
		_ = interruptBrokerIfCopilotOwner(broker)
		start = index + 1
	}
	if start < len(data) {
		_, err := broker.WriteInput(data[start:])
		return err
	}
	return nil
}

func interruptBrokerIfCopilotOwner(broker *terminal.Broker) error {
	if broker.Owner() != terminal.OwnerCopilot {
		return nil
	}
	interruptErr := broker.Interrupt()
	_, inputErr := broker.WriteInput([]byte{0x03})
	if inputErr == nil {
		return nil
	}
	if interruptErr != nil {
		return interruptErr
	}
	return inputErr
}

func resizeBrokerAndRenderer(broker *terminal.Broker, renderer *terminal.TerminalModalRenderer, size terminal.Size) {
	if renderer != nil {
		renderer.Resize(size)
	}
	_ = broker.Resize(size)
}

func handleModalInput(modalServer *terminal.ModalServer, data []byte) (int, error) {
	return modalServer.HandleInput(data)
}

func withNativeEnv(env []string, bootstrapPath string) []string {
	baseEnv := env
	if baseEnv == nil {
		baseEnv = os.Environ()
	}
	childEnv := withoutEnv(baseEnv,
		"AFTERBURNER_MODAL_PIPE",
		"AFTERBURNER_MODAL_SECRET",
		"AFTERBURNER_MODAL_OWNER_EXTENSION_ID",
		"AFTERBURNER_MODAL_CANVAS_ID",
		"AFTERBURNER_MODAL_SURFACE_ID",
		"AFTERBURNER_MODAL_BOOTSTRAP",
		"AFTERBURNER_NATIVE_BOOTSTRAP",
		"AFTERBURNER_SESSION_ROUTE",
	)
	if bootstrapPath == "" {
		return childEnv
	}
	if route, ok := nativeBootstrapRoute(bootstrapPath); ok {
		childEnv = withEnv(childEnv, "AFTERBURNER_SESSION_ROUTE", route)
	}
	return withEnv(childEnv, "AFTERBURNER_NATIVE_BOOTSTRAP", bootstrapPath)
}

func withModalEnv(env []string, bootstrapPath string) []string {
	childEnv := withNativeEnv(env, bootstrapPath)
	if bootstrapPath == "" {
		return childEnv
	}
	return withEnv(childEnv, "AFTERBURNER_MODAL_BOOTSTRAP", bootstrapPath)
}

func preissueModalCapabilities(server *terminal.ModalServer, value *registry.Registry) ([]terminal.ModalCapability, error) {
	if server == nil || value == nil {
		return nil, nil
	}
	registrations := preissuedModalRegistrations(value)
	capabilities := make([]terminal.ModalCapability, 0, len(registrations))
	for _, registration := range registrations {
		capability, err := server.RegisterModalCanvas(registration)
		if err != nil {
			return nil, fmt.Errorf("register %s/%s: %w", registration.OwnerExtensionID, registration.SurfaceID, err)
		}
		capabilities = append(capabilities, capability)
	}
	return capabilities, nil
}

func preissuedModalRegistrations(value *registry.Registry) []terminal.ModalRegistration {
	if value == nil {
		return nil
	}
	ids := make([]string, 0, len(value.Extensions))
	for id := range value.Extensions {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	registrations := []terminal.ModalRegistration{}
	for _, id := range ids {
		entry := value.Extensions[id]
		if !entry.Enabled || !entry.Verified || !manifestHasCapability(entry.Manifest, "modal-canvas") {
			continue
		}
		if entry.Manifest.UI != nil {
			for _, surface := range entry.Manifest.UI.Surfaces {
				if surface.Kind != "modal" {
					continue
				}
				registrations = append(registrations, terminal.ModalRegistration{
					OwnerExtensionID: id,
					CanvasID:         surface.ID,
					SurfaceID:        surface.ID,
				})
			}
			continue
		}
	}
	return registrations
}

func manifestHasCapability(manifest registry.Manifest, capability string) bool {
	for _, candidate := range manifest.Capabilities {
		if candidate == capability {
			return true
		}
	}
	return false
}

func prepareModalPipes(server *terminal.ModalServer, surfaces []terminal.ModalCapability) (func(), func() error, error) {
	listeners := make([]*terminal.ModalPipeListener, 0, len(surfaces))
	for index := range surfaces {
		listener, err := terminal.NewModalPipeListenerForCapability(server, surfaces[index])
		if err != nil {
			for _, existing := range listeners {
				_ = existing.Close()
			}
			return func() {}, nil, err
		}
		surfaces[index].Pipe = listener.PipeName()
		listeners = append(listeners, listener)
	}
	start := func() {
		for _, listener := range listeners {
			go listener.Serve()
		}
	}
	close := func() error {
		var first error
		for _, listener := range listeners {
			if err := listener.Close(); err != nil && first == nil {
				first = err
			}
		}
		return first
	}
	return start, close, nil
}

func newSessionRoute() (string, error) {
	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("generate session route: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

func nativeBootstrapRoute(path string) (string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	var payload struct {
		SessionRoute string `json:"sessionRoute"`
	}
	if err := json.Unmarshal(data, &payload); err != nil || payload.SessionRoute == "" {
		return "", false
	}
	return payload.SessionRoute, true
}

func writeNativeBootstrap(surfaces []terminal.ModalCapability, assertions []nativeIdentityAssertion) (string, func(), error) {
	if len(surfaces) == 0 && len(assertions) == 0 {
		return "", func() {}, nil
	}
	sessionRoute, err := newSessionRoute()
	if err != nil {
		return "", nil, err
	}
	dir, err := os.MkdirTemp("", "afterburner-native-")
	if err != nil {
		return "", func() {}, fmt.Errorf("create native bootstrap directory: %w", err)
	}
	path := filepath.Join(dir, "bootstrap.json")
	payload := struct {
		SchemaVersion int                        `json:"schemaVersion"`
		SessionRoute  string                     `json:"sessionRoute"`
		Surfaces      []terminal.ModalCapability `json:"modalSurfaces,omitempty"`
		Assertions    []nativeIdentityAssertion  `json:"verifiedExtensions,omitempty"`
	}{
		SchemaVersion: 2,
		SessionRoute:  sessionRoute,
		Surfaces:      append([]terminal.ModalCapability(nil), surfaces...),
		Assertions:    assertions,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", func() {}, fmt.Errorf("encode native bootstrap: %w", err)
	}
	if err := os.WriteFile(path, append(encoded, '\n'), 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return "", func() {}, fmt.Errorf("write native bootstrap: %w", err)
	}
	return path, func() { _ = os.RemoveAll(dir) }, nil
}

type nativeIdentityAssertion struct {
	ExtensionID    string `json:"extensionId"`
	ActivePath     string `json:"activePath"`
	ManifestHash   string `json:"manifestHash"`
	TreeHash       string `json:"treeHash"`
	SourceType     string `json:"sourceType"`
	SourceValue    string `json:"sourceValue"`
	TrustedBuiltin bool   `json:"trustedBuiltin,omitempty"`
}

func nativeIdentityAssertions(value *registry.Registry) []nativeIdentityAssertion {
	if value == nil {
		return nil
	}
	assertions := make([]nativeIdentityAssertion, 0, len(value.Extensions))
	for id, entry := range value.Extensions {
		if !entry.Enabled || !entry.Verified {
			continue
		}
		if entry.Identity.ManifestHash == "" || entry.Identity.TreeHash == "" {
			continue
		}
		assertions = append(assertions, nativeIdentityAssertion{
			ExtensionID:    id,
			ActivePath:     entry.ActivePath,
			ManifestHash:   entry.Identity.ManifestHash,
			TreeHash:       entry.Identity.TreeHash,
			SourceType:     entry.Source.Type,
			SourceValue:    entry.Source.Value,
			TrustedBuiltin: registry.IsTrustedBuiltinEntry(entry),
		})
	}
	return assertions
}

func brokerCleanup(closePipe func() error, closeModals func(), closeBroker func() error) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			if closePipe != nil {
				_ = closePipe()
			}
			if closeModals != nil {
				closeModals()
			}
			if closeBroker != nil {
				_ = closeBroker()
			}
		})
	}
}

func normalizeExitCode(code int, err error) int {
	if code < 0 {
		return 1
	}
	if err != nil && code == 0 {
		return 1
	}
	return code
}

func withoutEnv(env []string, keys ...string) []string {
	blocked := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		blocked[strings.ToUpper(key)] = struct{}{}
	}
	result := make([]string, 0, len(env))
	for _, existing := range env {
		name, _, ok := strings.Cut(existing, "=")
		if ok {
			if _, remove := blocked[strings.ToUpper(name)]; remove {
				continue
			}
		}
		result = append(result, existing)
	}
	return result
}

func withEnv(env []string, key, value string) []string {
	entry := key + "=" + value
	result := append([]string(nil), env...)
	for index, existing := range result {
		name, _, ok := strings.Cut(existing, "=")
		if ok && strings.EqualFold(name, key) {
			result[index] = entry
			return result
		}
	}
	return append(result, entry)
}

func validateExecutable(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve Copilot executable: %w", err)
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return "", fmt.Errorf("inspect Copilot executable: %w", err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("Copilot executable is a directory: %s", absolute)
	}
	base := strings.ToLower(filepath.Base(absolute))
	if base == "afterburn.exe" || base == "afterburn" {
		return "", fmt.Errorf("Copilot executable resolves recursively to Afterburner: %s", absolute)
	}
	return absolute, nil
}
