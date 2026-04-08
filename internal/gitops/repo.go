package gitops

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ValidRoles are the allowed server roles for component directory discovery.
var ValidRoles = map[string]bool{
	"core": true,
	"prod": true,
	"stg":  true,
	"dev":  true,
}

// TokenFunc is a function that returns a fresh git auth token.
// Called before each clone/pull if set.
type TokenFunc func() (token string, err error)

// Repo manages a local git clone of the client IDP repo.
type Repo struct {
	url       string
	token     string
	tokenFunc TokenFunc
	localPath string
	branch    string
}

// NewRepo creates a new Repo instance.
// The repoURL must use the https:// scheme. The token is injected into the
// HTTPS URL for authentication.
func NewRepo(repoURL, token, localPath string) (*Repo, error) {
	// Allow file:// in tests (local bare repos), enforce https:// otherwise.
	if !strings.HasPrefix(repoURL, "https://") && !strings.HasPrefix(repoURL, "file://") && !strings.HasPrefix(repoURL, "/") {
		return nil, fmt.Errorf("repo URL must use https:// scheme, got: %s", repoURL)
	}

	return &Repo{
		url:       repoURL,
		token:     token,
		localPath: localPath,
		branch:    "main",
	}, nil
}

// SetTokenFunc sets a function to fetch fresh tokens before git operations.
func (r *Repo) SetTokenFunc(fn TokenFunc) {
	r.tokenFunc = fn
}

// LocalPath returns the local path of the cloned repo.
func (r *Repo) LocalPath() string {
	return r.localPath
}

// Branch returns the branch name.
func (r *Repo) Branch() string {
	return r.branch
}

// sanitizeProgressFn wraps a ProgressFunc to strip the deploy token from
// every line before forwarding. Returns nil if the input is nil.
func (r *Repo) sanitizeProgressFn(fn ProgressFunc) ProgressFunc {
	if fn == nil {
		return nil
	}
	return func(line string) {
		if r.token != "" {
			line = strings.ReplaceAll(line, r.token, "[REDACTED]")
		}
		fn(line)
	}
}

// sanitizeOutput removes the deploy token from git command output to prevent
// credential leakage in logs and error messages.
func (r *Repo) sanitizeOutput(out []byte) string {
	s := string(out)
	if r.token != "" {
		s = strings.ReplaceAll(s, r.token, "[REDACTED]")
	}
	return s
}

// authURL injects the deploy token into the HTTPS URL.
// https://github.com/org/repo -> https://x-access-token:TOKEN@github.com/org/repo
func (r *Repo) authURL() (string, error) {
	// Refresh token if a token function is set
	if r.tokenFunc != nil {
		freshToken, err := r.tokenFunc()
		if err != nil {
			return "", fmt.Errorf("fetching git token: %w", err)
		}
		r.token = freshToken
	}

	if r.token == "" {
		return r.url, nil
	}

	parsed, err := url.Parse(r.url)
	if err != nil {
		return "", fmt.Errorf("parsing repo URL: %w", err)
	}

	parsed.User = url.UserPassword("x-access-token", r.token)
	return parsed.String(), nil
}

// stripCredentialFromRemote removes the deploy token from the stored git
// remote URL after clone, so the token is not persisted on disk in .git/config.
func (r *Repo) stripCredentialFromRemote() {
	cmd := exec.Command("git", "remote", "set-url", "origin", r.url)
	cmd.Dir = r.localPath
	if err := cmd.Run(); err != nil {
		slog.Warn("could not strip credential from git remote", "error", err)
	}
}

// Clone clones the repo to the local path. If already cloned, does nothing.
func (r *Repo) Clone(progressFn ...ProgressFunc) error {
	if _, err := os.Stat(filepath.Join(r.localPath, ".git")); err == nil {
		slog.Info("repo already cloned", "path", r.localPath)
		return nil
	}

	authURL, err := r.authURL()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(r.localPath), 0755); err != nil {
		return fmt.Errorf("creating parent directory: %w", err)
	}

	slog.Info("cloning IDP repo", "url", r.url, "path", r.localPath, "branch", r.branch)

	var pf ProgressFunc
	if len(progressFn) > 0 {
		pf = progressFn[0]
	}

	cmd := exec.CommandContext(context.Background(), "git", "clone",
		"--depth", "1",
		"--branch", r.branch,
		"--single-branch",
		authURL,
		r.localPath,
	)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")

	out, err := runStreamingCmd(cmd, r.sanitizeProgressFn(pf))
	if err != nil {
		return fmt.Errorf("git clone failed: %w\n%s", err, r.sanitizeOutput([]byte(out)))
	}

	// Remove the auth token from the stored remote URL in .git/config.
	if r.token != "" {
		r.stripCredentialFromRemote()
	}

	slog.Info("repo cloned successfully", "path", r.localPath)
	return nil
}

