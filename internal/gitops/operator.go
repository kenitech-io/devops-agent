package gitops

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/kenitech-io/devops-agent/internal/ws"
)

// DefaultPollInterval is the default interval between git pull checks.
const DefaultPollInterval = 30 * time.Second

// Operator manages the GitOps lifecycle: clone, poll, apply, report.
type Operator struct {
	repo         *Repo
	role         string
	pollInterval time.Duration

	mu           sync.RWMutex
	commitHash   string
	syncStatus   string // "synced", "syncing", "error", "pending"
	syncError    string
	lastSync     time.Time
	components   []*ComponentResult
}

// NewOperator creates a new GitOps operator.
func NewOperator(repo *Repo, role string) *Operator {
	return &Operator{
		repo:         repo,
		role:         role,
		pollInterval: DefaultPollInterval,
		syncStatus:   "pending",
	}
}

// Run starts the GitOps loop. It clones the repo, applies the initial state,
// then polls for changes. Blocks until the context is cancelled.
func (o *Operator) Run(ctx context.Context) error {
	slog.Info("gitops operator starting", "role", o.role, "repo", o.repo.url)

	// Phase 1: Clone
	o.setStatus("syncing", "")
	if err := o.repo.Clone(); err != nil {
		o.setStatus("error", fmt.Sprintf("clone failed: %s", err))
		return fmt.Errorf("initial clone: %w", err)
	}

	hash, err := o.repo.CommitHash()
	if err != nil {
		o.setStatus("error", fmt.Sprintf("commit hash: %s", err))
		return fmt.Errorf("getting initial commit: %w", err)
	}
	o.mu.Lock()
	o.commitHash = hash
	o.mu.Unlock()

	slog.Info("repo cloned", "commit", hash[:8])

	// Phase 2: Initial apply
	if err := o.applyAll(ctx); err != nil {
		slog.Error("initial apply failed", "error", err)
		// Continue running, will retry on next poll
	}

	// Phase 3: Poll loop
	ticker := time.NewTicker(o.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("gitops operator stopping")
			return ctx.Err()
		case <-ticker.C:
			o.pollAndApply(ctx)
		}
	}
}

// pollAndApply checks for repo changes and applies if updated.
func (o *Operator) pollAndApply(ctx context.Context) {
	o.setStatus("syncing", "")

	updated, err := o.repo.Pull()
	if err != nil {
		o.setStatus("error", fmt.Sprintf("pull failed: %s", err))
		slog.Error("git pull failed", "error", err)
		return
	}

	hash, err := o.repo.CommitHash()
	if err != nil {
		slog.Error("getting commit hash after pull", "error", err)
	} else {
		o.mu.Lock()
		o.commitHash = hash
		o.mu.Unlock()
	}

	if updated {
		slog.Info("repo changed, applying", "commit", hash[:8])
		if err := o.applyAll(ctx); err != nil {
			slog.Error("apply after pull failed", "error", err)
			return
		}
	}

	o.setStatus("synced", "")
	o.mu.Lock()
	o.lastSync = time.Now()
	o.mu.Unlock()
}

// applyAll discovers and applies all component directories for the server role.
func (o *Operator) applyAll(ctx context.Context) error {
	dirs, err := o.repo.ComponentDirs(o.role)
	if err != nil {
		o.setStatus("error", fmt.Sprintf("component discovery: %s", err))
		return err
	}

	if len(dirs) == 0 {
		slog.Warn("no components found for role", "role", o.role)
		o.setStatus("synced", "")
		return nil
	}

	slog.Info("applying components", "count", len(dirs), "role", o.role)
	results := ApplyAll(ctx, dirs)

	// Check for errors
	var errCount int
	for _, r := range results {
		if r.Status == "error" {
			errCount++
		}
	}

	// Update components, lastSync, and status atomically.
	o.mu.Lock()
	o.components = results
	o.lastSync = time.Now()
	if errCount > 0 {
		o.syncStatus = "error"
		o.syncError = fmt.Sprintf("%d/%d components failed", errCount, len(results))
	} else {
		o.syncStatus = "synced"
		o.syncError = ""
	}
	o.mu.Unlock()

	if errCount > 0 {
		return fmt.Errorf("%d components failed", errCount)
	}
	return nil
}

func (o *Operator) setStatus(status, errMsg string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.syncStatus = status
	o.syncError = errMsg
}

// Status returns the current GitOps status for inclusion in status reports.
func (o *Operator) Status() *ws.GitOpsStatus {
	o.mu.RLock()
	defer o.mu.RUnlock()

	status := &ws.GitOpsStatus{
		Enabled:    true,
		RepoURL:    o.repo.url,
		CommitHash: o.commitHash,
		Branch:     o.repo.Branch(),
		SyncStatus: o.syncStatus,
		Error:      o.syncError,
	}

	if !o.lastSync.IsZero() {
		status.LastSync = o.lastSync.UTC().Format(time.RFC3339)
	}

	for _, c := range o.components {
		comp := ws.GitOpsComponentStatus{
			Name:   c.Name,
			Path:   c.Path,
			Status: c.Status,
			Error:  c.Error,
		}
		if !c.UpdatedAt.IsZero() {
			comp.UpdatedAt = c.UpdatedAt.UTC().Format(time.RFC3339)
		}
		status.Components = append(status.Components, comp)
	}

	return status
}
