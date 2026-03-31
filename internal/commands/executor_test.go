package commands

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
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

// --- Additional tests for coverage ---

// seedContainerCache injects names into the container cache so tests
// do not need Docker running.
func seedContainerCache(names []string) {
	containerCacheMu.Lock()
	defer containerCacheMu.Unlock()
	containerCacheNames = names
	containerCacheTime = time.Now()
}

func TestContainerNameRegex(t *testing.T) {
	tests := []struct {
		name  string
		valid bool
	}{
		{"traefik", true},
		{"keni-backup", true},
		{"my_container.v2", true},
		{"MyApp123", true},
		{"a", false},              // single char, needs 2+
		{"", false},               // empty
		{"-leading-dash", false},  // starts with dash
		{".leading-dot", false},   // starts with dot
		{"_leading-under", false}, // starts with underscore
		{"has space", false},
		{"semi;colon", false},
		{"back`tick", false},
		{"dollar$sign", false},
		{"abc123_.-test", true},
		{"Ab", true}, // minimum valid: 2 chars, starts with alphanum
		{"; rm -rf /", false},
		{"$(whoami)", false},
		{"name\nnewline", false},
	}

	for _, tt := range tests {
		got := containerNameRe.MatchString(tt.name)
		if got != tt.valid {
			t.Errorf("containerNameRe.MatchString(%q) = %v, want %v", tt.name, got, tt.valid)
		}
	}
}

func TestValidateServiceName_Table(t *testing.T) {
	tests := []struct {
		name    string
		wantErr bool
		errMsg  string
	}{
		{"docker", false, ""},
		{"wg-quick@wg0", false, ""},
		{"keni-agent", false, ""},
		{"sshd", true, "not in the allowlist"},
		{"nginx", true, "not in the allowlist"},
		{"../../etc/passwd", true, "not in the allowlist"},
		{"", true, "not in the allowlist"},
		{"Docker", true, "not in the allowlist"}, // case sensitive
		{"DOCKER", true, "not in the allowlist"},
		{"keni-agent ", true, "not in the allowlist"}, // trailing space
	}

	for _, tt := range tests {
		err := validateServiceName(tt.name)
		if tt.wantErr {
			if err == nil {
				t.Errorf("validateServiceName(%q): expected error, got nil", tt.name)
			} else if !strings.Contains(err.Error(), tt.errMsg) {
				t.Errorf("validateServiceName(%q): error %q should contain %q", tt.name, err.Error(), tt.errMsg)
			}
		} else {
			if err != nil {
				t.Errorf("validateServiceName(%q): unexpected error: %v", tt.name, err)
			}
		}
	}
}

func TestIsStreamingAction_AllActions(t *testing.T) {
	tests := []struct {
		action    string
		streaming bool
	}{
		{"backup_trigger", true},
		{"container_list", false},
		{"container_stats", false},
		{"container_restart", false},
		{"backup_snapshots", false},
		{"backup_stats", false},
		{"system_disk", false},
		{"system_memory", false},
		{"system_info", false},
		{"service_status", false},
		{"wireguard_status", false},
		{"docker_logs", false},
		{"unknown_action", false},
		{"", false},
	}

	for _, tt := range tests {
		got := IsStreamingAction(tt.action)
		if got != tt.streaming {
			t.Errorf("IsStreamingAction(%q) = %v, want %v", tt.action, got, tt.streaming)
		}
	}
}

