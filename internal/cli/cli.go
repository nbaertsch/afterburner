package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/nbaertsch/afterburner/internal/byomodels"
	"github.com/nbaertsch/afterburner/internal/compatibility"
	"github.com/nbaertsch/afterburner/internal/copilot"
	"github.com/nbaertsch/afterburner/internal/doctor"
	"github.com/nbaertsch/afterburner/internal/extensions"
	"github.com/nbaertsch/afterburner/internal/home"
	"github.com/nbaertsch/afterburner/internal/installer"
	"github.com/nbaertsch/afterburner/internal/launch"
	"github.com/nbaertsch/afterburner/internal/preflight"
	"github.com/nbaertsch/afterburner/internal/registry"
	"github.com/nbaertsch/afterburner/internal/runtimepkg"
	"github.com/nbaertsch/afterburner/internal/sessions"
	"github.com/nbaertsch/afterburner/internal/telemetry"
	"github.com/nbaertsch/afterburner/internal/ui/render"
	"github.com/nbaertsch/afterburner/internal/ui/tooling"
	"github.com/nbaertsch/afterburner/internal/updater"
)

var reserved = map[string]struct{}{
	"install": {}, "enable": {}, "disable": {}, "uninstall": {},
	"extension": {}, "doctor": {}, "repair": {}, "update": {},
	"rollback": {}, "version": {}, "help": {}, "run": {},
	"compatibility": {},
	"core":          {},
	"ui":            {},
}

type Options struct {
	Version string
	Stdin   io.Reader
	Stdout  io.Writer
	Stderr  io.Writer
}

type Route struct {
	Command           string
	Args              []string
	ForcedPassthrough bool
}

func Classify(args []string) Route {
	if len(args) == 0 {
		return Route{Command: "run", Args: []string{}}
	}
	if args[0] == "--" {
		return Route{Command: "run", Args: clone(args[1:]), ForcedPassthrough: true}
	}
	if args[0] == "run" {
		return Route{Command: "run", Args: clone(args[1:]), ForcedPassthrough: true}
	}
	if _, ok := reserved[args[0]]; ok {
		return Route{Command: args[0], Args: clone(args[1:])}
	}
	return Route{Command: "run", Args: clone(args)}
}

func Run(ctx context.Context, args []string, opts Options) (int, error) {
	route := Classify(args)
	switch route.Command {
	case "run":
		return runCopilot(ctx, route.Args, route.ForcedPassthrough, opts)
	case "version":
		warnCoreUpdateStatus(opts)
		fmt.Fprintf(opts.Stdout, "Afterburn %s\n", opts.Version)
		return 0, nil
	case "help":
		io.WriteString(opts.Stdout, help)
		return 0, nil
	case "doctor":
		return runDoctor(route.Args, opts)
	case "repair":
		return runRepair(route.Args, opts)
	case "compatibility":
		return runCompatibility(route.Args, opts)
	case "update":
		return runUpdate(ctx, route.Args, opts)
	case "rollback":
		return runRollback(route.Args, opts)
	case "install", "enable", "disable", "uninstall", "extension":
		return runExtensionCommand(route, opts)
	case "core":
		return runCoreCommand(route.Args, opts)
	case "ui":
		return runUICommand(ctx, route.Args, opts)
	default:
		return 2, fmt.Errorf("native command %q is not implemented yet", route.Command)
	}
}

func warnCoreUpdateStatus(opts Options) {
	layout, err := home.Initialize()
	if err != nil {
		return
	}
	warnCoreUpdateStatusForLayout(layout, opts)
}

func warnCoreUpdateStatusForLayout(layout home.Layout, opts Options) {
	warning, ok := coreUpdateStatusWarning(layout.Root)
	if !ok {
		return
	}
	writer := opts.Stderr
	if writer == nil {
		writer = io.Discard
	}
	fmt.Fprint(writer, warning)
}

func coreUpdateStatusWarning(root string) (string, bool) {
	status, ok, err := updater.ReadStatus(root)
	if err != nil || !ok {
		return "", false
	}
	warning := updater.FormatStatusWarning(status)
	return warning, warning != ""
}

func runRollback(args []string, opts Options) (int, error) {
	if len(args) != 1 || args[0] != "core" {
		return 2, fmt.Errorf("usage: afterburn rollback core")
	}
	layout, err := home.Initialize()
	if err != nil {
		return 1, err
	}
	target := filepath.Join(layout.Root, "bin", "afterburn.exe")
	previous := filepath.Join(layout.Root, "bin", "afterburn.previous.exe")
	candidate, err := updater.StageRollback(layout.Root, previous)
	if err != nil {
		return 1, err
	}
	if err := updater.BeginReplacement(candidate, target, previous); err != nil {
		_ = os.RemoveAll(filepath.Dir(candidate))
		return 1, err
	}
	fmt.Fprintln(opts.Stdout, "Afterburner core rollback is staged; replacement will complete after this process exits.")
	return 0, nil
}

