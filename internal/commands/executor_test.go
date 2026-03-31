package commands

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestBuildCommand_ContainerList(t *testing.T) {
	cmd, err := buildCommand(context.Background(), "container_list", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	args := cmd.Args
	if len(args) != 5 || args[0] != "docker" || args[1] != "ps" {
		t.Errorf("unexpected command: %v", args)
	}
}

func TestBuildCommand_UnknownAction(t *testing.T) {
	_, err := buildCommand(context.Background(), "not_a_real_action", nil)
	if err == nil {
		t.Fatal("expected error for unknown action")
	}
	if !strings.Contains(err.Error(), "UNKNOWN_ACTION") {
		t.Errorf("expected UNKNOWN_ACTION error, got: %v", err)
	}
}

func TestBuildCommand_ServiceStatus_AllowedService(t *testing.T) {
	params, _ := json.Marshal(map[string]string{"name": "docker"})
	cmd, err := buildCommand(context.Background(), "service_status", params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cmd.Args[len(cmd.Args)-1] != "docker" {
		t.Errorf("expected service name docker, got %v", cmd.Args)
	}
}

func TestBuildCommand_ServiceStatus_DisallowedService(t *testing.T) {
	params, _ := json.Marshal(map[string]string{"name": "sshd"})
	_, err := buildCommand(context.Background(), "service_status", params)
	if err == nil {
		t.Fatal("expected error for disallowed service")
	}
	if !strings.Contains(err.Error(), "INVALID_PARAMS") {
		t.Errorf("expected INVALID_PARAMS error, got: %v", err)
	}
}

func TestBuildCommand_DockerLogs_ValidParams(t *testing.T) {
	// Skip container existence check in test by just checking param validation
	params, _ := json.Marshal(map[string]interface{}{"name": "traefik", "lines": 100})
	_, err := buildCommand(context.Background(), "docker_logs", params)
	// Will fail on container existence check, which is fine for this test
	if err != nil && !strings.Contains(err.Error(), "does not exist") && !strings.Contains(err.Error(), "listing containers") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBuildCommand_DockerLogs_InvalidLines(t *testing.T) {
	params, _ := json.Marshal(map[string]interface{}{"name": "traefik", "lines": 1000})
	_, err := buildCommand(context.Background(), "docker_logs", params)
	if err == nil {
		t.Fatal("expected error for lines > 500")
	}
	if !strings.Contains(err.Error(), "INVALID_PARAMS") {
		t.Errorf("expected INVALID_PARAMS error, got: %v", err)
	}
}

func TestBuildCommand_DockerLogs_MissingParams(t *testing.T) {
	_, err := buildCommand(context.Background(), "docker_logs", nil)
	if err == nil {
		t.Fatal("expected error for missing params")
	}
}

func TestBuildCommand_ContainerRestart_InvalidName(t *testing.T) {
	params, _ := json.Marshal(map[string]string{"name": "; rm -rf /"})
	_, err := buildCommand(context.Background(), "container_restart", params)
	if err == nil {
		t.Fatal("expected error for invalid container name")
	}
	if !strings.Contains(err.Error(), "INVALID_PARAMS") {
		t.Errorf("expected INVALID_PARAMS error, got: %v", err)
	}
}

func TestValidateServiceName(t *testing.T) {
	tests := []struct {
		name    string
		wantErr bool
	}{
		{"docker", false},
		{"wg-quick@wg0", false},
		{"keni-agent", false},
		{"sshd", true},
		{"nginx", true},
		{"../../etc/passwd", true},
	}

	for _, tt := range tests {
		err := validateServiceName(tt.name)
		if (err != nil) != tt.wantErr {
			t.Errorf("validateServiceName(%q) error = %v, wantErr %v", tt.name, err, tt.wantErr)
		}
	}
}

func TestIsStreamingAction(t *testing.T) {
	if !IsStreamingAction("backup_trigger") {
		t.Error("backup_trigger should be streaming")
	}
	if IsStreamingAction("container_list") {
		t.Error("container_list should not be streaming")
	}
}

func TestExtractStringParam(t *testing.T) {
	params, _ := json.Marshal(map[string]string{"name": "traefik"})
	val, err := extractStringParam(params, "name")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val != "traefik" {
		t.Errorf("expected traefik, got %s", val)
	}

	_, err = extractStringParam(params, "missing")
	if err == nil {
		t.Error("expected error for missing param")
	}

	_, err = extractStringParam(nil, "name")
	if err == nil {
		t.Error("expected error for nil params")
	}
}

func TestExtractIntParam(t *testing.T) {
	params, _ := json.Marshal(map[string]interface{}{"lines": 100})
	val, err := extractIntParam(params, "lines")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val != 100 {
		t.Errorf("expected 100, got %d", val)
	}
}
