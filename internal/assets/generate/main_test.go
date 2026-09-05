package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"os"
	"path/filepath"
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
