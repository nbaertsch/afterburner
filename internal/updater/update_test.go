package updater

import (
	"archive/zip"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestChecksumAndArchiveExtraction(t *testing.T) {
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	entry, err := writer.Create("afterburn.exe")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte("binary")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(archive.Bytes())
	checksums := []byte(hex.EncodeToString(sum[:]) + "  afterburn-windows-amd64.zip\n")
	got, err := checksumFor(checksums, "afterburn-windows-amd64.zip")
	if err != nil {
		t.Fatal(err)
	}
	if got != hex.EncodeToString(sum[:]) {
		t.Fatalf("checksum = %s", got)
	}
	executable, err := extractExecutable(archive.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if string(executable) != "binary" {
		t.Fatalf("executable = %q", executable)
	}
}

func TestStageValidatesReleaseAndExecutable(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	candidate := buildFixtureExecutable(t, "v9.8.7", true)
	archive := zipExecutable(t, candidate)
	sum := sha256.Sum256(archive)
	checksum := hex.EncodeToString(sum[:])
	assetName := "afterburn-windows-" + runtime.GOARCH + ".zip"
	manifest, err := json.Marshal(Manifest{
		SchemaVersion: 1,
		Repository:    repository,
		Version:       "v9.8.7",
		Commit:        "fixture",
		Assets: []ManifestAsset{{
			Name: assetName, OS: "windows", Architecture: runtime.GOARCH,
			SHA256: checksum, Size: int64(len(archive)),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	signature := base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, manifest))
	files := map[string][]byte{
		"/archive":   archive,
		"/checksums": []byte(checksum + "  " + assetName + "\n"),
		"/manifest":  manifest,
		"/signature": []byte(signature),
	}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		data, ok := files[request.URL.Path]
		if !ok {
			http.NotFound(response, request)
			return
		}
		_, _ = response.Write(data)
	}))
	defer server.Close()
	release := Release{
		TagName: "v9.8.7",
		Assets: []Asset{
			{Name: assetName, APIURL: server.URL + "/archive"},
			{Name: "checksums.txt", APIURL: server.URL + "/checksums"},
			{Name: "release-manifest.json", APIURL: server.URL + "/manifest"},
			{Name: "release-manifest.sig", APIURL: server.URL + "/signature"},
		},
	}
	staged, err := (Client{HTTP: server.Client(), ManifestPublicKey: publicKey}).Stage(t.Context(), release, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(staged); err != nil {
		t.Fatal(err)
	}
}

func TestStageRejectsTamperedManifestBeforeExecution(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	assetName := "afterburn-windows-" + runtime.GOARCH + ".zip"
	archive := []byte("not executed")
	sum := sha256.Sum256(archive)
	checksum := hex.EncodeToString(sum[:])
	manifest, err := json.Marshal(Manifest{
		SchemaVersion: 1,
		Repository:    repository,
		Version:       "v1.0.0",
		Commit:        "trusted",
		Assets: []ManifestAsset{{
			Name: assetName, OS: "windows", Architecture: runtime.GOARCH,
			SHA256: checksum, Size: int64(len(archive)),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	signature := base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, manifest))
	manifest = append(manifest, ' ')
	files := map[string][]byte{
		"/archive":   archive,
		"/checksums": []byte(checksum + "  " + assetName + "\n"),
		"/manifest":  manifest,
		"/signature": []byte(signature),
	}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		_, _ = response.Write(files[request.URL.Path])
	}))
	defer server.Close()
	release := Release{
		TagName: "v1.0.0",
		Assets: []Asset{
			{Name: assetName, APIURL: server.URL + "/archive"},
			{Name: "checksums.txt", APIURL: server.URL + "/checksums"},
			{Name: "release-manifest.json", APIURL: server.URL + "/manifest"},
			{Name: "release-manifest.sig", APIURL: server.URL + "/signature"},
		},
	}
	_, err = (Client{HTTP: server.Client(), ManifestPublicKey: publicKey}).Stage(t.Context(), release, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "signature verification failed") {
		t.Fatalf("error = %v", err)
	}
}

func TestStageBuiltinExtractsVerifiedArchive(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var archiveBuffer bytes.Buffer
	writer := zip.NewWriter(&archiveBuffer)
	entry, err := writer.Create("lib/session-extension.mjs")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte("export const marker = 'fixture';")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	archive := archiveBuffer.Bytes()
	sum := sha256.Sum256(archive)
	checksum := hex.EncodeToString(sum[:])
	manifest, err := json.Marshal(Manifest{
		SchemaVersion: 1,
		Repository:    repository,
		Version:       "v1.2.3",
		Commit:        "fixture",
		Builtins: []ManifestBuiltin{{
			ID: "black-box", Name: "black-box.zip", SHA256: checksum, Size: int64(len(archive)),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	signature := base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, manifest))
	files := map[string][]byte{
		"/archive":   archive,
		"/checksums": []byte(checksum + "  black-box.zip\n"),
		"/manifest":  manifest,
		"/signature": []byte(signature),
	}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		data, ok := files[request.URL.Path]
		if !ok {
			http.NotFound(response, request)
			return
		}
		_, _ = response.Write(data)
	}))
	defer server.Close()
	release := Release{
		TagName: "v1.2.3",
		Assets: []Asset{
			{Name: "black-box.zip", APIURL: server.URL + "/archive"},
			{Name: "checksums.txt", APIURL: server.URL + "/checksums"},
			{Name: "release-manifest.json", APIURL: server.URL + "/manifest"},
			{Name: "release-manifest.sig", APIURL: server.URL + "/signature"},
		},
	}
	staged, err := (Client{HTTP: server.Client(), ManifestPublicKey: publicKey}).StageBuiltin(t.Context(), release, t.TempDir(), "black-box")
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(staged, "lib", "session-extension.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "fixture") {
		t.Fatalf("extracted content = %q", content)
	}
}

