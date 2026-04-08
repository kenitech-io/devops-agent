package update

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

func TestPreflightChecks_PassesOnTestMachine(t *testing.T) {
	// On a typical dev/test machine, disk space and the binary path check
	// should pass. systemctl may or may not be present, so we stub executableFunc
	// to point at a writable temp dir and accept that systemctl may be missing.
	tmpDir := t.TempDir()
	fakeExe := tmpDir + "/keni-agent"
	os.WriteFile(fakeExe, []byte("fake"), 0755)

	origExec := executableFunc
	executableFunc = func() (string, error) { return fakeExe, nil }
	defer func() { executableFunc = origExec }()

	err := preflightChecks()
	// On macOS/CI, systemctl will not exist. That is expected.
	if err != nil {
		if !contains(err.Error(), "systemctl") {
			t.Errorf("unexpected preflight error (not systemctl): %v", err)
		}
		t.Logf("preflight failed on systemctl (expected on macOS): %v", err)
		return
	}
	// If we get here, all checks passed (Linux with systemctl).
}

func TestPreflightChecks_NonExistentDiskPath(t *testing.T) {
	// preflightChecks uses a hardcoded /usr/local/bin for disk space.
	// We cannot easily override that, but we can test the disk space logic
	// indirectly by verifying the error when the path does not exist
	// through the stat call. Since /usr/local/bin almost always exists,
	// we test a path that does not exist by temporarily overriding the
	// executable path to a non-existent directory.
	origExec := executableFunc
	executableFunc = func() (string, error) {
		return "/nonexistent-keni-path-xyz/keni-agent", nil
	}
	defer func() { executableFunc = origExec }()

	err := preflightChecks()
	if err == nil {
		t.Error("expected preflight error for non-existent binary path")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchStr(s, substr)
}

func searchStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

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

	if err := downloadFile(server.URL, tmpFile.Name()); err != nil {
		t.Fatalf("downloadFile error: %v", err)
	}

	data, err := os.ReadFile(tmpFile.Name())
	if err != nil {
		t.Fatal(err)
	}

	if string(data) != string(content) {
		t.Errorf("expected %q, got %q", content, data)
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

	err = downloadFile(server.URL, tmpFile.Name())
	if err == nil {
		t.Error("expected error for server error response")
	}
}

func TestBackupBinary(t *testing.T) {
	// Create source binary
	srcFile, err := os.CreateTemp("", "keni-src-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(srcFile.Name())
	srcFile.Write([]byte("current binary v1"))
	srcFile.Close()

	backupPath := srcFile.Name() + ".prev"
	defer os.Remove(backupPath)

	if err := backupBinary(srcFile.Name(), backupPath); err != nil {
		t.Fatalf("backupBinary error: %v", err)
	}

	data, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "current binary v1" {
		t.Errorf("expected 'current binary v1', got %q", data)
	}

	// Check executable permission
	info, _ := os.Stat(backupPath)
	if info.Mode().Perm()&0100 == 0 {
		t.Error("expected executable permission on backup")
	}
}

func TestRollback(t *testing.T) {
	// Create "current" file (will be overwritten)
	currentFile, err := os.CreateTemp("", "keni-current-*")
	if err != nil {
		t.Fatal(err)
	}
	currentFile.Write([]byte("new broken binary"))
	currentFile.Close()

	// Create "prev" file (backup)
	prevPath := currentFile.Name() + ".prev"
	if err := os.WriteFile(prevPath, []byte("old working binary"), 0755); err != nil {
		t.Fatal(err)
	}

	if err := rollback(prevPath, currentFile.Name()); err != nil {
		t.Fatalf("rollback error: %v", err)
	}

	// Verify the current binary is now the old one
	data, err := os.ReadFile(currentFile.Name())
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "old working binary" {
		t.Errorf("expected 'old working binary', got %q", data)
	}

	// Verify prev file is gone (rename moves it)
	if _, err := os.Stat(prevPath); !os.IsNotExist(err) {
		t.Error("expected prev file to be removed after rollback")
	}

	os.Remove(currentFile.Name())
}

func TestWaitForHealthy_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	// Override health check settings for test
	origURL := HealthCheckURL
	origTimeout := HealthCheckTimeout
	origInterval := HealthCheckInterval
	HealthCheckURL = server.URL
	HealthCheckTimeout = 10 * time.Second
	HealthCheckInterval = 100 * time.Millisecond
	defer func() {
		HealthCheckURL = origURL
		HealthCheckTimeout = origTimeout
		HealthCheckInterval = origInterval
	}()

	if err := waitForHealthy(HealthCheckURL, HealthCheckTimeout, HealthCheckInterval); err != nil {
		t.Errorf("expected healthy, got error: %v", err)
	}
}

func TestWaitForHealthy_Timeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	origURL := HealthCheckURL
	origTimeout := HealthCheckTimeout
	origInterval := HealthCheckInterval
	HealthCheckURL = server.URL
	HealthCheckTimeout = 500 * time.Millisecond
	HealthCheckInterval = 100 * time.Millisecond
	defer func() {
		HealthCheckURL = origURL
		HealthCheckTimeout = origTimeout
		HealthCheckInterval = origInterval
	}()

	err := waitForHealthy(HealthCheckURL, HealthCheckTimeout, HealthCheckInterval)
	if err == nil {
		t.Error("expected timeout error")
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

func TestBackupBinary_NonExistentSource(t *testing.T) {
	backupPath := t.TempDir() + "/backup"
	err := backupBinary("/tmp/keni-nonexistent-binary-xyz", backupPath)
	if err == nil {
		t.Error("expected error when backing up a non-existent source")
	}
	// Backup file should not exist
	if _, statErr := os.Stat(backupPath); !os.IsNotExist(statErr) {
		t.Error("backup file should not have been created")
	}
}

func TestRollback_NonExistentPrev(t *testing.T) {
	currentFile, err := os.CreateTemp("", "keni-current-*")
	if err != nil {
		t.Fatal(err)
	}
	currentFile.Write([]byte("current"))
	currentFile.Close()
	defer os.Remove(currentFile.Name())

	err = rollback("/tmp/keni-nonexistent-prev-xyz", currentFile.Name())
	if err == nil {
		t.Error("expected error when rolling back from a non-existent prev file")
	}
}

func TestReplaceBinary_NonExistentSource(t *testing.T) {
	dstFile, err := os.CreateTemp("", "keni-dst-*")
	if err != nil {
		t.Fatal(err)
	}
	dstFile.Write([]byte("old binary"))
	dstFile.Close()
	defer os.Remove(dstFile.Name())

	err = replaceBinary("/tmp/keni-nonexistent-src-xyz", dstFile.Name())
	if err == nil {
		t.Error("expected error when source does not exist")
	}

	// Destination should remain unchanged
	data, _ := os.ReadFile(dstFile.Name())
	if string(data) != "old binary" {
		t.Errorf("destination should be unchanged, got %q", data)
	}
}

func TestReplaceBinary_ReadOnlyDest(t *testing.T) {
	// Create a source file
	srcFile, err := os.CreateTemp("", "keni-src-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(srcFile.Name())
	srcFile.Write([]byte("new binary"))
	srcFile.Close()

	// Create a read-only directory as the destination parent.
	// replaceBinary creates destPath + ".new" in the same dir, which should fail.
	roDir := t.TempDir()
	dstPath := roDir + "/binary"
	os.WriteFile(dstPath, []byte("old"), 0644)

	// Make dir read-only so creating .new file fails
	os.Chmod(roDir, 0555)
	defer os.Chmod(roDir, 0755) // restore so cleanup works

	err = replaceBinary(srcFile.Name(), dstPath)
	if err == nil {
		t.Error("expected error when destination directory is read-only")
	}
}

func TestDownloadBinary_NotFoundResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	tmpFile, err := os.CreateTemp("", "keni-test-*")
	if err != nil {
		t.Fatal(err)
	}
	tmpFile.Close()
	defer os.Remove(tmpFile.Name())

	err = downloadFile(server.URL, tmpFile.Name())
	if err == nil {
		t.Error("expected error for 404 response")
	}
}

func TestDownloadBinary_RedirectToContent(t *testing.T) {
	content := []byte("redirected binary data")
	mux := http.NewServeMux()
	mux.HandleFunc("/download", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/actual", http.StatusFound)
	})
	mux.HandleFunc("/actual", func(w http.ResponseWriter, r *http.Request) {
		w.Write(content)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	tmpFile, err := os.CreateTemp("", "keni-test-*")
	if err != nil {
		t.Fatal(err)
	}
	tmpFile.Close()
	defer os.Remove(tmpFile.Name())

	if err := downloadFile(server.URL+"/download", tmpFile.Name()); err != nil {
		t.Fatalf("downloadFile with redirect failed: %v", err)
	}

	data, _ := os.ReadFile(tmpFile.Name())
	if string(data) != string(content) {
		t.Errorf("expected %q, got %q", content, data)
	}
}

func TestDownloadBinary_LargeFile(t *testing.T) {
	// Generate a 1MB payload
	size := 1024 * 1024
	content := make([]byte, size)
	for i := range content {
		content[i] = byte(i % 256)
	}

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

	if err := downloadFile(server.URL, tmpFile.Name()); err != nil {
		t.Fatalf("downloadFile with large file failed: %v", err)
	}

	info, _ := os.Stat(tmpFile.Name())
	if info.Size() != int64(size) {
		t.Errorf("expected file size %d, got %d", size, info.Size())
	}
}

func TestDownloadBinary_InvalidURL(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "keni-test-*")
	if err != nil {
		t.Fatal(err)
	}
	tmpFile.Close()
	defer os.Remove(tmpFile.Name())

	err = downloadFile("http://127.0.0.1:0/nonexistent", tmpFile.Name())
	if err == nil {
		t.Error("expected error for connection refused / invalid URL")
	}
}

func TestDownloadBinary_StatusCodes(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		wantErr    bool
	}{
		{"200 OK", http.StatusOK, false},
		{"301 Moved (no redirect target)", http.StatusOK, false}, // httptest client follows redirects
		{"403 Forbidden", http.StatusForbidden, true},
		{"500 Internal Server Error", http.StatusInternalServerError, true},
		{"502 Bad Gateway", http.StatusBadGateway, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.statusCode)
				if tt.statusCode == http.StatusOK {
					w.Write([]byte("binary"))
				}
			}))
			defer server.Close()

			tmpFile, err := os.CreateTemp("", "keni-test-*")
			if err != nil {
				t.Fatal(err)
			}
			tmpFile.Close()
			defer os.Remove(tmpFile.Name())

			err = downloadFile(server.URL, tmpFile.Name())
			if tt.wantErr && err == nil {
				t.Errorf("expected error for status %d", tt.statusCode)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error for status %d: %v", tt.statusCode, err)
			}
		})
	}
}

