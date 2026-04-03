package gitops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ComponentResult holds the result of applying a single component.
type ComponentResult struct {
	Name      string
	Path      string
	Status    string // "running", "error"
	Error     string
	UpdatedAt time.Time
}

// composeHashCache tracks the SHA-256 of each component's compose file
// to skip reapplying unchanged components.
var (
	composeHashMu sync.Mutex
	composeHashes = make(map[string]string) // dir path -> sha256 hex
)

// composeFileHash returns the SHA-256 hex of the docker-compose.yml in dir.
func composeFileHash(dir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dir, "docker-compose.yml"))
	if err != nil {
		return "", err
	}
	// Also include .env if it exists
	envData, _ := os.ReadFile(filepath.Join(dir, ".env"))
	h := sha256.New()
	h.Write(data)
	h.Write(envData)
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ApplyComponent runs docker compose up -d in the given component directory.
// Skips unchanged components (compose file + .env hash match) to avoid unnecessary restarts.
func ApplyComponent(ctx context.Context, dir string) *ComponentResult {
	name := filepath.Base(dir)
	result := &ComponentResult{
		Name:      name,
		Path:      dir,
		UpdatedAt: time.Now(),
	}

	// Check if compose files changed since last apply
	newHash, hashErr := composeFileHash(dir)
	if hashErr == nil {
		composeHashMu.Lock()
		oldHash, exists := composeHashes[dir]
		composeHashMu.Unlock()

		if exists && oldHash == newHash {
			slog.Debug("component unchanged, skipping", "name", name)
			result.Status = "running"
			return result
		}
	}

	slog.Info("applying component", "name", name, "dir", dir)

	cmd := exec.CommandContext(ctx, "docker", "compose", "up", "-d", "--pull", "always", "--remove-orphans")
	cmd.Dir = dir

	out, err := cmd.CombinedOutput()
	if err != nil {
		result.Status = "error"
		result.Error = fmt.Sprintf("%s: %s", err, strings.TrimSpace(string(out)))
		slog.Error("component apply failed", "name", name, "error", result.Error)
		return result
	}

	// Update hash cache on successful apply
	if hashErr == nil {
		composeHashMu.Lock()
		composeHashes[dir] = newHash
		composeHashMu.Unlock()
	}

	result.Status = "running"
	slog.Info("component applied", "name", name)
	return result
}

// ClearHashCache removes all cached compose file hashes, forcing a full redeploy on next apply.
func ClearHashCache() {
	composeHashMu.Lock()
	composeHashes = make(map[string]string)
	composeHashMu.Unlock()
}

// ApplyAll applies docker compose for all component directories.
// Returns results for each component.
func ApplyAll(ctx context.Context, dirs []string) []*ComponentResult {
	var results []*ComponentResult
	for _, dir := range dirs {
		result := ApplyComponent(ctx, dir)
		results = append(results, result)
	}
	return results
}

// StopComponent runs docker compose down in a component directory.
func StopComponent(ctx context.Context, dir string) error {
	name := filepath.Base(dir)
	slog.Info("stopping component", "name", name, "dir", dir)

	cmd := exec.CommandContext(ctx, "docker", "compose", "down", "--remove-orphans")
	cmd.Dir = dir

	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker compose down for %s: %w\n%s", name, err, string(out))
	}
	return nil
}

// StopComponentByProject stops a compose project by name without needing
// a compose file on disk. Uses Docker's project label to find containers.
func StopComponentByProject(ctx context.Context, projectName string) error {
	slog.Info("stopping removed component by project name", "project", projectName)
	cmd := exec.CommandContext(ctx, "docker", "compose", "-p", projectName, "down", "--remove-orphans")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker compose down -p %s: %w\n%s", projectName, err, string(out))
	}
	return nil
}

// ComponentStatus checks if a component's containers are running.
func ComponentStatus(ctx context.Context, dir string) string {
	cmd := exec.CommandContext(ctx, "docker", "compose", "ps", "--format", "json", "--status", "running")
	cmd.Dir = dir

	out, err := cmd.Output()
	if err != nil {
		return "error"
	}

	output := strings.TrimSpace(string(out))
	if output == "" || output == "[]" {
		return "stopped"
	}
	return "running"
}
