package updater

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"debug/pe"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/nbaertsch/afterburner/internal/extensions"
	"github.com/nbaertsch/afterburner/internal/home"
	"github.com/nbaertsch/afterburner/internal/platform"
	"github.com/nbaertsch/afterburner/internal/releasesign"
)

const maxExecutableBytes = 256 << 20

type ManifestAsset struct {
	Name         string `json:"name"`
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
	SHA256       string `json:"sha256"`
	Size         int64  `json:"size"`
}

type Manifest struct {
	SchemaVersion int                   `json:"schemaVersion"`
	Repository    string                `json:"repository"`
	Version       string                `json:"version"`
	Commit        string                `json:"commit"`
	Assets        []ManifestAsset       `json:"assets"`
	Builtins      []ManifestBuiltin     `json:"builtins,omitempty"`
	Compatibility ManifestCompatibility `json:"compatibility,omitempty"`
}

type ManifestCompatibility struct {
	RuntimeDigest   string   `json:"runtimeDigest"`
	CopilotProfiles []string `json:"copilotProfiles"`
	BuiltinIDs      []string `json:"builtinIds"`
}

// ManifestBuiltin describes a signed built-in extension package published as
// an additional asset on the same GitHub release as the core binary. Unlike
// ManifestAsset (which is OS/architecture specific), builtin packages are
// pure JS and are not bound to a platform.
type ManifestBuiltin struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

func builtinManifestEntry(manifest Manifest, id string) (ManifestBuiltin, bool) {
	for _, builtin := range manifest.Builtins {
		if builtin.ID == id {
			return builtin, true
		}
	}
	return ManifestBuiltin{}, false
}

func validateManifest(manifest Manifest) error {
	if manifest.SchemaVersion != 1 && manifest.SchemaVersion != 2 {
		return fmt.Errorf("unsupported release manifest schema %d", manifest.SchemaVersion)
	}
	if manifest.SchemaVersion == 1 &&
		manifest.Compatibility.RuntimeDigest == "" &&
		len(manifest.Compatibility.CopilotProfiles) == 0 &&
		len(manifest.Compatibility.BuiltinIDs) == 0 {
		return nil
	}
	if !strings.HasPrefix(manifest.Compatibility.RuntimeDigest, "sha256:") ||
		len(strings.TrimPrefix(manifest.Compatibility.RuntimeDigest, "sha256:")) != 64 {
		return fmt.Errorf("release compatibility tuple has an invalid runtime digest")
	}
	if len(manifest.Compatibility.CopilotProfiles) == 0 {
		return fmt.Errorf("release compatibility tuple has no Copilot profiles")
	}
	profiles := map[string]bool{}
	for _, id := range manifest.Compatibility.CopilotProfiles {
		if strings.TrimSpace(id) == "" || profiles[id] {
			return fmt.Errorf("release compatibility tuple has invalid Copilot profiles")
		}
		profiles[id] = true
	}
	builtins := map[string]bool{}
	for _, builtin := range manifest.Builtins {
		if builtin.ID == "" || builtins[builtin.ID] {
			return fmt.Errorf("release manifest has invalid built-in entries")
		}
		builtins[builtin.ID] = true
	}
	if len(builtins) != len(manifest.Compatibility.BuiltinIDs) {
		return fmt.Errorf("release compatibility tuple does not match built-in assets")
	}
	for _, id := range manifest.Compatibility.BuiltinIDs {
		if !builtins[id] {
			return fmt.Errorf("release compatibility tuple does not authorize built-in %q", id)
		}
	}
	return nil
}