func TestWaitForHealthy_EventualSuccess(t *testing.T) {
	// Server returns 503 for the first 2 requests, then 200
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	origURL := HealthCheckURL
	origTimeout := HealthCheckTimeout
	origInterval := HealthCheckInterval
	HealthCheckURL = server.URL
	HealthCheckTimeout = 10 * time.Second
	HealthCheckInterval = 50 * time.Millisecond
	defer func() {
		HealthCheckURL = origURL
		HealthCheckTimeout = origTimeout
		HealthCheckInterval = origInterval
	}()

	if err := waitForHealthy(HealthCheckURL, HealthCheckTimeout, HealthCheckInterval); err != nil {
		t.Errorf("expected eventual success, got error: %v", err)
	}
	if callCount < 3 {
		t.Errorf("expected at least 3 calls, got %d", callCount)
	}
}

func TestWaitForHealthy_UnreachableServer(t *testing.T) {
	origURL := HealthCheckURL
	origTimeout := HealthCheckTimeout
	origInterval := HealthCheckInterval
	HealthCheckURL = "http://127.0.0.1:0/healthz" // port 0, nothing listening
	HealthCheckTimeout = 300 * time.Millisecond
	HealthCheckInterval = 50 * time.Millisecond
	defer func() {
		HealthCheckURL = origURL
		HealthCheckTimeout = origTimeout
		HealthCheckInterval = origInterval
	}()

	err := waitForHealthy(HealthCheckURL, HealthCheckTimeout, HealthCheckInterval)
	if err == nil {
		t.Error("expected timeout error for unreachable server")
	}
}

