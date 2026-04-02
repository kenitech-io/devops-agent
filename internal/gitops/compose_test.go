package gitops

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestApplyComponent_MissingDir(t *testing.T) {
	ctx := context.Background()
	result := ApplyComponent(ctx, "/nonexistent/path")

	if result.Status != "error" {
		t.Errorf("expected error status, got %s", result.Status)
	}
	if result.Error == "" {
		t.Error("expected error message")
	}
	if result.Name != "path" {
		t.Errorf("expected name 'path', got %s", result.Name)
	}
}

func TestApplyComponent_NoComposeFile(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := context.Background()

	result := ApplyComponent(ctx, tmpDir)
	if result.Status != "error" {
		t.Errorf("expected error for missing compose file, got %s", result.Status)
	}
}

func TestApplyComponent_CancelledContext(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "docker-compose.yml"), []byte("services:\n  test:\n    image: alpine"), 0644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result := ApplyComponent(ctx, tmpDir)
	if result.Status != "error" {
		t.Errorf("expected error from cancelled context, got %s", result.Status)
	}
}

func TestApplyComponent_SetsUpdatedAt(t *testing.T) {
	ctx := context.Background()
	result := ApplyComponent(ctx, "/nonexistent")
	if result.UpdatedAt.IsZero() {
		t.Error("expected UpdatedAt to be set")
	}
}

func TestApplyAll_Empty(t *testing.T) {
	ctx := context.Background()
	results := ApplyAll(ctx, nil)
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

func TestApplyAll_MultipleFailing(t *testing.T) {
	ctx := context.Background()
	dirs := []string{"/nonexistent/a", "/nonexistent/b", "/nonexistent/c"}
	results := ApplyAll(ctx, dirs)

	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	for i, r := range results {
		if r.Status != "error" {
			t.Errorf("result[%d]: expected error, got %s", i, r.Status)
		}
	}
	// Verify names are the base dir names
	if results[0].Name != "a" {
		t.Errorf("expected name 'a', got %s", results[0].Name)
	}
}

func TestStopComponent_MissingDir(t *testing.T) {
	ctx := context.Background()
	err := StopComponent(ctx, "/nonexistent/path")
	if err == nil {
		t.Fatal("expected error for missing dir")
	}
	if err.Error() == "" {
		t.Error("expected non-empty error message")
	}
}

func TestStopComponent_CancelledContext(t *testing.T) {
	tmpDir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := StopComponent(ctx, tmpDir)
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
}

func TestComponentStatus_MissingDir(t *testing.T) {
	ctx := context.Background()
	status := ComponentStatus(ctx, "/nonexistent/path")
	if status != "error" {
		t.Errorf("expected error status, got %s", status)
	}
}
