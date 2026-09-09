package extensions

import (
	"strings"
	"testing"

	"github.com/nbaertsch/afterburner/internal/registry"
)

func TestValidateCompatibility(t *testing.T) {
	manifest := registry.Manifest{
		ID: "fixture",
		Requires: registry.Requirements{
			Afterburner: ">=0.2.0 <0.3.0",
			CopilotCLI:  []string{">=1.0.80 <1.0.83-3", ">=1.0.83-3 <2.0.0"},
		},
	}
	if err := ValidateCompatibility(manifest, "v0.2.106", "1.0.83-3"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateCompatibility(manifest, "dev", "1.0.83-3"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateCompatibility(manifest, "0.3.0", "1.0.83-3"); err == nil ||
		!strings.Contains(err.Error(), "Afterburner") {
		t.Fatalf("Afterburner incompatibility = %v", err)
	}
	if err := ValidateCompatibility(manifest, "0.2.106", "1.0.79"); err == nil ||
		!strings.Contains(err.Error(), "Copilot CLI") {
		t.Fatalf("Copilot incompatibility = %v", err)
	}
}

func TestValidateCompatibilityRejectsInvalidRanges(t *testing.T) {
	manifest := registry.Manifest{
		ID: "fixture",
		Requires: registry.Requirements{
			Afterburner: ">=0.2.0 || <1.0.0",
		},
	}
	if err := validateCompatibilitySyntax(manifest); err == nil {
		t.Fatal("expected unsupported range syntax to be rejected")
	}
}

func TestCompareVersionsHandlesCopilotBuildComponent(t *testing.T) {
	left, err := parseVersion("1.0.83-3")
	if err != nil {
		t.Fatal(err)
	}
	right, err := parseVersion("1.0.83-2")
	if err != nil {
		t.Fatal(err)
	}
	if got := compareVersionParts(left, right); got <= 0 {
		t.Fatalf("comparison = %d", got)
	}
	left, err = parseVersion("v0.2.106")
	if err != nil {
		t.Fatal(err)
	}
	right, err = parseVersion("0.2.106")
	if err != nil {
		t.Fatal(err)
	}
	if got := compareVersionParts(left, right); got != 0 {
		t.Fatalf("comparison = %d", got)
	}
	releaseCandidate, err := parseVersion("0.3.0-rc.1")
	if err != nil {
		t.Fatal(err)
	}
	release, err := parseVersion("0.3.0")
	if err != nil {
		t.Fatal(err)
	}
	if got := compareVersionParts(releaseCandidate, release); got >= 0 {
		t.Fatalf("prerelease comparison = %d", got)
	}
}