func TestStageBuiltinRejectsMissingManifestEntry(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	archive := []byte("PK\x03\x04")
	sum := sha256.Sum256(archive)
	checksum := hex.EncodeToString(sum[:])
	manifest, err := json.Marshal(Manifest{
		SchemaVersion: 1,
		Repository:    repository,
		Version:       "v1.2.3",
		Commit:        "fixture",
		// No Builtins entries: byo-models must not be authorized.
	})
	if err != nil {
		t.Fatal(err)
	}
	signature := base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, manifest))
	files := map[string][]byte{
		"/archive":   archive,
		"/checksums": []byte(checksum + "  byo-models.zip\n"),
		"/manifest":  manifest,
		"/signature": []byte(signature),
	}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		_, _ = response.Write(files[request.URL.Path])
	}))
	defer server.Close()
	release := Release{
		TagName: "v1.2.3",
		Assets: []Asset{
			{Name: "byo-models.zip", APIURL: server.URL + "/archive"},
			{Name: "checksums.txt", APIURL: server.URL + "/checksums"},
			{Name: "release-manifest.json", APIURL: server.URL + "/manifest"},
			{Name: "release-manifest.sig", APIURL: server.URL + "/signature"},
		},
	}
	_, err = (Client{HTTP: server.Client(), ManifestPublicKey: publicKey}).StageBuiltin(t.Context(), release, t.TempDir(), "byo-models")
	if err == nil || !strings.Contains(err.Error(), "does not authorize") {
		t.Fatalf("error = %v", err)
	}
}

func TestExtractBuiltinArchiveRejectsPathTraversal(t *testing.T) {
	var archiveBuffer bytes.Buffer
	writer := zip.NewWriter(&archiveBuffer)
	entry, err := writer.Create("../escape.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte("bad")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := extractBuiltinArchive(archiveBuffer.Bytes(), t.TempDir()); err == nil || !strings.Contains(err.Error(), "escapes the extraction root") {
		t.Fatalf("error = %v", err)
	}
}

func TestStageRequiresManifestSignature(t *testing.T) {
	assetName := "afterburn-windows-" + runtime.GOARCH + ".zip"
	_, err := (Client{}).Stage(t.Context(), Release{
		TagName: "v1.0.0",
		Assets: []Asset{
			{Name: assetName},
			{Name: "checksums.txt"},
			{Name: "release-manifest.json"},
		},
	}, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "release-manifest.sig") {
		t.Fatalf("error = %v", err)
	}
}

func TestApplyReplacementAndAutomaticRollback(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("replacement semantics are validated on Windows")
	}
	t.Run("success", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(root, "bin", "afterburn.exe")
		previous := filepath.Join(root, "bin", "afterburn.previous.exe")
		source := filepath.Join(root, "update-staging", "fixture", "afterburn.exe")
		copyFile(t, buildFixtureExecutable(t, "old", true), target)
		copyFile(t, buildFixtureExecutable(t, "new", true), source)
		if err := ApplyReplacement(0, source, target, previous); err != nil {
			t.Fatal(err)
		}
		assertVersion(t, target, "new")
		assertVersion(t, previous, "old")
	})
	t.Run("failed doctor", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(root, "bin", "afterburn.exe")
		previous := filepath.Join(root, "bin", "afterburn.previous.exe")
		source := filepath.Join(root, "update-staging", "fixture", "afterburn.exe")
		copyFile(t, buildFixtureExecutable(t, "old", true), target)
		copyFile(t, buildFixtureExecutable(t, "bad", false), source)
		if err := ApplyReplacement(0, source, target, previous); err == nil {
			t.Fatal("expected post-update validation failure")
		}
		assertVersion(t, target, "old")
	})
}

func buildFixtureExecutable(t *testing.T, version string, doctorOK bool) string {
	t.Helper()
	directory := t.TempDir()
	source := fmt.Sprintf(`package main
import ("fmt"; "os")
func main() {
	if len(os.Args) > 1 && os.Args[1] == "version" { fmt.Println(%q); return }
	if len(os.Args) > 1 && os.Args[1] == "doctor" && %t { return }
	os.Exit(1)
}`, version, doctorOK)
	sourcePath := filepath.Join(directory, "main.go")
	if err := os.WriteFile(sourcePath, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(directory, "fixture.exe")
	command := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go.exe"), "build", "-o", executable, sourcePath)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build fixture: %v\n%s", err, output)
	}
	return executable
}

func zipExecutable(t *testing.T, executable string) []byte {
	t.Helper()
	content, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	entry, err := writer.Create("afterburn.exe")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return archive.Bytes()
}

func copyFile(t *testing.T, source, target string) {
	t.Helper()
	content, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, content, 0o700); err != nil {
		t.Fatal(err)
	}
}

func assertVersion(t *testing.T, executable, version string) {
	t.Helper()
	output, err := exec.Command(executable, "version").Output()
	if err != nil {
		t.Fatal(err)
	}
	if string(bytes.TrimSpace(output)) != version {
		t.Fatalf("version = %q, want %q", bytes.TrimSpace(output), version)
	}
}
