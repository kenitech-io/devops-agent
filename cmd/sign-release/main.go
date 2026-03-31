// sign-release signs a checksums file with the ed25519 release key.
// Used in the GitHub Actions release workflow.
//
// Usage: RELEASE_SIGNING_KEY=<base64-private-key> go run ./cmd/sign-release checksums.txt
//
// Produces checksums.txt.sig containing the base64-encoded signature.
package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: sign-release <checksums-file>")
		os.Exit(1)
	}

	privKeyB64 := os.Getenv("RELEASE_SIGNING_KEY")
	if privKeyB64 == "" {
		fmt.Fprintln(os.Stderr, "RELEASE_SIGNING_KEY environment variable not set")
		os.Exit(1)
	}

	privKeyBytes, err := base64.StdEncoding.DecodeString(privKeyB64)
	if err != nil {
		fmt.Fprintf(os.Stderr, "decoding private key: %v\n", err)
		os.Exit(1)
	}

	if len(privKeyBytes) != ed25519.PrivateKeySize {
		fmt.Fprintf(os.Stderr, "invalid private key size: got %d, want %d\n", len(privKeyBytes), ed25519.PrivateKeySize)
		os.Exit(1)
	}

	filePath := os.Args[1]
	data, err := os.ReadFile(filePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "reading %s: %v\n", filePath, err)
		os.Exit(1)
	}

	sig := ed25519.Sign(privKeyBytes, data)
	sigB64 := base64.StdEncoding.EncodeToString(sig)

	sigPath := filePath + ".sig"
	if err := os.WriteFile(sigPath, []byte(sigB64), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "writing %s: %v\n", sigPath, err)
		os.Exit(1)
	}

	fmt.Printf("signed %s -> %s\n", filePath, sigPath)
}
