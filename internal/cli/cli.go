package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
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
	"github.com/nbaertsch/afterburner/internal/terminal"
	"github.com/nbaertsch/afterburner/internal/updater"
)

var reserved = map[string]struct{}{
	"install": {}, "enable": {}, "disable": {}, "uninstall": {},
	"extension": {}, "doctor": {}, "repair": {}, "update": {},
	"rollback": {}, "version": {}, "help": {}, "run": {},
	"compatibility": {},
	"core":          {},
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
	if !(route.Command == "core" && len(route.Args) > 0 && route.Args[0] == "replace") {
		if layout, resolveErr := home.Resolve(); resolveErr == nil {
			recovered, recoverErr := updater.RecoverInterruptedCoreUpdate(layout.Root)
			if recoverErr != nil {
				return 1, recoverErr
			}
			if recovered && opts.Stderr != nil {
				fmt.Fprintln(opts.Stderr, "Recovered an interrupted core update registry transaction.")
			}
		}
	}
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
	currentRegistrySnapshot, err := updater.SnapshotRegistry(layout.Root)
	if err != nil {
		_ = os.RemoveAll(filepath.Dir(candidate))
		return 1, err
	}
	rollbackRegistrySnapshot := updater.CoreRollbackRegistrySnapshot(layout.Root)
	if _, err := os.Stat(rollbackRegistrySnapshot); err != nil {
		_ = updater.DiscardRegistrySnapshot(layout.Root, currentRegistrySnapshot)
		_ = os.RemoveAll(filepath.Dir(candidate))
		if os.IsNotExist(err) {
			return 1, fmt.Errorf("no matching extension registry is available for core rollback")
		}
		return 1, fmt.Errorf("inspect core rollback registry snapshot: %w", err)
	}
	if err := updater.BeginCoreUpdateTransaction(layout.Root, candidate, target, previous, currentRegistrySnapshot); err != nil {
		_ = updater.DiscardRegistrySnapshot(layout.Root, currentRegistrySnapshot)
		_ = os.RemoveAll(filepath.Dir(candidate))
		return 1, err
	}
	if err := updater.SetCoreUpdateSuccessSnapshot(layout.Root, rollbackRegistrySnapshot); err != nil {
		_ = updater.AbortCoreUpdateTransaction(layout.Root)
		_ = os.RemoveAll(filepath.Dir(candidate))
		return 1, err
	}
	if err := updater.BeginReplacement(candidate, target, previous, currentRegistrySnapshot, rollbackRegistrySnapshot); err != nil {
		_ = updater.AbortCoreUpdateTransaction(layout.Root)
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
	manager := extensions.Manager{Layout: layout, Stdout: opts.Stdout, CoreVersion: release.TagName, BuiltinFetcher: updater.BuiltinReleaseFetcher{
		Client:  client,
		Root:    layout.Root,
		Version: release.TagName,
	}}
	registrySnapshot, err := updater.SnapshotRegistry(layout.Root)
	if err != nil {
		_ = os.RemoveAll(filepath.Dir(candidate))
		return 1, err
	}
	if err := updater.BeginCoreUpdateTransaction(layout.Root, candidate, target, previous, registrySnapshot); err != nil {
		_ = updater.DiscardRegistrySnapshot(layout.Root, registrySnapshot)
		_ = os.RemoveAll(filepath.Dir(candidate))
		return 1, err
	}
	if err := manager.SyncBuiltins(nil); err != nil {
		_ = updater.AbortCoreUpdateTransaction(layout.Root)
		_ = os.RemoveAll(filepath.Dir(candidate))
		return 1, fmt.Errorf("sync built-in extensions for %s: %w", release.TagName, err)
	}
	successRegistrySnapshot, err := updater.SnapshotRegistry(layout.Root)
	if err != nil {
		_ = updater.AbortCoreUpdateTransaction(layout.Root)
		_ = os.RemoveAll(filepath.Dir(candidate))
		return 1, err
	}
	if err := updater.SetCoreUpdateSuccessSnapshot(layout.Root, successRegistrySnapshot); err != nil {
		_ = updater.DiscardRegistrySnapshot(layout.Root, successRegistrySnapshot)
		_ = updater.AbortCoreUpdateTransaction(layout.Root)
		_ = os.RemoveAll(filepath.Dir(candidate))
		return 1, err
	}
	if err := updater.RestoreRegistrySnapshot(layout.Root, registrySnapshot); err != nil {
		_ = updater.AbortCoreUpdateTransaction(layout.Root)
		_ = os.RemoveAll(filepath.Dir(candidate))
		return 1, err
	}
	if err := updater.BeginReplacement(candidate, target, previous, registrySnapshot, successRegistrySnapshot); err != nil {
		_ = updater.AbortCoreUpdateTransaction(layout.Root)
		_ = os.RemoveAll(filepath.Dir(candidate))
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
	registrySnapshot, successRegistrySnapshot, replaceCommand := replacementRegistrySnapshots(args)
	if replaceCommand {
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
			!registry.Within(args[4], filepath.Join(layout.Root, "update-staging")) ||
			(registrySnapshot != "" && !registry.Within(registrySnapshot, filepath.Join(layout.Root, "state"))) ||
			(successRegistrySnapshot != "" && !registry.Within(successRegistrySnapshot, filepath.Join(layout.Root, "state"))) {
			return 2, fmt.Errorf("replacement paths are outside the managed update transaction")
		}
		if err := updater.ApplyReplacement(parent, args[4], args[6], args[8], registrySnapshot, successRegistrySnapshot); err != nil {
			return 1, err
		}
		return 0, nil
	}
	return 2, fmt.Errorf("usage: afterburn core install")
}

func replacementRegistrySnapshots(args []string) (string, string, bool) {
	if len(args) < 9 || args[0] != "replace" ||
		args[1] != "--parent" || args[3] != "--source" ||
		args[5] != "--target" || args[7] != "--previous" {
		return "", "", false
	}
	if len(args) == 9 {
		return "", "", true
	}
	if len(args) == 11 && args[9] == "--registry-snapshot" {
		return args[10], "", true
	}
	if len(args) == 13 && args[9] == "--registry-snapshot" && args[11] == "--success-registry-snapshot" {
		return args[10], args[12], true
	}
	return "", "", false
}

func runExtensionCommand(route Route, opts Options) (int, error) {
	layout, err := home.Initialize()
	if err != nil {
		return 1, err
	}
	warnCoreUpdateStatusForLayout(layout, opts)
	manager := extensions.Manager{Layout: layout, Stdout: opts.Stdout, CoreVersion: opts.Version}
	if commandNeedsCopilotCompatibility(route) {
		executable, findErr := launch.FindCopilot()
		if findErr == nil {
			inventory, discoverErr := copilot.Discover(copilot.DiscoveryOptions{
				ManagedHome:       layout.CopilotHome,
				CopilotExecutable: executable,
				HashCachePath:     filepath.Join(layout.Root, "state", "package-hashes.json"),
			})
			if discoverErr != nil {
				return 1, discoverErr
			}
			selection, selectErr := compatibility.Select(inventory)
			if selectErr != nil {
				return 1, selectErr
			}
			manager.CopilotVersion = selection.Package.Version
		}
	}
	manager.BuiltinFetcher = updater.BuiltinReleaseFetcher{
		Client: updater.NewClient(context.Background()),
		Root:   layout.Root,
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
				return 2, fmt.Errorf("usage: afterburn extension install <path|archive.zip|git-url|owner/repo[@ref]>")
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
		case "validate":
			if len(route.Args) != 2 {
				return 2, fmt.Errorf("usage: afterburn extension validate <path>")
			}
			manifest, validateErr := extensions.ValidatePackage(route.Args[1])
			if validateErr != nil {
				err = validateErr
				break
			}
			surfaceCount := 0
			if manifest.UI != nil {
				surfaceCount = len(manifest.UI.Surfaces)
			}
			fmt.Fprintf(opts.Stdout, "Valid extension %s with %d native UI surface(s).\n", manifest.ID, surfaceCount)
		case "preview":
			if len(route.Args) < 2 || len(route.Args) > 3 {
				return 2, fmt.Errorf("usage: afterburn extension preview <document.json> [width]")
			}
			width := 100
			if len(route.Args) == 3 {
				width, err = strconv.Atoi(route.Args[2])
				if err != nil || width < 20 || width > 500 {
					return 2, fmt.Errorf("preview width must be between 20 and 500")
				}
			}
			document, readErr := os.ReadFile(route.Args[1])
			if readErr != nil {
				err = readErr
				break
			}
			lines, previewErr := terminal.RenderUIDocumentPreview(document, width)
			if previewErr != nil {
				err = previewErr
				break
			}
			fmt.Fprintln(opts.Stdout, strings.Join(lines, "\n"))
		case "pack":
			if len(route.Args) != 3 {
				return 2, fmt.Errorf("usage: afterburn extension pack <path> <archive.zip>")
			}
			err = extensions.PackPackage(route.Args[1], route.Args[2])
			if err == nil {
				fmt.Fprintf(opts.Stdout, "Packed extension to %s\n", route.Args[2])
			}
		default:
			return 2, fmt.Errorf("native extension subcommand %q is not implemented yet", route.Args[0])
		}
	}
	if err != nil {
		return 1, err
	}
	return 0, nil
}

func commandNeedsCopilotCompatibility(route Route) bool {
	if route.Command == "install" || route.Command == "enable" {
		return true
	}
	if route.Command != "extension" || len(route.Args) == 0 {
		return false
	}
	switch route.Args[0] {
	case "install", "enable", "update", "rollback":
		return true
	default:
		return false
	}
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
	if !launchOptions.safeMode && opts.Version != "" && opts.Version != "dev" {
		repairBuiltins := builtinRepairIDs(extensionRegistry)
		if len(repairBuiltins) > 0 {
			manager := extensions.Manager{
				Layout:      baseLayout,
				Stdout:      opts.Stdout,
				CoreVersion: opts.Version,
				BuiltinFetcher: updater.BuiltinReleaseFetcher{
					Client:  updater.NewClient(ctx),
					Root:    baseLayout.Root,
					Version: opts.Version,
				},
			}
			if err := manager.SyncBuiltins(repairBuiltins); err != nil {
				return 1, fmt.Errorf("repair legacy built-in package authorization: %w", err)
			}
			extensionRegistry, err = registry.Load(baseLayout.Root)
			if err != nil {
				return 1, err
			}
		}
	}
	traceStartup("registry")
	effectiveRegistry := extensionRegistry
	disabledExtensionsForEnv := append([]string(nil), launchOptions.disabledExtensions...)
	if launchOptions.safeMode {
		for id, entry := range effectiveRegistry.Extensions {
			entry.Enabled = false
			effectiveRegistry.Extensions[id] = entry
		}
	} else {
		disabledExtensionsForEnv = disabledExtensionsForRuntime(launchOptions.disabledExtensions)
		for _, id := range launchOptions.disabledExtensions {
			canonical := canonicalLaunchExtensionID(id)
			key := canonical
			entry, ok := effectiveRegistry.Extensions[key]
			if !ok && canonical == registry.OpenAIServerID {
				key = registry.LegacyOpenAIServerID
				entry, ok = effectiveRegistry.Extensions[key]
			}
			if !ok {
				return 2, fmt.Errorf("unknown extension %q", id)
			}
			entry.Enabled = false
			effectiveRegistry.Extensions[key] = entry
		}
	}
	for id, entry := range effectiveRegistry.Extensions {
		if !entry.Enabled || entry.Verified {
			continue
		}
		entry.Enabled = false
		effectiveRegistry.Extensions[id] = entry
		disabledExtensionsForEnv = append(disabledExtensionsForEnv, id)
		if opts.Stderr != nil {
			fmt.Fprintf(opts.Stderr, "Warning: disabled unverified extension %q; update or reinstall it before use.\n", id)
		}
	}
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
	compatibleInventory := inventory[:0]
	var extensionCompatibilityErr error
	for _, candidate := range inventory {
		if err := validateEnabledExtensions(effectiveRegistry, opts.Version, candidate.Version); err != nil {
			if extensionCompatibilityErr == nil {
				extensionCompatibilityErr = err
			}
			continue
		}
		compatibleInventory = append(compatibleInventory, candidate)
	}
	if len(compatibleInventory) == 0 && extensionCompatibilityErr != nil {
		return 1, extensionCompatibilityErr
	}
	selection, err := compatibility.Select(compatibleInventory)
	if err != nil {
		return 1, err
	}
	if err := sessions.Reconcile(layout, effectiveRegistry); err != nil {
		return 1, err
	}
	traceStartup("sessions")
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
	} else if len(disabledExtensionsForEnv) > 0 {
		env = setEnv(env, "AFTERBURNER_DISABLED_EXTENSIONS", strings.Join(disabledExtensionsForEnv, ","))
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
	if err := validateEnabledExtensions(effectiveRegistry, opts.Version, selected.Version); err != nil {
		return 1, err
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

func builtinRepairIDs(value registry.Registry) []string {
	var ids []string
	for id, entry := range value.Extensions {
		if registry.IsReservedBuiltinID(id) && entry.Manifest.Visibility == "builtin" && !entry.Verified {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

func validateEnabledExtensions(value registry.Registry, coreVersion, copilotVersion string) error {
	for id, entry := range value.Extensions {
		if !entry.Enabled {
			continue
		}
		if err := extensions.ValidateCompatibility(entry.Manifest, coreVersion, copilotVersion); err != nil {
			return fmt.Errorf("enabled extension %q is incompatible: %w", id, err)
		}
	}
	return nil
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

func canonicalLaunchExtensionID(id string) string {
	if id == registry.LegacyOpenAIServerID {
		return registry.OpenAIServerID
	}
	return id
}

func disabledExtensionsForRuntime(ids []string) []string {
	seen := map[string]bool{}
	var result []string
	for _, id := range ids {
		canonical := canonicalLaunchExtensionID(id)
		if canonical == registry.OpenAIServerID {
			for _, alias := range []string{registry.OpenAIServerID, registry.LegacyOpenAIServerID} {
				if !seen[alias] {
					seen[alias] = true
					result = append(result, alias)
				}
			}
			continue
		}
		if !seen[canonical] {
			seen[canonical] = true
			result = append(result, canonical)
		}
	}
	return result
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
  afterburn extension validate <path>
  afterburn extension preview <document.json> [width]
  afterburn extension pack <path> <archive.zip>
  afterburn core install
  afterburn doctor
  afterburn repair
  afterburn update [--check|--version <version>]
  afterburn rollback core
  afterburn version
`
