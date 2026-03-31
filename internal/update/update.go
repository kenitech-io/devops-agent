package update

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Update downloads a new agent binary, verifies its checksum, replaces the
// current binary, and restarts via systemd.
func Update(downloadURL, expectedChecksum string) error {
	slog.Info("starting self-update", "url", downloadURL)

	// Download to a temp file
	tmpPath := "/tmp/keni-agent-update"
	if err := downloadBinary(downloadURL, tmpPath); err != nil {
		return fmt.Errorf("downloading binary: %w", err)
	}
	defer os.Remove(tmpPath)

	// Verify checksum
	if err := verifyChecksum(tmpPath, expectedChecksum); err != nil {
		return fmt.Errorf("checksum verification failed: %w", err)
	}

	// Get path of current binary
	currentPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("getting current executable path: %w", err)
	}

	// Replace the binary
	if err := replaceBinary(tmpPath, currentPath); err != nil {
		return fmt.Errorf("replacing binary: %w", err)
	}

	slog.Info("binary replaced, restarting via systemd")

	// Restart the service
	if err := restart(); err != nil {
		return fmt.Errorf("restarting service: %w", err)
	}

	return nil
}

func downloadBinary(url, destPath string) error {
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download returned status %d", resp.StatusCode)
	}

	out, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, resp.Body); err != nil {
		return err
	}

	return out.Chmod(0755)
}

func verifyChecksum(filePath, expectedChecksum string) error {
	// Expected format: "sha256:hexdigest"
	parts := strings.SplitN(expectedChecksum, ":", 2)
	if len(parts) != 2 || parts[0] != "sha256" {
		return fmt.Errorf("unsupported checksum format: %s (expected sha256:hex)", expectedChecksum)
	}
	expectedHex := parts[1]

	f, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer f.Close()

	hasher := sha256.New()
	if _, err := io.Copy(hasher, f); err != nil {
		return err
	}

	actualHex := hex.EncodeToString(hasher.Sum(nil))
	if actualHex != expectedHex {
		return fmt.Errorf("checksum mismatch: expected %s, got %s", expectedHex, actualHex)
	}

	slog.Info("checksum verified", "sha256", actualHex)
	return nil
}

func replaceBinary(srcPath, destPath string) error {
	src, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer src.Close()

	// Write to a temp file next to the destination, then rename (atomic on same filesystem)
	tmpDest := destPath + ".new"
	dst, err := os.Create(tmpDest)
	if err != nil {
		return err
	}

	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		os.Remove(tmpDest)
		return err
	}

	if err := dst.Chmod(0755); err != nil {
		dst.Close()
		os.Remove(tmpDest)
		return err
	}
	dst.Close()

	// Atomic rename
	if err := os.Rename(tmpDest, destPath); err != nil {
		os.Remove(tmpDest)
		return err
	}

	return nil
}

func restart() error {
	out, err := exec.Command("systemctl", "restart", "keni-agent").CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, string(out))
	}
	return nil
}
