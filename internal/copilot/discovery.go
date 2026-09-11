package copilot

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/nbaertsch/afterburner/internal/platform"
)

type Package struct {
	Version         string `json:"version"`
	Path            string `json:"path"`
	SourceRoot      string `json:"sourceRoot"`
	BuildCommit     string `json:"buildCommit,omitempty"`
	AppSHA256       string `json:"appSha256"`
	AppSize         int64  `json:"appSize,omitempty"`
	AppModified     int64  `json:"appModifiedUnixNano,omitempty"`
	IndexSHA256     string `json:"indexSha256,omitempty"`
	PackageSHA256   string `json:"packageSha256,omitempty"`
	RuntimeSHA256   string `json:"runtimeSha256"`
	RuntimeSize     int64  `json:"runtimeSize,omitempty"`
	RuntimeModified int64  `json:"runtimeModifiedUnixNano,omitempty"`
	Complete        bool   `json:"complete"`
	HashCacheHit    bool   `json:"-"`
}

type DiscoveryOptions struct {
	ManagedHome         string
	CopilotExecutable   string
	AdditionalRoots     []string
	HashCachePath       string
	OnlyAdditionalRoots bool
}

type fileHashCache struct {
	Size             int64  `json:"size"`
	ModifiedUnixNano int64  `json:"modifiedUnixNano"`
	SHA256           string `json:"sha256"`
}

type packageHashCache struct {
	App         fileHashCache `json:"app"`
	Index       fileHashCache `json:"index"`
	Package     fileHashCache `json:"package"`
	Runtime     fileHashCache `json:"runtime"`
	BuildCommit string        `json:"buildCommit,omitempty"`
}

type hashCache struct {
	SchemaVersion int                         `json:"schemaVersion"`
	Packages      map[string]packageHashCache `json:"packages"`
}

func Discover(opts DiscoveryOptions) ([]Package, error) {
	cache := loadHashCache(opts.HashCachePath)
	nextCache := hashCache{SchemaVersion: 1, Packages: map[string]packageHashCache{}}
	platform := "win32-" + mapArch(runtime.GOARCH)
	userHome, _ := os.UserHomeDir()
	var roots []string
	if !opts.OnlyAdditionalRoots {
		roots = append(roots,
			filepath.Join(userHome, ".copilot", "pkg", platform),
			filepath.Join(os.Getenv("LOCALAPPDATA"), "copilot", "pkg", platform),
			filepath.Join(opts.ManagedHome, "pkg", platform),
		)
		if opts.CopilotExecutable != "" {
			executableDir := filepath.Dir(opts.CopilotExecutable)
			roots = append(roots,
				filepath.Join(executableDir, "pkg", platform),
				filepath.Join(filepath.Dir(executableDir), "pkg", platform),
			)
		}
		if value := os.Getenv("AFTERBURNER_COPILOT_PACKAGE_ROOTS"); value != "" {
			roots = append(roots, filepath.SplitList(value)...)
		}
	}
	roots = append(roots, opts.AdditionalRoots...)
	seenRoots := map[string]bool{}
	seenPackages := map[string]bool{}
	var result []Package
	for _, root := range roots {
		if root == "" {
			continue
		}
		root, _ = filepath.Abs(root)
		key := strings.ToLower(root)
		if seenRoots[key] {
			continue
		}
		seenRoots[key] = true
		entries, err := os.ReadDir(root)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read Copilot package root %s: %w", root, err)
		}
		for _, entry := range entries {
			if !entry.IsDir() || strings.Contains(entry.Name(), "afterburner") {
				continue
			}
			pkg, cached := inspectPackage(root, entry.Name(), cache.Packages)
			nextCache.Packages[strings.ToLower(pkg.Path)] = cached
			identity := pkg.AppSHA256 + ":" + pkg.RuntimeSHA256
			if pkg.Complete && seenPackages[identity] {
				continue
			}
			if pkg.Complete {
				seenPackages[identity] = true
			}
			result = append(result, pkg)
		}
	}
	if opts.HashCachePath != "" {
		_ = saveHashCache(opts.HashCachePath, nextCache)
	}
	return result, nil
}

