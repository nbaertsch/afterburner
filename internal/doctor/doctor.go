package doctor

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/nbaertsch/afterburner/internal/compatibility"
	"github.com/nbaertsch/afterburner/internal/copilot"
	"github.com/nbaertsch/afterburner/internal/home"
	"github.com/nbaertsch/afterburner/internal/launch"
	"github.com/nbaertsch/afterburner/internal/preflight"
	"github.com/nbaertsch/afterburner/internal/registry"
	"github.com/nbaertsch/afterburner/internal/sessions"
)

type Report struct {
	Healthy              bool                   `json:"healthy"`
	Version              string                 `json:"version"`
	AfterburnerHome      string                 `json:"afterburnerHome"`
	ManagedCopilotHome   string                 `json:"managedCopilotHome"`
	CopilotExecutable    string                 `json:"copilotExecutable,omitempty"`
	Packages             []copilot.Package      `json:"packages,omitempty"`
	SelectedPackage      *copilot.Package       `json:"selectedPackage,omitempty"`
	CompatibilityProfile *compatibility.Profile `json:"compatibilityProfile,omitempty"`
	InstalledExtensions  int                    `json:"installedExtensions"`
	EnabledExtensions    int                    `json:"enabledExtensions"`
	LastKnownGoodPresent bool                   `json:"lastKnownGoodPresent"`
	Errors               []string               `json:"errors,omitempty"`
}

type RepairResult struct {
	Actions []string
}

func Repair() (RepairResult, error) {
	layout, err := home.Initialize()
	if err != nil {
		return RepairResult{}, err
	}
	value, err := registry.Load(layout.Root)
	if err != nil {
		return RepairResult{}, err
	}
	if err := sessions.Reconcile(layout, value); err != nil {
		return RepairResult{}, err
	}
	result := RepairResult{Actions: []string{"validated managed home", "reconciled session extensions"}}
	state := filepath.Join(layout.Root, "state", "last-known-good.json")
	if err := preflight.ValidateStored(layout.Root); err != nil && !os.IsNotExist(err) {
		if removeErr := os.Remove(state); removeErr != nil && !os.IsNotExist(removeErr) {
			return RepairResult{}, fmt.Errorf("remove invalid last-known-good state: %w", removeErr)
		}
		result.Actions = append(result.Actions, "removed invalid last-known-good state")
	}
	for _, name := range []string{"launch-homes", "staging", "update-staging"} {
		removed, err := removeAbandoned(filepath.Join(layout.Root, name), 24*time.Hour)
		if err != nil {
			return RepairResult{}, err
		}
		if removed > 0 {
			result.Actions = append(result.Actions, fmt.Sprintf("removed %d abandoned %s directories", removed, name))
		}
	}
	return result, nil
}

