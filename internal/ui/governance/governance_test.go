package governance

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nbaertsch/afterburner/internal/registry"
)

func TestIdentityBindingDetectsManifestAndSourceMismatch(t *testing.T) {
	root := t.TempDir()
	manifestJSON := []byte(`{"schemaVersion":1,"id":"fixture","displayName":"Fixture","visibility":"builtin","requires":{"afterburner":">=0.1.0"},"runtime":{"execution":"in-process","entrypoint":"runtime.mjs"}}`)
	if err := os.WriteFile(filepath.Join(root, "afterburner.json"), manifestJSON, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "runtime.mjs"), []byte("export default {}"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := registry.Manifest{ID: "fixture", Visibility: "builtin"}
	source := registry.Source{Type: "embedded", Value: "fixture", Version: "local-test"}
	binding, err := BindIdentity(IdentityInput{Manifest: manifest, Source: source, PackageRoot: root, Signer: Signer{ID: "afterburner-core", Fingerprint: "builtin:fixture"}, BuiltinDefault: true, RegistryEpoch: 7, GrantEpoch: 3, BoundAt: time.Date(2026, 9, 4, 20, 21, 10, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	entry := registry.Entry{Manifest: manifest, Source: source, Identity: binding}
	if err := VerifyIdentity(entry, root); err != nil {
		t.Fatalf("verify identity: %v", err)
	}
	entry.Identity.SourceValue = "other"
	if err := VerifyIdentity(entry, root); err == nil {
		t.Fatal("expected source mismatch to be rejected")
	}
	entry.Identity = binding
	if err := os.WriteFile(filepath.Join(root, "runtime.mjs"), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyIdentity(entry, root); err == nil {
		t.Fatal("expected tree hash mismatch to be rejected")
	}
}

func TestProvenanceRevocationAndGateDescriptors(t *testing.T) {
	entry := registry.Entry{Manifest: registry.Manifest{ID: "fixture", Visibility: "builtin"}, Source: registry.Source{Type: "embedded", Value: "fixture"}, Identity: registry.IdentityBinding{ExtensionID: "fixture", ManifestHash: "sha256:m", TreeHash: "sha256:t", SourceType: "embedded", SourceValue: "fixture", SignerID: "afterburner-core", SignerFingerprint: "builtin:fixture", BuiltinSigned: true}}
	provenance := &ProvenanceStatement{BuilderID: "builder", SourceURI: "builtin:fixture", ManifestHash: "sha256:m", TreeHash: "sha256:t", IssuedAt: time.Now().UTC()}
	result, err := DefaultVerifier{}.Verify(entry, provenance, VerificationPolicy{SignatureRequirement: SignatureRequiredForBuiltin, TrustedSigners: []string{"afterburner-core"}, RequireProvenance: true})
	if err != nil || !result.Allowed || result.Revocation != RevocationGood {
		t.Fatalf("verification = %#v err=%v", result, err)
	}
	result, err = DefaultVerifier{}.Verify(entry, provenance, VerificationPolicy{SignatureRequirement: SignatureRequiredForBuiltin, RevokedFingerprints: []string{"builtin:fixture"}, RequireProvenance: true})
	if err != nil || result.Allowed || result.Revocation != RevocationRevoked {
		t.Fatalf("revocation = %#v err=%v", result, err)
	}
	forged := entry
	forged.Source = registry.Source{Type: "path", Value: filepath.Join(t.TempDir(), "fixture")}
	forged.Identity.SourceType = "path"
	forged.Identity.SourceValue = forged.Source.Value
	result, err = DefaultVerifier{}.Verify(forged, provenance, VerificationPolicy{SignatureRequirement: SignatureRequiredForBuiltin, TrustedSigners: []string{"afterburner-core"}, RequireProvenance: true})
	if err != nil || result.Allowed || result.Reason != "signed built-in identity source mismatch" {
		t.Fatalf("forged source verification = %#v err=%v", result, err)
	}
	if err := ValidateSupplyChainGates(DefaultSupplyChainGates()); err != nil {
		t.Fatal(err)
	}
}
