package gitops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// composeArgs builds the docker compose argument list, adding --env-file flags
// for config.env and secrets.env if they exist in the component directory.
func composeArgs(dir string, subcommand ...string) []string {
	args := []string{"compose"}
	configEnv := filepath.Join(dir, "config.env")
	secretsEnv := filepath.Join(dir, "secrets.env")
	if _, err := os.Stat(configEnv); err == nil {
		args = append(args, "--env-file", "config.env")
	}
	if _, err := os.Stat(secretsEnv); err == nil {
		args = append(args, "--env-file", "secrets.env")
	}
	args = append(args, subcommand...)
	return args
}

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

// composeFileHash returns the SHA-256 hex of the docker-compose.yml + env files in dir.
func composeFileHash(dir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dir, "docker-compose.yml"))
	if err != nil {
		return "", err
	}
	h := sha256.New()
	h.Write(data)
	// Include config.env and secrets.env if they exist
	configData, _ := os.ReadFile(filepath.Join(dir, "config.env"))
	h.Write(configData)
	secretsData, _ := os.ReadFile(filepath.Join(dir, "secrets.env"))
	h.Write(secretsData)
	predeployData, _ := os.ReadFile(filepath.Join(dir, "predeploy.sh"))
	h.Write(predeployData)
	return hex.EncodeToString(h.Sum(nil)), nil
}

// runPredeploy executes a predeploy.sh script in the component directory if present.
// Runs before docker compose up. Non-zero exit fails the component.
func runPredeploy(ctx context.Context, dir string, pf ProgressFunc) error {
	script := filepath.Join(dir, "predeploy.sh")
	if _, err := os.Stat(script); err != nil {
		return nil
	}
	name := filepath.Base(dir)
	slog.Info("running predeploy", "name", name, "script", script)
	if pf != nil {
		pf(fmt.Sprintf("[%s] running predeploy.sh", name))
	}
	cmd := exec.CommandContext(ctx, "/bin/sh", script)
	cmd.Dir = dir

	var compPf ProgressFunc
	if pf != nil {
		compPf = func(line string) {
			pf(fmt.Sprintf("[%s] %s", name, line))
		}
	}

	_, err := runStreamingCmd(cmd, compPf)
	return err
}