func (client Client) Stage(ctx context.Context, release Release, root string) (string, error) {
	architecture := runtime.GOARCH
	archiveName := "afterburn-windows-" + architecture + ".zip"
	archiveAsset, ok := findAsset(release, archiveName)
	if !ok {
		return "", fmt.Errorf("release %s is missing %s", release.TagName, archiveName)
	}
	checksumAsset, ok := findAsset(release, "checksums.txt")
	if !ok {
		return "", fmt.Errorf("release %s is missing checksums.txt", release.TagName)
	}
	manifestAsset, ok := findAsset(release, "release-manifest.json")
	if !ok {
		return "", fmt.Errorf("release %s is missing release-manifest.json", release.TagName)
	}
	signatureAsset, ok := findAsset(release, "release-manifest.sig")
	if !ok {
		return "", fmt.Errorf("release %s is missing release-manifest.sig", release.TagName)
	}
	checksums, err := client.download(ctx, checksumAsset, 1<<20)
	if err != nil {
		return "", err
	}
	expected, err := checksumFor(checksums, archiveName)
	if err != nil {
		return "", err
	}
	manifestData, err := client.download(ctx, manifestAsset, 1<<20)
	if err != nil {
		return "", err
	}
	signatureData, err := client.download(ctx, signatureAsset, 4096)
	if err != nil {
		return "", err
	}
	publicKey := ed25519.PublicKey(client.ManifestPublicKey)
	if len(publicKey) == 0 {
		publicKey, err = releasesign.PublicKey()
		if err != nil {
			return "", err
		}
	}
	if err := releasesign.Verify(publicKey, manifestData, signatureData); err != nil {
		return "", err
	}
	var manifest Manifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return "", fmt.Errorf("parse release manifest: %w", err)
	}
	manifestAssetEntry, ok := manifestEntry(manifest, archiveName, architecture)
	if err := validateManifest(manifest); err != nil {
		return "", err
	}
	if manifest.Repository != repository ||
		manifest.Version != release.TagName || manifest.Commit == "" || !ok ||
		!strings.EqualFold(manifestAssetEntry.SHA256, expected) {
		return "", fmt.Errorf("release manifest does not authorize %s for windows/%s", archiveName, architecture)
	}
	archive, err := client.download(ctx, archiveAsset, 256<<20)
	if err != nil {
		return "", err
	}
	if manifestAssetEntry.Size != int64(len(archive)) {
		return "", fmt.Errorf("release archive size does not match the manifest")
	}
	sum := sha256.Sum256(archive)
	if !strings.EqualFold(hex.EncodeToString(sum[:]), expected) {
		return "", fmt.Errorf("release archive checksum mismatch")
	}
	executable, err := extractExecutable(archive)
	if err != nil {
		return "", err
	}
	stagingRoot := filepath.Join(root, "update-staging")
	if err := os.MkdirAll(stagingRoot, 0o700); err != nil {
		return "", err
	}
	staging, err := os.MkdirTemp(stagingRoot, sanitizeTag(release.TagName)+"-")
	if err != nil {
		return "", err
	}
	success := false
	defer func() {
		if !success {
			_ = os.RemoveAll(staging)
		}
	}()
	target := filepath.Join(staging, "afterburn.exe")
	if err := os.WriteFile(target, executable, 0o700); err != nil {
		return "", err
	}
	if err := validateArchitecture(target, architecture); err != nil {
		return "", err
	}
	command := exec.CommandContext(ctx, target, "version")
	output, err := command.Output()
	if err != nil || !strings.Contains(string(output), release.TagName) {
		return "", fmt.Errorf("release executable version validation failed")
	}
	success = true
	return target, nil
}

