package gitops

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/kenitech-io/devops-agent/internal/secrets"
	"github.com/kenitech-io/devops-agent/internal/ws"
)

// DefaultPollInterval is the default interval between git pull checks.
const DefaultPollInterval = 30 * time.Second

// SecretsConfig holds the parameters needed to fetch secrets from the dashboard.
type SecretsConfig struct {
	DashboardURL string
	AgentID      string
	WSToken      string
}

// Operator manages the GitOps lifecycle: clone, poll, apply, report.
type Operator struct {
	repo         *Repo
	role         string
	pollInterval time.Duration
	triggerCh    chan struct{}
	secretsCfg   *SecretsConfig

	mu           sync.RWMutex
	commitHash   string
	syncStatus   string // "synced", "syncing", "error", "pending"
	syncError    string
	lastSync     time.Time
	components   []*ComponentResult
	driftInfo    map[string]*DriftInfo // component name -> drift state
}

// NewOperator creates a new GitOps operator.
func NewOperator(repo *Repo, role string) *Operator {
	return &Operator{
		repo:         repo,
		role:         role,
		pollInterval: DefaultPollInterval,
		syncStatus:   "pending",
		triggerCh:    make(chan struct{}, 1),
	}
}

// SetSecretsConfig configures the operator to fetch and inject secrets
// from the dashboard before applying components.
func (o *Operator) SetSecretsConfig(cfg *SecretsConfig) {
	o.secretsCfg = cfg
}

// TriggerSync requests an immediate sync cycle. Non-blocking.
func (o *Operator) TriggerSync() {
	select {
	case o.triggerCh <- struct{}{}:
		slog.Info("immediate sync triggered")
	default:
		slog.Info("sync already pending, skipping trigger")
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
		case <-o.triggerCh:
			slog.Info("running triggered sync")
			o.pollAndApply(ctx)
		}
	}
}

// pollAndApply checks for repo changes and applies if updated.
// Detects orphaned components (removed from Git) and stops them.
func (o *Operator) pollAndApply(ctx context.Context) {
	o.setStatus("syncing", "")

	// Phase 1: Record component names before pull
	oldNames := o.repo.ComponentDirNames(o.role)

	// Phase 2: Pull
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

	// Phase 3: Record component names after pull
	newNames := o.repo.ComponentDirNames(o.role)
	newSet := make(map[string]bool, len(newNames))
	for _, n := range newNames {
		newSet[n] = true
	}

	// Phase 4: Stop orphaned components (in old but not in new)
	for _, name := range oldNames {
		if !newSet[name] {
			slog.Info("component removed from repo, stopping", "name", name, "role", o.role)
			if err := StopComponentByProject(ctx, name); err != nil {
				slog.Error("failed to stop orphaned component", "name", name, "error", err)
			}
		}
	}

	// Phase 5: Apply current components if repo changed
	if updated {
		slog.Info("repo changed, applying", "commit", hash[:8])
		if err := o.applyAll(ctx); err != nil {
			slog.Error("apply after pull failed", "error", err)
			return
		}
	}

	// Phase 6: Drift detection. Check all components are actually running,
	// regardless of whether git changed. Auto-remediate drifted components.
	o.checkAndRemediateDrift(ctx)

	o.setStatus("synced", "")
	o.mu.Lock()
	o.lastSync = time.Now()
	o.mu.Unlock()
}

// checkAndRemediateDrift checks if any expected containers are not running
// and re-applies their compose files to restore them.
func (o *Operator) checkAndRemediateDrift(ctx context.Context) {
	dirs, err := o.repo.ComponentDirs(o.role)
	if err != nil {
		slog.Debug("drift check: cannot list component dirs", "error", err)
		return
	}
	driftMap := make(map[string]*DriftInfo, len(dirs))

	for _, dir := range dirs {
		info, err := DriftCheck(ctx, dir)
		if err != nil {
			slog.Debug("drift check failed", "dir", dir, "error", err)
			continue
		}

		driftMap[info.Name] = info

		if info.Drifted {
			slog.Warn("drift detected, re-applying component",
				"component", info.Name,
				"containers", info.ContainerCount,
				"running", info.RunningCount,
			)
			ClearHashForDir(dir)
			result := ApplyComponent(ctx, dir)
			if result.Status == "error" {
				slog.Error("drift remediation failed", "component", info.Name, "error", result.Error)
			} else {
				slog.Info("drift remediated", "component", info.Name)
			}
		}
	}

	o.mu.Lock()
	o.driftInfo = driftMap
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

	// Inject secrets into .env files before applying
	o.injectSecrets(dirs)

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

// injectSecrets fetches secrets from the dashboard and injects them into .env
// files that contain ${VAR} patterns. Non-fatal: logs errors but does not fail the sync.
func (o *Operator) injectSecrets(dirs []string) {
	if o.secretsCfg == nil || o.secretsCfg.DashboardURL == "" || o.secretsCfg.WSToken == "" {
		return
	}

	// Check if any dir has an .env file with ${VAR} patterns before fetching
	hasEnvFiles := false
	for _, dir := range dirs {
		envPath := filepath.Join(dir, ".env")
		if _, err := os.Stat(envPath); err == nil {
			hasEnvFiles = true
			break
		}
	}
	if !hasEnvFiles {
		return
	}

	fetched, err := secrets.FetchSecrets(o.secretsCfg.DashboardURL, o.secretsCfg.AgentID, o.secretsCfg.WSToken)
	if err != nil {
		slog.Error("failed to fetch secrets from dashboard", "error", err)
		return
	}

	if len(fetched) == 0 {
		return
	}

	for _, dir := range dirs {
		envPath := filepath.Join(dir, ".env")
		if _, err := os.Stat(envPath); err != nil {
			continue
		}
		if err := secrets.InjectSecrets(envPath, fetched); err != nil {
			slog.Error("failed to inject secrets", "dir", dir, "error", err)
		}
	}
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
		// Populate drift info if available
		if di, ok := o.driftInfo[c.Name]; ok {
			comp.ContainerCount = di.ContainerCount
			comp.RunningCount = di.RunningCount
			comp.Drifted = di.Drifted
		}
		status.Components = append(status.Components, comp)
	}

	return status
}