// ApplyComponent runs docker compose up -d in the given component directory.
// Skips unchanged components (compose file + env file hash match) to avoid unnecessary restarts.
func ApplyComponent(ctx context.Context, dir string, progressFn ...ProgressFunc) *ComponentResult {
	name := filepath.Base(dir)
	result := &ComponentResult{
		Name:      name,
		Path:      dir,
		UpdatedAt: time.Now(),
	}

	var pf ProgressFunc
	if len(progressFn) > 0 {
		pf = progressFn[0]
	}

	// Check if compose files changed since last apply
	newHash, hashErr := composeFileHash(dir)
	if hashErr == nil {
		composeHashMu.Lock()
		oldHash, exists := composeHashes[dir]
		composeHashMu.Unlock()

		if exists && oldHash == newHash {
			slog.Debug("component unchanged, skipping", "name", name)
			if pf != nil {
				pf(fmt.Sprintf("[%s] unchanged, skipping", name))
			}
			result.Status = "running"
			return result
		}
	}

	// Run predeploy hook if present
	if err := runPredeploy(ctx, dir, pf); err != nil {
		result.Status = "error"
		result.Error = fmt.Sprintf("predeploy failed: %s", err)
		slog.Error("predeploy failed", "name", name, "error", err)
		if pf != nil {
			pf(fmt.Sprintf("[%s] ERROR predeploy: %s", name, err))
		}
		return result
	}

	args := composeArgs(dir, "up", "-d", "--pull", "always", "--remove-orphans")
	slog.Info("applying component", "name", name, "dir", dir, "cmd", "docker "+strings.Join(args, " "))
	if pf != nil {
		pf(fmt.Sprintf("[%s] docker %s", name, strings.Join(args, " ")))
	}

	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Dir = dir

	// Prefix each output line with the component name.
	var compPf ProgressFunc
	if pf != nil {
		compPf = func(line string) {
			pf(fmt.Sprintf("[%s] %s", name, line))
		}
	}

	out, err := runStreamingCmd(cmd, compPf)
	if err != nil {
		result.Status = "error"
		result.Error = fmt.Sprintf("%s: %s", err, strings.TrimSpace(out))
		slog.Error("component apply failed", "name", name, "error", result.Error)
		if pf != nil {
			pf(fmt.Sprintf("[%s] ERROR: %s", name, result.Error))
		}
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
	if pf != nil {
		pf(fmt.Sprintf("[%s] applied successfully", name))
	}
	return result
}

// ClearHashCache removes all cached compose file hashes, forcing a full redeploy on next apply.
func ClearHashCache() {
	composeHashMu.Lock()
	composeHashes = make(map[string]string)
	composeHashMu.Unlock()
}

// ClearHashForDir removes the cached hash for a single component directory,
// forcing re-apply on next cycle even if compose files haven't changed.
func ClearHashForDir(dir string) {
	composeHashMu.Lock()
	delete(composeHashes, dir)
	composeHashMu.Unlock()
}

// DriftInfo describes the drift state of a single component.
type DriftInfo struct {
	Name           string `json:"name"`
	Running        bool   `json:"running"`
	ContainerCount int    `json:"containerCount"`
	RunningCount   int    `json:"runningCount"`
	Drifted        bool   `json:"drifted"`
}

// composeContainer is the JSON structure from docker compose ps --format json.
type composeContainer struct {
	State string `json:"State"`
}

// DriftCheck compares the expected state (compose file exists) with actual running containers.
// Returns drift info showing whether any expected containers are not running.
func DriftCheck(ctx context.Context, dir string) (*DriftInfo, error) {
	name := filepath.Base(dir)
	info := &DriftInfo{Name: name}

	// Get ALL containers for this compose project (not just running)
	args := composeArgs(dir, "ps", "--format", "json")
	slog.Debug("drift check", "name", name, "cmd", "docker "+strings.Join(args, " "))
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Dir = dir

	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("docker compose ps for %s: %w", name, err)
	}

	output := strings.TrimSpace(string(out))
	if output == "" || output == "[]" {
		// No containers at all. This is drift if compose file exists.
		_, composeErr := os.Stat(filepath.Join(dir, "docker-compose.yml"))
		if composeErr == nil {
			info.Drifted = true
		}
		return info, nil
	}

	// Parse JSON output. docker compose ps --format json outputs one JSON object per line
	// or a JSON array depending on version.
	var containers []composeContainer
	if strings.HasPrefix(output, "[") {
		if err := json.Unmarshal([]byte(output), &containers); err != nil {
			return nil, fmt.Errorf("parse compose ps JSON array for %s: %w", name, err)
		}
	} else {
		for _, line := range strings.Split(output, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var c composeContainer
			if err := json.Unmarshal([]byte(line), &c); err != nil {
				continue
			}
			containers = append(containers, c)
		}
	}

	info.ContainerCount = len(containers)
	for _, c := range containers {
		if strings.EqualFold(c.State, "running") {
			info.RunningCount++
		}
	}

	info.Running = info.RunningCount > 0 && info.RunningCount == info.ContainerCount
	info.Drifted = info.ContainerCount > 0 && info.RunningCount < info.ContainerCount

	return info, nil
}

// ApplyAll applies docker compose for all component directories.
// Returns results for each component.
func ApplyAll(ctx context.Context, dirs []string, progressFn ...ProgressFunc) []*ComponentResult {
	var pf ProgressFunc
	if len(progressFn) > 0 {
		pf = progressFn[0]
	}
	var results []*ComponentResult
	for _, dir := range dirs {
		result := ApplyComponent(ctx, dir, pf)
		results = append(results, result)
	}
	return results
}

// StopComponent runs docker compose down in a component directory.
func StopComponent(ctx context.Context, dir string) error {
	name := filepath.Base(dir)
	args := composeArgs(dir, "down", "--remove-orphans")
	slog.Info("stopping component", "name", name, "dir", dir, "cmd", "docker "+strings.Join(args, " "))
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Dir = dir

	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker compose down for %s: %w\n%s", name, err, string(out))
	}
	return nil
}

// StopComponentByProject stops a compose project by name without needing
// a compose file on disk. Uses Docker's project label to find containers.
func StopComponentByProject(ctx context.Context, projectName string, progressFn ...ProgressFunc) error {
	slog.Info("stopping removed component by project name", "project", projectName)

	var pf ProgressFunc
	if len(progressFn) > 0 {
		pf = progressFn[0]
	}
	if pf != nil {
		pf(fmt.Sprintf("[%s] docker compose down --remove-orphans", projectName))
	}

	cmd := exec.CommandContext(ctx, "docker", "compose", "-p", projectName, "down", "--remove-orphans")

	var compPf ProgressFunc
	if pf != nil {
		compPf = func(line string) {
			pf(fmt.Sprintf("[%s] %s", projectName, line))
		}
	}

	out, err := runStreamingCmd(cmd, compPf)
	if err != nil {
		return fmt.Errorf("docker compose down -p %s: %w\n%s", projectName, err, out)
	}
	return nil
}

// ComponentStatus checks if a component's containers are running.
func ComponentStatus(ctx context.Context, dir string) string {
	args := composeArgs(dir, "ps", "--format", "json", "--status", "running")
	cmd := exec.CommandContext(ctx, "docker", args...)
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
