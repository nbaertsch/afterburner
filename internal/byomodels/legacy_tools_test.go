package byomodels

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLegacyToolsRemainSessionOwned(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.json")
	value := config{Version: 1, Providers: []provider{{
		Name: "legacy", BaseURL: "https://example.test",
		Auth: auth{Type: "azure-cli"},
		RequestCompatibility: requestCompatibility{
			LegacyTools: true, ForceStreaming: true, MaxInputItemIDLength: 64, ProxyPort: availablePort(t),
		},
	}}}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	manager, err := Start(path)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	if len(manager.services) != 0 {
		t.Fatal("native proxy must not claim schema translation owned by the session proxy")
	}
	legacyIdentity := proxyConfiguration(value.Providers[0])
	value.Providers[0].RequestCompatibility.LegacyTools = false
	if legacyIdentity == proxyConfiguration(value.Providers[0]) {
		t.Fatal("legacy tool settings must participate in proxy identity")
	}
}