func runUpdate(ctx context.Context, args []string, opts Options) (int, error) {
	warnCoreUpdateStatus(opts)
	checkOnly := len(args) == 1 && args[0] == "--check"
	pinned := len(args) == 2 && args[0] == "--version" && args[1] != ""
	if len(args) != 0 && !checkOnly && !pinned {
		return 2, fmt.Errorf("usage: afterburn update [--check|--version <version>]")
	}
	client := updater.NewClient(ctx)
	var release updater.Release
	var err error
	if pinned {
		release, err = client.Release(ctx, args[1])
	} else {
		release, err = client.Latest(ctx)
	}
	if err == updater.ErrNoRelease {
		fmt.Fprintln(opts.Stdout, "No Afterburner GitHub release exists yet.")
		return 0, nil
	}
	if err != nil {
		return 1, err
	}
	if !pinned && !updater.IsNewer(opts.Version, release.TagName) {
		fmt.Fprintf(opts.Stdout, "Afterburner %s is current.\n", opts.Version)
		return 0, nil
	}
	if checkOnly {
		fmt.Fprintf(opts.Stdout, "Afterburner %s is available.\n", release.TagName)
		return 0, nil
	}
	layout, err := home.Initialize()
	if err != nil {
		return 1, err
	}
	candidate, err := client.Stage(ctx, release, layout.Root)
	if err != nil {
		return 1, err
	}
	target := filepath.Join(layout.Root, "bin", "afterburn.exe")
	previous := filepath.Join(layout.Root, "bin", "afterburn.previous.exe")
	manager := extensions.Manager{Layout: layout, Stdout: opts.Stdout, BuiltinFetcher: updater.BuiltinReleaseFetcher{
		Client:  client,
		Root:    layout.Root,
		Version: release.TagName,
	}}
	if err := manager.SyncBuiltins(nil); err != nil {
		_ = os.RemoveAll(filepath.Dir(candidate))
		return 1, fmt.Errorf("sync built-in extensions for %s: %w", release.TagName, err)
	}
	if err := updater.BeginReplacement(candidate, target, previous); err != nil {
		return 1, err
	}
	fmt.Fprintf(opts.Stdout, "Afterburner %s and built-in extensions are staged; core replacement will complete after this process exits.\n", release.TagName)
	return 0, nil
}

func runCoreCommand(args []string, opts Options) (int, error) {
	if len(args) == 1 && args[0] == "install" {
		layout, err := home.Initialize()
		if err != nil {
			return 1, err
		}
		executable, err := os.Executable()
		if err != nil {
			return 1, fmt.Errorf("resolve running executable: %w", err)
		}
		if _, err := installer.Install(layout, executable, opts.Stdout); err != nil {
			return 1, err
		}
		return 0, nil
	}
	if len(args) == 9 && args[0] == "replace" &&
		args[1] == "--parent" && args[3] == "--source" &&
		args[5] == "--target" && args[7] == "--previous" {
		parent, err := strconv.Atoi(args[2])
		if err != nil || parent <= 0 {
			return 2, fmt.Errorf("invalid replacement parent PID")
		}
		layout, err := home.Resolve()
		if err != nil {
			return 1, err
		}
		expectedTarget := filepath.Join(layout.Root, "bin", "afterburn.exe")
		expectedPrevious := filepath.Join(layout.Root, "bin", "afterburn.previous.exe")
		if !strings.EqualFold(filepath.Clean(args[6]), filepath.Clean(expectedTarget)) ||
			!strings.EqualFold(filepath.Clean(args[8]), filepath.Clean(expectedPrevious)) ||
			!registry.Within(args[4], filepath.Join(layout.Root, "update-staging")) {
			return 2, fmt.Errorf("replacement paths are outside the managed update transaction")
		}
		if err := updater.ApplyReplacement(parent, args[4], args[6], args[8]); err != nil {
			return 1, err
		}
		return 0, nil
	}
	return 2, fmt.Errorf("usage: afterburn core install")
}