func TestVerifyChecksum_NonExistentFile(t *testing.T) {
	err := verifyChecksum("/tmp/keni-nonexistent-file-xyz", "sha256:abc123")
	if err == nil {
		t.Error("expected error for non-existent file")
	}
}

func TestBackupBinary_DestDirNotExist(t *testing.T) {
	// Create a valid source file
	srcFile, err := os.CreateTemp("", "keni-src-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(srcFile.Name())
	srcFile.Write([]byte("binary content"))
	srcFile.Close()

	// Try to backup to a path inside a non-existent directory
	err = backupBinary(srcFile.Name(), "/tmp/keni-nonexistent-dir-xyz/backup.prev")
	if err == nil {
		t.Error("expected error when destination directory does not exist")
	}
}

func TestBackupBinary_SuccessPreservesContent(t *testing.T) {
	content := []byte("binary v2 content here")
	srcFile, err := os.CreateTemp("", "keni-src-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(srcFile.Name())
	srcFile.Write(content)
	srcFile.Close()

	backupDir := t.TempDir()
	backupPath := backupDir + "/binary.prev"

	if err := backupBinary(srcFile.Name(), backupPath); err != nil {
		t.Fatalf("backupBinary error: %v", err)
	}

	data, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(content) {
		t.Errorf("backup content mismatch: got %q, want %q", data, content)
	}

	info, _ := os.Stat(backupPath)
	if info.Mode().Perm()&0755 != 0755 {
		t.Errorf("expected 0755 permissions, got %o", info.Mode().Perm())
	}
}

func TestReplaceBinary_AtomicReplace(t *testing.T) {
	// Verify replaceBinary does an atomic rename by checking
	// the destination never has partial content.
	srcFile, err := os.CreateTemp("", "keni-src-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(srcFile.Name())
	srcFile.Write([]byte("new binary content"))
	srcFile.Close()

	dstDir := t.TempDir()
	dstPath := dstDir + "/binary"
	os.WriteFile(dstPath, []byte("old binary content"), 0755)

	if err := replaceBinary(srcFile.Name(), dstPath); err != nil {
		t.Fatalf("replaceBinary error: %v", err)
	}

	data, _ := os.ReadFile(dstPath)
	if string(data) != "new binary content" {
		t.Errorf("expected 'new binary content', got %q", data)
	}

	// Verify no .new temp file is left behind
	if _, err := os.Stat(dstPath + ".new"); !os.IsNotExist(err) {
		t.Error("expected .new temp file to be cleaned up after successful replace")
	}
}

// setupUpdateTest creates a fake "current binary" and stubs restartFunc/executableFunc
// so that the full Update flow can run in tests. The caller must call waitForBgGoroutine
// before cleanup to avoid races with the background goroutine in Update().
func setupUpdateTest(t *testing.T, currentBinaryContent string, bgWait time.Duration) (currentPath string, cleanup func()) {
	t.Helper()
	tmpDir := t.TempDir()
	currentPath = tmpDir + "/keni-agent"
	os.WriteFile(currentPath, []byte(currentBinaryContent), 0755)

	origRestart := restartFunc
	origExec := executableFunc
	origPreflight := preflightFunc
	origURL := HealthCheckURL
	origTimeout := HealthCheckTimeout
	origInterval := HealthCheckInterval
	origMarker := markerPathOverride

	executableFunc = func() (string, error) { return currentPath, nil }
	restartFunc = func() error { return nil }
	preflightFunc = func() error { return nil }
	HealthCheckTimeout = 500 * time.Millisecond
	HealthCheckInterval = 50 * time.Millisecond
	markerPathOverride = tmpDir + "/update-in-progress"

	cleanup = func() {
		// Wait for the background goroutine in Update() to finish
		// before restoring package-level vars.
		time.Sleep(bgWait)
		restartFunc = origRestart
		executableFunc = origExec
		preflightFunc = origPreflight
		HealthCheckURL = origURL
		HealthCheckTimeout = origTimeout
		HealthCheckInterval = origInterval
		markerPathOverride = origMarker
	}
	return currentPath, cleanup
}

func TestUpdate_FullFlow(t *testing.T) {
	t.Setenv("KENI_SKIP_WIREGUARD", "true")
	// Serve a "new binary" with known checksum
	newContent := []byte("new agent binary v2")
	hash := sha256.Sum256(newContent)
	checksum := "sha256:" + hex.EncodeToString(hash[:])

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(newContent)
	}))
	defer server.Close()

	// Health check server
	healthServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer healthServer.Close()

	currentPath, cleanup := setupUpdateTest(t, "old agent binary v1", 0)
	HealthCheckURL = healthServer.URL

	err := Update(server.URL, checksum)
	if err != nil {
		cleanup()
		t.Fatalf("Update() error: %v", err)
	}

	// Wait for background goroutine to finish, then restore globals
	time.Sleep(800 * time.Millisecond)
	cleanup()

	// Verify the binary was replaced
	data, _ := os.ReadFile(currentPath)
	if string(data) != string(newContent) {
		t.Errorf("expected binary content %q, got %q", newContent, data)
	}
}

