package releasesign

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReleaseWorkflowKeepsSigningKeyOutOfTagSelectedJobs(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	workflow := string(data)
	if !strings.Contains(workflow, "workflow_dispatch:") || strings.Contains(workflow, "\n  push:") {
		t.Fatal("release signing must only be dispatched from a trusted workflow ref")
	}
	if !strings.Contains(workflow, "environment: release-signing") ||
		!strings.Contains(workflow, "ref: main") ||
		!strings.Contains(workflow, `$env:GITHUB_REF -ne "refs/heads/main"`) {
		t.Fatal("release signing job is not constrained to the protected main environment")
	}
	build, sign, ok := strings.Cut(workflow, "  sign-and-publish:")
	if !ok {
		t.Fatal("release workflow is missing a separate signing job")
	}
	if strings.Contains(build, "AFTERBURNER_RELEASE_SIGNING_KEY") {
		t.Fatal("unsigned build job can access the release signing key")
	}
	if !strings.Contains(sign, "AFTERBURNER_RELEASE_SIGNING_KEY") {
		t.Fatal("trusted signing job does not use the protected signing key")
	}
	if strings.Contains(workflow, `"${{ inputs.tag }}"`) ||
		strings.Contains(workflow, `"${{ secrets.AFTERBURNER_RELEASE_SIGNING_KEY }}"`) {
		t.Fatal("untrusted workflow data is interpolated into PowerShell source")
	}
	if !strings.Contains(workflow, "RELEASE_TAG: ${{ inputs.tag }}") ||
		!strings.Contains(workflow, "RELEASE_SIGNING_KEY: ${{ secrets.AFTERBURNER_RELEASE_SIGNING_KEY }}") ||
		!strings.Contains(workflow, "-notmatch '^v[0-9]+\\.[0-9]+\\.[0-9]+") {
		t.Fatal("release workflow does not pass and validate untrusted values as data")
	}
	if !strings.Contains(build, "Remove-Item Env:GOOS") ||
		!strings.Contains(build, "Remove-Item Env:GOARCH") ||
		!strings.Contains(build, "go run ./internal/releaseinfo") {
		t.Fatal("release compatibility metadata may execute under the cross-compilation environment")
	}
	if !strings.Contains(build, "schemaVersion = 1") ||
		!strings.Contains(build, "compatibility = $compatibility") {
		t.Fatal("release manifest must remain readable by legacy updaters while carrying the additive compatibility tuple")
	}
}