// StageBuiltin downloads, verifies, and extracts a signed built-in extension
// package published as a release asset named "<id>.zip". It returns the
// staging directory containing the extracted extension source tree, which
// callers are responsible for removing once no longer needed. It performs
// the same manifest signature verification as Stage, but validates the
// archive against the release manifest's Builtins entries (which are not
// bound to an OS/architecture) rather than its Assets entries.
func (client Client) StageBuiltin(ctx context.Context, release Release, root, id string) (string, error) {
	archiveName := id + ".zip"
	archiveAsset, ok := findAsset(release, archiveName)
	if !ok {
		return "", fmt.Errorf("release %s is missing %s", release.TagName, archiveName)
	}
	checksumAsset, ok := findAsset(release, "checksums.txt")
	if !ok {
		return "", fmt.Errorf("release %s is missing checksums.txt", release.TagName)
	}
	manifestAsset, ok := findAsset(release, "release-manifest.json")
	if !ok {
		return "", fmt.Errorf("release %s is missing release-manifest.json", release.TagName)
	}
	signatureAsset, ok := findAsset(release, "release-manifest.sig")
	if !ok {
		return "", fmt.Errorf("release %s is missing release-manifest.sig", release.TagName)
	}
	checksums, err := client.download(ctx, checksumAsset, 1<<20)
	if err != nil {
		return "", err
	}
	expected, err := checksumFor(checksums, archiveName)
	if err != nil {
		return "", err
	}
	manifestData, err := client.download(ctx, manifestAsset, 1<<20)
	if err != nil {
		return "", err
	}
	signatureData, err := client.download(ctx, signatureAsset, 4096)
	if err != nil {
		return "", err
	}
	publicKey := ed25519.PublicKey(client.ManifestPublicKey)
	if len(publicKey) == 0 {
		publicKey, err = releasesign.PublicKey()
		if err != nil {
			return "", err
		}
	}
	if err := releasesign.Verify(publicKey, manifestData, signatureData); err != nil {
		return "", err
	}
	var manifest Manifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return "", fmt.Errorf("parse release manifest: %w", err)
	}
	builtinEntry, ok := builtinManifestEntry(manifest, id)
	if err := validateManifest(manifest); err != nil {
		return "", err
	}
	if manifest.Repository != repository ||
		manifest.Version != release.TagName || manifest.Commit == "" || !ok ||
		!strings.EqualFold(builtinEntry.SHA256, expected) {
		return "", fmt.Errorf("release manifest does not authorize %s for built-in %q", archiveName, id)
	}
	archive, err := client.download(ctx, archiveAsset, 64<<20)
	if err != nil {
		return "", err
	}
	if builtinEntry.Size != int64(len(archive)) {
		return "", fmt.Errorf("release archive size does not match the manifest")
	}
	sum := sha256.Sum256(archive)
	if !strings.EqualFold(hex.EncodeToString(sum[:]), expected) {
		return "", fmt.Errorf("release archive checksum mismatch")
	}
	stagingRoot := filepath.Join(root, "update-staging")
	if err := os.MkdirAll(stagingRoot, 0o700); err != nil {
		return "", err
	}
	staging, err := os.MkdirTemp(stagingRoot, sanitizeTag(release.TagName)+"-"+id+"-")
	if err != nil {
		return "", err
	}
	success := false
	defer func() {
		if !success {
			_ = os.RemoveAll(staging)
		}
	}()
	if err := extractBuiltinArchive(archive, staging); err != nil {
		return "", err
	}
	success = true
	return staging, nil
}

// extractBuiltinArchive extracts a zip archive into destination, rejecting
// any entry that would escape destination via path traversal or an absolute
// path. Unlike extractExecutable (which reads a single named file from an
// archive), this extracts an entire directory tree.
func extractBuiltinArchive(data []byte, destination string) error {
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return fmt.Errorf("open built-in archive: %w", err)
	}
	for _, file := range reader.File {
		cleaned := filepath.Clean(file.Name)
		if filepath.IsAbs(cleaned) || strings.HasPrefix(cleaned, "..") || strings.Contains(cleaned, ".."+string(filepath.Separator)) {
			return fmt.Errorf("built-in archive entry %q escapes the extraction root", file.Name)
		}
		target := filepath.Join(destination, cleaned)
		if file.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		input, err := file.Open()
		if err != nil {
			return err
		}
		content, readErr := io.ReadAll(io.LimitReader(input, 32<<20))
		closeErr := input.Close()
		if readErr != nil || closeErr != nil {
			return fmt.Errorf("read built-in archive entry %q", file.Name)
		}
		if err := os.WriteFile(target, content, 0o600); err != nil {
			return err
		}
	}
	return nil
}

