package update

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// minDiskSpaceBytes is the minimum free disk space required for an update (100 MB).
const minDiskSpaceBytes = 100 * 1024 * 1024

// UpdateMarkerPath is the file whose existence indicates an update is in progress.
// Used by startup recovery to detect incomplete updates.
const UpdateMarkerPath = "/etc/keni-agent/update-in-progress"

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

// AllowedHosts is the list of hosts permitted for update downloads.
// Extend this slice to allow additional sources.
var AllowedHosts = []string{"github.com", "dashboard.kenitech.io"}

// preflightFunc is the function called before update. Override in tests.
var preflightFunc = preflightChecks

// markerPathOverride allows tests to use a temp path instead of UpdateMarkerPath.
var markerPathOverride string

// preflightChecks verifies the system is ready for a self-update:
// sufficient disk space, writable binary path, and systemctl available.
func preflightChecks() error {
	// 1. Check disk space on the partition containing /usr/local/bin/
	binDir := "/usr/local/bin"
	var stat syscall.Statfs_t
	if err := syscall.Statfs(binDir, &stat); err != nil {
		return fmt.Errorf("preflight: cannot stat filesystem at %s: %w", binDir, err)
	}
	freeBytes := stat.Bavail * uint64(stat.Bsize)
	if freeBytes < minDiskSpaceBytes {
		return fmt.Errorf("preflight: insufficient disk space on %s: %d bytes free, need at least %d", binDir, freeBytes, minDiskSpaceBytes)
	}

	// 2. Check that the current binary's parent directory is writable
	currentPath, err := executableFunc()
	if err != nil {
		return fmt.Errorf("preflight: cannot determine executable path: %w", err)
	}
	parentDir := filepath.Dir(currentPath)
	testFile := filepath.Join(parentDir, ".keni-update-check")
	f, err := os.Create(testFile)
	if err != nil {
		return fmt.Errorf("preflight: binary directory %s is not writable: %w", parentDir, err)
	}
	f.Close()
	os.Remove(testFile)

	// 3. Check that systemctl is available in PATH
	if _, err := exec.LookPath("systemctl"); err != nil {
		return fmt.Errorf("preflight: systemctl not found in PATH: %w", err)
	}

	slog.Info("preflight checks passed", "free_bytes", freeBytes, "binary_dir", parentDir)
	return nil
}

// isDevMode returns true when KENI_SKIP_WIREGUARD=true (local development).
func isDevMode() bool {
	return os.Getenv("KENI_SKIP_WIREGUARD") == "true"
}

// ValidateDownloadURL checks that the given URL is safe for downloading an update.
// It enforces HTTPS (unless dev mode), and only allows hosts in AllowedHosts,
// hosts matching *.kenitech.io, or github.com/kenidevops/ paths.
// In dev mode, localhost and 127.0.0.1 are also permitted over HTTP.
func ValidateDownloadURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid download URL: %w", err)
	}

	dev := isDevMode()
	host := strings.ToLower(parsed.Hostname())

	// Scheme check: require https unless dev mode
	switch parsed.Scheme {
	case "https":
		// always allowed
	case "http":
		if !dev {
			return fmt.Errorf("download URL requires https, got http (set KENI_SKIP_WIREGUARD=true for dev mode)")
		}
	default:
		return fmt.Errorf("unsupported URL scheme: %s", parsed.Scheme)
	}

	// In dev mode, allow localhost / 127.0.0.1
	if dev && (host == "localhost" || host == "127.0.0.1") {
		return nil
	}

	// Reject localhost / 127.0.0.1 in prod
	if host == "localhost" || host == "127.0.0.1" {
		return fmt.Errorf("download from %s is only allowed in dev mode (KENI_SKIP_WIREGUARD=true)", host)
	}

	// Check AllowedHosts exact match
	for _, allowed := range AllowedHosts {
		if host == strings.ToLower(allowed) {
			// For github.com, require the kenidevops org path
			if host == "github.com" {
				if !strings.HasPrefix(parsed.Path, "/kenidevops/") && !strings.HasPrefix(parsed.Path, "/kenitech-io/") {
					return fmt.Errorf("github.com downloads must be from /kenidevops/, got path: %s", parsed.Path)
				}
			}
			return nil
		}
	}

	// Check *.kenitech.io wildcard
	if strings.HasSuffix(host, ".kenitech.io") {
		return nil
	}

	return fmt.Errorf("download host %q is not in the allowed list", host)
}

