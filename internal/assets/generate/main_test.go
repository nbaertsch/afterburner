package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreateArchiveExcludesTransientArtifactsDeterministically(t *testing.T) {
	temp := t.TempDir()
	source := filepath.Join(temp, "source")
	mustWrite(t, filepath.Join(source, "afterburner.json"), "{}")
	mustWrite(t, filepath.Join(source, "lib", "service.mjs"), "export {}\n")
	mustWrite(t, filepath.Join(source, ".npmrc"), "engine-strict=true\n")
	mustWrite(t, filepath.Join(source, ".test-work", "run-a", "state.json"), "first")
	mustWrite(t, filepath.Join(source, "node_modules", "pkg", "index.js"), "module.exports = 1")
	mustWrite(t, filepath.Join(source, "coverage", "lcov.info"), "TN:\n")
	mustWrite(t, filepath.Join(source, "logs", "service.log"), "log")
	mustWrite(t, filepath.Join(source, ".cache", "cache.bin"), "cache")
	mustWrite(t, filepath.Join(source, "debug.log"), "log")
	mustWrite(t, filepath.Join(source, "scratch.tmp"), "tmp")
	mustWrite(t, filepath.Join(source, ".env"), "SECRET=value")
	mustWrite(t, filepath.Join(source, ".hidden-dir", "config.json"), "hidden")

	first := filepath.Join(temp, "first.zip")
	if err := createArchive(source, first); err != nil {
		t.Fatalf("create first archive: %v", err)
	}
	firstBytes, err := os.ReadFile(first)
	if err != nil {
		t.Fatalf("read first archive: %v", err)
	}
	firstHash := sha256.Sum256(firstBytes)

	mustWrite(t, filepath.Join(source, ".test-work", "run-a", "state.json"), "second")
	mustWrite(t, filepath.Join(source, ".test-work", "run-b", "state.json"), "third")
	mustWrite(t, filepath.Join(source, "debug.log"), "changed")

	second := filepath.Join(temp, "second.zip")
	if err := createArchive(source, second); err != nil {
		t.Fatalf("create second archive: %v", err)
	}
	secondBytes, err := os.ReadFile(second)
	if err != nil {
		t.Fatalf("read second archive: %v", err)
	}
	secondHash := sha256.Sum256(secondBytes)

	if !bytes.Equal(firstBytes, secondBytes) {
		t.Fatalf("archive bytes changed after excluded artifacts changed: %x != %x", firstHash, secondHash)
	}

	entries := readZipEntries(t, second)
	for _, name := range []string{
		"afterburner.json",
		"lib/service.mjs",
		".npmrc",
	} {
		if !entries[name] {
			t.Fatalf("expected archive to include %q", name)
		}
	}
	for _, name := range []string{
		".test-work/run-a/state.json",
		".test-work/run-b/state.json",
		"node_modules/pkg/index.js",
		"coverage/lcov.info",
		"logs/service.log",
		".cache/cache.bin",
		"debug.log",
		"scratch.tmp",
		".env",
		".hidden-dir/config.json",
	} {
		if entries[name] {
			t.Fatalf("expected archive to exclude %q", name)
		}
	}
}

func TestCreateArchiveNormalizesTextLineEndings(t *testing.T) {
	temp := t.TempDir()
	source := filepath.Join(temp, "source")
	path := filepath.Join(source, "runtime", "extension.mjs")
	mustWrite(t, path, "export const value = 1;\nexport default value;\n")
	lfArchive := filepath.Join(temp, "lf.zip")
	if err := createArchive(source, lfArchive); err != nil {
		t.Fatal(err)
	}
	lfBytes, err := os.ReadFile(lfArchive)
	if err != nil {
		t.Fatal(err)
	}

	mustWrite(t, path, "export const value = 1;\r\nexport default value;\r\n")
	crlfArchive := filepath.Join(temp, "crlf.zip")
	if err := createArchive(source, crlfArchive); err != nil {
		t.Fatal(err)
	}
	crlfBytes, err := os.ReadFile(crlfArchive)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(lfBytes, crlfBytes) {
		t.Fatal("archive changed when only source text line endings changed")
	}
}

