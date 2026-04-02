package gitops

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func mustNewRepo(t *testing.T, url, token, path string) *Repo {
	t.Helper()
	r, err := NewRepo(url, token, path)
	if err != nil {
		t.Fatalf("NewRepo failed: %v", err)
	}
	return r
}

func TestNewRepo_HTTPS(t *testing.T) {
	r := mustNewRepo(t, "https://github.com/org/repo", "token123", "/tmp/test")
	if r.url != "https://github.com/org/repo" {
		t.Errorf("unexpected url: %s", r.url)
	}
	if r.token != "token123" {
		t.Errorf("unexpected token: %s", r.token)
	}
	if r.localPath != "/tmp/test" {
		t.Errorf("unexpected localPath: %s", r.localPath)
	}
	if r.branch != "main" {
		t.Errorf("unexpected branch: %s", r.branch)
	}
}

func TestNewRepo_RejectsSSH(t *testing.T) {
	_, err := NewRepo("ssh://git@github.com/org/repo", "", "/tmp/test")
	if err == nil {
		t.Fatal("expected error for ssh:// scheme")
	}
	if !strings.Contains(err.Error(), "https://") {
		t.Errorf("expected https scheme error, got: %v", err)
	}
}

func TestNewRepo_RejectsHTTP(t *testing.T) {
	_, err := NewRepo("http://github.com/org/repo", "", "/tmp/test")
	if err == nil {
		t.Fatal("expected error for http:// scheme")
	}
}

func TestNewRepo_AllowsLocalPath(t *testing.T) {
	// Local bare repo paths (used in tests) should be allowed.
	r, err := NewRepo("/tmp/bare-repo", "", "/tmp/clone")
	if err != nil {
		t.Fatalf("expected local path to be allowed: %v", err)
	}
	if r.url != "/tmp/bare-repo" {
		t.Errorf("unexpected url: %s", r.url)
	}
}

func TestLocalPath(t *testing.T) {
	r := mustNewRepo(t, "https://github.com/org/repo", "", "/var/lib/test")
	if r.LocalPath() != "/var/lib/test" {
		t.Errorf("unexpected LocalPath: %s", r.LocalPath())
	}
}

func TestAuthURL_WithToken(t *testing.T) {
	r := mustNewRepo(t, "https://github.com/org/repo", "ghp_abc123", "/tmp/test")
	url, err := r.authURL()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(url, "x-access-token:ghp_abc123@github.com") {
		t.Errorf("expected token in URL, got: %s", url)
	}
}

func TestAuthURL_WithoutToken(t *testing.T) {
	r := mustNewRepo(t, "https://github.com/org/repo", "", "/tmp/test")
	url, err := r.authURL()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if url != "https://github.com/org/repo" {
		t.Errorf("expected original URL, got: %s", url)
	}
}

func TestSanitizeOutput_RedactsToken(t *testing.T) {
	r := mustNewRepo(t, "https://github.com/org/repo", "ghp_secret_token", "/tmp/test")
	out := r.sanitizeOutput([]byte("fatal: could not read from https://x-access-token:ghp_secret_token@github.com/org/repo"))
	if strings.Contains(out, "ghp_secret_token") {
		t.Errorf("token should be redacted, got: %s", out)
	}
	if !strings.Contains(out, "[REDACTED]") {
		t.Errorf("expected [REDACTED] in output, got: %s", out)
	}
}

func TestSanitizeOutput_NoToken(t *testing.T) {
	r := mustNewRepo(t, "https://github.com/org/repo", "", "/tmp/test")
	out := r.sanitizeOutput([]byte("fatal: some error"))
	if out != "fatal: some error" {
		t.Errorf("unexpected output: %s", out)
	}
}

func TestClone_AlreadyCloned(t *testing.T) {
	tmpDir := t.TempDir()
	repoDir := filepath.Join(tmpDir, "repo")

	// Create a fake .git directory
	if err := os.MkdirAll(filepath.Join(repoDir, ".git"), 0755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}

	r := mustNewRepo(t, "https://github.com/org/repo", "", repoDir)
	if err := r.Clone(); err != nil {
		t.Fatalf("expected no error for already cloned repo, got: %v", err)
	}
}

