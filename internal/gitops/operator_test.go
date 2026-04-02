package gitops

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewOperator(t *testing.T) {
	repo := mustNewRepo(t, "https://github.com/org/repo", "token", "/tmp/test")
	op := NewOperator(repo, "CORE")

	if op.role != "CORE" {
		t.Errorf("expected role CORE, got %s", op.role)
	}
	if op.pollInterval != DefaultPollInterval {
		t.Errorf("expected poll interval %v, got %v", DefaultPollInterval, op.pollInterval)
	}
	if op.syncStatus != "pending" {
		t.Errorf("expected pending status, got %s", op.syncStatus)
	}
}

func TestOperatorStatus_Initial(t *testing.T) {
	repo := mustNewRepo(t, "https://github.com/org/repo", "", "/tmp/test")
	op := NewOperator(repo, "CORE")

	status := op.Status()

	if !status.Enabled {
		t.Error("expected enabled")
	}
	if status.RepoURL != "https://github.com/org/repo" {
		t.Errorf("unexpected repo URL: %s", status.RepoURL)
	}
	if status.Branch != "main" {
		t.Errorf("expected branch main, got %s", status.Branch)
	}
	if status.SyncStatus != "pending" {
		t.Errorf("expected pending, got %s", status.SyncStatus)
	}
	if status.CommitHash != "" {
		t.Error("expected empty commit hash initially")
	}
	if status.LastSync != "" {
		t.Error("expected empty last sync initially")
	}
}

func TestOperatorStatus_WithData(t *testing.T) {
	repo := mustNewRepo(t, "https://github.com/org/repo", "", "/tmp/test")
	op := NewOperator(repo, "PROD")

	op.mu.Lock()
	op.commitHash = "abc123def456789012345678901234567890abcd"
	op.syncStatus = "synced"
	op.lastSync = time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	op.components = []*ComponentResult{
		{Name: "traefik", Path: "/var/lib/keni-agent/idp/prod/traefik", Status: "running", UpdatedAt: time.Now()},
		{Name: "monitoring", Path: "/var/lib/keni-agent/idp/prod/monitoring", Status: "error", Error: "image pull failed"},
		{Name: "pending", Path: "/var/lib/keni-agent/idp/prod/pending", Status: "pending"},
	}
	op.mu.Unlock()

	status := op.Status()

	if status.CommitHash != "abc123def456789012345678901234567890abcd" {
		t.Errorf("unexpected commit hash: %s", status.CommitHash)
	}
	if status.SyncStatus != "synced" {
		t.Errorf("expected synced, got %s", status.SyncStatus)
	}
	if status.LastSync != "2026-04-01T12:00:00Z" {
		t.Errorf("unexpected last sync: %s", status.LastSync)
	}
	if len(status.Components) != 3 {
		t.Fatalf("expected 3 components, got %d", len(status.Components))
	}
	if status.Components[0].Name != "traefik" {
		t.Errorf("expected traefik, got %s", status.Components[0].Name)
	}
	if status.Components[0].Status != "running" {
		t.Errorf("expected running, got %s", status.Components[0].Status)
	}
	if status.Components[1].Status != "error" {
		t.Errorf("expected error, got %s", status.Components[1].Status)
	}
	if status.Components[1].Error != "image pull failed" {
		t.Errorf("unexpected error message: %s", status.Components[1].Error)
	}
	// Zero UpdatedAt should produce empty string
	if status.Components[2].UpdatedAt != "" {
		t.Errorf("expected empty UpdatedAt for pending component, got %s", status.Components[2].UpdatedAt)
	}
}

func TestSetStatus(t *testing.T) {
	repo := mustNewRepo(t, "https://github.com/org/repo", "", "/tmp/test")
	op := NewOperator(repo, "CORE")

	op.setStatus("syncing", "")
	status := op.Status()
	if status.SyncStatus != "syncing" {
		t.Errorf("expected syncing, got %s", status.SyncStatus)
	}

	op.setStatus("error", "clone failed")
	status = op.Status()
	if status.SyncStatus != "error" {
		t.Errorf("expected error, got %s", status.SyncStatus)
	}
	if status.Error != "clone failed" {
		t.Errorf("unexpected error: %s", status.Error)
	}
}

