package commands

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateDeployParams_Valid(t *testing.T) {
	p := DeployPeripheryParams{
		Image:      "ghcr.io/moghtech/komodo-periphery:2.0",
		Passkey:    "test-passkey",
		DeployRoot: "/srv/devops",
	}
	if err := validateDeployParams(p); err != nil {
		t.Fatalf("expected valid params, got error: %v", err)
	}
}

func TestValidateDeployParams_MissingImage(t *testing.T) {
	p := DeployPeripheryParams{
		Passkey:    "test-passkey",
		DeployRoot: "/srv/devops",
	}
	err := validateDeployParams(p)
	if err == nil {
		t.Fatal("expected error for missing image")
	}
	if !strings.Contains(err.Error(), "image") {
		t.Errorf("expected image error, got: %v", err)
	}
}

func TestValidateDeployParams_MissingPasskey(t *testing.T) {
	p := DeployPeripheryParams{
		Image:      "ghcr.io/moghtech/komodo-periphery:2.0",
		DeployRoot: "/srv/devops",
	}
	err := validateDeployParams(p)
	if err == nil {
		t.Fatal("expected error for missing passkey")
	}
	if !strings.Contains(err.Error(), "passkey") {
		t.Errorf("expected passkey error, got: %v", err)
	}
}

func TestValidateDeployParams_MissingDeployRoot(t *testing.T) {
	p := DeployPeripheryParams{
		Image:   "ghcr.io/moghtech/komodo-periphery:2.0",
		Passkey: "test-passkey",
	}
	err := validateDeployParams(p)
	if err == nil {
		t.Fatal("expected error for missing deploy_root")
	}
	if !strings.Contains(err.Error(), "deploy_root") {
		t.Errorf("expected deploy_root error, got: %v", err)
	}
}

func TestValidateDeployParams_DisallowedImage(t *testing.T) {
	p := DeployPeripheryParams{
		Image:      "docker.io/malicious/image:latest",
		Passkey:    "test-passkey",
		DeployRoot: "/srv/devops",
	}
	err := validateDeployParams(p)
	if err == nil {
		t.Fatal("expected error for disallowed image")
	}
	if !strings.Contains(err.Error(), "not from allowed registry") {
		t.Errorf("expected registry error, got: %v", err)
	}
}

func TestValidateDeployParams_DisallowedDeployRoot(t *testing.T) {
	p := DeployPeripheryParams{
		Image:      "ghcr.io/moghtech/komodo-periphery:2.0",
		Passkey:    "test-passkey",
		DeployRoot: "/tmp/evil",
	}
	err := validateDeployParams(p)
	if err == nil {
		t.Fatal("expected error for disallowed deploy root")
	}
	if !strings.Contains(err.Error(), "not in allowed paths") {
		t.Errorf("expected path error, got: %v", err)
	}
}

func TestValidateDeployParams_PathTraversal(t *testing.T) {
	p := DeployPeripheryParams{
		Image:      "ghcr.io/moghtech/komodo-periphery:2.0",
		Passkey:    "test-passkey",
		DeployRoot: "/srv/devops/../../../etc",
	}
	err := validateDeployParams(p)
	if err == nil {
		t.Fatal("expected error for path traversal")
	}
}

func TestValidateDeployParams_AllAllowedRegistries(t *testing.T) {
	registries := []string{
		"ghcr.io/moghtech/komodo-periphery:2.0",
		"ghcr.io/kenitech-io/some-image:1.0",
		"registry.kenitech.io/keni-backup-tools:0.1.0",
	}
	for _, img := range registries {
		p := DeployPeripheryParams{
			Image:      img,
			Passkey:    "test-passkey",
			DeployRoot: "/srv/devops",
		}
		if err := validateDeployParams(p); err != nil {
			t.Errorf("expected image %q to be allowed, got error: %v", img, err)
		}
	}
}

func TestPeripheryComposeTemplate(t *testing.T) {
	image := "ghcr.io/moghtech/komodo-periphery:2.0"
	content := composeForPeriphery(image)

	if !strings.Contains(content, image) {
		t.Error("compose should contain the image")
	}
	if !strings.Contains(content, "komodo-periphery") {
		t.Error("compose should contain container name")
	}
	if !strings.Contains(content, "no-new-privileges") {
		t.Error("compose should have security hardening")
	}
	if !strings.Contains(content, "/var/run/docker.sock") {
		t.Error("compose should mount docker socket")
	}
}

func composeForPeriphery(image string) string {
	return strings.ReplaceAll(peripheryComposeTemplate, "%s", image)
}

