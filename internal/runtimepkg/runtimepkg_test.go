package runtimepkg

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/nbaertsch/afterburner/internal/copilot"
	"github.com/nbaertsch/afterburner/internal/home"
)

func TestEmbeddedRuntimeMatchesCanonicalSource(t *testing.T) {
	canonical, err := os.ReadFile(filepath.Join("..", "..", "src", "app.js"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(runtimeHost, canonical) {
		t.Fatal("embedded runtime host is stale; copy src/app.js to internal/runtimepkg/app.js")
	}
}

func TestPrepareRepairsStalePackage(t *testing.T) {
	layout := home.Layout{CopilotHome: filepath.Join(t.TempDir(), "copilot-home")}
	base := copilot.Package{
		Path:          filepath.Join(t.TempDir(), "base"),
		AppSHA256:     "app",
		RuntimeSHA256: "runtime",
	}
	prepared, err := Prepare(layout, base)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prepared.Path, "package.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	repaired, err := Prepare(layout, base)
	if err != nil {
		t.Fatal(err)
	}
	if repaired != prepared || !validPreparedPackage(repaired.Path, repaired.Version, base) {
		t.Fatal("stale prepared runtime was not repaired")
	}
}
