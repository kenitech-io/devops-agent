package metrics

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthz_Connected(t *testing.T) {
	Init("0.1.0", "ag_test")
	SetWSConnected(true)
	RecordHeartbeat()

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	handleHealthz(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var resp healthResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if resp.Status != "ok" {
		t.Errorf("expected status ok, got %s", resp.Status)
	}
	if resp.Version != "0.1.0" {
		t.Errorf("expected version 0.1.0, got %s", resp.Version)
	}
	if resp.AgentID != "ag_test" {
		t.Errorf("expected agentId ag_test, got %s", resp.AgentID)
	}
	if !resp.WSConnected {
		t.Error("expected wsConnected=true")
	}
	if resp.LastHeartbeat == 0 {
		t.Error("expected non-zero lastHeartbeat")
	}
	if resp.UptimeSeconds < 0 {
		t.Error("expected non-negative uptime")
	}
}

func TestHealthz_Disconnected(t *testing.T) {
	Init("0.1.0", "ag_test")
	SetWSConnected(false)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	handleHealthz(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", w.Code)
	}

	var resp healthResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Status != "degraded" {
		t.Errorf("expected status degraded, got %s", resp.Status)
	}
}

func TestUpdateRuntimeMetrics(t *testing.T) {
	Init("0.1.0", "ag_test")
	UpdateRuntimeMetrics()

	// After calling UpdateRuntimeMetrics, the goroutine gauge must be > 0
	// (there is always at least 1 goroutine: the one running the test).
	goroutines := state.lastGoroutines.Load()
	if goroutines <= 0 {
		t.Errorf("expected goroutines > 0, got %d", goroutines)
	}

	heapBytes := state.lastHeapAllocBytes.Load()
	if heapBytes <= 0 {
		t.Errorf("expected heapAllocBytes > 0, got %d", heapBytes)
	}
}

func TestHealthz_IncludesRuntimeFields(t *testing.T) {
	Init("0.1.0", "ag_test")
	SetWSConnected(true)
	UpdateRuntimeMetrics()

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	handleHealthz(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp healthResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if resp.Goroutines <= 0 {
		t.Errorf("expected goroutines > 0, got %d", resp.Goroutines)
	}
	if resp.HeapAllocMb <= 0 {
		t.Errorf("expected heapAllocMb > 0, got %f", resp.HeapAllocMb)
	}
}
