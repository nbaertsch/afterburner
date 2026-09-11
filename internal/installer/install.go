package installer

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nbaertsch/afterburner/internal/home"
	"github.com/nbaertsch/afterburner/internal/platform"
)

type Result struct {
	Path   string
	SHA256 string
}

func Install(layout home.Layout, executable string, stdout io.Writer) (Result, error) {
	if err := os.MkdirAll(layout.Root, 0o700); err != nil {
		return Result{}, fmt.Errorf("create Afterburner home: %w", err)
	}
	release, err := platform.AcquireDirectoryLock(filepath.Join(layout.Root, ".core.lock"), 2*time.Minute)
	if err != nil {
		return Result{}, err
	}
	defer release()
	executable, err = filepath.Abs(executable)
	if err != nil {
		return Result{}, err
	}
	data, err := os.ReadFile(executable)
	if err != nil {
		return Result{}, fmt.Errorf("read running executable: %w", err)
	}
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	bin := filepath.Join(layout.Root, "bin")
	target := filepath.Join(bin, "afterburn.exe")
	previous := filepath.Join(bin, "afterburn.previous.exe")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		return Result{}, fmt.Errorf("create install directory: %w", err)
	}
	if !samePath(executable, target) {
		temporary, err := os.CreateTemp(bin, ".afterburn-*.exe")
		if err != nil {
			return Result{}, err
		}
		temporaryPath := temporary.Name()
		defer os.Remove(temporaryPath)
		if err := temporary.Chmod(0o700); err != nil {
			temporary.Close()
			return Result{}, err
		}
		if _, err := temporary.Write(data); err != nil {
			temporary.Close()
			return Result{}, err
		}
		if err := temporary.Sync(); err != nil {
			temporary.Close()
			return Result{}, err
		}
		if err := temporary.Close(); err != nil {
			return Result{}, err
		}
		if current, err := os.ReadFile(target); err == nil {
			if err := os.WriteFile(previous, current, 0o700); err != nil {
				return Result{}, fmt.Errorf("retain previous executable: %w", err)
			}
		} else if !os.IsNotExist(err) {
			return Result{}, err
		}
		retiredExecutable, err := platform.RetiredFilePath(target)
		if err != nil {
			return Result{}, fmt.Errorf("prepare retired executable path: %w", err)
		}
		if err := platform.ReplaceFileRetiring(temporaryPath, target, retiredExecutable); err != nil {
			return Result{}, fmt.Errorf("install executable: %w", err)
		}
	}
	installed, err := os.ReadFile(target)
	if err != nil {
		return Result{}, fmt.Errorf("verify installed executable: %w", err)
	}
	installedSum := sha256.Sum256(installed)
	if installedSum != sum {
		return Result{}, fmt.Errorf("installed executable hash mismatch")
	}
	if os.Getenv("AFTERBURNER_SKIP_PATH_UPDATE") != "1" {
		if err := platform.EnsureUserPath(bin); err != nil {
			return Result{}, err
		}
	}
	for _, legacy := range []string{"afterburn.cmd", "afterburn.ps1"} {
		if err := os.Remove(filepath.Join(bin, legacy)); err != nil && !os.IsNotExist(err) {
			return Result{}, fmt.Errorf("remove legacy launcher %s: %w", legacy, err)
		}
	}
	fmt.Fprintf(stdout, "Installed afterburn.exe at %s\nSHA-256: %s\n", target, hash)
	return Result{Path: target, SHA256: hash}, nil
}

func samePath(left, right string) bool {
	return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
}
