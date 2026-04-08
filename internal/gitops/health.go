package gitops

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// HealthCheckTimeout is how long to wait for containers to become healthy after apply.
const HealthCheckTimeout = 60 * time.Second

// HealthCheckInterval is how often to poll container health during verification.
const HealthCheckInterval = 5 * time.Second

// healthContainer is the JSON structure from docker compose ps --format json
// with health-relevant fields.
type healthContainer struct {
	Name   string `json:"Name"`
	State  string `json:"State"`
	Health string `json:"Health"`
	Status string `json:"Status"`
}

// HealthVerify waits for all containers in a compose project to be running
// and healthy (if they have health checks). Returns nil on success, error
// if any container is unhealthy or not running within the timeout.
func HealthVerify(ctx context.Context, dir string) error {
	name := filepath.Base(dir)

	deadline := time.After(HealthCheckTimeout)
	ticker := time.NewTicker(HealthCheckInterval)
	defer ticker.Stop()

	// Initial wait for containers to start
	time.Sleep(3 * time.Second)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			// One final check
			healthy, detail := checkHealth(ctx, dir)
			if healthy {
				return nil
			}
			return fmt.Errorf("health check timeout for %s: %s", name, detail)
		case <-ticker.C:
			healthy, _ := checkHealth(ctx, dir)
			if healthy {
				slog.Info("health check passed", "component", name)
				return nil
			}
		}
	}
}

// checkHealth returns true if all containers are running and healthy.
// Returns a detail string describing the current state.
func checkHealth(ctx context.Context, dir string) (bool, string) {
	cmd := exec.CommandContext(ctx, "docker", "compose", "ps", "--format", "json")
	cmd.Dir = dir

	out, err := cmd.Output()
	if err != nil {
		return false, fmt.Sprintf("docker compose ps failed: %s", err)
	}

	output := strings.TrimSpace(string(out))
	if output == "" || output == "[]" {
		return false, "no containers found"
	}

	var containers []healthContainer
	if strings.HasPrefix(output, "[") {
		if err := json.Unmarshal([]byte(output), &containers); err != nil {
			return false, fmt.Sprintf("parse error: %s", err)
		}
	} else {
		for _, line := range strings.Split(output, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var c healthContainer
			if err := json.Unmarshal([]byte(line), &c); err != nil {
				continue
			}
			containers = append(containers, c)
		}
	}

	if len(containers) == 0 {
		return false, "no containers found"
	}

	for _, c := range containers {
		if !strings.EqualFold(c.State, "running") {
			return false, fmt.Sprintf("%s is %s", c.Name, c.State)
		}
		// If container has a health check, it must be healthy
		health := parseHealthFromStatus(c.Status)
		if health == "unhealthy" {
			return false, fmt.Sprintf("%s is unhealthy", c.Name)
		}
		// "starting" means health check hasn't passed yet
		if health == "starting" {
			return false, fmt.Sprintf("%s health check starting", c.Name)
		}
	}

	return true, ""
}

// parseHealthFromStatus extracts health status from Docker's status string.
// e.g., "Up 5 seconds (healthy)" -> "healthy"
func parseHealthFromStatus(status string) string {
	lower := strings.ToLower(status)
	if strings.Contains(lower, "(healthy)") {
		return "healthy"
	}
	if strings.Contains(lower, "(unhealthy)") {
		return "unhealthy"
	}
	if strings.Contains(lower, "(health: starting)") || strings.Contains(lower, "health: starting") {
		return "starting"
	}
	// No health check configured
	return ""
}