// BuiltinReleaseFetcher resolves built-in extension IDs to their signed
// package published on the current core release. It implements the
// extensions.BuiltinFetcher interface via structural typing so that
// internal/extensions does not need to import internal/updater.
type BuiltinReleaseFetcher struct {
	Client Client
	Root   string
	// Version, when non-empty, pins the fetcher to a specific release tag
	// instead of the latest release. Leave empty to always use latest.
	Version string
}

func (fetcher BuiltinReleaseFetcher) FetchBuiltin(id string) (string, func(), error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var release Release
	var err error
	if fetcher.Version != "" {
		release, err = fetcher.Client.Release(ctx, fetcher.Version)
	} else {
		release, err = fetcher.Client.Latest(ctx)
	}
	if err != nil {
		return "", nil, fmt.Errorf("resolve release for built-in %q: %w", id, err)
	}
	staging, err := fetcher.Client.StageBuiltin(ctx, release, fetcher.Root, id)
	if err != nil {
		return "", nil, err
	}
	return staging, func() { _ = os.RemoveAll(staging) }, nil
}

func BeginReplacement(candidate, target, previous, registrySnapshot string) error {
	args := []string{
		"core", "replace",
		"--parent", strconv.Itoa(os.Getpid()),
		"--source", candidate,
		"--target", target,
		"--previous", previous,
	}
	if registrySnapshot != "" {
		args = append(args, "--registry-snapshot", registrySnapshot)
	}
	command := exec.Command(candidate, args...)
	return platform.StartDetached(command)
}

func StageRollback(root, previous string) (string, error) {
	info, err := os.Stat(previous)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("no previous Afterburner core is available")
		}
		return "", fmt.Errorf("inspect previous Afterburner core: %w", err)
	}
	if info.Size() <= 0 || info.Size() > maxExecutableBytes {
		return "", fmt.Errorf("previous Afterburner core has invalid size")
	}
	stagingRoot := filepath.Join(root, "update-staging")
	if err := os.MkdirAll(stagingRoot, 0o700); err != nil {
		return "", fmt.Errorf("create rollback staging root: %w", err)
	}
	staging, err := os.MkdirTemp(stagingRoot, "rollback-")
	if err != nil {
		return "", fmt.Errorf("create rollback staging directory: %w", err)
	}
	candidate := filepath.Join(staging, "afterburn.exe")
	content, err := os.ReadFile(previous)
	if err != nil {
		_ = os.RemoveAll(staging)
		return "", fmt.Errorf("read previous Afterburner core: %w", err)
	}
	if err := os.WriteFile(candidate, content, 0o700); err != nil {
		_ = os.RemoveAll(staging)
		return "", fmt.Errorf("stage previous Afterburner core: %w", err)
	}
	if err := validateArchitecture(candidate, runtime.GOARCH); err != nil {
		_ = os.RemoveAll(staging)
		return "", err
	}
	if err := exec.Command(candidate, "version").Run(); err != nil {
		_ = os.RemoveAll(staging)
		return "", fmt.Errorf("validate previous Afterburner core: %w", err)
	}
	return candidate, nil
}

