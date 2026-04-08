package signing

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
)

// ReleasePublicKey is the ed25519 public key used to verify release signatures.
// This key is paired with the private key stored in GitHub Secrets (RELEASE_SIGNING_KEY).
// Generate a new key pair with: go run ./cmd/generate-signing-key
var ReleasePublicKey = "9RFDOEVhPakur8ZSc89qphz7a3LLJAAE29oV6kPDIYs="

// VerifyChecksum verifies that the checksum string was signed with the release key.
// The signature is base64-encoded ed25519 signature over the checksum bytes.
func VerifyChecksum(checksum, signatureB64 string) error {
	if ReleasePublicKey == "" {
		// No public key configured (dev builds). Skip verification.
		return nil
	}

	pubKeyBytes, err := base64.StdEncoding.DecodeString(ReleasePublicKey)
	if err != nil {
		return fmt.Errorf("decoding public key: %w", err)
	}

	if len(pubKeyBytes) != ed25519.PublicKeySize {
		return fmt.Errorf("invalid public key size: got %d, want %d", len(pubKeyBytes), ed25519.PublicKeySize)
	}

	sigBytes, err := base64.StdEncoding.DecodeString(signatureB64)
	if err != nil {
		return fmt.Errorf("decoding signature: %w", err)
	}

	if !ed25519.Verify(pubKeyBytes, []byte(checksum), sigBytes) {
		return fmt.Errorf("signature verification failed: checksum was not signed by the release key")
	}

	return nil
}