func runExtensionCommand(route Route, opts Options) (int, error) {
	layout, err := home.Initialize()
	if err != nil {
		return 1, err
	}
	warnCoreUpdateStatusForLayout(layout, opts)
	manager := extensions.Manager{Layout: layout, Stdout: opts.Stdout}
	if strings.TrimSpace(os.Getenv("AFTERBURNER_BUILTIN_SOURCE_OVERRIDE")) == "" &&
		strings.TrimSpace(os.Getenv("AFTERBURNER_DISABLE_BUILTIN_RELEASE_FETCH")) == "" {
		manager.BuiltinFetcher = updater.BuiltinReleaseFetcher{
			Client: updater.NewClient(context.Background()),
			Root:   layout.Root,
		}
	}
	switch route.Command {
	case "install":
		err = manager.InstallBuiltins(route.Args)
	case "enable":
		err = manager.SetBuiltinsEnabled(route.Args, true)
	case "disable":
		err = manager.SetBuiltinsEnabled(route.Args, false)
	case "uninstall":
		err = manager.UninstallBuiltins(route.Args)
	case "extension":
		if len(route.Args) == 0 {
			return 2, fmt.Errorf("extension subcommand is required")
		}
		switch route.Args[0] {
		case "install":
			if len(route.Args) != 2 {
				return 2, fmt.Errorf("usage: afterburn extension install <local-path>")
			}
			err = manager.Install(route.Args[1])
		case "enable":
			if len(route.Args) != 2 {
				return 2, fmt.Errorf("usage: afterburn extension enable <id>")
			}
			err = manager.SetEnabled(route.Args[1], true)
		case "disable":
			if len(route.Args) != 2 {
				return 2, fmt.Errorf("usage: afterburn extension disable <id>")
			}
			err = manager.SetEnabled(route.Args[1], false)
		case "list":
			err = manager.List()
		case "inspect":
			if len(route.Args) != 2 {
				return 2, fmt.Errorf("usage: afterburn extension inspect <id>")
			}
			err = manager.Inspect(route.Args[1])
		case "update":
			if len(route.Args) != 2 {
				return 2, fmt.Errorf("usage: afterburn extension update <id>|--all")
			}
			if route.Args[1] == "--all" {
				err = manager.UpdateAll()
			} else {
				err = manager.Update(route.Args[1])
			}
		case "rollback":
			if len(route.Args) != 2 {
				return 2, fmt.Errorf("usage: afterburn extension rollback <id>")
			}
			err = manager.Rollback(route.Args[1])
		default:
			return 2, fmt.Errorf("native extension subcommand %q is not implemented yet", route.Args[0])
		}
	}
	if err != nil {
		return 1, err
	}
	return 0, nil
}

