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

// componentSnapshot stores compose file contents for rollback.
type componentSnapshot struct {
	files map[string]map[string][]byte // dir -> filename -> content
}

// snapshotComponents saves the compose files and .env for each component directory.
func snapshotComponents(dirs []string) *componentSnapshot {
	s := &componentSnapshot{files: make(map[string]map[string][]byte)}
	for _, dir := range dirs {
		s.files[dir] = make(map[string][]byte)
		for _, name := range []string{"docker-compose.yml", ".env"} {
			data, err := os.ReadFile(filepath.Join(dir, name))
			if err == nil {
				s.files[dir][name] = data
			}
		}
	}
	return s
}

// restore writes the snapshotted files back to disk.
func (s *componentSnapshot) restore() error {
	for dir, files := range s.files {
		for name, content := range files {
			if err := os.WriteFile(filepath.Join(dir, name), content, 0644); err != nil {
				return fmt.Errorf("restoring %s/%s: %w", dir, name, err)
			}
		}
	}
	return nil
}

// DefaultPollInterval is the default interval between git pull checks.
const DefaultPollInterval = 30 * time.Second

// SecretsConfig holds the parameters needed to fetch secrets from the dashboard.
type SecretsConfig struct {
	DashboardURL string
	AgentID      string
	WSToken      string
}

// SyncResult holds the outcome of a single sync cycle.
type SyncResult struct {
	CommitHash string
	Updated    bool
	Components []*ComponentResult
	DriftInfo  map[string]*DriftInfo
	Error      string
	DurationMs int64
}

// SyncNotifyFunc is called after every sync cycle with the result.
type SyncNotifyFunc func(SyncResult)

// Operator manages the GitOps lifecycle: clone, poll, apply, report.
type Operator struct {
	repo         *Repo
	role         string
	pollInterval time.Duration
	triggerCh    chan struct{}
	secretsCfg   *SecretsConfig
	onSyncDone   SyncNotifyFunc

	mu           sync.RWMutex
	commitHash   string
	syncStatus   string // "synced", "syncing", "error", "pending"
	syncError    string
	lastSync     time.Time
	components   []*ComponentResult
	driftInfo    map[string]*DriftInfo // component name -> drift state

	// Rollback state: commit hash that failed health checks.
	// When set, the operator skips applying this commit on subsequent polls.
	badCommit string
	// Snapshot of compose files before the last pull, used for rollback.
	snapshot *componentSnapshot

	waitersMu sync.Mutex
	waiters   []syncWaiter
}