func TestGeneratedBlackBoxArchiveExcludesGenericCanvasFallback(t *testing.T) {
	archivePath := filepath.Join("..", "generated", "black-box.zip")
	contents := readZipContents(t, archivePath)
	manifestData, ok := contents["afterburner.json"]
	if !ok {
		t.Fatal("generated Black Box archive missing afterburner.json")
	}
	var manifest struct {
		Capabilities []string `json:"capabilities"`
	}
	if err := json.Unmarshal([]byte(manifestData), &manifest); err != nil {
		t.Fatalf("decode generated Black Box manifest: %v", err)
	}
	for _, capability := range manifest.Capabilities {
		if capability == "canvas" {
			t.Fatal("generated Black Box archive must not advertise generic canvas capability")
		}
	}
	if !containsString(manifest.Capabilities, "modal-canvas") {
		t.Fatal("generated Black Box archive missing modal-canvas capability")
	}
	if containsString(manifest.Capabilities, "enterprise-surface") {
		t.Fatal("generated Black Box archive must not advertise enterprise-surface capability")
	}
	for _, name := range []string{"lib/session-extension.mjs", "extensions/BlackBox/extension.mjs"} {
		content, ok := contents[name]
		if !ok {
			t.Fatalf("generated Black Box archive missing %s", name)
		}
		if strings.Contains(content, "createCanvas") || strings.Contains(content, "openModalCanvas") || strings.Contains(content, "canvasRpc.open") || strings.Contains(content, "canvases: canvas") {
			t.Fatalf("generated Black Box archive contains generic canvas fallback in %s", name)
		}
	}
}

func TestGeneratedArchivesContainRuntimeFilesOnly(t *testing.T) {
	for _, archiveName := range []string{"black-box.zip", "byo-models.zip", "openai-server.zip"} {
		entries := readZipEntries(t, filepath.Join("..", "generated", archiveName))
		for name := range entries {
			lower := strings.ToLower(name)
			if lower == "readme.md" || lower == "package.json" || lower == "package-lock.json" ||
				strings.HasPrefix(lower, "test/") || strings.HasPrefix(lower, "tests/") {
				t.Fatalf("%s contains development-only file %s", archiveName, name)
			}
		}
	}
}

func TestArchiveNameRejectsTraversal(t *testing.T) {
	temp := t.TempDir()
	source := filepath.Join(temp, "source")
	outside := filepath.Join(temp, "outside.txt")
	if _, err := archiveName(source, outside); err == nil {
		t.Fatal("expected archiveName to reject path outside source")
	}
}

func TestCreateArchiveSkipsSymlinks(t *testing.T) {
	temp := t.TempDir()
	source := filepath.Join(temp, "source")
	mustWrite(t, filepath.Join(source, "afterburner.json"), "{}")
	secret := filepath.Join(temp, "secret.txt")
	mustWrite(t, secret, "do not include")
	link := filepath.Join(source, "linked-secret.txt")
	if err := os.Symlink(secret, link); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}

	target := filepath.Join(temp, "archive.zip")
	if err := createArchive(source, target); err != nil {
		t.Fatalf("create archive: %v", err)
	}
	entries := readZipEntries(t, target)
	if entries["linked-secret.txt"] {
		t.Fatal("expected archive to skip symlinked file")
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create parent for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func readZipContents(t *testing.T, path string) map[string]string {
	t.Helper()
	reader, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("open zip %s: %v", path, err)
	}
	defer reader.Close()
	contents := map[string]string{}
	for _, file := range reader.File {
		opened, err := file.Open()
		if err != nil {
			t.Fatalf("open zip entry %s: %v", file.Name, err)
		}
		var buffer bytes.Buffer
		_, copyErr := buffer.ReadFrom(opened)
		closeErr := opened.Close()
		if copyErr != nil {
			t.Fatalf("read zip entry %s: %v", file.Name, copyErr)
		}
		if closeErr != nil {
			t.Fatalf("close zip entry %s: %v", file.Name, closeErr)
		}
		contents[file.Name] = buffer.String()
	}
	return contents
}

func readZipEntries(t *testing.T, path string) map[string]bool {
	t.Helper()
	reader, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("open zip %s: %v", path, err)
	}
	defer reader.Close()
	entries := map[string]bool{}
	for _, file := range reader.File {
		entries[file.Name] = true
	}
	return entries
}

func TestGeneratedOpenAIServerArchiveUsesCanonicalAndLegacyWrappers(t *testing.T) {
	archivePath := filepath.Join("..", "generated", "openai-server.zip")
	contents := readZipContents(t, archivePath)
	manifestData, ok := contents["afterburner.json"]
	if !ok {
		t.Fatal("generated OpenAI Server archive missing afterburner.json")
	}
	var manifest struct {
		ID         string `json:"id"`
		Visibility string `json:"visibility"`
	}
	if err := json.Unmarshal([]byte(manifestData), &manifest); err != nil {
		t.Fatalf("decode generated OpenAI Server manifest: %v", err)
	}
	if manifest.ID != "openai-server" || manifest.Visibility != "builtin" {
		t.Fatalf("unexpected OpenAI Server manifest identity: %#v", manifest)
	}
	for _, name := range []string{"com.github.copilot/extensions/OpenAIServer/extension.mjs", "com.github.copilot/extensions/CopilotOpenAI/extension.mjs", "extensions/OpenAIServer/extension.mjs"} {
		if _, ok := contents[name]; !ok {
			t.Fatalf("generated OpenAI Server archive missing %s", name)
		}
	}
}
