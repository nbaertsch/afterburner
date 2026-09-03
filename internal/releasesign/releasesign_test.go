package releasesign

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"testing"
)

func TestVerifyRejectsTamperingAndWrongSigner(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	manifest := []byte(`{"repository":"nbaertsch/afterburner","version":"v1.0.0"}`)
	signature := []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, manifest)))
	if err := Verify(publicKey, manifest, signature); err != nil {
		t.Fatal(err)
	}
	if err := Verify(publicKey, append(append([]byte{}, manifest...), ' '), signature); err == nil {
		t.Fatal("tampered manifest was accepted")
	}
	otherPublic, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify(otherPublic, manifest, signature); err == nil {
		t.Fatal("signature from an untrusted signer was accepted")
	}
}

func TestEmbeddedPublicKeyIsValid(t *testing.T) {
	key, err := PublicKey()
	if err != nil {
		t.Fatal(err)
	}
	if len(key) != ed25519.PublicKeySize {
		t.Fatalf("public key length = %d", len(key))
	}
}