type syncWaiter struct {
	ch         chan SyncResult
	progressFn ProgressFunc
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

// SetSyncCallback registers a function called after every sync cycle.
func (o *Operator) SetSyncCallback(fn SyncNotifyFunc) {
	o.onSyncDone = fn
}

// TriggerSyncWait triggers an immediate sync and blocks until it completes.
// If progressFn is provided, real-time output from git and docker compose
// commands is streamed to it.
func (o *Operator) TriggerSyncWait(ctx context.Context, progressFn ...ProgressFunc) (SyncResult, error) {
	ch := make(chan SyncResult, 1)
	w := syncWaiter{ch: ch}
	if len(progressFn) > 0 {
		w.progressFn = progressFn[0]
	}
	o.waitersMu.Lock()
	o.waiters = append(o.waiters, w)
	o.waitersMu.Unlock()

	o.TriggerSync()

	select {
	case result := <-ch:
		return result, nil
	case <-ctx.Done():
		// Remove our waiter to avoid leaking
		o.waitersMu.Lock()
		for i, ww := range o.waiters {
			if ww.ch == ch {
				o.waiters = append(o.waiters[:i], o.waiters[i+1:]...)
				break
			}
		}
		o.waitersMu.Unlock()
		return SyncResult{}, ctx.Err()
	}
}

// notifySyncDone sends the result to the callback and all waiters.
func (o *Operator) notifySyncDone(result SyncResult) {
	if o.onSyncDone != nil {
		o.onSyncDone(result)
	}
	o.waitersMu.Lock()
	waiters := o.waiters
	o.waiters = nil
	o.waitersMu.Unlock()
	for _, w := range waiters {
		w.ch <- result
		close(w.ch)
	}
}

// collectProgressFn returns a ProgressFunc that broadcasts to all current
// waiters that have a progressFn. Returns nil if none do.
func (o *Operator) collectProgressFn() ProgressFunc {
	o.waitersMu.Lock()
	var fns []ProgressFunc
	for _, w := range o.waiters {
		if w.progressFn != nil {
			fns = append(fns, w.progressFn)
		}
	}
	o.waitersMu.Unlock()

	if len(fns) == 0 {
		return nil
	}
	if len(fns) == 1 {
		return fns[0]
	}
	return func(line string) {
		for _, fn := range fns {
			fn(line)
		}
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
	if err := o.repo.Clone(ctx); err != nil {
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
			o.pollAndApply(ctx, nil)
		case <-o.triggerCh:
			slog.Info("running triggered sync")
			pf := o.collectProgressFn()
			o.pollAndApply(ctx, pf)
		}
	}
}

// pollAndApply checks for repo changes and applies if updated.
// Detects orphaned components (removed from Git) and stops them.
// After applying a new commit, verifies container health. If health fails,
// restores the previous compose files and re-applies (rollback).
// progressFn, if non-nil, receives real-time output from every command.
func (o *Operator) pollAndApply(ctx context.Context, progressFn ProgressFunc) {
	start := time.Now()
	o.setStatus("syncing", "")

	emit := func(line string) {
		if progressFn != nil {
			progressFn(line)
		}
	}

	result := SyncResult{}

	// Phase 1: Record component names and snapshot compose files before pull
	oldNames := o.repo.ComponentDirNames(o.role)
	dirs, _ := o.repo.ComponentDirs(o.role)
	o.snapshot = snapshotComponents(dirs)

	// Phase 2: Pull
	emit("--- git pull ---")
	updated, err := o.repo.Pull(ctx, progressFn)
	if err != nil {
		o.setStatus("error", fmt.Sprintf("pull failed: %s", err))
		slog.Error("git pull failed", "error", err)
		result.Error = fmt.Sprintf("pull failed: %s", err)
		result.DurationMs = time.Since(start).Milliseconds()
		emit(fmt.Sprintf("FAILED: %s", err))
		o.notifySyncDone(result)
		return
	}
	result.Updated = updated

	hash, err := o.repo.CommitHash()
	if err != nil {
		slog.Error("getting commit hash after pull", "error", err)
	} else {
		o.mu.Lock()
		o.commitHash = hash
		o.mu.Unlock()
		result.CommitHash = hash
	}

	// Phase 3: Check if this commit was previously marked as bad
	o.mu.RLock()
	isBadCommit := o.badCommit != "" && o.badCommit == hash
	o.mu.RUnlock()
	if isBadCommit && !updated {
		emit(fmt.Sprintf("Skipping apply: commit %s previously failed health checks", hash[:8]))
		// Still run drift check to keep containers running
		emit("--- drift check ---")
		o.checkAndRemediateDrift(ctx, progressFn)
		o.setStatus("synced", "")
		o.mu.Lock()
		o.lastSync = time.Now()
		result.Components = o.components
		result.DriftInfo = o.driftInfo
		o.mu.Unlock()
		result.DurationMs = time.Since(start).Milliseconds()
		o.notifySyncDone(result)
		return
	}

	// Clear bad commit if a new commit arrived
	if updated {
		o.mu.Lock()
		o.badCommit = ""
		o.mu.Unlock()
	}

	// Phase 4: Record component names after pull
	newNames := o.repo.ComponentDirNames(o.role)
	newSet := make(map[string]bool, len(newNames))
	for _, n := range newNames {
		newSet[n] = true
	}

	// Phase 5: Stop orphaned components (in old but not in new)
	for _, name := range oldNames {
		if !newSet[name] {
			slog.Info("component removed from repo, stopping", "name", name, "role", o.role)
			emit(fmt.Sprintf("--- removing orphaned component: %s ---", name))
			if err := StopComponentByProject(ctx, name, progressFn); err != nil {
				slog.Error("failed to stop orphaned component", "name", name, "error", err)
			}
		}
	}

	// Phase 6: Apply current components if repo changed
	if updated {
		slog.Info("repo changed, applying", "commit", hash[:8])
		emit(fmt.Sprintf("--- applying components (commit %s) ---", hash[:8]))
		if err := o.applyAll(ctx, progressFn); err != nil {
			slog.Error("apply after pull failed", "error", err)
			result.Error = fmt.Sprintf("apply failed: %s", err)
			result.DurationMs = time.Since(start).Milliseconds()
			o.mu.RLock()
			result.Components = o.components
			o.mu.RUnlock()
			o.notifySyncDone(result)
			return
		}

		// Phase 6b: Verify health after applying new commit.
		// If health fails, rollback to previous compose files.
		emit("--- health verification ---")
		if err := o.verifyAndRollback(ctx, hash, progressFn); err != nil {
			slog.Error("health verification failed, rollback attempted", "error", err)
			result.Error = fmt.Sprintf("rollback: %s", err)
		}
	} else {
		emit("No changes, skipping apply")
	}

	// Phase 7: Drift detection. Check all components are actually running,
	// regardless of whether git changed. Auto-remediate drifted components.
	emit("--- drift check ---")
	o.checkAndRemediateDrift(ctx, progressFn)

	o.setStatus("synced", "")
	o.mu.Lock()
	o.lastSync = time.Now()
	result.Components = o.components
	result.DriftInfo = o.driftInfo
	o.mu.Unlock()

	result.DurationMs = time.Since(start).Milliseconds()
	emit(fmt.Sprintf("Sync complete in %dms", result.DurationMs))
	o.notifySyncDone(result)
}

// verifyAndRollback checks health of all components after a new deploy.
// If any component fails health checks, restores the previous compose files
// and re-applies them.
func (o *Operator) verifyAndRollback(ctx context.Context, commitHash string, progressFn ProgressFunc) error {
	emit := func(line string) {
		if progressFn != nil {
			progressFn(line)
		}
	}

	dirs, err := o.repo.ComponentDirs(o.role)
	if err != nil {
		return nil // Can't verify without dirs
	}

	// Verify health of each component
	var unhealthy []string
	for _, dir := range dirs {
		name := filepath.Base(dir)
		if err := HealthVerify(ctx, dir); err != nil {
			slog.Warn("component failed health check", "component", name, "error", err)
			emit(fmt.Sprintf("[%s] UNHEALTHY: %s", name, err))
			unhealthy = append(unhealthy, name)
		} else {
			emit(fmt.Sprintf("[%s] healthy", name))
		}
	}

	if len(unhealthy) == 0 {
		emit("All components healthy")
		return nil
	}

	// Health check failed. Attempt rollback.
	emit(fmt.Sprintf("--- ROLLBACK: %d unhealthy components ---", len(unhealthy)))
	slog.Warn("initiating rollback", "unhealthy", unhealthy, "commit", commitHash[:8])

	if o.snapshot == nil || len(o.snapshot.files) == 0 {
		emit("No snapshot available for rollback (first deploy)")
		o.mu.Lock()
		o.badCommit = commitHash
		o.mu.Unlock()
		return fmt.Errorf("%d components unhealthy, no snapshot for rollback", len(unhealthy))
	}

	// Restore previous compose files
	if err := o.snapshot.restore(); err != nil {
		emit(fmt.Sprintf("Snapshot restore failed: %s", err))
		o.mu.Lock()
		o.badCommit = commitHash
		o.mu.Unlock()
		return fmt.Errorf("snapshot restore: %w", err)
	}
	emit("Previous compose files restored")

	// Clear hash cache and re-apply
	ClearHashCache()
	o.injectSecrets(dirs)
	emit("Re-applying previous configuration")
	ApplyAll(ctx, dirs, progressFn)

	// Mark this commit as bad so we don't retry it
	o.mu.Lock()
	o.badCommit = commitHash
	o.mu.Unlock()

	emit(fmt.Sprintf("Rollback complete. Commit %s marked as bad.", commitHash[:8]))
	return fmt.Errorf("rolled back commit %s: %d components unhealthy", commitHash[:8], len(unhealthy))
}

// checkAndRemediateDrift checks if any expected containers are not running
// and re-applies their compose files to restore them.
func (o *Operator) checkAndRemediateDrift(ctx context.Context, progressFn ProgressFunc) {
	dirs, err := o.repo.ComponentDirs(o.role)
	if err != nil {
		slog.Debug("drift check: cannot list component dirs", "error", err)
		return
	}
	driftMap := make(map[string]*DriftInfo, len(dirs))

	emit := func(line string) {
		if progressFn != nil {
			progressFn(line)
		}
	}

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
			emit(fmt.Sprintf("[%s] DRIFT: %d/%d containers running, re-applying", info.Name, info.RunningCount, info.ContainerCount))
			ClearHashForDir(dir)
			result := ApplyComponent(ctx, dir, progressFn)
			if result.Status == "error" {
				slog.Error("drift remediation failed", "component", info.Name, "error", result.Error)
			} else {
				slog.Info("drift remediated", "component", info.Name)
				emit(fmt.Sprintf("[%s] drift remediated", info.Name))
			}
		} else {
			emit(fmt.Sprintf("[%s] OK (%d/%d running)", info.Name, info.RunningCount, info.ContainerCount))
		}
	}

	o.mu.Lock()
	o.driftInfo = driftMap
	o.mu.Unlock()
}

// applyAll discovers and applies all component directories for the server role.
func (o *Operator) applyAll(ctx context.Context, progressFn ...ProgressFunc) error {
	var pf ProgressFunc
	if len(progressFn) > 0 {
		pf = progressFn[0]
	}

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
	results := ApplyAll(ctx, dirs, pf)

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