func ApplyReplacement(parentPID int, source, target, previous, registrySnapshot string) (resultErr error) {
	root := filepath.Dir(filepath.Dir(target))
	defer func() {
		if resultErr != nil && registrySnapshot != "" {
			if restoreErr := RestoreRegistrySnapshot(root, registrySnapshot); restoreErr != nil {
				resultErr = fmt.Errorf("%v; restore extension registry: %w", resultErr, restoreErr)
			}
		}
		if discardErr := DiscardRegistrySnapshot(root, registrySnapshot); discardErr != nil && resultErr == nil {
			resultErr = discardErr
		}
		_ = writeStatus(root, resultErr)
	}()
	if err := platform.WaitForPID(parentPID, 2*time.Minute); err != nil {
		return fmt.Errorf("wait for updater parent PID %d: %w", parentPID, err)
	}
	// Running Afterburner processes keep their executable image mapped, so
	// replacing the on-disk path is safe: existing sessions continue on the
	// old image and new launches use the replacement. The transaction lock
	// serializes competing updates without unnecessarily stopping sessions.
	release, err := platform.AcquireDirectoryLock(filepath.Join(root, ".core.lock"), 2*time.Minute)
	if err != nil {
		return err
	}
	defer release()
	defer os.RemoveAll(filepath.Dir(source))
	data, err := os.ReadFile(source)
	if err != nil {
		return fmt.Errorf("read staged update: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	if current, err := os.ReadFile(target); err == nil {
		if err := os.WriteFile(previous, current, 0o700); err != nil {
			return fmt.Errorf("retain previous executable: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	incoming, err := os.CreateTemp(filepath.Dir(target), ".afterburn-update-*.exe")
	if err != nil {
		return err
	}
	incomingPath := incoming.Name()
	defer os.Remove(incomingPath)
	if _, err := incoming.Write(data); err != nil {
		incoming.Close()
		return err
	}
	if err := incoming.Sync(); err != nil {
		incoming.Close()
		return err
	}
	if err := incoming.Close(); err != nil {
		return err
	}
	if err := platform.ReplaceFile(incomingPath, target); err != nil {
		return fmt.Errorf("replace installed executable: %w. Latest core remains staged at %s; close running Afterburner terminals/sessions, including this one if it launched from %s, then retry `afterburn update --version <tag>`", err, source, target)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	validationErr := syncEmbeddedBuiltins(root)
	if validationErr == nil {
		doctor := exec.CommandContext(ctx, target, "doctor", "--json")
		validationErr = doctor.Run()
	}
	if validationErr != nil {
		rollbackData, readErr := os.ReadFile(previous)
		if readErr != nil {
			return fmt.Errorf("post-update validation failed and rollback binary is unavailable: %w", validationErr)
		}
		rollback, createErr := os.CreateTemp(filepath.Dir(target), ".afterburn-rollback-*.exe")
		if createErr != nil {
			return createErr
		}
		rollbackPath := rollback.Name()
		defer os.Remove(rollbackPath)
		if _, createErr = rollback.Write(rollbackData); createErr != nil {
			rollback.Close()
			return createErr
		}
		if createErr = rollback.Close(); createErr != nil {
			return createErr
		}
		if replaceErr := platform.ReplaceFile(rollbackPath, target); replaceErr != nil {
			return fmt.Errorf("post-update validation failed and automatic rollback failed: %w", replaceErr)
		}
		return fmt.Errorf("post-update validation failed; previous executable restored: %w", validationErr)
	}
	return nil
}

func syncEmbeddedBuiltins(root string) error {
	layout, err := home.Resolve()
	if err != nil {
		return fmt.Errorf("resolve home for built-in synchronization: %w", err)
	}
	layout.Root = root
	layout.CopilotHome = filepath.Join(root, "copilot-home")
	layout.Config = filepath.Join(root, "config")
	layout.ExtensionData = filepath.Join(root, "extension-data")
	layout.Extensions = filepath.Join(root, "extensions")
	layout.Staging = filepath.Join(root, "staging")
	layout.BYOModelsConfig = filepath.Join(root, "config", "byomodels.json")
	for _, path := range []string{
		layout.Root,
		layout.CopilotHome,
		layout.Config,
		layout.ExtensionData,
		layout.Extensions,
		layout.Staging,
	} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return fmt.Errorf("prepare built-in synchronization: %w", err)
		}
	}
	manager := extensions.Manager{Layout: layout, Stdout: io.Discard}
	if err := manager.InstallBuiltins(nil); err != nil {
		return fmt.Errorf("sync built-ins embedded in replacement core: %w", err)
	}
	return nil
}

type Status struct {
	SchemaVersion int       `json:"schemaVersion"`
	Status        string    `json:"status"`
	CompletedAt   time.Time `json:"completedAt"`
	Error         string    `json:"error,omitempty"`
}

func writeStatus(root string, resultErr error) error {
	status := Status{
		SchemaVersion: 1,
		Status:        "succeeded",
		CompletedAt:   time.Now().UTC(),
	}
	if resultErr != nil {
		status.Status = "failed"
		status.Error = resultErr.Error()
	}
	data, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	directory := filepath.Join(root, "state")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	path := filepath.Join(directory, "core-update-status.json")
	temporary, err := os.CreateTemp(directory, ".core-update-status-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return platform.ReplaceFile(temporaryPath, path)
}

func ReadStatus(root string) (Status, bool, error) {
	data, err := os.ReadFile(filepath.Join(root, "state", "core-update-status.json"))
	if os.IsNotExist(err) {
		return Status{}, false, nil
	}
	if err != nil {
		return Status{}, false, err
	}
	var status Status
	if err := json.Unmarshal(data, &status); err != nil {
		return Status{}, false, err
	}
	if status.SchemaVersion != 1 || status.Status == "" {
		return Status{}, false, fmt.Errorf("invalid core update status")
	}
	return status, true, nil
}

func FormatStatusWarning(status Status) string {
	if status.Status != "failed" || strings.TrimSpace(status.Error) == "" {
		return ""
	}
	return fmt.Sprintf("Warning: previous Afterburner core update failed at %s: %s\n", status.CompletedAt.Format(time.RFC3339), status.Error)
}

func (client Client) download(ctx context.Context, asset Asset, maximum int64) ([]byte, error) {
	location := asset.APIURL
	if location == "" {
		location = asset.BrowserDownloadURL
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, location, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/octet-stream")
	request.Header.Set("User-Agent", "afterburn-updater")
	if client.Token != "" {
		request.Header.Set("Authorization", "Bearer "+client.Token)
	}
	response, err := client.HTTP.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s returned %s", asset.Name, response.Status)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maximum {
		return nil, fmt.Errorf("release asset %s exceeds the size limit", asset.Name)
	}
	return data, nil
}

func findAsset(release Release, name string) (Asset, bool) {
	for _, asset := range release.Assets {
		if asset.Name == name {
			return asset, true
		}
	}
	return Asset{}, false
}

func checksumFor(data []byte, name string) (string, error) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 2 && fields[1] == name {
			if len(fields[0]) != 64 {
				break
			}
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("checksums.txt is missing %s", name)
}

func manifestEntry(manifest Manifest, name, architecture string) (ManifestAsset, bool) {
	for _, asset := range manifest.Assets {
		if asset.Name == name && asset.OS == "windows" && asset.Architecture == architecture {
			return asset, true
		}
	}
	return ManifestAsset{}, false
}

func extractExecutable(data []byte) ([]byte, error) {
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("open release archive: %w", err)
	}
	for _, file := range reader.File {
		if filepath.Base(file.Name) != "afterburn.exe" || strings.Contains(file.Name, "..") {
			continue
		}
		input, err := file.Open()
		if err != nil {
			return nil, err
		}
		executable, readErr := io.ReadAll(io.LimitReader(input, 128<<20))
		closeErr := input.Close()
		if readErr != nil || closeErr != nil || uint64(len(executable)) != file.UncompressedSize64 {
			return nil, fmt.Errorf("read release executable")
		}
		return executable, nil
	}
	return nil, fmt.Errorf("release archive does not contain afterburn.exe")
}

func validateArchitecture(path, architecture string) error {
	file, err := pe.Open(path)
	if err != nil {
		return fmt.Errorf("open release executable: %w", err)
	}
	defer file.Close()
	expected := uint16(pe.IMAGE_FILE_MACHINE_AMD64)
	if architecture == "arm64" {
		expected = pe.IMAGE_FILE_MACHINE_ARM64
	}
	if file.FileHeader.Machine != expected {
		return fmt.Errorf("release executable has the wrong architecture")
	}
	return nil
}

func sanitizeTag(tag string) string {
	var result strings.Builder
	for _, value := range tag {
		if value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z' ||
			value >= '0' && value <= '9' || value == '.' || value == '-' {
			result.WriteRune(value)
		}
	}
	if result.Len() == 0 {
		return "release"
	}
	return result.String()
}
