package installer

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/nbaertsch/afterburner/internal/home"
)

func TestInstallReplacesAndRetainsPreviousBinary(t *testing.T) {
	t.Setenv("AFTERBURNER_SKIP_PATH_UPDATE", "1")
	root := t.TempDir()
	source := filepath.Join(root, "source.exe")
	if err := os.WriteFile(source, []byte("version-one"), 0o700); err != nil {
		t.Fatal(err)
	}
	layout := home.Layout{Root: filepath.Join(root, "home")}
	if _, err := Install(layout, source, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(layout.Root, "bin", "afterburn.cmd")
	if err := os.WriteFile(legacy, []byte("legacy"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("version-two"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(layout, source, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("legacy launcher still exists: %v", err)
	}
	previous, err := os.ReadFile(filepath.Join(layout.Root, "bin", "afterburn.previous.exe"))
	if err != nil {
		t.Fatal(err)
	}
	if string(previous) != "version-one" {
		t.Fatalf("previous binary = %q", previous)
	}
}
