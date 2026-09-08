package sessions

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/nbaertsch/afterburner/internal/home"
	"github.com/nbaertsch/afterburner/internal/platform"
	"github.com/nbaertsch/afterburner/internal/registry"
)

const (
	openAIServerPlugin       = "afterburner-openai-server"
	legacyOpenAIServerPlugin = "afterburner-copilot-openai"
)

func Reconcile(layout home.Layout, value registry.Registry) error {
	configPath := filepath.Join(layout.CopilotHome, "config.json")
	config := map[string]any{}
	prefix := ""
	if data, err := os.ReadFile(configPath); err == nil {
		body, preservedPrefix, splitErr := splitConfig(data)
		if splitErr != nil {
			return fmt.Errorf("parse managed Copilot config: %w", splitErr)
		}
		prefix = preservedPrefix
		if err := json.Unmarshal(body, &config); err != nil {
			return fmt.Errorf("parse managed Copilot config: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read managed Copilot config: %w", err)
	}
	existing, _ := config["installedPlugins"].([]any)
	existingByNameAndPath := map[string]map[string]any{}
	for _, raw := range existing {
		plugin, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		name, _ := plugin["name"].(string)
		cachePath, _ := plugin["cache_path"].(string)
		existingByNameAndPath[name+"\x00"+cachePath] = plugin
	}

	desiredNames := map[string]bool{}
	var desired []any
	ids := make([]string, 0, len(value.Extensions))
	for id := range value.Extensions {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		entry := value.Extensions[id]
		if !entry.Enabled || entry.Manifest.SessionExtension == nil {
			continue
		}
		if !registry.Within(entry.ActivePath, layout.Extensions) {
			return fmt.Errorf("session extension %q escapes the managed package root", id)
		}
		manifestPath := filepath.Join(entry.ActivePath, "plugin.json")
		data, err := os.ReadFile(manifestPath)
		if err != nil {
			return fmt.Errorf("read session manifest for %q: %w", id, err)
		}
		var pluginManifest struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		}
		if err := json.Unmarshal(data, &pluginManifest); err != nil {
			return fmt.Errorf("parse session manifest for %q: %w", id, err)
		}
		if pluginManifest.Name == "" || pluginManifest.Version == "" {
			return fmt.Errorf("session manifest for %q is missing name or version", id)
		}
		desiredNames[pluginManifest.Name] = true
		installedAt := time.Now().UTC().Format(time.RFC3339Nano)
		if previous := existingByNameAndPath[pluginManifest.Name+"\x00"+entry.ActivePath]; previous != nil {
			if value, ok := previous["installed_at"].(string); ok && value != "" {
				installedAt = value
			}
		}
		desired = append(desired, map[string]any{
			"name":         pluginManifest.Name,
			"marketplace":  "",
			"version":      pluginManifest.Version,
			"installed_at": installedAt,
			"cache_path":   entry.ActivePath,
			"enabled":      true,
			"source": map[string]any{
				"source": "local",
				"path":   entry.ActivePath,
			},
		})
	}

	retained := make([]any, 0, len(existing))
	for _, raw := range existing {
		plugin, ok := raw.(map[string]any)
		if !ok {
			retained = append(retained, raw)
			continue
		}
		name, _ := plugin["name"].(string)
		cachePath, _ := plugin["cache_path"].(string)
		sourcePath := ""
		if source, ok := plugin["source"].(map[string]any); ok {
			sourcePath, _ = source["path"].(string)
		}
		if desiredNames[name] || registry.Within(cachePath, layout.Extensions) || registry.Within(sourcePath, layout.Extensions) {
			continue
		}
		retained = append(retained, raw)
	}
	config["installedPlugins"] = append(retained, desired...)
	migrateOpenAIServerPluginState(config, desiredNames)
	return saveConfig(configPath, prefix, config)
}

func migrateOpenAIServerPluginState(config map[string]any, desiredNames map[string]bool) {
	if !desiredNames[openAIServerPlugin] {
		return
	}
	enabled, ok := config["enabledPlugins"].(map[string]any)
	if !ok {
		return
	}
	if _, hasLegacy := enabled[legacyOpenAIServerPlugin]; !hasLegacy {
		return
	}
	if _, hasCanonical := enabled[openAIServerPlugin]; !hasCanonical {
		enabled[openAIServerPlugin] = enabled[legacyOpenAIServerPlugin]
	}
	delete(enabled, legacyOpenAIServerPlugin)
}

func splitConfig(data []byte) ([]byte, string, error) {
	for i, value := range data {
		if value == '{' {
			return data[i:], string(data[:i]), nil
		}
	}
	return nil, "", fmt.Errorf("JSON object body is missing")
}

func saveConfig(path, prefix string, value map[string]any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(append([]byte(prefix), data...), '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".config-*.tmp")
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
