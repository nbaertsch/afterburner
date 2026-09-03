package home

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nbaertsch/afterburner/internal/platform"
)

type Layout struct {
	Root              string
	CopilotHome       string
	Config            string
	ExtensionData     string
	Extensions        string
	Staging           string
	BYOModelsConfig   string
	NormalCopilotHome string
}

func Resolve() (Layout, error) {
	userHome, err := os.UserHomeDir()
	if err != nil {
		return Layout{}, fmt.Errorf("resolve user home: %w", err)
	}
	root := os.Getenv("AFTERBURNER_HOME")
	if root == "" {
		root = filepath.Join(userHome, ".afterburner")
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return Layout{}, fmt.Errorf("resolve Afterburner home: %w", err)
	}
	normal := os.Getenv("AFTERBURNER_NORMAL_COPILOT_HOME")
	if normal == "" {
		normal = filepath.Join(userHome, ".copilot")
	}
	return Layout{
		Root:              root,
		CopilotHome:       filepath.Join(root, "copilot-home"),
		Config:            filepath.Join(root, "config"),
		ExtensionData:     filepath.Join(root, "extension-data"),
		Extensions:        filepath.Join(root, "extensions"),
		Staging:           filepath.Join(root, "staging"),
		BYOModelsConfig:   filepath.Join(root, "config", "byomodels.json"),
		NormalCopilotHome: normal,
	}, nil
}

func Initialize() (Layout, error) {
	layout, err := Resolve()
	if err != nil {
		return Layout{}, err
	}

	for _, path := range []string{
		layout.Root, layout.CopilotHome, layout.Config,
		layout.ExtensionData, layout.Extensions, layout.Staging,
	} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return Layout{}, fmt.Errorf("create %s: %w", path, err)
		}
	}
	for _, name := range []string{"settings.json", "permissions-config.json"} {
		source := filepath.Join(layout.NormalCopilotHome, name)
		target := filepath.Join(layout.CopilotHome, name)
		if err := copyMissingFile(source, target); err != nil {
			return Layout{}, err
		}
		if err := removeMatchingInheritedFile(
			filepath.Join(layout.NormalCopilotHome, "mcp-config.json"),
			filepath.Join(layout.CopilotHome, "mcp-config.json"),
		); err != nil {
			return Layout{}, err
		}
	}
	if err := linkSessions(layout); err != nil {
		return Layout{}, err
	}
	return layout, nil
}

func CreateLaunchHome(base Layout) (Layout, func(), error) {
	parent := filepath.Join(base.Root, "launch-homes")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return Layout{}, nil, err
	}
	path, err := os.MkdirTemp(parent, "launch-")
	if err != nil {
		return Layout{}, nil, err
	}
	cleanup := func() { _ = os.RemoveAll(path) }
	layout := base
	layout.CopilotHome = path
	for _, name := range []string{"settings.json", "permissions-config.json", "config.json"} {
		if err := copyMissingFile(filepath.Join(base.CopilotHome, name), filepath.Join(path, name)); err != nil {
			cleanup()
			return Layout{}, nil, err
		}
	}
	if err := linkSessions(layout); err != nil {
		cleanup()
		return Layout{}, nil, err
	}
	return layout, cleanup, nil
}

func removeMatchingInheritedFile(source, target string) error {
	sourceData, sourceErr := os.ReadFile(source)
	targetData, targetErr := os.ReadFile(target)
	if os.IsNotExist(sourceErr) || os.IsNotExist(targetErr) {
		return nil
	}
	if sourceErr != nil {
		return fmt.Errorf("read inherited source %s: %w", source, sourceErr)
	}
	if targetErr != nil {
		return fmt.Errorf("read managed file %s: %w", target, targetErr)
	}
	if bytes.Equal(sourceData, targetData) {
		if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove inherited managed file %s: %w", target, err)
		}
	}
	return nil
}

func ManagedEnvironment(layout Layout, current []string) []string {
	result := replaceEnv(current, "COPILOT_HOME", layout.CopilotHome)
	return replaceEnv(result, "AFTERBURNER_HOME", layout.Root)
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

func copyMissingFile(source, target string) error {
	if _, err := os.Stat(target); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect %s: %w", target, err)
	}
	data, err := os.ReadFile(source)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", source, err)
	}
	if err := os.WriteFile(target, data, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", target, err)
	}
	return nil
}

func linkSessions(layout Layout) error {
	source := filepath.Join(layout.NormalCopilotHome, "session-state")
	target := filepath.Join(layout.CopilotHome, "session-state")
	if _, err := os.Stat(source); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return fmt.Errorf("inspect session state: %w", err)
	}
	if _, err := os.Lstat(target); err == nil {
		sourceInfo, sourceErr := os.Stat(source)
		if sourceErr != nil {
			return fmt.Errorf("inspect session state source: %w", sourceErr)
		}
		targetInfo, targetErr := os.Stat(target)
		if targetErr != nil {
			return fmt.Errorf("resolve managed session link: %w", targetErr)
		}
		if !os.SameFile(sourceInfo, targetInfo) {
			return fmt.Errorf("managed session state is not linked to %s", source)
		}
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect managed session state: %w", err)
	}
	if err := platform.CreateDirectoryLink(source, target); err != nil {
		return fmt.Errorf("create managed session link: %w", err)
	}
	return nil
}
