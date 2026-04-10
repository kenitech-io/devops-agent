package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// AllowedDeployRoots restricts where deployments can write files.
var AllowedDeployRoots = []string{"/srv/devops"}

// AllowedImagePrefixes restricts which container images can be deployed.
var AllowedImagePrefixes = []string{
	"ghcr.io/moghtech/",
	"ghcr.io/kenidevops/",
	"ghcr.io/kenitech-io/",
	"registry.kenitech.io/",
}

// DeployPeripheryParams holds the parameters for the deploy_periphery action.
type DeployPeripheryParams struct {
	Image      string `json:"image"`
	Passkey    string `json:"passkey"`
	DeployRoot string `json:"deploy_root"`
}

const peripheryComposeTemplate = `services:
  komodo-periphery:
    image: %s
    container_name: komodo-periphery
    restart: unless-stopped
    security_opt:
      - no-new-privileges:true
    env_file:
      - config.env
      - secrets.env
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - /proc:/proc
      - ${PERIPHERY_ROOT_DIRECTORY:-/srv/projects}:/srv/projects
    networks:
      - komodo
    deploy:
      resources:
        limits:
          memory: 256M
    labels:
      - komodo.skip

networks:
  komodo:
    name: komodo
`

const peripheryConfigEnvTemplate = `PERIPHERY_ROOT_DIRECTORY=/srv/projects
PERIPHERY_DISABLE_TERMINALS=false
PERIPHERY_SSL_ENABLED=false
TZ=Etc/UTC
`

const peripherySecretsEnvTemplate = `PERIPHERY_PASSKEYS=%s
`

// ExecuteDeployPeriphery handles the deploy_periphery action.
// It writes compose + env files and runs docker compose up.
func ExecuteDeployPeriphery(ctx context.Context, params json.RawMessage) (*Result, error) {
	start := time.Now()

	var p DeployPeripheryParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("INVALID_PARAMS: %w", err)
	}

	if err := validateDeployParams(p); err != nil {
		return nil, fmt.Errorf("INVALID_PARAMS: %s", err)
	}

	deployDir := filepath.Join(p.DeployRoot, "komodo-peri")

	// Create directories
	projectDir := filepath.Join(p.DeployRoot, "projects")
	for _, dir := range []string{deployDir, projectDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("EXECUTION_FAILED: creating directory %s: %w", dir, err)
		}
	}

	// Write docker-compose.yml
	composePath := filepath.Join(deployDir, "docker-compose.yml")
	composeContent := fmt.Sprintf(peripheryComposeTemplate, p.Image)
	if err := os.WriteFile(composePath, []byte(composeContent), 0644); err != nil {
		return nil, fmt.Errorf("EXECUTION_FAILED: writing compose file: %w", err)
	}

	// Write config.env (non-secret config)
	configEnvPath := filepath.Join(deployDir, "config.env")
	if err := os.WriteFile(configEnvPath, []byte(peripheryConfigEnvTemplate), 0644); err != nil {
		return nil, fmt.Errorf("EXECUTION_FAILED: writing config.env: %w", err)
	}

	// Write secrets.env (secrets, never committed to git)
	secretsEnvPath := filepath.Join(deployDir, "secrets.env")
	secretsContent := fmt.Sprintf(peripherySecretsEnvTemplate, p.Passkey)
	if err := os.WriteFile(secretsEnvPath, []byte(secretsContent), 0600); err != nil {
		return nil, fmt.Errorf("EXECUTION_FAILED: writing secrets.env: %w", err)
	}

	// Run docker compose up
	cmd := exec.CommandContext(ctx, "docker", "compose", "up", "-d", "--pull", "always")
	cmd.Dir = deployDir

	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return &Result{
			ExitCode:   1,
			Stdout:     stdout.String(),
			Stderr:     fmt.Sprintf("docker compose up failed: %s\n%s", err, stderr.String()),
			DurationMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Verify container is running
	verifyCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	if err := waitForContainer(verifyCtx, "komodo-periphery"); err != nil {
		return &Result{
			ExitCode:   0,
			Stdout:     fmt.Sprintf("compose up succeeded but container health check pending: %s\n%s", err, stdout.String()),
			Stderr:     stderr.String(),
			DurationMs: time.Since(start).Milliseconds(),
		}, nil
	}

	return &Result{
		ExitCode:   0,
		Stdout:     fmt.Sprintf("periphery deployed successfully at %s\n%s", deployDir, stdout.String()),
		Stderr:     stderr.String(),
		DurationMs: time.Since(start).Milliseconds(),
	}, nil
}

func validateDeployParams(p DeployPeripheryParams) error {
	if p.Image == "" {
		return fmt.Errorf("missing required param \"image\"")
	}
	if p.Passkey == "" {
		return fmt.Errorf("missing required param \"passkey\"")
	}
	if p.DeployRoot == "" {
		return fmt.Errorf("missing required param \"deploy_root\"")
	}

	// Validate image against allowlist
	imageAllowed := false
	for _, prefix := range AllowedImagePrefixes {
		if strings.HasPrefix(p.Image, prefix) {
			imageAllowed = true
			break
		}
	}
	if !imageAllowed {
		return fmt.Errorf("image %q not from allowed registry (allowed: %v)", p.Image, AllowedImagePrefixes)
	}

	// Validate deploy root against allowlist
	cleanRoot := filepath.Clean(p.DeployRoot)
	rootAllowed := false
	for _, allowed := range AllowedDeployRoots {
		if cleanRoot == allowed || strings.HasPrefix(cleanRoot, allowed+"/") {
			rootAllowed = true
			break
		}
	}
	if !rootAllowed {
		return fmt.Errorf("deploy_root %q not in allowed paths (allowed: %v)", p.DeployRoot, AllowedDeployRoots)
	}

	return nil
}

// waitForContainer polls docker inspect until the container is running.
func waitForContainer(ctx context.Context, name string) error {
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for container %s", name)
		default:
		}

		out, err := exec.CommandContext(ctx, "docker", "inspect", "-f", "{{.State.Running}}", name).Output()
		if err == nil && strings.TrimSpace(string(out)) == "true" {
			return nil
		}

		time.Sleep(2 * time.Second)
	}
}
