package update

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestVerifyChecksum_Valid(t *testing.T) {
	content := []byte("test binary content")
	hash := sha256.Sum256(content)
	expectedChecksum := "sha256:" + hex.EncodeToString(hash[:])

	tmpFile, err := os.CreateTemp("", "keni-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.Write(content); err != nil {
		t.Fatal(err)
	}
	tmpFile.Close()

	if err := verifyChecksum(tmpFile.Name(), expectedChecksum); err != nil {
		t.Errorf("expected valid checksum, got error: %v", err)
	}
}

func TestVerifyChecksum_Invalid(t *testing.T) {
	content := []byte("test binary content")
	tmpFile, err := os.CreateTemp("", "keni-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.Write(content); err != nil {
		t.Fatal(err)
	}
	tmpFile.Close()

	err = verifyChecksum(tmpFile.Name(), "sha256:0000000000000000000000000000000000000000000000000000000000000000")
	if err == nil {
		t.Error("expected checksum mismatch error")
	}
}

func TestVerifyChecksum_BadFormat(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "keni-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	err = verifyChecksum(tmpFile.Name(), "md5:abcdef")
	if err == nil {
		t.Error("expected unsupported format error")
	}
}

func TestDownloadBinary(t *testing.T) {
	content := []byte("fake binary data")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(content)
	}))
	defer server.Close()

	tmpFile, err := os.CreateTemp("", "keni-test-*")
	if err != nil {
		t.Fatal(err)
	}
	tmpFile.Close()
	defer os.Remove(tmpFile.Name())

	if err := downloadBinary(server.URL, tmpFile.Name()); err != nil {
		t.Fatalf("downloadBinary error: %v", err)
	}

	data, err := os.ReadFile(tmpFile.Name())
	if err != nil {
		t.Fatal(err)
	}

	if string(data) != string(content) {
		t.Errorf("expected %q, got %q", content, data)
	}

	// Check file is executable
	info, _ := os.Stat(tmpFile.Name())
	if info.Mode().Perm()&0100 == 0 {
		t.Error("expected executable permission on downloaded binary")
	}
}

func TestDownloadBinary_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	tmpFile, err := os.CreateTemp("", "keni-test-*")
	if err != nil {
		t.Fatal(err)
	}
	tmpFile.Close()
	defer os.Remove(tmpFile.Name())

	err = downloadBinary(server.URL, tmpFile.Name())
	if err == nil {
		t.Error("expected error for server error response")
	}
}

func TestReplaceBinary(t *testing.T) {
	// Create source file
	srcFile, err := os.CreateTemp("", "keni-src-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(srcFile.Name())
	srcFile.Write([]byte("new binary"))
	srcFile.Close()

	// Create dest file
	dstFile, err := os.CreateTemp("", "keni-dst-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(dstFile.Name())
	dstFile.Write([]byte("old binary"))
	dstFile.Close()

	if err := replaceBinary(srcFile.Name(), dstFile.Name()); err != nil {
		t.Fatalf("replaceBinary error: %v", err)
	}

	data, err := os.ReadFile(dstFile.Name())
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new binary" {
		t.Errorf("expected 'new binary', got %q", data)
	}
}