func runCopilot(ctx context.Context, args []string, forcedPassthrough bool, opts Options) (int, error) {
	startupStarted := time.Now()
	startupPhase := startupStarted
	traceStartup := func(name string) {
		if os.Getenv("AFTERBURNER_TRACE_STARTUP") == "1" {
			fmt.Fprintf(opts.Stderr, "[afterburn startup] %s=%s total=%s\n",
				name, time.Since(startupPhase).Round(time.Millisecond), time.Since(startupStarted).Round(time.Millisecond))
		}
		startupPhase = time.Now()
	}
	launchOptions := nativeLaunchOptions{}
	var err error
	if !forcedPassthrough {
		launchOptions, args, err = extractLaunchOptions(args)
		if err != nil {
			return 2, err
		}
	}
	baseLayout, err := home.Initialize()
	if err != nil {
		return 1, err
	}
	warnCoreUpdateStatusForLayout(baseLayout, opts)
	traceStartup("home")
	layout := baseLayout
	cleanup := func() {}
	if launchOptions.safeMode || len(launchOptions.disabledExtensions) > 0 {
		layout, cleanup, err = home.CreateLaunchHome(baseLayout)
		if err != nil {
			return 1, err
		}
		defer cleanup()
	}
	extensionRegistry, err := registry.Load(baseLayout.Root)
	if err != nil {
		return 1, err
	}
	traceStartup("registry")
	effectiveRegistry := extensionRegistry
	if launchOptions.safeMode {
		for id, entry := range effectiveRegistry.Extensions {
			entry.Enabled = false
			effectiveRegistry.Extensions[id] = entry
		}
	} else {
		for _, id := range launchOptions.disabledExtensions {
			entry, ok := effectiveRegistry.Extensions[id]
			if !ok {
				return 2, fmt.Errorf("unknown extension %q", id)
			}
			entry.Enabled = false
			effectiveRegistry.Extensions[id] = entry
		}
	}
	if err := sessions.Reconcile(layout, effectiveRegistry); err != nil {
		return 1, err
	}
	traceStartup("sessions")
	executable, err := launch.FindCopilot()
	if err != nil {
		return 1, err
	}
	traceStartup("executable")
	inventory, err := copilot.Discover(copilot.DiscoveryOptions{
		ManagedHome:       layout.CopilotHome,
		CopilotExecutable: executable,
		HashCachePath:     filepath.Join(layout.Root, "state", "package-hashes.json"),
	})
	if err != nil {
		return 1, err
	}
	traceStartup("discovery")
	selection, err := compatibility.Select(inventory)
	if err != nil {
		return 1, err
	}
	traceStartup("compatibility")
	selected := selection.Package
	prepared, err := runtimepkg.Prepare(layout, selected)
	if err != nil {
		return 1, err
	}
	traceStartup("runtime")
	env := home.ManagedEnvironment(layout, os.Environ())
	env = setEnv(env, "AFTERBURNER_BASE_PACKAGE", selected.Path)
	env = setEnv(env, "AFTERBURNER_BASE_APP_SHA256", selected.AppSHA256)
	env = setEnv(env, "AFTERBURNER_BASE_RUNTIME_SHA256", selected.RuntimeSHA256)
	env = setEnv(env, "AFTERBURNER_COMPATIBILITY_PROFILE", selection.Profile.ID)
	if launchOptions.safeMode {
		env = setEnv(env, "AFTERBURNER_DISABLED_EXTENSIONS", "*")
	} else if len(launchOptions.disabledExtensions) > 0 {
		env = setEnv(env, "AFTERBURNER_DISABLED_EXTENSIONS", strings.Join(launchOptions.disabledExtensions, ","))
	}
	if _, ok := os.LookupEnv("AFTERBURNER_BYOMODELS_CONFIG"); !ok {
		if _, err := os.Stat(layout.BYOModelsConfig); err == nil {
			env = setEnv(env, "AFTERBURNER_BYOMODELS_CONFIG", layout.BYOModelsConfig)
		}
	}
	byoModelsConfig := layout.BYOModelsConfig
	if explicit := os.Getenv("AFTERBURNER_BYOMODELS_CONFIG"); explicit != "" {
		byoModelsConfig = explicit
	}
	var proxyManager *byomodels.Manager
	if entry, ok := effectiveRegistry.Extensions["byo-models"]; ok && entry.Enabled {
		proxyManager, err = byomodels.Start(byoModelsConfig)
		if err != nil {
			return 1, err
		}
		defer proxyManager.Close()
	}
	traceStartup("byomodels-proxy")
	validated := preflight.Result{Package: selected, Profile: selection.Profile, Prepared: prepared}
	if !launchOptions.safeMode && len(launchOptions.disabledExtensions) == 0 {
		validated, err = preflight.Ensure(ctx, layout, executable, selection, prepared, env, opts.Stderr)
		if err != nil {
			return 1, err
		}
	}
	traceStartup("preflight")
	if validated.UsedFallback {
		selected = validated.Package
		prepared = validated.Prepared
		env = setEnv(env, "AFTERBURNER_BASE_PACKAGE", selected.Path)
		env = setEnv(env, "AFTERBURNER_BASE_APP_SHA256", selected.AppSHA256)
		env = setEnv(env, "AFTERBURNER_BASE_RUNTIME_SHA256", selected.RuntimeSHA256)
		env = setEnv(env, "AFTERBURNER_COMPATIBILITY_PROFILE", validated.Profile.ID)
	}
	childArgs := append([]string{"--prefer-version", prepared.Version}, args...)
	telemetry.Record(layout.Root, "launch.started", map[string]any{
		"copilotVersion": selected.Version,
		"profile":        validated.Profile.ID,
		"runtimeVersion": prepared.Version,
		"argumentCount":  len(args),
		"safeMode":       launchOptions.safeMode,
		"fallback":       validated.UsedFallback,
	})
	traceStartup("telemetry")
	started := time.Now()
	exitCode, launchErr := launch.Run(ctx, launch.Options{
		Executable:        executable,
		Args:              childArgs,
		Env:               env,
		Stdin:             opts.Stdin,
		Stdout:            opts.Stdout,
		Stderr:            opts.Stderr,
		ExtensionRegistry: &effectiveRegistry,
	})
	attributes := map[string]any{
		"exitCode":             exitCode,
		"durationMillis":       time.Since(started).Milliseconds(),
		"copilotVersion":       selected.Version,
		"compatibilityProfile": validated.Profile.ID,
	}
	if launchErr != nil {
		attributes["status"] = "failed"
	} else {
		attributes["status"] = "completed"
	}
	telemetry.Record(layout.Root, "launch.completed", attributes)
	return exitCode, launchErr
}

