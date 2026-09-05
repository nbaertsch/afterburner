package governance

import (
	"fmt"
	"time"

	"github.com/nbaertsch/afterburner/internal/registry"
)

type SignatureRequirement string

const (
	SignatureRequiredForBuiltin SignatureRequirement = "required-for-builtin"
	SignatureRequiredForAll     SignatureRequirement = "required-for-all"
	SignatureOptional           SignatureRequirement = "optional"
)

type RevocationStatus string

const (
	RevocationGood    RevocationStatus = "good"
	RevocationRevoked RevocationStatus = "revoked"
	RevocationUnknown RevocationStatus = "unknown"
)

type ProvenanceStatement struct {
	BuilderID     string    `json:"builderId"`
	SourceURI     string    `json:"sourceUri"`
	Commit        string    `json:"commit,omitempty"`
	ManifestHash  string    `json:"manifestHash"`
	TreeHash      string    `json:"treeHash"`
	SBOMHash      string    `json:"sbomHash,omitempty"`
	LicensePolicy string    `json:"licensePolicy,omitempty"`
	IssuedAt      time.Time `json:"issuedAt"`
}

type VerificationPolicy struct {
	SignatureRequirement SignatureRequirement `json:"signatureRequirement"`
	TrustedSigners       []string             `json:"trustedSigners,omitempty"`
	RevokedFingerprints  []string             `json:"revokedFingerprints,omitempty"`
	RequireProvenance    bool                 `json:"requireProvenance"`
}

type VerificationResult struct {
	Allowed    bool             `json:"allowed"`
	Reason     string           `json:"reason,omitempty"`
	Revocation RevocationStatus `json:"revocation"`
}

type Verifier interface {
	Verify(entry registry.Entry, provenance *ProvenanceStatement, policy VerificationPolicy) (VerificationResult, error)
}

type DefaultVerifier struct{}

func (DefaultVerifier) Verify(entry registry.Entry, provenance *ProvenanceStatement, policy VerificationPolicy) (VerificationResult, error) {
	if entry.Identity.IsZero() {
		return VerificationResult{Allowed: false, Reason: "identity binding is missing", Revocation: RevocationUnknown}, nil
	}
	if policy.RequireProvenance && provenance == nil {
		return VerificationResult{Allowed: false, Reason: "provenance statement is required", Revocation: RevocationUnknown}, nil
	}
	if provenance != nil && (provenance.ManifestHash != entry.Identity.ManifestHash || provenance.TreeHash != entry.Identity.TreeHash) {
		return VerificationResult{Allowed: false, Reason: "provenance hash mismatch", Revocation: RevocationUnknown}, nil
	}
	for _, revoked := range policy.RevokedFingerprints {
		if revoked == entry.Identity.SignerFingerprint {
			return VerificationResult{Allowed: false, Reason: "signer fingerprint is revoked", Revocation: RevocationRevoked}, nil
		}
	}
	requiresSignature := policy.SignatureRequirement == SignatureRequiredForAll || (policy.SignatureRequirement == SignatureRequiredForBuiltin && entry.Manifest.Visibility == "builtin")
	if requiresSignature && (entry.Identity.SignerID == "" || entry.Identity.SignerFingerprint == "") {
		return VerificationResult{Allowed: false, Reason: "signature is required", Revocation: RevocationUnknown}, nil
	}
	if entry.Identity.BuiltinSigned {
		if entry.Manifest.Visibility != "builtin" || !registry.IsTrustedBuiltinSourceType(entry.Source.Type) || !registry.IsTrustedBuiltinSourceType(entry.Identity.SourceType) || entry.Source.Value != entry.Manifest.ID || entry.Identity.SourceValue != entry.Manifest.ID {
			return VerificationResult{Allowed: false, Reason: "signed built-in identity source mismatch", Revocation: RevocationUnknown}, nil
		}
		if entry.Identity.SignerID != "afterburner-core" || entry.Identity.SignerFingerprint != "builtin:"+entry.Manifest.ID {
			return VerificationResult{Allowed: false, Reason: "signed built-in signer is not trusted", Revocation: RevocationUnknown}, nil
		}
	}
	if len(policy.TrustedSigners) > 0 {
		trusted := false
		for _, signer := range policy.TrustedSigners {
			if signer == entry.Identity.SignerID || signer == entry.Identity.SignerFingerprint {
				trusted = true
				break
			}
		}
		if !trusted {
			return VerificationResult{Allowed: false, Reason: "signer is not trusted", Revocation: RevocationUnknown}, nil
		}
	}
	return VerificationResult{Allowed: true, Reason: "verified", Revocation: RevocationGood}, nil
}

type GateDescriptor struct {
	ID       string   `json:"id"`
	Kind     string   `json:"kind"`
	Severity string   `json:"severity"`
	Blocks   bool     `json:"blocks"`
	Inputs   []string `json:"inputs,omitempty"`
}

type SupplyChainGates struct {
	SBOM          GateDescriptor `json:"sbom"`
	License       GateDescriptor `json:"license"`
	Vulnerability GateDescriptor `json:"vulnerability"`
}

func DefaultSupplyChainGates() SupplyChainGates {
	return SupplyChainGates{
		SBOM:          GateDescriptor{ID: "ui.gate.sbom", Kind: "sbom", Severity: "high", Blocks: true, Inputs: []string{"spdx", "cyclonedx"}},
		License:       GateDescriptor{ID: "ui.gate.license", Kind: "license", Severity: "high", Blocks: true, Inputs: []string{"declaredLicense", "dependencyLicense"}},
		Vulnerability: GateDescriptor{ID: "ui.gate.vulnerability", Kind: "vulnerability", Severity: "critical", Blocks: true, Inputs: []string{"osv", "ghsa", "cve"}},
	}
}

func ValidateSupplyChainGates(gates SupplyChainGates) error {
	for _, gate := range []GateDescriptor{gates.SBOM, gates.License, gates.Vulnerability} {
		if gate.ID == "" || gate.Kind == "" {
			return fmt.Errorf("supply-chain gate descriptor is incomplete")
		}
	}
	return nil
}