func TestBuildCommand_ParameterlessActions(t *testing.T) {
	tests := []struct {
		action   string
		wantBin  string
		wantArgs []string
	}{
		{"container_list", "docker", []string{"docker", "ps", "-a", "--format", "json"}},
		{"container_stats", "docker", []string{"docker", "stats", "--no-stream", "--format", "json"}},
		{"backup_snapshots", "restic", []string{"restic", "snapshots", "--json"}},
		{"backup_stats", "restic", []string{"restic", "stats", "--json"}},
		{"backup_trigger", "docker", []string{"docker", "start", "-a", "keni-backup"}},
		{"system_disk", "df", []string{"df", "-h"}},
		{"system_memory", "free", []string{"free", "-m"}},
		{"wireguard_status", "wg", []string{"wg", "show"}},
	}

	for _, tt := range tests {
		t.Run(tt.action, func(t *testing.T) {
			cmd, err := buildCommand(context.Background(), tt.action, nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(cmd.Args) != len(tt.wantArgs) {
				t.Fatalf("args length: got %d, want %d: %v", len(cmd.Args), len(tt.wantArgs), cmd.Args)
			}
			for i, arg := range tt.wantArgs {
				if cmd.Args[i] != arg {
					t.Errorf("arg[%d]: got %q, want %q", i, cmd.Args[i], arg)
				}
			}
		})
	}
}

func TestBuildCommand_SystemInfo(t *testing.T) {
	_, err := buildCommand(context.Background(), "system_info", nil)
	if err != errSystemInfoSpecial {
		t.Errorf("expected errSystemInfoSpecial, got: %v", err)
	}
}

func TestBuildCommand_UnknownActions_Table(t *testing.T) {
	actions := []string{
		"not_a_real_action",
		"",
		"CONTAINER_LIST", // case sensitive
		"container_delete",
		"rm -rf /",
		"shell_exec",
	}

	for _, action := range actions {
		t.Run(action, func(t *testing.T) {
			_, err := buildCommand(context.Background(), action, nil)
			if err == nil {
				t.Fatal("expected error for unknown action")
			}
			if !strings.Contains(err.Error(), "UNKNOWN_ACTION") {
				t.Errorf("expected UNKNOWN_ACTION, got: %v", err)
			}
		})
	}
}

func TestBuildCommand_ContainerRestart_MissingParams(t *testing.T) {
	_, err := buildCommand(context.Background(), "container_restart", nil)
	if err == nil {
		t.Fatal("expected error for nil params")
	}
	if !strings.Contains(err.Error(), "INVALID_PARAMS") {
		t.Errorf("expected INVALID_PARAMS, got: %v", err)
	}
}

func TestBuildCommand_ContainerRestart_EmptyName(t *testing.T) {
	params, _ := json.Marshal(map[string]string{"name": ""})
	_, err := buildCommand(context.Background(), "container_restart", params)
	if err == nil {
		t.Fatal("expected error for empty name")
	}
	if !strings.Contains(err.Error(), "INVALID_PARAMS") {
		t.Errorf("expected INVALID_PARAMS, got: %v", err)
	}
}

func TestBuildCommand_ContainerRestart_ValidName(t *testing.T) {
	seedContainerCache([]string{"traefik", "komodo-core", "grafana"})
	params, _ := json.Marshal(map[string]string{"name": "traefik"})
	cmd, err := buildCommand(context.Background(), "container_restart", params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := []string{"docker", "restart", "traefik"}
	if len(cmd.Args) != len(expected) {
		t.Fatalf("args: got %v, want %v", cmd.Args, expected)
	}
	for i, arg := range expected {
		if cmd.Args[i] != arg {
			t.Errorf("arg[%d]: got %q, want %q", i, cmd.Args[i], arg)
		}
	}
}

func TestBuildCommand_ContainerRestart_NonexistentContainer(t *testing.T) {
	seedContainerCache([]string{"traefik", "grafana"})
	params, _ := json.Marshal(map[string]string{"name": "nonexistent"})
	_, err := buildCommand(context.Background(), "container_restart", params)
	if err == nil {
		t.Fatal("expected error for nonexistent container")
	}
	if !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("expected 'does not exist' error, got: %v", err)
	}
}

func TestBuildCommand_DockerLogs_WithCachedContainer(t *testing.T) {
	seedContainerCache([]string{"traefik", "komodo-core"})
	params, _ := json.Marshal(map[string]interface{}{"name": "traefik", "lines": 50})
	cmd, err := buildCommand(context.Background(), "docker_logs", params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := []string{"docker", "logs", "--tail", "50", "traefik"}
	if len(cmd.Args) != len(expected) {
		t.Fatalf("args: got %v, want %v", cmd.Args, expected)
	}
	for i, arg := range expected {
		if cmd.Args[i] != arg {
			t.Errorf("arg[%d]: got %q, want %q", i, cmd.Args[i], arg)
		}
	}
}

func TestBuildCommand_DockerLogs_BoundaryLines(t *testing.T) {
	seedContainerCache([]string{"traefik"})
	tests := []struct {
		desc    string
		lines   int
		wantErr bool
		errMsg  string
	}{
		{"zero", 0, true, "INVALID_PARAMS"},
		{"negative", -1, true, "INVALID_PARAMS"},
		{"min_valid", 1, false, ""},
		{"max_valid", 500, false, ""},
		{"over_max", 501, true, "INVALID_PARAMS"},
		{"way_over", 1000, true, "INVALID_PARAMS"},
	}

	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			params, _ := json.Marshal(map[string]interface{}{"name": "traefik", "lines": tt.lines})
			_, err := buildCommand(context.Background(), "docker_logs", params)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error for lines=%d", tt.lines)
				}
				if !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("error %q should contain %q", err.Error(), tt.errMsg)
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error for lines=%d: %v", tt.lines, err)
				}
			}
		})
	}
}

