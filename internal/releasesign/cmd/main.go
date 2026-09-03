package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"strings"
)

func main() {
	if len(os.Args) < 2 {
		fail("usage: go run ./internal/releasesign/cmd generate <private-key-file> | sign <private-key-file> <manifest> <signature>")
	}
	switch os.Args[1] {
	case "generate":
		if len(os.Args) != 3 {
			fail("generate requires a private-key file")
		}
		publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			fail(err.Error())
		}
		encoded := base64.StdEncoding.EncodeToString(privateKey.Seed())
		if err := os.WriteFile(os.Args[2], []byte(encoded), 0o600); err != nil {
			fail(err.Error())
		}
		fmt.Println(base64.StdEncoding.EncodeToString(publicKey))
	case "sign":
		if len(os.Args) != 5 {
			fail("sign requires a private-key file, manifest, and signature path")
		}
		keyData, err := os.ReadFile(os.Args[2])
		if err != nil {
			fail(err.Error())
		}
		seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(keyData)))
		if err != nil || len(seed) != ed25519.SeedSize {
			fail("private release signing key is invalid")
		}
		manifest, err := os.ReadFile(os.Args[3])
		if err != nil {
			fail(err.Error())
		}
		signature := ed25519.Sign(ed25519.NewKeyFromSeed(seed), manifest)
		if err := os.WriteFile(os.Args[4], []byte(base64.StdEncoding.EncodeToString(signature)), 0o600); err != nil {
			fail(err.Error())
		}
	default:
		fail("unknown command")
	}
}

func fail(message string) {
	fmt.Fprintln(os.Stderr, message)
	os.Exit(1)
}
