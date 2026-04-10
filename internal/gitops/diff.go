package gitops

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"
)

// DiffResult describes what would change if a full sync were applied.
type DiffResult struct {
	CommitHash string          `json:"commitHash"`
	Components []ComponentDiff `json:"components"`
}

// ComponentDiff describes the expected change for a single component.
type ComponentDiff struct {
	Name               string `json:"name"`
	Action             string `json:"action"` // "start", "stop", "update", "unchanged", "restart"
	Detail             string `json:"detail,omitempty"`
	RunningContainers  int    `json:"runningContainers"`
	ExpectedContainers int    `json:"expectedContainers"`
	ConfigChanged      bool   `json:"configChanged"`
}

// composeLsEntry is the JSON structure from docker compose ls --format json.
type composeLsEntry struct {
	Name   string `json:"Name"`
	Status string `json:"Status"`
}

// Diff compares the desired state in the git repo against actually running
// containers. If componentFilter is non-empty, only those components are
// included. Returns a structured diff the dashboard can show in a modal.
func (o *Operator) Diff(ctx context.Context, componentFilter []string) (*DiffResult, error) {
	// Pull latest to make sure we're comparing against current desired state.
	if _, err := o.repo.Pull(ctx); err != nil {
		slog.Warn("diff: pull failed, using current clone state", "error", err)
	}

	hash, _ := o.repo.CommitHash()

	result := &DiffResult{CommitHash: hash}

	// Desired components from git.
	dirs, err := o.repo.ComponentDirs(o.role)
	if err != nil {
		return nil, fmt.Errorf("listing component dirs: %w", err)
	}

	filterSet := make(map[string]bool)
	for _, f := range componentFilter {
		filterSet[strings.ToLower(f)] = true
	}

	desiredNames := make(map[string]bool)
	for _, dir := range dirs {
		name := filepath.Base(dir)
		desiredNames[name] = true

		if len(filterSet) > 0 && !filterSet[name] {
			continue
		}

		diff := ComponentDiff{Name: name}

		// Check running state via drift detection.
		drift, driftErr := DriftCheck(ctx, dir)
		if driftErr != nil {
			diff.Action = "start"
			diff.Detail = fmt.Sprintf("cannot check state: %s", driftErr)
			result.Components = append(result.Components, diff)
			continue
		}

		diff.RunningContainers = drift.RunningCount
		diff.ExpectedContainers = drift.ContainerCount

		// Check if compose config has changed since last apply.
		newHash, hashErr := composeFileHash(dir)
		if hashErr == nil {
			composeHashMu.Lock()
			oldHash, exists := composeHashes[dir]
			composeHashMu.Unlock()
			if !exists || oldHash != newHash {
				diff.ConfigChanged = true
			}
		}

		switch {
		case drift.ContainerCount == 0 && drift.RunningCount == 0:
			// No containers at all. Component will be started from scratch.
			diff.Action = "start"
			diff.Detail = "new component, will pull images and start"
		case drift.Drifted:
			// Some containers not running.
			diff.Action = "restart"
			diff.Detail = fmt.Sprintf("%d/%d containers running", drift.RunningCount, drift.ContainerCount)
		case diff.ConfigChanged:
			// All running but config changed.
			diff.Action = "update"
			diff.Detail = "compose config changed, will recreate"
		default:
			diff.Action = "unchanged"
		}

		result.Components = append(result.Components, diff)
	}

	// Detect orphans: compose projects running on the server but not in git.
	if len(filterSet) == 0 {
		orphans := listOrphanProjects(ctx, desiredNames, o.role)
		for _, name := range orphans {
			result.Components = append(result.Components, ComponentDiff{
				Name:   name,
				Action: "stop",
				Detail: "not in git repo, will be stopped",
			})
		}
	}

	return result, nil
}

// listOrphanProjects finds compose projects running on the server that are
// not present in the git repo for this role.
func listOrphanProjects(ctx context.Context, desired map[string]bool, role string) []string {
	cmd := exec.CommandContext(ctx, "docker", "compose", "ls", "--format", "json")
	out, err := cmd.Output()
	if err != nil {
		return nil
	}

	var entries []composeLsEntry
	if err := json.Unmarshal(out, &entries); err != nil {
		return nil
	}

	// Role prefix used in compose project names (e.g. "core-traefik", "prod-backup").
	prefix := strings.ToLower(role) + "-"

	var orphans []string
	for _, e := range entries {
		name := strings.ToLower(e.Name)
		// Only consider projects that match this server's role.
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		compName := strings.TrimPrefix(name, prefix)
		if !desired[compName] {
			orphans = append(orphans, compName)
		}
	}
	return orphans
}