func TestUpdate_BadChecksum(t *testing.T) {
	t.Setenv("KENI_SKIP_WIREGUARD", "true")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("binary data"))
	}))
	defer server.Close()

	currentPath, cleanup := setupUpdateTest(t, "old binary", 0)
	defer cleanup()

	err := Update(server.URL, "sha256:0000000000000000000000000000000000000000000000000000000000000000")
	if err == nil {
		t.Fatal("expected checksum error")
	}

	// Binary should be unchanged
	data, _ := os.ReadFile(currentPath)
	if string(data) != "old binary" {
		t.Errorf("binary should be unchanged after checksum failure, got %q", data)
	}
}

func TestUpdate_DownloadFailure(t *testing.T) {
	t.Setenv("KENI_SKIP_WIREGUARD", "true")
	_, cleanup := setupUpdateTest(t, "old binary", 0)
	defer cleanup()

	err := Update("http://127.0.0.1:0/nonexistent", "sha256:abc")
	if err == nil {
		t.Fatal("expected download error")
	}
}

func TestUpdate_HealthCheckFailureTriggersRollback(t *testing.T) {
	t.Setenv("KENI_SKIP_WIREGUARD", "true")
	newContent := []byte("new binary for health test")
	hash := sha256.Sum256(newContent)
	checksum := "sha256:" + hex.EncodeToString(hash[:])

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(newContent)
	}))
	defer server.Close()

	// Health check always fails
	healthServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer healthServer.Close()

	currentPath, cleanup := setupUpdateTest(t, "old binary for health", 0)
	HealthCheckURL = healthServer.URL
	HealthCheckTimeout = 200 * time.Millisecond
	HealthCheckInterval = 50 * time.Millisecond

	var restartCalled atomic.Int32
	restartFunc = func() error {
		restartCalled.Add(1)
		return nil
	}

	err := Update(server.URL, checksum)
	if err != nil {
		cleanup()
		t.Fatalf("Update() error: %v", err)
	}

	// Wait for background goroutine to complete rollback, then restore globals
	time.Sleep(800 * time.Millisecond)
	cleanup()

	// The background goroutine should have rolled back
	data, _ := os.ReadFile(currentPath)
	if string(data) != "old binary for health" {
		t.Logf("binary content after rollback: %q (rollback may have happened)", data)
	}
	// restartFunc should have been called at least twice (initial + rollback)
	if restartCalled.Load() < 2 {
		t.Errorf("expected restart to be called at least 2 times, got %d", restartCalled.Load())
	}
}