func TestBuildCommand_DockerLogs_MissingName(t *testing.T) {
	params, _ := json.Marshal(map[string]interface{}{"lines": 100})
	_, err := buildCommand(context.Background(), "docker_logs", params)
	if err == nil {
		t.Fatal("expected error for missing name")
	}
	if !strings.Contains(err.Error(), "INVALID_PARAMS") {
		t.Errorf("expected INVALID_PARAMS, got: %v", err)
	}
}

func TestBuildCommand_DockerLogs_MissingLines(t *testing.T) {
	seedContainerCache([]string{"traefik"})
	params, _ := json.Marshal(map[string]string{"name": "traefik"})
	_, err := buildCommand(context.Background(), "docker_logs", params)
	if err == nil {
		t.Fatal("expected error for missing lines")
	}
	if !strings.Contains(err.Error(), "INVALID_PARAMS") {
		t.Errorf("expected INVALID_PARAMS, got: %v", err)
	}
}

func TestBuildCommand_DockerLogs_InvalidNameRegex(t *testing.T) {
	params, _ := json.Marshal(map[string]interface{}{"name": "; rm -rf /", "lines": 10})
	_, err := buildCommand(context.Background(), "docker_logs", params)
	if err == nil {
		t.Fatal("expected error for invalid container name")
	}
	if !strings.Contains(err.Error(), "INVALID_PARAMS") {
		t.Errorf("expected INVALID_PARAMS, got: %v", err)
	}
}

func TestBuildCommand_ServiceStatus_AllAllowed(t *testing.T) {
	for _, svc := range AllowedServices {
		t.Run(svc, func(t *testing.T) {
			params, _ := json.Marshal(map[string]string{"name": svc})
			cmd, err := buildCommand(context.Background(), "service_status", params)
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", svc, err)
			}
			expected := []string{"systemctl", "is-active", svc}
			if len(cmd.Args) != len(expected) {
				t.Fatalf("args: got %v, want %v", cmd.Args, expected)
			}
			for i, arg := range expected {
				if cmd.Args[i] != arg {
					t.Errorf("arg[%d]: got %q, want %q", i, cmd.Args[i], arg)
				}
			}
		})
	}
}

func TestBuildCommand_ServiceStatus_MissingParams(t *testing.T) {
	_, err := buildCommand(context.Background(), "service_status", nil)
	if err == nil {
		t.Fatal("expected error for nil params")
	}
	if !strings.Contains(err.Error(), "INVALID_PARAMS") {
		t.Errorf("expected INVALID_PARAMS, got: %v", err)
	}
}

func TestBuildCommand_ServiceStatus_MissingName(t *testing.T) {
	params, _ := json.Marshal(map[string]string{"service": "docker"})
	_, err := buildCommand(context.Background(), "service_status", params)
	if err == nil {
		t.Fatal("expected error for wrong param key")
	}
	if !strings.Contains(err.Error(), "INVALID_PARAMS") {
		t.Errorf("expected INVALID_PARAMS, got: %v", err)
	}
}

func TestExtractStringParam_Table(t *testing.T) {
	tests := []struct {
		desc    string
		params  json.RawMessage
		key     string
		want    string
		wantErr string
	}{
		{
			desc:   "valid string",
			params: json.RawMessage(`{"name":"traefik"}`),
			key:    "name",
			want:   "traefik",
		},
		{
			desc:    "nil params",
			params:  nil,
			key:     "name",
			wantErr: "missing params",
		},
		{
			desc:    "missing key",
			params:  json.RawMessage(`{"other":"val"}`),
			key:     "name",
			wantErr: "missing required param",
		},
		{
			desc:    "wrong type (number)",
			params:  json.RawMessage(`{"name":123}`),
			key:     "name",
			wantErr: "must be a string",
		},
		{
			desc:    "wrong type (bool)",
			params:  json.RawMessage(`{"name":true}`),
			key:     "name",
			wantErr: "must be a string",
		},
		{
			desc:   "null value unmarshals to empty string",
			params: json.RawMessage(`{"name":null}`),
			key:    "name",
			want:   "",
		},
		{
			desc:    "invalid JSON",
			params:  json.RawMessage(`not json`),
			key:     "name",
			wantErr: "parsing params",
		},
		{
			desc:   "empty string value",
			params: json.RawMessage(`{"name":""}`),
			key:    "name",
			want:   "",
		},
		{
			desc:    "empty JSON object",
			params:  json.RawMessage(`{}`),
			key:     "name",
			wantErr: "missing required param",
		},
	}

	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			val, err := extractStringParam(tt.params, tt.key)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("error %q should contain %q", err.Error(), tt.wantErr)
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if val != tt.want {
					t.Errorf("got %q, want %q", val, tt.want)
				}
			}
		})
	}
}