type nativeLaunchOptions struct {
	safeMode           bool
	disabledExtensions []string
}

func extractLaunchOptions(args []string) (nativeLaunchOptions, []string, error) {
	var options nativeLaunchOptions
	forwarded := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--safe-mode":
			options.safeMode = true
		case args[i] == "--disable-extension":
			if i+1 >= len(args) || args[i+1] == "" {
				return nativeLaunchOptions{}, nil, fmt.Errorf("--disable-extension requires an ID")
			}
			i++
			options.disabledExtensions = append(options.disabledExtensions, args[i])
		case strings.HasPrefix(args[i], "--disable-extension="):
			id := strings.TrimPrefix(args[i], "--disable-extension=")
			if id == "" {
				return nativeLaunchOptions{}, nil, fmt.Errorf("--disable-extension requires an ID")
			}
			options.disabledExtensions = append(options.disabledExtensions, id)
		default:
			forwarded = append(forwarded, args[i])
		}
	}
	return options, forwarded, nil
}

func runDoctor(args []string, opts Options) (int, error) {
	if len(args) == 1 && args[0] == "--bundle" {
		path, err := doctor.Bundle(opts.Version)
		if err != nil {
			return 1, err
		}
		fmt.Fprintf(opts.Stdout, "Diagnostic bundle created at %s\n", path)
		return 0, nil
	}
	asJSON := len(args) == 1 && args[0] == "--json"
	if len(args) > 0 && !asJSON {
		return 2, fmt.Errorf("usage: afterburn doctor [--json|--bundle]")
	}
	report := doctor.Inspect(opts.Version)
	if err := doctor.Write(report, opts.Stdout, asJSON); err != nil {
		return 1, err
	}
	if !report.Healthy {
		return 1, nil
	}
	return 0, nil
}

func runRepair(args []string, opts Options) (int, error) {
	if len(args) != 0 {
		return 2, fmt.Errorf("usage: afterburn repair")
	}
	result, err := doctor.Repair()
	if err != nil {
		return 1, err
	}
	for _, action := range result.Actions {
		fmt.Fprintf(opts.Stdout, "OK  %s\n", action)
	}
	return 0, nil
}

func runCompatibility(args []string, opts Options) (int, error) {
	if len(args) != 1 || (args[0] != "list" && args[0] != "status" && args[0] != "retry") {
		return 2, fmt.Errorf("usage: afterburn compatibility list|status|retry")
	}
	switch args[0] {
	case "list":
		for _, profile := range compatibility.Profiles() {
			fmt.Fprintf(opts.Stdout, "%s %s %s\n", profile.ID, profile.Version, profile.BYOModelsTransform)
		}
	case "status":
		report := doctor.Inspect(opts.Version)
		if report.SelectedPackage == nil || report.CompatibilityProfile == nil {
			return 1, fmt.Errorf("no compatible Copilot package is selected")
		}
		fmt.Fprintf(opts.Stdout, "%s -> %s\n", report.SelectedPackage.Version, report.CompatibilityProfile.ID)
	case "retry":
		layout, err := home.Resolve()
		if err != nil {
			return 1, err
		}
		path := filepath.Join(layout.Root, "state", "last-known-good.json")
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return 1, err
		}
		fmt.Fprintln(opts.Stdout, "Compatibility preflight will run on the next launch.")
	}
	return 0, nil
}