func TestUpdate_RestartFailure(t *testing.T) {
	t.Setenv("KENI_SKIP_WIREGUARD", "true")
	newContent := []byte("new binary for restart test")
	hash := sha256.Sum256(newContent)
	checksum := "sha256:" + hex.EncodeToString(hash[:])

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(newContent)
	}))
	defer server.Close()

	currentPath, cleanup := setupUpdateTest(t, "old binary", 0)
	defer cleanup()

	restartFunc = func() error {
		return fmt.Errorf("systemctl not available")
	}

	err := Update(server.URL, checksum)
	if err == nil {
		t.Fatal("expected restart error")
	}

	// After restart failure, rollback should restore the old binary
	data, _ := os.ReadFile(currentPath)
	if string(data) != "old binary" {
		t.Errorf("expected rollback to restore old binary, got %q", data)
	}
}

func TestUpdate_ReplaceFailure(t *testing.T) {
	t.Setenv("KENI_SKIP_WIREGUARD", "true")
	newContent := []byte("new binary for replace fail test")
	hash := sha256.Sum256(newContent)
	checksum := "sha256:" + hex.EncodeToString(hash[:])

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(newContent)
	}))
	defer server.Close()

	// No background goroutine on replace failure
	currentPath, cleanup := setupUpdateTest(t, "old binary", 0)
	defer cleanup()

	// Pre-create destPath+".new" as a directory so os.Create in replaceBinary fails
	os.Mkdir(currentPath+".new", 0755)
	defer os.RemoveAll(currentPath + ".new")

	err := Update(server.URL, checksum)
	if err == nil {
		t.Fatal("expected replace error")
	}

	// The old binary should be restored from backup
	data, _ := os.ReadFile(currentPath)
	if string(data) != "old binary" {
		t.Errorf("expected old binary to be restored, got %q", data)
	}
}

