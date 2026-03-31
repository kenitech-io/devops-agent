package signing

import (
	"crypto/ed25519"
	"encoding/base64"
	"testing"
)

func TestVerifyChecksum_Valid(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}

	// Set the test public key
	origKey := ReleasePublicKey
	ReleasePublicKey = base64.StdEncoding.EncodeToString(pub)
	defer func() { ReleasePublicKey = origKey }()

	checksum := "sha256:abcdef1234567890"
	sig := ed25519.Sign(priv, []byte(checksum))
	sigB64 := base64.StdEncoding.EncodeToString(sig)

	if err := VerifyChecksum(checksum, sigB64); err != nil {
		t.Errorf("expected valid signature, got error: %v", err)
	}
}

func TestVerifyChecksum_InvalidSignature(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}

	origKey := ReleasePublicKey
	ReleasePublicKey = base64.StdEncoding.EncodeToString(pub)
	defer func() { ReleasePublicKey = origKey }()

	checksum := "sha256:abcdef1234567890"
	// Use a fake signature
	fakeSig := make([]byte, ed25519.SignatureSize)
	sigB64 := base64.StdEncoding.EncodeToString(fakeSig)

	err = VerifyChecksum(checksum, sigB64)
	if err == nil {
		t.Error("expected signature verification to fail")
	}
}

func TestVerifyChecksum_TamperedChecksum(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}

	origKey := ReleasePublicKey
	ReleasePublicKey = base64.StdEncoding.EncodeToString(pub)
	defer func() { ReleasePublicKey = origKey }()

	original := "sha256:abcdef1234567890"
	sig := ed25519.Sign(priv, []byte(original))
	sigB64 := base64.StdEncoding.EncodeToString(sig)

	// Verify with tampered checksum
	tampered := "sha256:0000000000000000"
	err = VerifyChecksum(tampered, sigB64)
	if err == nil {
		t.Error("expected verification to fail for tampered checksum")
	}
}

func TestVerifyChecksum_NoPublicKey(t *testing.T) {
	origKey := ReleasePublicKey
	ReleasePublicKey = ""
	defer func() { ReleasePublicKey = origKey }()

	// Should skip verification when no public key is configured
	if err := VerifyChecksum("sha256:anything", "anything"); err != nil {
		t.Errorf("expected no error when public key is empty, got: %v", err)
	}
}

func TestVerifyChecksum_InvalidBase64Signature(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}

	origKey := ReleasePublicKey
	ReleasePublicKey = base64.StdEncoding.EncodeToString(pub)
	defer func() { ReleasePublicKey = origKey }()

	err = VerifyChecksum("sha256:abc", "not-valid-base64!!!")
	if err == nil {
		t.Error("expected error for invalid base64 signature")
	}
}