func TestClone_ParentDirCreationFails(t *testing.T) {
	tmpDir := t.TempDir()
	// Create a file where a directory should be
	blocker := filepath.Join(tmpDir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	r := mustNewRepo(t, "https://github.com/org/repo", "", filepath.Join(blocker, "sub", "clone"))
	err := r.Clone()
	if err == nil {
		t.Fatal("expected error when parent dir creation fails")
	}
	if !strings.Contains(err.Error(), "creating parent directory") {
		t.Errorf("expected parent dir error, got: %v", err)
	}
}

func TestClone_InvalidRemote(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	cloneDir := filepath.Join(t.TempDir(), "clone")
	r := mustNewRepo(t, "/nonexistent/bare/repo", "", cloneDir)
	err := r.Clone()
	if err == nil {
		t.Fatal("expected error for invalid remote")
	}
	if !strings.Contains(err.Error(), "git clone failed") {
		t.Errorf("expected clone failed error, got: %v", err)
	}
}

// TestCloneAndPull creates a real local git repo, clones it, makes a change,
// and verifies Pull detects the update.
func TestCloneAndPull(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	// Create a bare "remote" repo
	remoteDir := t.TempDir()
	run(t, remoteDir, "git", "init", "--bare")

	// Create a working copy to push an initial commit
	workDir := t.TempDir()
	run(t, workDir, "git", "clone", remoteDir, ".")
	run(t, workDir, "git", "config", "user.email", "test@test.com")
	run(t, workDir, "git", "config", "user.name", "Test")

	// Create initial structure: core/traefik/docker-compose.yml
	coreTraefik := filepath.Join(workDir, "core", "traefik")
	if err := os.MkdirAll(coreTraefik, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(coreTraefik, "docker-compose.yml"), []byte("services: {}"), 0644); err != nil {
		t.Fatal(err)
	}
	run(t, workDir, "git", "add", "-A")
	run(t, workDir, "git", "commit", "-m", "initial")
	run(t, workDir, "git", "push", "origin", "main")

	// Clone using our Repo
	cloneDir := filepath.Join(t.TempDir(), "clone")
	r := mustNewRepo(t, remoteDir, "", cloneDir)
	if err := r.Clone(); err != nil {
		t.Fatalf("clone failed: %v", err)
	}

	hash1, err := r.CommitHash()
	if err != nil {
		t.Fatalf("commit hash failed: %v", err)
	}
	if len(hash1) != 40 {
		t.Errorf("expected 40 char hash, got %d: %s", len(hash1), hash1)
	}

	// Verify component dirs
	dirs, err := r.ComponentDirs("CORE")
	if err != nil {
		t.Fatalf("component dirs failed: %v", err)
	}
	if len(dirs) != 1 {
		t.Fatalf("expected 1 component dir, got %d", len(dirs))
	}
	if filepath.Base(dirs[0]) != "traefik" {
		t.Errorf("expected traefik dir, got %s", dirs[0])
	}

	// Pull with no changes
	updated, err := r.Pull()
	if err != nil {
		t.Fatalf("pull failed: %v", err)
	}
	if updated {
		t.Error("expected no update on first pull")
	}

	// Push a new commit from workDir
	if err := os.WriteFile(filepath.Join(coreTraefik, "docker-compose.yml"), []byte("services:\n  traefik:\n    image: traefik:v3"), 0644); err != nil {
		t.Fatal(err)
	}
	run(t, workDir, "git", "add", "-A")
	run(t, workDir, "git", "commit", "-m", "update traefik")
	run(t, workDir, "git", "push", "origin", "main")

	// Pull should detect change
	updated, err = r.Pull()
	if err != nil {
		t.Fatalf("pull after push failed: %v", err)
	}
	if !updated {
		t.Error("expected update after push")
	}

	hash2, err := r.CommitHash()
	if err != nil {
		t.Fatalf("commit hash after pull failed: %v", err)
	}
	if hash1 == hash2 {
		t.Error("hash should have changed after pull")
	}
}

func TestClone_StripsCredentialFromRemote(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	// Create a bare remote
	remoteDir := t.TempDir()
	run(t, remoteDir, "git", "init", "--bare")

	workDir := t.TempDir()
	run(t, workDir, "git", "clone", remoteDir, ".")
	run(t, workDir, "git", "config", "user.email", "test@test.com")
	run(t, workDir, "git", "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(workDir, "README.md"), []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}
	run(t, workDir, "git", "add", "-A")
	run(t, workDir, "git", "commit", "-m", "init")
	run(t, workDir, "git", "push", "origin", "main")

	// Clone without token (local path), then verify stripCredentialFromRemote works
	cloneDir := filepath.Join(t.TempDir(), "clone")
	r := mustNewRepo(t, remoteDir, "", cloneDir)
	if err := r.Clone(); err != nil {
		t.Fatalf("clone failed: %v", err)
	}

	// Manually set a token-containing URL, then strip it
	fakeAuthURL := "https://x-access-token:secret_token_123@github.com/org/repo"
	setCmd := exec.Command("git", "remote", "set-url", "origin", fakeAuthURL)
	setCmd.Dir = cloneDir
	if err := setCmd.Run(); err != nil {
		t.Fatalf("set-url failed: %v", err)
	}

	// Simulate what happens after clone: strip credentials
	r.token = "secret_token_123"
	r.url = "https://github.com/org/repo"
	r.stripCredentialFromRemote()

	// Verify the stored remote URL does NOT contain the token
	cmd := exec.Command("git", "remote", "get-url", "origin")
	cmd.Dir = cloneDir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git remote get-url failed: %v", err)
	}
	storedURL := strings.TrimSpace(string(out))
	if strings.Contains(storedURL, "secret_token_123") {
		t.Errorf("stored remote URL should not contain token, got: %s", storedURL)
	}
	if storedURL != "https://github.com/org/repo" {
		t.Errorf("expected clean URL, got: %s", storedURL)
	}
}

func TestComponentDirs_InvalidRole(t *testing.T) {
	tmpDir := t.TempDir()
	r := mustNewRepo(t, "https://github.com/org/repo", "", tmpDir)

	_, err := r.ComponentDirs("INVALID")
	if err == nil {
		t.Fatal("expected error for invalid role")
	}
	if !strings.Contains(err.Error(), "invalid role") {
		t.Errorf("expected invalid role error, got: %v", err)
	}
}

func TestComponentDirs_PathTraversal(t *testing.T) {
	tmpDir := t.TempDir()
	r := mustNewRepo(t, "https://github.com/org/repo", "", tmpDir)

	// Even if we bypass ValidRoles check, the path check should catch traversal.
	// Test with a valid role that has no directory.
	_, err := r.ComponentDirs("../../etc")
	if err == nil {
		t.Fatal("expected error for path traversal")
	}
	// Should be caught by role validation first.
	if !strings.Contains(err.Error(), "invalid role") {
		t.Errorf("expected invalid role error, got: %v", err)
	}
}

func TestComponentDirs_MissingRole(t *testing.T) {
	tmpDir := t.TempDir()
	r := mustNewRepo(t, "https://github.com/org/repo", "", tmpDir)

	_, err := r.ComponentDirs("PROD")
	if err == nil {
		t.Fatal("expected error for missing role directory")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected not found error, got: %v", err)
	}
}

func TestComponentDirs_EmptyRoleDir(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, "core"), 0755); err != nil {
		t.Fatal(err)
	}

	r := mustNewRepo(t, "https://github.com/org/repo", "", tmpDir)
	dirs, err := r.ComponentDirs("CORE")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(dirs) != 0 {
		t.Errorf("expected 0 dirs for empty role, got %d", len(dirs))
	}
}

