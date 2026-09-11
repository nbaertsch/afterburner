package copilot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDiscoverAndSelect(t *testing.T) {
	root := t.TempDir()
	for _, version := range []string{"1.0.83-2", "1.0.83-3"} {
		path := filepath.Join(root, version)
		if err := os.MkdirAll(filepath.Join(path, "prebuilds", runtimePlatform()), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "app.js"), []byte(version), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "prebuilds", runtimePlatform(), "runtime.node"), []byte("runtime-"+version), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	found, err := Discover(DiscoveryOptions{AdditionalRoots: []string{root}, OnlyAdditionalRoots: true})
	if err != nil {
		t.Fatal(err)
	}
	selected, err := SelectNewestComplete(found)
	if err != nil {
		t.Fatal(err)
	}
	if selected.Version != "1.0.83-3" {
		t.Fatalf("selected %s", selected.Version)
	}
}

func TestDiscoverCachesAndInvalidatesPackageHashes(t *testing.T) {
	root := t.TempDir()
	cachePath := filepath.Join(t.TempDir(), "package-hashes.json")
	version := "1.0.83-3"
	path := filepath.Join(root, version)
	if err := os.MkdirAll(filepath.Join(path, "prebuilds", runtimePlatform()), 0o755); err != nil {
		t.Fatal(err)
	}
	appPath := filepath.Join(path, "app.js")
	if err := os.WriteFile(appPath, []byte("first"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(path, "prebuilds", runtimePlatform(), "runtime.node"), []byte("runtime"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Discover(DiscoveryOptions{
		AdditionalRoots: []string{root},
		HashCachePath:   cachePath,
	}); err != nil {
		t.Fatal(err)
	}

	cache := loadHashCache(cachePath)
	key := strings.ToLower(path)
	entry := cache.Packages[key]
	entry.App.SHA256 = strings.Repeat("a", 64)
	cache.Packages[key] = entry
	if err := saveHashCache(cachePath, cache); err != nil {
		t.Fatal(err)
	}
	found, err := Discover(DiscoveryOptions{
		AdditionalRoots: []string{root},
		HashCachePath:   cachePath,
	})
	if err != nil {
		t.Fatal(err)
	}
	cachedPackage := findPackageByPath(found, path)
	if cachedPackage.AppSHA256 != strings.Repeat("a", 64) {
		t.Fatal("unchanged package did not reuse its cached hash")
	}
	if !cachedPackage.HashCacheHit {
		t.Fatal("unchanged package was not marked as a warm cache hit")
	}

	if err := os.WriteFile(appPath, []byte("second"), 0o644); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Second)
	if err := os.Chtimes(appPath, future, future); err != nil {
		t.Fatal(err)
	}
	found, err = Discover(DiscoveryOptions{
		AdditionalRoots: []string{root},
		HashCachePath:   cachePath,
	})
	if err != nil {
		t.Fatal(err)
	}
	changedPackage := findPackageByPath(found, path)
	if changedPackage.AppSHA256 == strings.Repeat("a", 64) {
		t.Fatal("changed package retained a stale cached hash")
	}

	if err := os.WriteFile(cachePath, []byte("{corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Discover(DiscoveryOptions{
		AdditionalRoots: []string{root},
		HashCachePath:   cachePath,
	}); err != nil {
		t.Fatalf("corrupt cache did not fail safely: %v", err)
	}
}

func findPackageByPath(packages []Package, path string) Package {
	for _, pkg := range packages {
		if strings.EqualFold(pkg.Path, path) {
			return pkg
		}
	}
	return Package{}
}

func TestDiscoverDoesNotRewriteUnchangedHashCache(t *testing.T) {
	root := t.TempDir()
	cachePath := filepath.Join(t.TempDir(), "package-hashes.json")
	path := filepath.Join(root, "1.0.0")
	if err := os.MkdirAll(filepath.Join(path, "prebuilds", runtimePlatform()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "app.js"), []byte("app"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "prebuilds", runtimePlatform(), "runtime.node"), []byte("runtime"), 0o644); err != nil {
		t.Fatal(err)
	}
	options := DiscoveryOptions{AdditionalRoots: []string{root}, OnlyAdditionalRoots: true, HashCachePath: cachePath}
	if _, err := Discover(options); err != nil {
		t.Fatal(err)
	}
	first, err := os.Stat(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if _, err := Discover(options); err != nil {
		t.Fatal(err)
	}
	second, err := os.Stat(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if !second.ModTime().Equal(first.ModTime()) {
		t.Fatal("unchanged discovery cache was rewritten")
	}
}