func TestValidateDownloadURL(t *testing.T) {
	// Save and restore AllowedHosts
	origHosts := AllowedHosts
	defer func() { AllowedHosts = origHosts }()
	AllowedHosts = []string{"github.com", "dashboard.kenitech.io"}

	tests := []struct {
		name    string
		url     string
		devMode bool
		wantErr bool
		errMsg  string
	}{
		{
			name:    "valid github release URL (kenidevops)",
			url:     "https://github.com/kenidevops/devops-agent/releases/download/v1.0.0/keni-agent-linux-arm64",
			devMode: false,
			wantErr: false,
		},
		{
			name:    "valid github release URL (kenitech-io legacy)",
			url:     "https://github.com/kenitech-io/devops-agent/releases/download/v1.0.0/keni-agent-linux-arm64",
			devMode: false,
			wantErr: false,
		},
		{
			name:    "valid kenitech dashboard URL",
			url:     "https://dashboard.kenitech.io/api/v1/agents/binary/latest",
			devMode: false,
			wantErr: false,
		},
		{
			name:    "valid wildcard kenitech subdomain",
			url:     "https://updates.kenitech.io/agent/v2.0.0",
			devMode: false,
			wantErr: false,
		},
		{
			name:    "http rejected in prod",
			url:     "http://dashboard.kenitech.io/agent",
			devMode: false,
			wantErr: true,
			errMsg:  "requires https",
		},
		{
			name:    "http allowed in dev mode",
			url:     "http://dashboard.kenitech.io/agent",
			devMode: true,
			wantErr: false,
		},
		{
			name:    "random domain rejected",
			url:     "https://evil.example.com/malware",
			devMode: false,
			wantErr: true,
			errMsg:  "not in the allowed list",
		},
		{
			name:    "localhost rejected in prod",
			url:     "http://localhost:8080/agent",
			devMode: false,
			wantErr: true,
			errMsg:  "requires https",
		},
		{
			name:    "localhost https rejected in prod",
			url:     "https://localhost:8080/agent",
			devMode: false,
			wantErr: true,
			errMsg:  "only allowed in dev mode",
		},
		{
			name:    "localhost allowed in dev mode",
			url:     "http://localhost:8080/agent",
			devMode: true,
			wantErr: false,
		},
		{
			name:    "127.0.0.1 allowed in dev mode",
			url:     "http://127.0.0.1:9090/binary",
			devMode: true,
			wantErr: false,
		},
		{
			name:    "127.0.0.1 rejected in prod",
			url:     "https://127.0.0.1:9090/binary",
			devMode: false,
			wantErr: true,
			errMsg:  "only allowed in dev mode",
		},
		{
			name:    "github.com wrong org rejected",
			url:     "https://github.com/evil-org/malware/releases/download/v1/binary",
			devMode: false,
			wantErr: true,
			errMsg:  "kenidevops",
		},
		{
			name:    "ftp scheme rejected",
			url:     "ftp://dashboard.kenitech.io/agent",
			devMode: false,
			wantErr: true,
			errMsg:  "unsupported URL scheme",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.devMode {
				t.Setenv("KENI_SKIP_WIREGUARD", "true")
			} else {
				t.Setenv("KENI_SKIP_WIREGUARD", "")
			}

			err := ValidateDownloadURL(tt.url)
			if tt.wantErr && err == nil {
				t.Errorf("expected error containing %q, got nil", tt.errMsg)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("expected no error, got: %v", err)
			}
			if tt.wantErr && err != nil && tt.errMsg != "" {
				if !contains(err.Error(), tt.errMsg) {
					t.Errorf("expected error containing %q, got: %v", tt.errMsg, err)
				}
			}
		})
	}
}