// ProgressFunc is called at each step of the update process.
type ProgressFunc func(step, status, detail string)

// Update downloads a new agent binary, verifies its checksum, replaces the
// current binary with rollback support, and restarts via systemd.
func Update(downloadURL, expectedChecksum string) error {
	return UpdateWithProgress(downloadURL, expectedChecksum, nil)
}

// UpdateWithProgress is like Update but calls progress at each step.
func UpdateWithProgress(downloadURL, expectedChecksum string, progress ProgressFunc) error {
	report := func(step, status, detail string) {
		if progress != nil {
			progress(step, status, detail)
		}
	}

	report("Preflight checks", "running", "")
	if err := preflightFunc(); err != nil {
		report("Preflight checks", "error", err.Error())
		return err
	}
	report("Preflight checks", "done", "")

	if err := ValidateDownloadURL(downloadURL); err != nil {
		report("Validating URL", "error", err.Error())
		return fmt.Errorf("download URL rejected: %w", err)
	}

	slog.Info("starting self-update", "url", downloadURL)

	currentPath, err := executableFunc()
	if err != nil {
		return fmt.Errorf("getting current executable path: %w", err)
	}

	isTarGz := strings.HasSuffix(downloadURL, ".tar.gz") || strings.HasSuffix(downloadURL, ".tgz")
	tmpPath := currentPath + ".download"

	if isTarGz {
		// For tar.gz: download archive, verify checksum on archive, then extract binary.
		archivePath := currentPath + ".tar.gz"
		report("Downloading binary", "running", filepath.Base(downloadURL))
		if err := downloadFile(downloadURL, archivePath); err != nil {
			os.Remove(archivePath)
			report("Downloading binary", "error", err.Error())
			return fmt.Errorf("downloading binary: %w", err)
		}
		defer os.Remove(archivePath)
		report("Downloading binary", "done", "")

		report("Verifying checksum", "running", "")
		if err := verifyChecksum(archivePath, expectedChecksum); err != nil {
			report("Verifying checksum", "error", err.Error())
			return fmt.Errorf("checksum verification failed: %w", err)
		}
		report("Verifying checksum", "done", "")

		report("Extracting binary", "running", "")
		f, err := os.Open(archivePath)
		if err != nil {
			report("Extracting binary", "error", err.Error())
			return fmt.Errorf("opening archive: %w", err)
		}
		defer f.Close()
		if err := extractBinaryFromTarGz(f, tmpPath); err != nil {
			os.Remove(tmpPath)
			report("Extracting binary", "error", err.Error())
			return fmt.Errorf("extracting binary: %w", err)
		}
		report("Extracting binary", "done", "")
	} else {
		// Raw binary: download and verify checksum directly.
		report("Downloading binary", "running", filepath.Base(downloadURL))
		if err := downloadFile(downloadURL, tmpPath); err != nil {
			os.Remove(tmpPath)
			report("Downloading binary", "error", err.Error())
			return fmt.Errorf("downloading binary: %w", err)
		}
		report("Downloading binary", "done", "")

		report("Verifying checksum", "running", "")
		if err := verifyChecksum(tmpPath, expectedChecksum); err != nil {
			os.Remove(tmpPath)
			report("Verifying checksum", "error", err.Error())
			return fmt.Errorf("checksum verification failed: %w", err)
		}
		report("Verifying checksum", "done", "")
	}
	defer os.Remove(tmpPath)

	markerPath := UpdateMarkerPath
	if markerOverride := markerPathOverride; markerOverride != "" {
		markerPath = markerOverride
	}
	if err := os.WriteFile(markerPath, []byte(""), 0600); err != nil {
		slog.Warn("could not write update marker file", "error", err)
	}

	report("Backing up current binary", "running", "")
	prevPath := currentPath + ".prev"
	if err := backupBinary(currentPath, prevPath); err != nil {
		os.Remove(markerPath)
		report("Backing up current binary", "error", err.Error())
		return fmt.Errorf("backing up current binary: %w", err)
	}
	report("Backing up current binary", "done", "")

	report("Replacing binary", "running", "")
	if err := replaceBinary(tmpPath, currentPath); err != nil {
		restoreErr := os.Rename(prevPath, currentPath)
		if restoreErr != nil {
			slog.Error("failed to restore backup after replace failure", "error", restoreErr)
		}
		os.Remove(markerPath)
		report("Replacing binary", "error", err.Error())
		return fmt.Errorf("replacing binary: %w", err)
	}
	report("Replacing binary", "done", "")

	slog.Info("binary replaced, restarting via systemd")
	report("Restarting service", "running", "")

	if err := restartFunc(); err != nil {
		slog.Error("restart failed, rolling back", "error", err)
		if rollbackErr := rollback(prevPath, currentPath); rollbackErr != nil {
			slog.Error("rollback failed", "error", rollbackErr)
		}
		os.Remove(markerPath)
		report("Restarting service", "error", err.Error())
		return fmt.Errorf("restarting service: %w", err)
	}
	report("Restarting service", "done", "Agent will reconnect with new version")

	restartFn := restartFunc
	healthURL := HealthCheckURL
	healthTimeout := HealthCheckTimeout
	healthInterval := HealthCheckInterval
	markerForGoroutine := markerPath
	go func() {
		if err := waitForHealthy(healthURL, healthTimeout, healthInterval); err != nil {
			slog.Error("post-update health check failed, rolling back", "error", err)
			if rollbackErr := rollback(prevPath, currentPath); rollbackErr != nil {
				slog.Error("rollback after health check failure failed", "error", rollbackErr)
				return
			}
			os.Remove(markerForGoroutine)
			restartFn()
		} else {
			slog.Info("post-update health check passed, removing backup")
			os.Remove(prevPath)
			os.Remove(markerForGoroutine)
		}
	}()

	return nil
}

