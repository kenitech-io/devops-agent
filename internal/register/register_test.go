package register

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRegister_Success(t *testing.T) {
	expected := Response{
		AgentID:            "ag_test123",
		AssignedIP:         "10.99.0.5",
		DashboardPublicKey: "dashboard-pub-key-base64",
		DashboardEndpoint:  "203.0.113.10:51820",
		WSEndpoint:         "wss://10.99.0.1:443/ws/agent",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/api/agent/register" {
			t.Errorf("expected /api/agent/register, got %s", r.URL.Path)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("expected application/json content type")
		}

		var req Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decoding request: %v", err)
		}

		if req.Token != "keni_testtoken123" {
			t.Errorf("expected token keni_testtoken123, got %s", req.Token)
		}
		if req.PublicKey != "agent-pub-key" {
			t.Errorf("expected publicKey agent-pub-key, got %s", req.PublicKey)
		}
		if req.Hostname != "test-server" {
			t.Errorf("expected hostname test-server, got %s", req.Hostname)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(expected)
	}))
	defer server.Close()

	req := Request{
		Token:         "keni_testtoken123",
		PublicKey:     "agent-pub-key",
		Hostname:      "test-server",
		OS:            "Ubuntu 24.04 LTS",
		Arch:          "amd64",
		DockerVersion: "27.1.0",
		KernelVersion: "6.8.0-40-generic",
	}

	resp, err := Register(server.URL, req)
	if err != nil {
		t.Fatalf("Register() error: %v", err)
	}

	if resp.AgentID != expected.AgentID {
		t.Errorf("AgentID = %s, want %s", resp.AgentID, expected.AgentID)
	}
	if resp.AssignedIP != expected.AssignedIP {
		t.Errorf("AssignedIP = %s, want %s", resp.AssignedIP, expected.AssignedIP)
	}
	if resp.DashboardPublicKey != expected.DashboardPublicKey {
		t.Errorf("DashboardPublicKey = %s, want %s", resp.DashboardPublicKey, expected.DashboardPublicKey)
	}
	if resp.DashboardEndpoint != expected.DashboardEndpoint {
		t.Errorf("DashboardEndpoint = %s, want %s", resp.DashboardEndpoint, expected.DashboardEndpoint)
	}
	if resp.WSEndpoint != expected.WSEndpoint {
		t.Errorf("WSEndpoint = %s, want %s", resp.WSEndpoint, expected.WSEndpoint)
	}
}

func TestRegister_InvalidToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(ErrorResponse{
			Code:    "INVALID_TOKEN",
			Message: "Invalid or expired token",
		})
	}))
	defer server.Close()

	req := Request{
		Token:    "keni_badtoken",
		PublicKey: "key",
		Hostname: "test",
	}

	_, err := Register(server.URL, req)
	if err == nil {
		t.Fatal("expected error for invalid token")
	}
	if !contains(err.Error(), "401") {
		t.Errorf("expected 401 in error, got: %v", err)
	}
}

func TestRegister_TokenAlreadyUsed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(ErrorResponse{
			Code:    "TOKEN_USED",
			Message: "Token already used",
		})
	}))
	defer server.Close()

	req := Request{
		Token:    "keni_usedtoken",
		PublicKey: "key",
		Hostname: "test",
	}

	_, err := Register(server.URL, req)
	if err == nil {
		t.Fatal("expected error for used token")
	}
	if !contains(err.Error(), "409") {
		t.Errorf("expected 409 in error, got: %v", err)
	}
}

func TestRegister_IncompleteResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(Response{AgentID: "", AssignedIP: ""})
	}))
	defer server.Close()

	req := Request{
		Token:    "keni_test",
		PublicKey: "key",
		Hostname: "test",
	}

	_, err := Register(server.URL, req)
	if err == nil {
		t.Fatal("expected error for incomplete response")
	}
}

func TestGatherSystemInfo(t *testing.T) {
	info, err := GatherSystemInfo()
	if err != nil {
		t.Fatalf("GatherSystemInfo() error: %v", err)
	}

	if info.Hostname == "" {
		t.Error("Hostname should not be empty")
	}
	if info.Arch == "" {
		t.Error("Arch should not be empty")
	}
	if info.KernelVersion == "" {
		t.Error("KernelVersion should not be empty")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