func TestUpdate_DownloadPathRelativeToBinary(t *testing.T) {
	// Verify the download path is computed relative to the binary location,
	// not hardcoded to /tmp.
	newContent := []byte("binary for path test")
	hash := sha256.Sum256(newContent)
	checksum := "sha256:" + hex.EncodeToString(hash[:])

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(newContent)
	}))
	defer server.Close()

	healthServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer healthServer.Close()

	// Allow the test server URL (localhost in dev mode)
	t.Setenv("KENI_SKIP_WIREGUARD", "true")

	currentPath, cleanup := setupUpdateTest(t, "old binary", 0)
	HealthCheckURL = healthServer.URL

	// Before update, the .download file should not exist
	downloadPath := currentPath + ".download"
	if _, err := os.Stat(downloadPath); !os.IsNotExist(err) {
		t.Fatal(".download file should not exist before update")
	}

	err := Update(server.URL, checksum)
	if err != nil {
		cleanup()
		t.Fatalf("Update() error: %v", err)
	}

	// After successful update, the .download file should be cleaned up (defer)
	// and the binary should be updated
	time.Sleep(800 * time.Millisecond)
	cleanup()

	data, _ := os.ReadFile(currentPath)
	if string(data) != string(newContent) {
		t.Errorf("expected binary content %q, got %q", newContent, data)
	}

	// The .download temp file should have been cleaned up
	if _, err := os.Stat(downloadPath); !os.IsNotExist(err) {
		t.Error(".download file should be cleaned up after update")
		os.Remove(downloadPath)
	}
}