func TestComponentDirs_UnreadableDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission test not reliable on Windows")
	}

	tmpDir := t.TempDir()
	coreDir := filepath.Join(tmpDir, "core")
	if err := os.MkdirAll(coreDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(coreDir, 0000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(coreDir, 0755)

	r := mustNewRepo(t, "https://github.com/org/repo", "", tmpDir)
	_, err := r.ComponentDirs("CORE")
	if err == nil {
		t.Fatal("expected error for unreadable directory")
	}
	if !strings.Contains(err.Error(), "reading role directory") {
		t.Errorf("expected reading role dir error, got: %v", err)
	}
}

func TestComponentDirs_SkipsNonCompose(t *testing.T) {
	tmpDir := t.TempDir()
	coreDir := filepath.Join(tmpDir, "core")

	// Create a dir with compose
	if err := os.MkdirAll(filepath.Join(coreDir, "traefik"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(coreDir, "traefik", "docker-compose.yml"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create a dir without compose
	if err := os.MkdirAll(filepath.Join(coreDir, "readme-only"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(coreDir, "readme-only", "README.md"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create a file (not a dir)
	if err := os.WriteFile(filepath.Join(coreDir, ".gitkeep"), []byte(""), 0644); err != nil {
		t.Fatal(err)
	}

	r := mustNewRepo(t, "https://github.com/org/repo", "", tmpDir)
	dirs, err := r.ComponentDirs("CORE")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(dirs) != 1 {
		t.Fatalf("expected 1 dir, got %d", len(dirs))
	}
	if filepath.Base(dirs[0]) != "traefik" {
		t.Errorf("expected traefik, got %s", dirs[0])
	}
}

func TestValidRoles(t *testing.T) {
	valid := []string{"core", "prod", "stg", "dev"}
	for _, r := range valid {
		if !ValidRoles[r] {
			t.Errorf("expected %q to be a valid role", r)
		}
	}

	invalid := []string{"CORE", "staging", "production", "test", "../../etc"}
	for _, r := range invalid {
		if ValidRoles[r] {
			t.Errorf("expected %q to be invalid", r)
		}
	}
}

func run(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v failed: %v\n%s", name, args, err, string(out))
	}
}
