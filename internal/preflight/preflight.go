package preflight

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/nbaertsch/afterburner/internal/compatibility"
	"github.com/nbaertsch/afterburner/internal/copilot"
	"github.com/nbaertsch/afterburner/internal/home"
	"github.com/nbaertsch/afterburner/internal/platform"
	"github.com/nbaertsch/afterburner/internal/runtimepkg"
)

type Tuple struct {
	SchemaVersion       int             `json:"schemaVersion"`
	Package             copilot.Package `json:"package"`
	ProfileID           string          `json:"profileId"`
	RuntimeVersion      string          `json:"runtimeVersion"`
	RuntimePath         string          `json:"runtimePath"`
	RegistryFingerprint string          `json:"registryFingerprint"`
	ValidatedAt         time.Time       `json:"validatedAt"`
}

type Result struct {
	Package      copilot.Package
	Profile      compatibility.Profile
	Prepared     runtimepkg.Prepared
	UsedFallback bool
	Failure      string
}

func Ensure(
	ctx context.Context,
	layout home.Layout,
	executable string,
	selection compatibility.Selection,
	prepared runtimepkg.Prepared,
	env []string,
	stderr io.Writer,
) (Result, error) {
	fingerprint, err := registryFingerprint(layout.Root)
	if err != nil {
		return Result{}, err
	}
	if os.Getenv("AFTERBURNER_SKIP_PREFLIGHT") == "1" {
		return Result{Package: selection.Package, Profile: selection.Profile, Prepared: prepared}, nil
	}
	state, _ := load(layout.Root)
	if tupleMatches(state, selection, prepared, fingerprint) {
		return Result{Package: selection.Package, Profile: selection.Profile, Prepared: prepared}, nil
	}
	if err := validate(ctx, executable, prepared.Version, env); err == nil {
		tuple := Tuple{
			SchemaVersion:       1,
			Package:             selection.Package,
			ProfileID:           selection.Profile.ID,
			RuntimeVersion:      prepared.Version,
			RuntimePath:         prepared.Path,
			RegistryFingerprint: fingerprint,
			ValidatedAt:         time.Now().UTC(),
		}
		if err := save(layout.Root, tuple); err != nil {
			return Result{}, err
		}
		return Result{Package: selection.Package, Profile: selection.Profile, Prepared: prepared}, nil
	} else {
		failure := err.Error()
		fallback, loadErr := load(layout.Root)
		if loadErr != nil {
			return Result{}, fmt.Errorf("runtime preflight failed (%s) and no last-known-good tuple is available: %w", failure, loadErr)
		}
		if validateErr := validateTuple(fallback); validateErr != nil {
			return Result{}, fmt.Errorf("runtime preflight failed (%s) and last-known-good tuple is invalid: %w", failure, validateErr)
		}
		if fallback.RegistryFingerprint != fingerprint {
			return Result{}, fmt.Errorf("runtime preflight failed (%s) and the last-known-good tuple was validated with a different extension registry", failure)
		}
		profile, ok := compatibility.FindProfile(fallback.ProfileID)
		if !ok {
			return Result{}, fmt.Errorf("last-known-good compatibility profile %q is unavailable", fallback.ProfileID)
		}
		fmt.Fprintf(stderr, "Warning: Copilot %s failed Afterburner preflight; falling back to validated %s. Run 'afterburn doctor'.\n", selection.Package.Version, fallback.Package.Version)
		return Result{
			Package:      fallback.Package,
			Profile:      profile,
			Prepared:     runtimepkg.Prepared{Version: fallback.RuntimeVersion, Path: fallback.RuntimePath},
			UsedFallback: true,
			Failure:      failure,
		}, nil
	}
}

func validate(ctx context.Context, executable, runtimeVersion string, env []string) error {
	testContext, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(testContext, executable, "--prefer-version", runtimeVersion, "--version")
	cmd.Env = replaceEnv(env, "COPILOT_RUNTIME_EXTENSION_SELF_TEST", "1")
	var stdout, stderr limitedBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if testContext.Err() != nil {
			return fmt.Errorf("self-test timed out")
		}
		return fmt.Errorf("self-test exited unsuccessfully: %w: %s", err, stderr.String())
	}
	var output struct {
		Projection struct {
			HasReasoningColumn bool `json:"hasReasoningColumn"`
			HasContextColumn   bool `json:"hasContextColumn"`
		} `json:"projection"`
		RuntimeObservers any `json:"runtimeObservers"`
	}
	data := stdout.Bytes()
	start := bytes.IndexByte(data, '{')
	if start < 0 || json.Unmarshal(data[start:], &output) != nil {
		return fmt.Errorf("self-test did not return valid JSON")
	}
	if !output.Projection.HasReasoningColumn || !output.Projection.HasContextColumn || output.RuntimeObservers == nil {
		return fmt.Errorf("self-test runtime seams are incomplete")
	}
	return nil
}