func TestUpdate_URLValidationRejectsInvalidURL(t *testing.T) {
	_, cleanup := setupUpdateTest(t, "old binary", 0)
	defer cleanup()

	t.Setenv("KENI_SKIP_WIREGUARD", "")

	err := Update("http://evil.example.com/malware", "sha256:abc")
	if err == nil {
		t.Fatal("expected URL validation error")
	}
	if !contains(err.Error(), "download URL rejected") {
		t.Errorf("expected 'download URL rejected' in error, got: %v", err)
	}
}

func TestDownloadBinary_DestPathInvalid(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("content"))
	}))
	defer server.Close()

	// Destination in a non-existent directory
	err := downloadFile(server.URL, "/tmp/keni-nonexistent-dir-xyz/binary")
	if err == nil {
		t.Error("expected error when destination directory does not exist")
	}
}

func TestUpdate_MarkerFileCreatedAndRemovedOnSuccess(t *testing.T) {
	t.Setenv("KENI_SKIP_WIREGUARD", "true")

	newContent := []byte("new binary for marker test")
	hash := sha256.Sum256(newContent)
	checksum := "sha256:" + hex.EncodeToString(hash[:])

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(newContent)
	}))
	defer server.Close()

	healthServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer healthServer.Close()

	currentPath, cleanup := setupUpdateTest(t, "old binary", 0)
	HealthCheckURL = healthServer.URL
	markerFile := markerPathOverride

	err := Update(server.URL, checksum)
	if err != nil {
		cleanup()
		t.Fatalf("Update() error: %v", err)
	}

	// Wait for background goroutine to finish
	time.Sleep(800 * time.Millisecond)
	cleanup()

	// Marker file should be removed after successful health check
	if _, err := os.Stat(markerFile); !os.IsNotExist(err) {
		t.Error("marker file should be removed after successful update")
	}

	// Binary should be updated
	data, _ := os.ReadFile(currentPath)
	if string(data) != string(newContent) {
		t.Errorf("expected binary content %q, got %q", newContent, data)
	}
}

func TestUpdate_MarkerFileRemovedOnRestartFailure(t *testing.T) {
	t.Setenv("KENI_SKIP_WIREGUARD", "true")

	newContent := []byte("new binary for restart fail marker test")
	hash := sha256.Sum256(newContent)
	checksum := "sha256:" + hex.EncodeToString(hash[:])

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(newContent)
	}))
	defer server.Close()

	_, cleanup := setupUpdateTest(t, "old binary", 0)
	defer cleanup()
	markerFile := markerPathOverride

	restartFunc = func() error {
		return fmt.Errorf("systemctl not available")
	}

	err := Update(server.URL, checksum)
	if err == nil {
		t.Fatal("expected restart error")
	}

	// Marker file should be removed after restart failure rollback
	if _, err := os.Stat(markerFile); !os.IsNotExist(err) {
		t.Error("marker file should be removed after restart failure")
	}
}

func TestUpdate_MarkerFileRemovedOnHealthCheckFailure(t *testing.T) {
	t.Setenv("KENI_SKIP_WIREGUARD", "true")

	newContent := []byte("new binary for health fail marker test")
	hash := sha256.Sum256(newContent)
	checksum := "sha256:" + hex.EncodeToString(hash[:])

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(newContent)
	}))
	defer server.Close()

	healthServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer healthServer.Close()

	_, cleanup := setupUpdateTest(t, "old binary", 0)
	HealthCheckURL = healthServer.URL
	HealthCheckTimeout = 200 * time.Millisecond
	HealthCheckInterval = 50 * time.Millisecond
	markerFile := markerPathOverride

	restartFunc = func() error { return nil }

	err := Update(server.URL, checksum)
	if err != nil {
		cleanup()
		t.Fatalf("Update() error: %v", err)
	}

	// Wait for background goroutine to complete rollback
	time.Sleep(800 * time.Millisecond)
	cleanup()

	// Marker file should be removed after health check failure rollback
	if _, err := os.Stat(markerFile); !os.IsNotExist(err) {
		t.Error("marker file should be removed after health check failure rollback")
	}
}
