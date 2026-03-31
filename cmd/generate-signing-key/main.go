// generate-signing-key creates an ed25519 key pair for signing releases.
// Run once, then:
//   - Store the private key in GitHub Secrets as RELEASE_SIGNING_KEY
//   - Set the public key in internal/signing/signing.go as ReleasePublicKey
//
// Usage: go run ./cmd/generate-signing-key
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
)

func main() {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error generating key pair: %v\n", err)
		os.Exit(1)
	}

	pubB64 := base64.StdEncoding.EncodeToString(pub)
	privB64 := base64.StdEncoding.EncodeToString(priv)

	fmt.Println("=== Ed25519 Release Signing Key Pair ===")
	fmt.Println()
	fmt.Println("PUBLIC KEY (set in internal/signing/signing.go):")
	fmt.Println(pubB64)
	fmt.Println()
	fmt.Println("PRIVATE KEY (store in GitHub Secrets as RELEASE_SIGNING_KEY):")
	fmt.Println(privB64)
	fmt.Println()
	fmt.Println("IMPORTANT: Save the private key now. It cannot be recovered.")
}