func Bundle(version string) (string, error) {
	layout, err := home.Resolve()
	if err != nil {
		return "", err
	}
	directory := filepath.Join(layout.Root, "diagnostics")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(directory, "afterburn-doctor-"+time.Now().UTC().Format("20060102T150405Z")+".zip")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	writer := zip.NewWriter(file)
	fail := func(cause error) (string, error) {
		_ = writer.Close()
		_ = file.Close()
		_ = os.Remove(path)
		return "", cause
	}
	reportData, err := json.MarshalIndent(Inspect(version), "", "  ")
	if err != nil {
		return fail(err)
	}
	if err := writeBundleFile(writer, "doctor.json", append(reportData, '\n')); err != nil {
		return fail(err)
	}
	value, err := registry.Load(layout.Root)
	if err != nil {
		return fail(err)
	}
	type extensionSummary struct {
		ID         string `json:"id"`
		Version    string `json:"version"`
		Enabled    bool   `json:"enabled"`
		SourceType string `json:"sourceType"`
	}
	summaries := make([]extensionSummary, 0, len(value.Extensions))
	for id, entry := range value.Extensions {
		summaries = append(summaries, extensionSummary{
			ID: id, Version: entry.Source.Version, Enabled: entry.Enabled, SourceType: entry.Source.Type,
		})
	}
	registryData, err := json.MarshalIndent(summaries, "", "  ")
	if err != nil {
		return fail(err)
	}
	if err := writeBundleFile(writer, "extensions.json", append(registryData, '\n')); err != nil {
		return fail(err)
	}
	for _, name := range []string{"launcher.jsonl", "launcher.previous.jsonl", "core-update-status.json"} {
		data, readErr := os.ReadFile(filepath.Join(layout.Root, "state", name))
		if os.IsNotExist(readErr) {
			continue
		}
		if readErr != nil {
			return fail(readErr)
		}
		if len(data) > 6<<20 {
			return fail(fmt.Errorf("diagnostic state file %s exceeds the size limit", name))
		}
		if err := writeBundleFile(writer, name, data); err != nil {
			return fail(err)
		}
	}
	if err := writer.Close(); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return "", err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return "", err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

func writeBundleFile(writer *zip.Writer, name string, data []byte) error {
	entry, err := writer.Create(name)
	if err != nil {
		return err
	}
	_, err = entry.Write(data)
	return err
}

func removeAbandoned(root string, age time.Duration) (int, error) {
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	cutoff := time.Now().Add(-age)
	removed := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(root, entry.Name())
		info, err := entry.Info()
		if err != nil {
			return removed, err
		}
		if info.Mode()&os.ModeSymlink != 0 || info.ModTime().After(cutoff) || !registry.Within(path, root) {
			continue
		}
		if err := os.RemoveAll(path); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

func Inspect(version string) Report {
	layout, err := home.Resolve()
	report := Report{Healthy: true, Version: version}
	if err != nil {
		report.Healthy = false
		report.Errors = append(report.Errors, err.Error())
		return report
	}
	report.AfterburnerHome = layout.Root
	report.ManagedCopilotHome = layout.CopilotHome
	executable, err := launch.FindCopilot()
	if err != nil {
		report.Healthy = false
		report.Errors = append(report.Errors, err.Error())
	} else {
		report.CopilotExecutable = executable
		packages, discoverErr := copilot.Discover(copilot.DiscoveryOptions{
			ManagedHome: layout.CopilotHome, CopilotExecutable: executable,
		})
		if discoverErr != nil {
			report.Healthy = false
			report.Errors = append(report.Errors, discoverErr.Error())
		} else {
			report.Packages = packages
			if selected, selectErr := compatibility.Select(packages); selectErr == nil {
				report.SelectedPackage = &selected.Package
				report.CompatibilityProfile = &selected.Profile
			} else {
				report.Healthy = false
				report.Errors = append(report.Errors, selectErr.Error())
			}
		}
	}
	value, err := registry.Load(layout.Root)
	if err != nil {
		report.Healthy = false
		report.Errors = append(report.Errors, err.Error())
	} else {
		report.InstalledExtensions = len(value.Extensions)
		for _, entry := range value.Extensions {
			if entry.Enabled {
				report.EnabledExtensions++
			}
		}
	}
	if _, err := os.Stat(filepath.Join(layout.Root, "state", "last-known-good.json")); err == nil {
		report.LastKnownGoodPresent = true
	}
	return report
}

func Write(report Report, writer io.Writer, asJSON bool) error {
	if asJSON {
		encoder := json.NewEncoder(writer)
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	}
	status := "OK"
	if !report.Healthy {
		status = "FAIL"
	}
	fmt.Fprintf(writer, "%s Afterburner %s\n", status, report.Version)
	fmt.Fprintf(writer, "OK  managed home: %s\n", report.ManagedCopilotHome)
	if report.CopilotExecutable != "" {
		fmt.Fprintf(writer, "OK  copilot: %s\n", report.CopilotExecutable)
	}
	if report.SelectedPackage != nil && report.CompatibilityProfile != nil {
		fmt.Fprintf(writer, "OK  package: %s (%s)\n", report.SelectedPackage.Version, report.CompatibilityProfile.ID)
	}
	fmt.Fprintf(writer, "OK  extensions: %d installed, %d enabled\n", report.InstalledExtensions, report.EnabledExtensions)
	for _, message := range report.Errors {
		fmt.Fprintf(writer, "FAIL %s\n", message)
	}
	return nil
}