func runUICommand(ctx context.Context, args []string, opts Options) (int, error) {
	if len(args) == 0 {
		return 2, fmt.Errorf("usage: afterburn ui doctor|catalog|inspect|trace|validate-manifest|render-fixture|simulate|certify|grant|revoke|policy")
	}
	subcommand := args[0]
	args = args[1:]
	switch subcommand {
	case "doctor":
		if len(args) != 0 && !(len(args) == 1 && args[0] == "--json") {
			return 2, fmt.Errorf("usage: afterburn ui doctor [--json]")
		}
		layout, err := home.Initialize()
		if err != nil {
			return 1, err
		}
		report := tooling.Doctor(layout.Root)
		if len(args) == 1 {
			return writeToolingJSON(opts.Stdout, report)
		}
		io.WriteString(opts.Stdout, tooling.HumanReport(report))
		return statusExit(report), nil
	case "catalog":
		if len(args) != 0 && !(len(args) == 1 && args[0] == "--json") {
			return 2, fmt.Errorf("usage: afterburn ui catalog [--json]")
		}
		catalog := tooling.Catalog()
		if len(args) == 1 {
			return writeToolingJSON(opts.Stdout, catalog)
		}
		fmt.Fprint(opts.Stdout, tooling.FormatCatalog(catalog))
		return 0, nil
	case "inspect":
		if len(args) != 1 && !(len(args) == 2 && args[0] == "--json") {
			return 2, fmt.Errorf("usage: afterburn ui inspect [--json] <extension>")
		}
		asJSON := len(args) == 2 && args[0] == "--json"
		extensionID := args[len(args)-1]
		layout, err := home.Initialize()
		if err != nil {
			return 1, err
		}
		report, brief, err := tooling.InspectInstalled(layout.Root, extensionID)
		if err != nil {
			return 1, err
		}
		if asJSON {
			return writeToolingJSON(opts.Stdout, struct {
				Report   tooling.Report         `json:"report"`
				Manifest *tooling.ManifestBrief `json:"manifest,omitempty"`
			}{report, brief})
		}
		if brief != nil {
			fmt.Fprintf(opts.Stdout, "%s (%s) enabled=%t\n", brief.DisplayName, brief.ID, brief.Enabled)
		}
		io.WriteString(opts.Stdout, tooling.HumanReport(report))
		return statusExit(report), nil
	case "trace":
		extensionID, redacted, asJSON, err := parseTraceArgs(args)
		if err != nil {
			return 2, err
		}
		layout, err := home.Initialize()
		if err != nil {
			return 1, err
		}
		view, err := tooling.Trace(tooling.TraceOptions{HomeRoot: layout.Root, ExtensionID: extensionID, Redacted: redacted})
		if err != nil {
			return 1, err
		}
		if asJSON {
			return writeToolingJSON(opts.Stdout, view)
		}
		io.WriteString(opts.Stdout, tooling.FormatTrace(view))
		return 0, nil
	case "validate-manifest":
		if len(args) != 1 && !(len(args) == 2 && args[0] == "--json") {
			return 2, fmt.Errorf("usage: afterburn ui validate-manifest [--json] <path>")
		}
		asJSON := len(args) == 2 && args[0] == "--json"
		result := tooling.ValidateManifest(args[len(args)-1])
		if asJSON {
			code, err := writeToolingJSON(opts.Stdout, result)
			if err != nil || !result.Valid {
				return 1, err
			}
			return code, nil
		}
		if result.Valid {
			fmt.Fprintf(opts.Stdout, "OK  %s\n", result.Path)
			return 0, nil
		}
		for _, message := range result.Errors {
			fmt.Fprintf(opts.Stdout, "ERR %s\n", message)
		}
		return 1, nil
	case "render-fixture":
		fixture, renderOpts, asJSON, err := parseRenderArgs(args)
		if err != nil {
			return 2, err
		}
		result, err := tooling.RenderFixture(ctx, fixture, renderOpts)
		if err != nil {
			return 1, err
		}
		if asJSON {
			return writeToolingJSON(opts.Stdout, result)
		}
		fmt.Fprintln(opts.Stdout, result.Frame.Plain)
		return 0, nil
	case "simulate":
		simOpts, asJSON, err := parseSimulateArgs(args)
		if err != nil {
			return 2, err
		}
		layout, err := home.Initialize()
		if err != nil {
			return 1, err
		}
		simOpts.HomeRoot = layout.Root
		result, err := tooling.Simulate(ctx, simOpts)
		if err != nil {
			return 1, err
		}
		if asJSON {
			return writeToolingJSON(opts.Stdout, result)
		}
		io.WriteString(opts.Stdout, tooling.HumanReport(result.Report))
		return statusExit(result.Report), nil
	case "certify", "certification":
		certOpts, asJSON, err := parseCertifyArgs(args)
		if err != nil {
			return 2, err
		}
		layout, err := home.Initialize()
		if err != nil {
			return 1, err
		}
		certOpts.HomeRoot = layout.Root
		result, err := tooling.Certify(ctx, certOpts)
		if err != nil {
			return 1, err
		}
		if asJSON {
			return writeToolingJSON(opts.Stdout, result)
		}
		io.WriteString(opts.Stdout, result.Human)
		return statusExit(result.Report), nil
	case "grant":
		if len(args) < 2 || len(args) > 3 {
			return 2, fmt.Errorf("usage: afterburn ui grant <extension> <capability> [resource]")
		}
		layout, err := home.Initialize()
		if err != nil {
			return 1, err
		}
		resource := "*"
		if len(args) == 3 {
			resource = args[2]
		}
		record, err := tooling.Grant(layout.Root, args[0], args[1], resource, "")
		if err != nil {
			return 1, err
		}
		fmt.Fprintf(opts.Stdout, "Granted %s to %s as %s\n", record.Capability, record.ExtensionID, record.ID)
		return 0, nil
	case "revoke":
		if len(args) != 2 {
			return 2, fmt.Errorf("usage: afterburn ui revoke <extension> <grant-id>")
		}
		layout, err := home.Initialize()
		if err != nil {
			return 1, err
		}
		record, ok, err := tooling.Revoke(layout.Root, args[0], args[1], "")
		if err != nil {
			return 1, err
		}
		if !ok {
			return 1, fmt.Errorf("grant %q for extension %q was not found", args[1], args[0])
		}
		fmt.Fprintf(opts.Stdout, "Revoked %s for %s\n", record.ID, record.ExtensionID)
		return 0, nil
	case "policy":
		if len(args) != 1 && !(len(args) == 2 && args[0] == "set") {
			return 2, fmt.Errorf("usage: afterburn ui policy show|set <path>")
		}
		layout, err := home.Initialize()
		if err != nil {
			return 1, err
		}
		if args[0] == "show" {
			policy, err := tooling.ShowEnterprisePolicy(layout.Root)
			if err != nil {
				return 1, err
			}
			return writeToolingJSON(opts.Stdout, policy)
		}
		if args[0] == "set" {
			policy, err := tooling.SetEnterprisePolicy(layout.Root, args[1])
			if err != nil {
				return 1, err
			}
			fmt.Fprintf(opts.Stdout, "Installed UI enterprise policy %s\n", policy.Version)
			return 0, nil
		}
		return 2, fmt.Errorf("usage: afterburn ui policy show|set <path>")
	default:
		return 2, fmt.Errorf("native ui subcommand %q is not implemented yet", subcommand)
	}
}