func downloadFile(rawURL, destPath string) error {
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Get(rawURL)
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

	return nil
}

// extractBinaryFromTarGz reads a tar.gz stream and extracts the first executable file
// named "keni-agent" (the binary produced by goreleaser).
func extractBinaryFromTarGz(r io.Reader, destPath string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("gzip reader: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("tar reader: %w", err)
		}

		// Look for the keni-agent binary (skip directories and other files)
		name := filepath.Base(hdr.Name)
		if hdr.Typeflag == tar.TypeReg && name == "keni-agent" {
			out, err := os.Create(destPath)
			if err != nil {
				return err
			}
			defer out.Close()

			if _, err := io.Copy(out, tr); err != nil {
				return err
			}
			return out.Chmod(0755)
		}
	}

	return fmt.Errorf("keni-agent binary not found in archive")
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
	// Before replacing the binary, verify the target is not a symlink
	targetInfo, err := os.Lstat(destPath)
	if err == nil && targetInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to replace symlink at %s", destPath)
	}

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
	// Before backing up, verify the source is not a symlink
	srcInfo, err := os.Lstat(srcPath)
	if err == nil && srcInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to backup symlink at %s", srcPath)
	}

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
	// Verify neither path is a symlink before rollback
	if info, err := os.Lstat(currentPath); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing rollback: target %s is a symlink", currentPath)
	}

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
