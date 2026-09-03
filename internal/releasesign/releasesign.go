package releasesign

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
)

const PublicKeyBase64 = "at5fM/ZfyioEEd3zPHEeDOZq17bOOAweiYgZFj94aIA="

func PublicKey() (ed25519.PublicKey, error) {
	decoded, err := base64.StdEncoding.DecodeString(PublicKeyBase64)
	if err != nil || len(decoded) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("embedded release signing key is invalid")
	}
	return ed25519.PublicKey(decoded), nil
}

func Verify(publicKey ed25519.PublicKey, manifest, encodedSignature []byte) error {
	signature, err := base64.StdEncoding.DecodeString(string(encodedSignature))
	if err != nil || len(signature) != ed25519.SignatureSize {
		return fmt.Errorf("release manifest signature is invalid")
	}
	if !ed25519.Verify(publicKey, manifest, signature) {
		return fmt.Errorf("release manifest signature verification failed")
	}
	return nil
}
