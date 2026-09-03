package compatibility

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nbaertsch/afterburner/internal/copilot"
)

func TestSelectKnownProfile(t *testing.T) {
	pkg := copilot.Package{
		Version:       "1.0.83-3",
		AppSHA256:     profiles[2].AppSHA256,
		RuntimeSHA256: profiles[2].RuntimeSHA256,
		Complete:      true,
	}
	selected, err := Select([]copilot.Package{pkg})
	if err != nil {
		t.Fatal(err)
	}
	if selected.Profile.ID != profiles[2].ID {
		t.Fatalf("profile = %s", selected.Profile.ID)
	}
}

func TestRejectUnknownProfile(t *testing.T) {
	t.Setenv("AFTERBURNER_ALLOW_UNPROFILED", "")
	_, err := Select([]copilot.Package{{
		Version: "9.9.9", AppSHA256: "unknown", RuntimeSHA256: "unknown", Complete: true,
	}})
	if err == nil {
		t.Fatal("unknown package was accepted")
	}
}

func TestRejectKnownHashesWhenStructuralProbeFails(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "app.js"), []byte("unexpected source"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Select([]copilot.Package{{
		Version:       profiles[2].Version,
		Path:          root,
		AppSHA256:     profiles[2].AppSHA256,
		RuntimeSHA256: profiles[2].RuntimeSHA256,
		Complete:      true,
	}})
	if err == nil || !strings.Contains(err.Error(), "structural probes") {
		t.Fatalf("error = %v", err)
	}
}