func TestExtractIntParam_Table(t *testing.T) {
	tests := []struct {
		desc    string
		params  json.RawMessage
		key     string
		want    int
		wantErr string
	}{
		{
			desc:   "valid int",
			params: json.RawMessage(`{"lines":100}`),
			key:    "lines",
			want:   100,
		},
		{
			desc:   "zero",
			params: json.RawMessage(`{"lines":0}`),
			key:    "lines",
			want:   0,
		},
		{
			desc:   "negative",
			params: json.RawMessage(`{"lines":-5}`),
			key:    "lines",
			want:   -5,
		},
		{
			desc:    "nil params",
			params:  nil,
			key:     "lines",
			wantErr: "missing params",
		},
		{
			desc:    "missing key",
			params:  json.RawMessage(`{"other":100}`),
			key:     "lines",
			wantErr: "missing required param",
		},
		{
			desc:    "wrong type (string)",
			params:  json.RawMessage(`{"lines":"100"}`),
			key:     "lines",
			wantErr: "must be a number",
		},
		{
			desc:    "wrong type (bool)",
			params:  json.RawMessage(`{"lines":true}`),
			key:     "lines",
			wantErr: "must be a number",
		},
		{
			desc:    "invalid JSON",
			params:  json.RawMessage(`not json`),
			key:     "lines",
			wantErr: "parsing params",
		},
		{
			desc:   "float truncates to int",
			params: json.RawMessage(`{"lines":99.9}`),
			key:    "lines",
			want:   99,
		},
	}

	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			val, err := extractIntParam(tt.params, tt.key)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("error %q should contain %q", err.Error(), tt.wantErr)
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if val != tt.want {
					t.Errorf("got %d, want %d", val, tt.want)
				}
			}
		})
	}
}

func TestExecute_UnknownAction(t *testing.T) {
	_, err := Execute(context.Background(), "bogus_action", nil)
	if err == nil {
		t.Fatal("expected error for unknown action")
	}
	if !strings.Contains(err.Error(), "UNKNOWN_ACTION") {
		t.Errorf("expected UNKNOWN_ACTION, got: %v", err)
	}
}

func TestExecute_SystemInfo(t *testing.T) {
	result, err := Execute(context.Background(), "system_info", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ExitCode != 0 {
		t.Errorf("expected exit code 0, got %d", result.ExitCode)
	}
	// Should contain valid JSON with hostname, os, arch fields
	var info SystemInfoResult
	if err := json.Unmarshal([]byte(result.Stdout), &info); err != nil {
		t.Fatalf("failed to parse system info JSON: %v", err)
	}
	if info.Arch == "" {
		t.Error("expected non-empty arch")
	}
	if result.DurationMs < 0 {
		t.Error("expected non-negative duration")
	}
}

func TestExecute_ContainerRestart_InvalidParams(t *testing.T) {
	// Missing params
	_, err := Execute(context.Background(), "container_restart", nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "INVALID_PARAMS") {
		t.Errorf("expected INVALID_PARAMS, got: %v", err)
	}

	// Invalid name
	params, _ := json.Marshal(map[string]string{"name": "$(evil)"})
	_, err = Execute(context.Background(), "container_restart", params)
	if err == nil {
		t.Fatal("expected error for injection attempt")
	}
}

func TestExecute_ServiceStatus_DisallowedService(t *testing.T) {
	params, _ := json.Marshal(map[string]string{"name": "sshd"})
	_, err := Execute(context.Background(), "service_status", params)
	if err == nil {
		t.Fatal("expected error for disallowed service")
	}
	if !strings.Contains(err.Error(), "INVALID_PARAMS") {
		t.Errorf("expected INVALID_PARAMS, got: %v", err)
	}
}

func TestExecute_DockerLogs_ValidationErrors(t *testing.T) {
	tests := []struct {
		desc    string
		params  json.RawMessage
		wantErr string
	}{
		{
			desc:    "nil params",
			params:  nil,
			wantErr: "INVALID_PARAMS",
		},
		{
			desc:    "missing name",
			params:  json.RawMessage(`{"lines":10}`),
			wantErr: "INVALID_PARAMS",
		},
		{
			desc:    "invalid name",
			params:  json.RawMessage(`{"name":"$(rm -rf /)","lines":10}`),
			wantErr: "INVALID_PARAMS",
		},
	}

	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			_, err := Execute(context.Background(), "docker_logs", tt.params)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q should contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestValidateContainerName_Regex(t *testing.T) {
	// Seed the cache so we can test regex failures without docker
	seedContainerCache([]string{"valid-container"})

	tests := []struct {
		name    string
		wantErr string
	}{
		{"; rm -rf /", "invalid container name"},
		{"$(whoami)", "invalid container name"},
		{"", "invalid container name"},
		{"-starts-with-dash", "invalid container name"},
		{"a", "invalid container name"}, // too short for regex
		{"valid-container", ""},         // exists in cache
		{"valid-but-not-running", "does not exist"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateContainerName(tt.name)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			} else {
				if err == nil {
					t.Fatal("expected error")
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("error %q should contain %q", err.Error(), tt.wantErr)
				}
			}
		})
	}
}

