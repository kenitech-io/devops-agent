package gitops

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"
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

// ApplyComponent runs docker compose up -d in the given component directory.
func ApplyComponent(ctx context.Context, dir string) *ComponentResult {
	name := filepath.Base(dir)
	result := &ComponentResult{
		Name:      name,
		Path:      dir,
		UpdatedAt: time.Now(),
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

	result.Status = "running"
	slog.Info("component applied", "name", name)
	return result
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
