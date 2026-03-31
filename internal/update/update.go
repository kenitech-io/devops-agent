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

// HealthCheckURL is the local health endpoint used to verify the agent after update.
var HealthCheckURL = "http://127.0.0.1:9100/healthz"

// HealthCheckTimeout is how long to wait for the new binary to become healthy.
var HealthCheckTimeout = 60 * time.Second

// HealthCheckInterval is the polling interval during post-update health check.
var HealthCheckInterval = 5 * time.Second

// restartFunc is the function used to restart the service. Override in tests.
var restartFunc = restartSystemd

// executableFunc returns the path of the current executable. Override in tests.
var executableFunc = os.Executable

// Update downloads a new agent binary, verifies its checksum, replaces the
// current binary with rollback support, and restarts via systemd.
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
	currentPath, err := executableFunc()
	if err != nil {
		return fmt.Errorf("getting current executable path: %w", err)
	}

	// Back up the current binary for rollback
	prevPath := currentPath + ".prev"
	if err := backupBinary(currentPath, prevPath); err != nil {
		return fmt.Errorf("backing up current binary: %w", err)
	}
	slog.Info("backed up current binary", "path", prevPath)

	// Replace the binary
	if err := replaceBinary(tmpPath, currentPath); err != nil {
		// Restore from backup on replace failure
		restoreErr := os.Rename(prevPath, currentPath)
		if restoreErr != nil {
			slog.Error("failed to restore backup after replace failure", "error", restoreErr)
		}
		return fmt.Errorf("replacing binary: %w", err)
	}

	slog.Info("binary replaced, restarting via systemd")

	// Restart the service
	if err := restartFunc(); err != nil {
		slog.Error("restart failed, rolling back", "error", err)
		if rollbackErr := rollback(prevPath, currentPath); rollbackErr != nil {
			slog.Error("rollback failed", "error", rollbackErr)
		}
		return fmt.Errorf("restarting service: %w", err)
	}

	// Post-restart health check (runs in a goroutine since systemd will restart us)
	// The new process handles its own health. If this process survives long enough
	// to reach here, check health and rollback if needed.
	// Snapshot config to avoid race with tests that swap globals.
	restartFn := restartFunc
	healthURL := HealthCheckURL
	healthTimeout := HealthCheckTimeout
	healthInterval := HealthCheckInterval
	go func() {
		if err := waitForHealthy(healthURL, healthTimeout, healthInterval); err != nil {
			slog.Error("post-update health check failed, rolling back", "error", err)
			if rollbackErr := rollback(prevPath, currentPath); rollbackErr != nil {
				slog.Error("rollback after health check failure failed", "error", rollbackErr)
				return
			}
			restartFn()
		} else {
			slog.Info("post-update health check passed, removing backup")
			os.Remove(prevPath)
		}
	}()

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

func backupBinary(srcPath, destPath string) error {
	src, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer src.Close()

	dst, err := os.Create(destPath)
	if err != nil {
		return err
	}

	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		os.Remove(destPath)
		return err
	}

	if err := dst.Chmod(0755); err != nil {
		dst.Close()
		os.Remove(destPath)
		return err
	}
	return dst.Close()
}

func rollback(prevPath, currentPath string) error {
	slog.Warn("rolling back to previous binary", "from", prevPath, "to", currentPath)
	if err := os.Rename(prevPath, currentPath); err != nil {
		return fmt.Errorf("rename %s -> %s: %w", prevPath, currentPath, err)
	}
	return nil
}

func waitForHealthy(url string, timeout, interval time.Duration) error {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 5 * time.Second}

	// Give the new process a moment to start
	time.Sleep(interval)

	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			slog.Warn("health check returned non-200", "status", resp.StatusCode)
		} else {
			slog.Warn("health check failed", "error", err)
		}
		time.Sleep(interval)
	}

	return fmt.Errorf("health check did not pass within %s", timeout)
}

func restartSystemd() error {
	out, err := exec.Command("systemctl", "restart", "keni-agent").CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, string(out))
	}
	return nil
}
