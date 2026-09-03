package runtimepkg

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/nbaertsch/afterburner/internal/copilot"
	"github.com/nbaertsch/afterburner/internal/home"
	"github.com/nbaertsch/afterburner/internal/platform"
)

//go:embed app.js
var runtimeHost []byte

type Prepared struct {
	Version string
	Path    string
}

func Prepare(layout home.Layout, base copilot.Package) (Prepared, error) {
	platformName := "win32-" + mapArch(runtime.GOARCH)
	sum := sha256.Sum256(append(append([]byte{}, runtimeHost...), []byte(base.AppSHA256+base.RuntimeSHA256)...))
	version := "9999.0.0-afterburner-" + hex.EncodeToString(sum[:6])
	root := filepath.Join(layout.CopilotHome, "pkg", platformName)
	target := filepath.Join(root, version)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return Prepared{}, fmt.Errorf("create runtime package root: %w", err)
	}
	release, err := platform.AcquireDirectoryLock(filepath.Join(root, ".afterburner-prepare.lock"), 30*time.Second)
	if err != nil {
		return Prepared{}, err
	}
	defer release()
	if validPreparedPackage(target, version, base) {
		return Prepared{Version: version, Path: target}, nil
	}
	if err := os.RemoveAll(target); err != nil {
		return Prepared{}, fmt.Errorf("remove stale runtime package: %w", err)
	}
	staging, err := os.MkdirTemp(root, "."+version+".staging-")
	if err != nil {
		return Prepared{}, fmt.Errorf("create runtime staging directory: %w", err)
	}
	defer os.RemoveAll(staging)
	if err := os.WriteFile(filepath.Join(staging, "app.js"), runtimeHost, 0o600); err != nil {
		return Prepared{}, fmt.Errorf("write runtime host: %w", err)
	}
	metadata := map[string]any{
		"name":    "copilot-afterburner-runtime-host",
		"version": version,
		"private": true,
		"type":    "module",
		"afterburner": map[string]string{
			"basePath":          base.Path,
			"baseAppSHA256":     base.AppSHA256,
			"baseRuntimeSHA256": base.RuntimeSHA256,
		},
	}
	encoded, _ := json.MarshalIndent(metadata, "", "  ")
	encoded = append(encoded, '\n')
	if err := os.WriteFile(filepath.Join(staging, "package.json"), encoded, 0o600); err != nil {
		return Prepared{}, fmt.Errorf("write runtime package metadata: %w", err)
	}
	if err := os.Rename(staging, target); err != nil {
		if _, statErr := os.Stat(target); statErr == nil {
			return Prepared{Version: version, Path: target}, nil
		}
		return Prepared{}, fmt.Errorf("activate runtime package: %w", err)
	}
	return Prepared{Version: version, Path: target}, nil
}

func validPreparedPackage(target, version string, base copilot.Package) bool {
	data, err := os.ReadFile(filepath.Join(target, "app.js"))
	if err != nil {
		return false
	}
	existing := sha256.Sum256(data)
	host := sha256.Sum256(runtimeHost)
	if existing != host {
		return false
	}
	metadataData, err := os.ReadFile(filepath.Join(target, "package.json"))
	if err != nil {
		return false
	}
	var metadata struct {
		Version     string `json:"version"`
		Afterburner struct {
			BasePath          string `json:"basePath"`
			BaseAppSHA256     string `json:"baseAppSHA256"`
			BaseRuntimeSHA256 string `json:"baseRuntimeSHA256"`
		} `json:"afterburner"`
	}
	if json.Unmarshal(metadataData, &metadata) != nil {
		return false
	}
	return metadata.Version == version &&
		strings.EqualFold(filepath.Clean(metadata.Afterburner.BasePath), filepath.Clean(base.Path)) &&
		strings.EqualFold(metadata.Afterburner.BaseAppSHA256, base.AppSHA256) &&
		strings.EqualFold(metadata.Afterburner.BaseRuntimeSHA256, base.RuntimeSHA256)
}

func mapArch(arch string) string {
	if arch == "amd64" {
		return "x64"
	}
	return arch
}