func TestOperator_Run_CloneFailure(t *testing.T) {
	repo := mustNewRepo(t, "/nonexistent/bare/repo", "", filepath.Join(t.TempDir(), "clone"))
	op := NewOperator(repo, "CORE")

	err := op.Run(context.Background())
	if err == nil {
		t.Fatal("expected error from clone failure")
	}

	status := op.Status()
	if status.SyncStatus != "error" {
		t.Errorf("expected error status, got %s", status.SyncStatus)
	}
}

func TestOperator_Run_ContextCancellation(t *testing.T) {
	remoteDir, _ := setupTestRepo(t, map[string]string{
		"core/.gitkeep": "",
	})

	cloneDir := filepath.Join(t.TempDir(), "clone")
	repo := mustNewRepo(t, remoteDir, "", cloneDir)
	op := NewOperator(repo, "CORE")
	op.pollInterval = 10 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- op.Run(ctx)
	}()

	// Let it clone and do at least one poll
	time.Sleep(100 * time.Millisecond)
	cancel()

	err := <-done
	if err != context.Canceled {
		t.Errorf("expected context.Canceled, got: %v", err)
	}

	status := op.Status()
	if status.CommitHash == "" {
		t.Error("expected commit hash to be set after clone")
	}
}

func TestOperator_applyAll_InvalidRole(t *testing.T) {
	tmpDir := t.TempDir()
	repo := mustNewRepo(t, "https://github.com/org/repo", "", tmpDir)
	op := NewOperator(repo, "INVALID")

	err := op.applyAll(context.Background())
	if err == nil {
		t.Fatal("expected error for invalid role")
	}

	status := op.Status()
	if status.SyncStatus != "error" {
		t.Errorf("expected error status, got %s", status.SyncStatus)
	}
}

func TestOperator_applyAll_EmptyRoleDir(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, "core"), 0755); err != nil {
		t.Fatal(err)
	}

	repo := mustNewRepo(t, "https://github.com/org/repo", "", tmpDir)
	op := NewOperator(repo, "CORE")

	err := op.applyAll(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	status := op.Status()
	if status.SyncStatus != "synced" {
		t.Errorf("expected synced, got %s", status.SyncStatus)
	}
}

func TestOperator_applyAll_ComponentFailures(t *testing.T) {
	tmpDir := t.TempDir()
	coreDir := filepath.Join(tmpDir, "core")
	for _, name := range []string{"traefik", "monitoring"} {
		dir := filepath.Join(coreDir, name)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte("services: {}"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	repo := mustNewRepo(t, "https://github.com/org/repo", "", tmpDir)
	op := NewOperator(repo, "CORE")

	err := op.applyAll(context.Background())
	if err == nil {
		t.Fatal("expected error from component failures")
	}

	status := op.Status()
	if status.SyncStatus != "error" {
		t.Errorf("expected error status, got %s", status.SyncStatus)
	}
	if len(status.Components) != 2 {
		t.Errorf("expected 2 components, got %d", len(status.Components))
	}
	if status.LastSync == "" {
		t.Error("expected lastSync to be set even on failure")
	}
}

func TestOperator_pollAndApply_PullError(t *testing.T) {
	remoteDir, _ := setupTestRepo(t, map[string]string{
		"core/.gitkeep": "",
	})

	cloneDir := filepath.Join(t.TempDir(), "clone")
	repo := mustNewRepo(t, remoteDir, "", cloneDir)
	if err := repo.Clone(); err != nil {
		t.Fatal(err)
	}

	// Remove the remote to make fetch fail
	os.RemoveAll(remoteDir)

	op := NewOperator(repo, "CORE")
	op.pollAndApply(context.Background())

	status := op.Status()
	if status.SyncStatus != "error" {
		t.Errorf("expected error from pull failure, got %s", status.SyncStatus)
	}
}