func parseTraceArgs(args []string) (string, bool, bool, error) {
	var extensionID string
	redacted := false
	asJSON := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--extension":
			if i+1 >= len(args) {
				return "", false, false, fmt.Errorf("usage: afterburn ui trace --extension <id> [--redacted] [--json]")
			}
			i++
			extensionID = args[i]
		case "--redacted":
			redacted = true
		case "--json":
			asJSON = true
		default:
			return "", false, false, fmt.Errorf("usage: afterburn ui trace --extension <id> [--redacted] [--json]")
		}
	}
	if extensionID == "" {
		return "", false, false, fmt.Errorf("usage: afterburn ui trace --extension <id> [--redacted] [--json]")
	}
	return extensionID, redacted, asJSON, nil
}

func parseRenderArgs(args []string) (string, tooling.RenderOptions, bool, error) {
	renderOpts := tooling.RenderOptions{Width: 96, Height: 40, ColorMode: render.ColorModeMono, Plain: true, Unicode: false}
	asJSON := false
	var fixture string
	usage := "usage: afterburn ui render-fixture [--json] [--width <columns>] [--height <rows>] [--theme <id>] [--color mono|16|256|truecolor|high-contrast] [--unicode] <fixture>"
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			asJSON = true
		case "--unicode":
			renderOpts.Unicode = true
		case "--width":
			if i+1 >= len(args) {
				return "", renderOpts, false, errors.New(usage)
			}
			i++
			width, err := strconv.Atoi(args[i])
			if err != nil || width <= 0 {
				return "", renderOpts, false, fmt.Errorf("--width must be a positive integer")
			}
			renderOpts.Width = width
		case "--height":
			if i+1 >= len(args) {
				return "", renderOpts, false, errors.New(usage)
			}
			i++
			height, err := strconv.Atoi(args[i])
			if err != nil || height <= 0 {
				return "", renderOpts, false, fmt.Errorf("--height must be a positive integer")
			}
			renderOpts.Height = height
		case "--theme":
			if i+1 >= len(args) {
				return "", renderOpts, false, errors.New(usage)
			}
			i++
			renderOpts.Theme = args[i]
		case "--color":
			if i+1 >= len(args) {
				return "", renderOpts, false, errors.New(usage)
			}
			i++
			switch render.ColorMode(args[i]) {
			case render.ColorModeMono, render.ColorModeANSI16, render.ColorModeANSI256, render.ColorModeTrueColor, render.ColorModeHighContrast:
				renderOpts.ColorMode = render.ColorMode(args[i])
				renderOpts.Plain = renderOpts.ColorMode == render.ColorModeMono
			default:
				return "", renderOpts, false, fmt.Errorf("--color must be one of mono, 16, 256, truecolor, high-contrast")
			}
		default:
			if fixture == "" {
				fixture = args[i]
			} else {
				return "", renderOpts, false, errors.New(usage)
			}
		}
	}
	if fixture == "" {
		return "", renderOpts, false, errors.New(usage)
	}
	return fixture, renderOpts, asJSON, nil
}