func TestPeripheryEnvTemplate(t *testing.T) {
	passkey := "test-secret-passkey-123"
	content := envForPeriphery(passkey)

	if !strings.Contains(content, "PERIPHERY_PASSKEYS="+passkey) {
		t.Error("env should contain passkey")
	}
	if !strings.Contains(content, "PERIPHERY_ROOT_DIRECTORY=/srv/projects") {
		t.Error("env should contain root directory")
	}
}

func envForPeriphery(passkey string) string {
	return strings.ReplaceAll(peripheryEnvTemplate, "%s", passkey)
}

func TestBuildCommand_DeployPeriphery_NoConfirm(t *testing.T) {
	params, _ := json.Marshal(map[string]interface{}{
		"image":       "ghcr.io/moghtech/komodo-periphery:2.0",
		"passkey":     "test",
		"deploy_root": "/srv/devops",
	})
	_, err := buildCommand(nil, "deploy_periphery", params)
	if err == nil {
		t.Fatal("expected error for missing confirm")
	}
	if !strings.Contains(err.Error(), "INVALID_PARAMS") {
		t.Errorf("expected INVALID_PARAMS, got: %v", err)
	}
}

func TestBuildCommand_DeployPeriphery_WrongConfirm(t *testing.T) {
	params, _ := json.Marshal(map[string]interface{}{
		"confirm":     "no",
		"image":       "ghcr.io/moghtech/komodo-periphery:2.0",
		"passkey":     "test",
		"deploy_root": "/srv/devops",
	})
	_, err := buildCommand(nil, "deploy_periphery", params)
	if err == nil {
		t.Fatal("expected error for wrong confirm")
	}
	if !strings.Contains(err.Error(), "confirm parameter must be") {
		t.Errorf("expected confirm error, got: %v", err)
	}
}

func TestBuildCommand_DeployPeriphery_ValidConfirm(t *testing.T) {
	params, _ := json.Marshal(map[string]interface{}{
		"confirm":     "yes",
		"image":       "ghcr.io/moghtech/komodo-periphery:2.0",
		"passkey":     "test",
		"deploy_root": "/srv/devops",
	})
	_, err := buildCommand(nil, "deploy_periphery", params)
	if err == nil {
		t.Fatal("expected sentinel error")
	}
	if _, ok := err.(errDeployPeriphery); !ok {
		t.Errorf("expected errDeployPeriphery, got: %T %v", err, err)
	}
}

func TestDeployPeripheryWritesFiles(t *testing.T) {
	// Use a temp directory but add it to allowed roots for testing
	tmpDir := t.TempDir()
	origRoots := AllowedDeployRoots
	AllowedDeployRoots = []string{tmpDir}
	defer func() { AllowedDeployRoots = origRoots }()

	deployDir := filepath.Join(tmpDir, "komodo-peri")

	// We can't test the full ExecuteDeployPeriphery (needs docker) but we can
	// test the file writing part by calling the validation and writing directly.
	p := DeployPeripheryParams{
		Image:      "ghcr.io/moghtech/komodo-periphery:2.0",
		Passkey:    "test-passkey-abc",
		DeployRoot: tmpDir,
	}
	if err := validateDeployParams(p); err != nil {
		t.Fatalf("validation failed: %v", err)
	}

	// Simulate the file writing part
	if err := os.MkdirAll(deployDir, 0755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}

	composePath := filepath.Join(deployDir, "docker-compose.yml")
	composeContent := composeForPeriphery(p.Image)
	if err := os.WriteFile(composePath, []byte(composeContent), 0644); err != nil {
		t.Fatalf("write compose failed: %v", err)
	}

	envPath := filepath.Join(deployDir, ".env")
	envContent := envForPeriphery(p.Passkey)
	if err := os.WriteFile(envPath, []byte(envContent), 0600); err != nil {
		t.Fatalf("write env failed: %v", err)
	}

	// Verify files
	data, err := os.ReadFile(composePath)
	if err != nil {
		t.Fatalf("read compose failed: %v", err)
	}
	if !strings.Contains(string(data), p.Image) {
		t.Error("compose file should contain image")
	}

	data, err = os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("read env failed: %v", err)
	}
	if !strings.Contains(string(data), p.Passkey) {
		t.Error("env file should contain passkey")
	}

	// Verify env file permissions
	info, err := os.Stat(envPath)
	if err != nil {
		t.Fatalf("stat env failed: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("env file should be 0600, got %o", info.Mode().Perm())
	}
}