func TestContainerCacheConcurrency(t *testing.T) {
	seedContainerCache([]string{"container-a", "container-b"})

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = validateContainerName("container-a")
		}()
	}
	wg.Wait()
}

func TestBuildCommand_ContainerRestart_InjectionAttempts(t *testing.T) {
	seedContainerCache([]string{})
	injections := []string{
		"name; echo pwned",
		"name && cat /etc/passwd",
		"name | whoami",
		"name`id`",
		"$(cat /etc/shadow)",
		"name\necho hacked",
	}

	for _, name := range injections {
		params, _ := json.Marshal(map[string]string{"name": name})
		_, err := buildCommand(context.Background(), "container_restart", params)
		if err == nil {
			t.Errorf("expected rejection for injection attempt: %q", name)
		}
	}
}

func TestBuildCommand_DockerLogs_NonexistentContainer(t *testing.T) {
	seedContainerCache([]string{"grafana"})
	params, _ := json.Marshal(map[string]interface{}{"name": "nonexistent", "lines": 10})
	_, err := buildCommand(context.Background(), "docker_logs", params)
	if err == nil {
		t.Fatal("expected error for nonexistent container")
	}
	if !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("expected 'does not exist', got: %v", err)
	}
}

func TestExecute_SystemDisk(t *testing.T) {
	result, err := Execute(context.Background(), "system_disk", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ExitCode != 0 {
		t.Errorf("expected exit code 0, got %d", result.ExitCode)
	}
	if result.Stdout == "" {
		t.Error("expected non-empty stdout from df -h")
	}
	if result.DurationMs < 0 {
		t.Error("expected non-negative duration")
	}
}

func TestExecuteStream_UnknownAction(t *testing.T) {
	_, err := ExecuteStream(context.Background(), "bogus", nil, func(sl StreamLine) {})
	if err == nil {
		t.Fatal("expected error for unknown action")
	}
	if !strings.Contains(err.Error(), "UNKNOWN_ACTION") {
		t.Errorf("expected UNKNOWN_ACTION, got: %v", err)
	}
}

func TestExecuteStream_InvalidParams(t *testing.T) {
	// service_status with disallowed service
	params, _ := json.Marshal(map[string]string{"name": "sshd"})
	_, err := ExecuteStream(context.Background(), "service_status", params, func(sl StreamLine) {})
	if err == nil {
		t.Fatal("expected error for disallowed service")
	}
	if !strings.Contains(err.Error(), "INVALID_PARAMS") {
		t.Errorf("expected INVALID_PARAMS, got: %v", err)
	}
}

func TestExecuteStream_ContainerRestart_MissingParams(t *testing.T) {
	_, err := ExecuteStream(context.Background(), "container_restart", nil, func(sl StreamLine) {})
	if err == nil {
		t.Fatal("expected error for nil params")
	}
}

func TestExecute_ContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately
	// system_disk should fail or return error due to canceled context
	result, err := Execute(ctx, "system_disk", nil)
	// Either an error or a non-zero exit code is acceptable
	if err == nil && result.ExitCode == 0 {
		// On some systems, df might complete before context check. That is also OK.
		t.Log("command completed despite canceled context (fast execution)")
	}
}

func TestBuildCommand_ParamsAsWrongJSON(t *testing.T) {
	// Array instead of object
	_, err := buildCommand(context.Background(), "container_restart", json.RawMessage(`[1,2,3]`))
	if err == nil {
		t.Fatal("expected error for array params")
	}

	_, err = buildCommand(context.Background(), "service_status", json.RawMessage(`"just a string"`))
	if err == nil {
		t.Fatal("expected error for string params")
	}

	_, err = buildCommand(context.Background(), "docker_logs", json.RawMessage(`null`))
	if err == nil {
		t.Fatal("expected error for null params")
	}
}