func parseSimulateArgs(args []string) (tooling.SimulationOptions, bool, error) {
	var opts tooling.SimulationOptions
	asJSON := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--extension":
			if i+1 >= len(args) {
				return opts, false, fmt.Errorf("usage: afterburn ui simulate --extension <id> --surface <surface> [--json]")
			}
			i++
			opts.ExtensionID = args[i]
		case "--surface":
			if i+1 >= len(args) {
				return opts, false, fmt.Errorf("usage: afterburn ui simulate --extension <id> --surface <surface> [--json]")
			}
			i++
			opts.SurfaceID = args[i]
		case "--json":
			asJSON = true
		default:
			return opts, false, fmt.Errorf("usage: afterburn ui simulate --extension <id> --surface <surface> [--json]")
		}
	}
	if opts.ExtensionID == "" || opts.SurfaceID == "" {
		return opts, false, fmt.Errorf("usage: afterburn ui simulate --extension <id> --surface <surface> [--json]")
	}
	return opts, asJSON, nil
}

func parseCertifyArgs(args []string) (tooling.CertificationOptions, bool, error) {
	var opts tooling.CertificationOptions
	asJSON := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--extension":
			if i+1 >= len(args) {
				return opts, false, fmt.Errorf("usage: afterburn ui certify --extension <id> [--surface <surface>] [--json]")
			}
			i++
			opts.ExtensionID = args[i]
		case "--surface":
			if i+1 >= len(args) {
				return opts, false, fmt.Errorf("usage: afterburn ui certify --extension <id> [--surface <surface>] [--json]")
			}
			i++
			opts.SurfaceID = args[i]
		case "--json":
			asJSON = true
		default:
			return opts, false, fmt.Errorf("usage: afterburn ui certify --extension <id> [--surface <surface>] [--json]")
		}
	}
	if opts.ExtensionID == "" {
		return opts, false, fmt.Errorf("usage: afterburn ui certify --extension <id> [--surface <surface>] [--json]")
	}
	return opts, asJSON, nil
}

func writeToolingJSON(writer io.Writer, value any) (int, error) {
	data, err := tooling.MarshalDeterministic(value)
	if err != nil {
		return 1, err
	}
	_, err = writer.Write(data)
	if err != nil {
		return 1, err
	}
	return 0, nil
}

func statusExit(report tooling.Report) int {
	if report.Summary.Status == tooling.StatusFail {
		return 1
	}
	return 0
}

func setEnv(env []string, key, value string) []string {
	prefix := strings.ToUpper(key) + "="
	result := make([]string, 0, len(env)+1)
	for _, item := range env {
		if !strings.HasPrefix(strings.ToUpper(item), prefix) {
			result = append(result, item)
		}
	}
	return append(result, key+"="+value)
}

func clone(values []string) []string {
	return append([]string(nil), values...)
}

const help = `Afterburn - GitHub Copilot CLI with trusted Afterburner extensions

Usage:
  afterburn
  afterburn [copilot arguments]
  afterburn run <copilot arguments>
  afterburn -- <copilot arguments>
  afterburn install [id...]
  afterburn enable <id...>
  afterburn disable <id...>
  afterburn uninstall <id...>
  afterburn extension <command>
  afterburn core install
  afterburn ui doctor [--json]
  afterburn ui catalog [--json]
  afterburn ui inspect [--json] <extension>
  afterburn ui trace --extension <id> --redacted [--json]
  afterburn ui validate-manifest [--json] <path>
  afterburn ui render-fixture [--json] [--width <columns>] [--height <rows>] [--theme <id>] [--color <mode>] [--unicode] <fixture>
  afterburn ui simulate --extension <id> --surface <surface> [--json]
  afterburn ui certify --extension <id> [--surface <surface>] [--json]
  afterburn ui grant <extension> <capability> [resource]
  afterburn ui revoke <extension> <grant-id>
  afterburn ui policy show|set <path>
  afterburn doctor
  afterburn repair
  afterburn update [--check|--version <version>]
  afterburn rollback core
  afterburn version
`