func SelectNewestComplete(packages []Package) (Package, error) {
	candidates := make([]Package, 0, len(packages))
	for _, pkg := range packages {
		if pkg.Complete {
			candidates = append(candidates, pkg)
		}
	}
	if len(candidates) == 0 {
		return Package{}, fmt.Errorf("no complete Copilot CLI package was found")
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return compareVersion(candidates[i].Version, candidates[j].Version) > 0
	})
	return candidates[0], nil
}

func inspectPackage(root, version string, cache map[string]packageHashCache) (Package, packageHashCache) {
	path := filepath.Join(root, version)
	pkg := Package{Version: version, Path: path, SourceRoot: root}
	previous := cache[strings.ToLower(path)]
	var current packageHashCache
	var appCacheHit, runtimeCacheHit bool
	pkg.AppSHA256, current.App, appCacheHit = cachedHash(filepath.Join(path, "app.js"), previous.App)
	pkg.IndexSHA256, current.Index, _ = cachedHash(filepath.Join(path, "index.js"), previous.Index)
	pkg.PackageSHA256, current.Package, _ = cachedHash(filepath.Join(path, "package.json"), previous.Package)
	pkg.RuntimeSHA256, current.Runtime, runtimeCacheHit = cachedHash(
		filepath.Join(path, "prebuilds", runtimePlatform(), "runtime.node"), previous.Runtime)
	pkg.AppSize = current.App.Size
	pkg.AppModified = current.App.ModifiedUnixNano
	pkg.RuntimeSize = current.Runtime.Size
	pkg.RuntimeModified = current.Runtime.ModifiedUnixNano
	pkg.HashCacheHit = appCacheHit && runtimeCacheHit
	pkg.Complete = pkg.AppSHA256 != "" && pkg.RuntimeSHA256 != ""
	if current.Package == previous.Package && previous.BuildCommit != "" {
		pkg.BuildCommit = previous.BuildCommit
	} else if data, err := os.ReadFile(filepath.Join(path, "package.json")); err == nil {
		var metadata map[string]any
		if json.Unmarshal(data, &metadata) == nil {
			for _, key := range []string{"commit", "buildCommit", "gitCommit"} {
				if value, ok := metadata[key].(string); ok {
					pkg.BuildCommit = value
					break
				}
			}
		}
	}
	current.BuildCommit = pkg.BuildCommit
	return pkg, current
}

func hashFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func cachedHash(path string, previous fileHashCache) (string, fileHashCache, bool) {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return "", fileHashCache{}, false
	}
	current := fileHashCache{Size: info.Size(), ModifiedUnixNano: info.ModTime().UnixNano()}
	if current.Size == previous.Size &&
		current.ModifiedUnixNano == previous.ModifiedUnixNano &&
		previous.SHA256 != "" {
		current.SHA256 = previous.SHA256
		return current.SHA256, current, true
	}
	current.SHA256, _ = hashFile(path)
	return current.SHA256, current, false
}

func loadHashCache(path string) hashCache {
	cache := hashCache{SchemaVersion: 1, Packages: map[string]packageHashCache{}}
	if path == "" {
		return cache
	}
	data, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(data, &cache) != nil ||
		cache.SchemaVersion != 1 || cache.Packages == nil {
		return hashCache{SchemaVersion: 1, Packages: map[string]packageHashCache{}}
	}
	return cache
}

func saveHashCache(path string, cache hashCache) error {
	data, err := json.Marshal(cache)
	if err != nil {
		return err
	}
	if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, data) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".package-hashes-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return platform.ReplaceFile(temporaryPath, path)
}

func compareVersion(left, right string) int {
	parse := func(value string) []int {
		fields := strings.FieldsFunc(value, func(r rune) bool { return r < '0' || r > '9' })
		result := make([]int, len(fields))
		for i, field := range fields {
			result[i], _ = strconv.Atoi(field)
		}
		return result
	}
	a, b := parse(left), parse(right)
	for i := 0; i < len(a) || i < len(b); i++ {
		var av, bv int
		if i < len(a) {
			av = a[i]
		}
		if i < len(b) {
			bv = b[i]
		}
		if av > bv {
			return 1
		}
		if av < bv {
			return -1
		}
	}
	return strings.Compare(left, right)
}

func mapArch(arch string) string {
	if arch == "amd64" {
		return "x64"
	}
	return arch
}

func runtimePlatform() string {
	if runtime.GOOS == "windows" {
		return "win32-" + mapArch(runtime.GOARCH)
	}
	return runtime.GOOS + "-" + mapArch(runtime.GOARCH)
}