// Pull fetches and fast-forwards to the latest commit.
// Returns true if the repo was updated (new commits), false if already up to date.
func (r *Repo) Pull(progressFn ...ProgressFunc) (bool, error) {
	oldHash, err := r.CommitHash()
	if err != nil {
		return false, fmt.Errorf("getting current hash: %w", err)
	}

	authURL, err := r.authURL()
	if err != nil {
		return false, err
	}

	var pf ProgressFunc
	if len(progressFn) > 0 {
		pf = progressFn[0]
	}
	sanitized := r.sanitizeProgressFn(pf)

	// Fetch using positional URL (not stored remote) so the token is not persisted.
	fetchCmd := exec.CommandContext(context.Background(), "git", "fetch", "--depth", "1", authURL, r.branch)
	fetchCmd.Dir = r.localPath
	fetchCmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")

	out, err := runStreamingCmd(fetchCmd, sanitized)
	if err != nil {
		return false, fmt.Errorf("git fetch failed: %w\n%s", err, r.sanitizeOutput([]byte(out)))
	}

	// Reset to fetched commit.
	resetCmd := exec.CommandContext(context.Background(), "git", "reset", "--hard", "FETCH_HEAD")
	resetCmd.Dir = r.localPath
	out, err = runStreamingCmd(resetCmd, sanitized)
	if err != nil {
		return false, fmt.Errorf("git reset failed: %w\n%s", err, r.sanitizeOutput([]byte(out)))
	}

	newHash, err := r.CommitHash()
	if err != nil {
		return false, fmt.Errorf("getting new hash: %w", err)
	}

	if oldHash != newHash {
		slog.Info("repo updated", "from", oldHash[:8], "to", newHash[:8])
		if pf != nil {
			pf(fmt.Sprintf("Updated %s -> %s", oldHash[:8], newHash[:8]))
		}
		return true, nil
	}

	if pf != nil {
		pf(fmt.Sprintf("Already up to date at %s", oldHash[:8]))
	}
	return false, nil
}

// CommitHash returns the current HEAD commit hash.
func (r *Repo) CommitHash() (string, error) {
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = r.localPath

	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse HEAD: %w", err)
	}

	return strings.TrimSpace(string(out)), nil
}

// ComponentDirs returns the list of component directories for a given server role.
// E.g., for role "CORE", returns paths like /var/lib/keni-agent/idp/core/traefik/
func (r *Repo) ComponentDirs(role string) ([]string, error) {
	lower := strings.ToLower(role)
	if !ValidRoles[lower] {
		return nil, fmt.Errorf("invalid role %q: must be one of CORE, PROD, STG, DEV", role)
	}

	roleDir := filepath.Join(r.localPath, lower)

	// Verify the resolved path is within the repo to prevent path traversal.
	absRole, err := filepath.Abs(roleDir)
	if err != nil {
		return nil, fmt.Errorf("resolving role path: %w", err)
	}
	absRepo, err := filepath.Abs(r.localPath)
	if err != nil {
		return nil, fmt.Errorf("resolving repo path: %w", err)
	}
	if !strings.HasPrefix(absRole, absRepo+string(os.PathSeparator)) {
		return nil, fmt.Errorf("role path escapes repo directory")
	}

	entries, err := os.ReadDir(roleDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("role directory %q not found in repo", lower)
		}
		return nil, fmt.Errorf("reading role directory: %w", err)
	}

	var dirs []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		compDir := filepath.Join(roleDir, entry.Name())
		// Only include directories that have a docker-compose.yml
		composePath := filepath.Join(compDir, "docker-compose.yml")
		if _, err := os.Stat(composePath); err == nil {
			dirs = append(dirs, compDir)
		}
	}

	return dirs, nil
}

// ComponentDirNames returns the names of component directories for a role.
// Returns an empty slice (not error) if the role directory does not exist.
func (r *Repo) ComponentDirNames(role string) []string {
	lower := strings.ToLower(role)
	if !ValidRoles[lower] {
		return nil
	}

	roleDir := filepath.Join(r.localPath, lower)
	entries, err := os.ReadDir(roleDir)
	if err != nil {
		return nil
	}

	var names []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		composePath := filepath.Join(roleDir, entry.Name(), "docker-compose.yml")
		if _, err := os.Stat(composePath); err == nil {
			names = append(names, entry.Name())
		}
	}
	return names
}