func tupleMatches(tuple Tuple, selection compatibility.Selection, prepared runtimepkg.Prepared, fingerprint string) bool {
	return tuple.SchemaVersion == 1 &&
		tuple.ProfileID == selection.Profile.ID &&
		tuple.Package.Path == selection.Package.Path &&
		strings.EqualFold(tuple.Package.AppSHA256, selection.Package.AppSHA256) &&
		strings.EqualFold(tuple.Package.RuntimeSHA256, selection.Package.RuntimeSHA256) &&
		tuple.Package.AppSize == selection.Package.AppSize &&
		tuple.Package.AppModified == selection.Package.AppModified &&
		tuple.Package.RuntimeSize == selection.Package.RuntimeSize &&
		tuple.Package.RuntimeModified == selection.Package.RuntimeModified &&
		tuple.RuntimeVersion == prepared.Version &&
		tuple.RuntimePath == prepared.Path &&
		tuple.RegistryFingerprint == fingerprint &&
		validateTuple(tuple) == nil
}

func validateTuple(tuple Tuple) error {
	if tuple.SchemaVersion != 1 || tuple.Package.Path == "" || tuple.RuntimePath == "" {
		return fmt.Errorf("tuple is incomplete")
	}
	appPath := filepath.Join(tuple.Package.Path, "app.js")
	if !metadataMatches(appPath, tuple.Package.AppSize, tuple.Package.AppModified) {
		appHash, err := hashFile(appPath)
		if err != nil || !strings.EqualFold(appHash, tuple.Package.AppSHA256) {
			return fmt.Errorf("base app.js no longer matches")
		}
	}
	runtimePath := filepath.Join(tuple.Package.Path, "prebuilds", runtimePlatform(), "runtime.node")
	if !metadataMatches(runtimePath, tuple.Package.RuntimeSize, tuple.Package.RuntimeModified) {
		runtimeHash, err := hashFile(runtimePath)
		if err != nil || !strings.EqualFold(runtimeHash, tuple.Package.RuntimeSHA256) {
			return fmt.Errorf("base runtime.node no longer matches")
		}
	}

	if _, err := os.Stat(filepath.Join(tuple.RuntimePath, "app.js")); err != nil {
		return fmt.Errorf("prepared runtime is unavailable: %w", err)
	}
	return nil
}

func metadataMatches(path string, size, modified int64) bool {
	if size <= 0 || modified == 0 {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() &&
		info.Size() == size && info.ModTime().UnixNano() == modified
}

func load(root string) (Tuple, error) {
	data, err := os.ReadFile(statePath(root))
	if err != nil {
		return Tuple{}, err
	}
	var tuple Tuple
	if err := json.Unmarshal(data, &tuple); err != nil {
		return Tuple{}, err
	}
	return tuple, nil
}

func save(root string, tuple Tuple) error {
	path := statePath(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(tuple, "", "  ")
	data = append(data, '\n')
	temporary, err := os.CreateTemp(filepath.Dir(path), ".last-known-good-*.tmp")
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

func statePath(root string) string {
	return filepath.Join(root, "state", "last-known-good.json")
}

func ValidateStored(root string) error {
	tuple, err := load(root)
	if err != nil {
		return err
	}
	return validateTuple(tuple)
}

func registryFingerprint(root string) (string, error) {
	data, err := os.ReadFile(filepath.Join(root, "registry.json"))
	if os.IsNotExist(err) {
		data = []byte("{}")
	} else if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func hashFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func runtimePlatform() string {
	if runtime.GOOS == "windows" {
		arch := runtime.GOARCH
		if arch == "amd64" {
			arch = "x64"
		}
		return "win32-" + arch
	}
	return runtime.GOOS + "-" + runtime.GOARCH
}

func replaceEnv(env []string, key, value string) []string {
	prefix := strings.ToUpper(key) + "="
	result := make([]string, 0, len(env)+1)
	for _, item := range env {
		if !strings.HasPrefix(strings.ToUpper(item), prefix) {
			result = append(result, item)
		}
	}
	return append(result, key+"="+value)
}

type limitedBuffer struct {
	bytes.Buffer
}

func (b *limitedBuffer) Write(data []byte) (int, error) {
	const maximum = 2 << 20
	remaining := maximum - b.Len()
	if remaining <= 0 {
		return len(data), nil
	}
	if len(data) > remaining {
		_, _ = b.Buffer.Write(data[:remaining])
		return len(data), nil
	}
	return b.Buffer.Write(data)
}
