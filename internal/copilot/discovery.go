package copilot

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

type Package struct {
	Version       string `json:"version"`
	Path          string `json:"path"`
	SourceRoot    string `json:"sourceRoot"`
	BuildCommit   string `json:"buildCommit,omitempty"`
	AppSHA256     string `json:"appSha256"`
	IndexSHA256   string `json:"indexSha256,omitempty"`
	PackageSHA256 string `json:"packageSha256,omitempty"`
	RuntimeSHA256 string `json:"runtimeSha256"`
	Complete      bool   `json:"complete"`
}

type DiscoveryOptions struct {
	ManagedHome       string
	CopilotExecutable string
	AdditionalRoots   []string
}

func Discover(opts DiscoveryOptions) ([]Package, error) {
	platform := "win32-" + mapArch(runtime.GOARCH)
	userHome, _ := os.UserHomeDir()
	roots := []string{
		filepath.Join(userHome, ".copilot", "pkg", platform),
		filepath.Join(os.Getenv("LOCALAPPDATA"), "copilot", "pkg", platform),
		filepath.Join(opts.ManagedHome, "pkg", platform),
	}
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
			pkg := inspectPackage(root, entry.Name())
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

func inspectPackage(root, version string) Package {
	path := filepath.Join(root, version)
	pkg := Package{Version: version, Path: path, SourceRoot: root}
	pkg.AppSHA256, _ = hashFile(filepath.Join(path, "app.js"))
	pkg.IndexSHA256, _ = hashFile(filepath.Join(path, "index.js"))
	pkg.PackageSHA256, _ = hashFile(filepath.Join(path, "package.json"))
	pkg.RuntimeSHA256, _ = hashFile(filepath.Join(path, "prebuilds", runtimePlatform(), "runtime.node"))
	pkg.Complete = pkg.AppSHA256 != "" && pkg.RuntimeSHA256 != ""
	if data, err := os.ReadFile(filepath.Join(path, "package.json")); err == nil {
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
	return pkg
}

func hashFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
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
