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
	SchemaVersion int             `json:"schemaVersion"`
	Repository    string          `json:"repository"`
	Version       string          `json:"version"`
	Commit        string          `json:"commit"`
	Assets        []ManifestAsset `json:"assets"`
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
	if manifest.SchemaVersion != 1 || manifest.Repository != repository ||
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

func BeginReplacement(candidate, target, previous string) error {
	command := exec.Command(candidate,
		"core", "replace",
		"--parent", strconv.Itoa(os.Getpid()),
		"--source", candidate,
		"--target", target,
		"--previous", previous,
	)
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

func ApplyReplacement(parentPID int, source, target, previous string) (resultErr error) {
	root := filepath.Dir(filepath.Dir(target))
	defer func() {
		_ = writeStatus(root, resultErr)
	}()
	if err := platform.WaitForPID(parentPID, 2*time.Minute); err != nil {
		return fmt.Errorf("wait for updater parent: %w", err)
	}
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
		return fmt.Errorf("replace installed executable: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	doctor := exec.CommandContext(ctx, target, "doctor", "--json")
	if err := doctor.Run(); err != nil {
		rollbackData, readErr := os.ReadFile(previous)
		if readErr != nil {
			return fmt.Errorf("post-update validation failed and rollback binary is unavailable: %w", err)
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
		return fmt.Errorf("post-update validation failed; previous executable restored")
	}
	return nil
}

func writeStatus(root string, resultErr error) error {
	status := struct {
		SchemaVersion int       `json:"schemaVersion"`
		Status        string    `json:"status"`
		CompletedAt   time.Time `json:"completedAt"`
		Error         string    `json:"error,omitempty"`
	}{
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
