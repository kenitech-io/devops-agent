package ws

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestClient_SendsBearerToken(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	tokenReceived := make(chan string, 1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokenReceived <- r.Header.Get("Authorization")
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		// Read one message then close
		conn.ReadMessage()
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	client := NewClient(wsURL, "ag_auth", "wst_secret123", func(msg *Message) {})

	ctx, cancel := context.WithCancel(context.Background())
	go client.Run(ctx)

	select {
	case auth := <-tokenReceived:
		if auth != "Bearer wst_secret123" {
			t.Errorf("expected 'Bearer wst_secret123', got %q", auth)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for WS connection")
	}
	cancel()
}

func TestClient_ConnectAndHeartbeat(t *testing.T) {
	var received []Message
	var mu sync.Mutex

	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify agentId query param
		agentID := r.URL.Query().Get("agentId")
		if agentID != "ag_test123" {
			t.Errorf("expected agentId=ag_test123, got %s", agentID)
		}

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Fatalf("upgrade error: %v", err)
		}
		defer conn.Close()

		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var msg Message
			if json.Unmarshal(data, &msg) == nil {
				mu.Lock()
				received = append(received, msg)
				mu.Unlock()
			}
		}
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")

	var handledMsgs []Message
	var handlerMu sync.Mutex
	handler := func(msg *Message) {
		handlerMu.Lock()
		handledMsgs = append(handledMsgs, *msg)
		handlerMu.Unlock()
	}

	client := NewClient(wsURL, "ag_test123", "test-ws-token", handler)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Run client in background
	errCh := make(chan error, 1)
	go func() {
		errCh <- client.Run(ctx)
	}()

	// Send a heartbeat
	hbPayload := HeartbeatPayload{
		Uptime:        86400,
		LoadAvg:       []float64{0.5, 0.3, 0.2},
		MemoryUsedMb:  2048,
		MemoryTotalMb: 8192,
		DiskUsedGb:    45.2,
		DiskTotalGb:   100.0,
		AgentVersion:  "0.1.0",
	}

	// Wait for connection
	time.Sleep(100 * time.Millisecond)

	msg, err := NewMessage(TypeHeartbeat, hbPayload)
	if err != nil {
		t.Fatalf("NewMessage error: %v", err)
	}

	if err := client.Send(msg); err != nil {
		t.Fatalf("Send error: %v", err)
	}

	// Wait for message to arrive
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()

	if len(received) == 0 {
		t.Fatal("expected at least 1 message received by server")
	}

	if received[0].Type != TypeHeartbeat {
		t.Errorf("expected type heartbeat, got %s", received[0].Type)
	}

	var hb HeartbeatPayload
	if err := json.Unmarshal(received[0].Payload, &hb); err != nil {
		t.Fatalf("unmarshal heartbeat payload: %v", err)
	}
	if hb.Uptime != 86400 {
		t.Errorf("expected uptime 86400, got %d", hb.Uptime)
	}
	if hb.AgentVersion != "0.1.0" {
		t.Errorf("expected version 0.1.0, got %s", hb.AgentVersion)
	}

	cancel()
}

func TestClient_ReceivePingRespondPong(t *testing.T) {
	var pongReceived bool
	var pongMu sync.Mutex

	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Fatalf("upgrade error: %v", err)
		}
		defer conn.Close()

		// Send a ping to the agent
		ping, _ := NewMessage(TypePing, PingPayload{})
		data, _ := json.Marshal(ping)
		conn.WriteMessage(websocket.TextMessage, data)

		// Read the pong response
		_, respData, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var msg Message
		if json.Unmarshal(respData, &msg) == nil && msg.Type == TypePong {
			pongMu.Lock()
			pongReceived = true
			pongMu.Unlock()
		}
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")

	var clientRef *Client
	handler := func(msg *Message) {
		if msg.Type == TypePing {
			pong, _ := NewMessage(TypePong, PongPayload{PingID: msg.ID})
			clientRef.Send(pong)
		}
	}
	clientRef = NewClient(wsURL, "ag_test", "test-ws-token", handler)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go clientRef.Run(ctx)

	time.Sleep(300 * time.Millisecond)

	pongMu.Lock()
	defer pongMu.Unlock()
	if !pongReceived {
		t.Error("expected pong response to ping")
	}

	cancel()
}

func TestClient_BuffersHeartbeatWhenDisconnected(t *testing.T) {
	// Create a client that is NOT connected (no Run called).
	// Fill the send channel completely, then send a heartbeat.
	// It should be buffered (no error), not dropped.
	handler := func(msg *Message) {}
	client := NewClient("ws://localhost:9999", "ag_buf_test", "tok", handler)

	// Fill the send channel to capacity.
	for i := 0; i < cap(client.sendCh); i++ {
		client.sendCh <- []byte(`{"type":"filler"}`)
	}

	// Now send a heartbeat: should be buffered, not return an error.
	hb, err := NewMessage(TypeHeartbeat, HeartbeatPayload{Uptime: 42, AgentVersion: "0.1.0"})
	if err != nil {
		t.Fatalf("NewMessage error: %v", err)
	}
	if err := client.Send(hb); err != nil {
		t.Fatalf("expected heartbeat to be buffered, got error: %v", err)
	}

	// Verify it was stored.
	client.pendingMu.Lock()
	if client.pendingHeartbeat == nil {
		t.Error("expected pendingHeartbeat to be non-nil")
	}
	client.pendingMu.Unlock()

	// Also test status_report buffering.
	sr, err := NewMessage(TypeStatusReport, StatusReportPayload{})
	if err != nil {
		t.Fatalf("NewMessage error: %v", err)
	}
	if err := client.Send(sr); err != nil {
		t.Fatalf("expected status_report to be buffered, got error: %v", err)
	}

	client.pendingMu.Lock()
	if client.pendingStatusReport == nil {
		t.Error("expected pendingStatusReport to be non-nil")
	}
	client.pendingMu.Unlock()

	// A non-bufferable message type should return an error.
	pong, _ := NewMessage(TypePong, PongPayload{PingID: "test"})
	if err := client.Send(pong); err == nil {
		t.Error("expected error for non-bufferable message type when channel is full")
	}
}

func TestClient_FlushesOnReconnect(t *testing.T) {
	var received []Message
	var mu sync.Mutex

	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var msg Message
			if json.Unmarshal(data, &msg) == nil {
				mu.Lock()
				received = append(received, msg)
				mu.Unlock()
			}
		}
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	handler := func(msg *Message) {}
	client := NewClient(wsURL, "ag_flush", "tok", handler)

	// Pre-buffer a heartbeat and status report before connecting.
	hbMsg, _ := NewMessage(TypeHeartbeat, HeartbeatPayload{Uptime: 99, AgentVersion: "0.1.0"})
	hbData, _ := json.Marshal(hbMsg)
	srMsg, _ := NewMessage(TypeStatusReport, StatusReportPayload{})
	srData, _ := json.Marshal(srMsg)

	client.pendingMu.Lock()
	client.pendingHeartbeat = hbData
	client.pendingStatusReport = srData
	client.pendingMu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go client.Run(ctx)

	// Wait for connection and flush.
	time.Sleep(300 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()

	if len(received) < 2 {
		t.Fatalf("expected at least 2 flushed messages, got %d", len(received))
	}

	types := map[string]bool{}
	for _, m := range received {
		types[m.Type] = true
	}
	if !types[TypeHeartbeat] {
		t.Error("expected flushed heartbeat message")
	}
	if !types[TypeStatusReport] {
		t.Error("expected flushed status_report message")
	}

	// Verify pending buffers are cleared.
	client.pendingMu.Lock()
	if client.pendingHeartbeat != nil {
		t.Error("expected pendingHeartbeat to be nil after flush")
	}
	if client.pendingStatusReport != nil {
		t.Error("expected pendingStatusReport to be nil after flush")
	}
	client.pendingMu.Unlock()

	cancel()
}

func TestConfigBackupPayload_NoSecrets(t *testing.T) {
	// ConfigBackupPayload must NOT include sensitive fields like ws_token or wireguard keys.
	payload := ConfigBackupPayload{
		AgentID:       "ag_test123",
		AssignedIP:    "10.99.0.5",
		WSEndpoint:    "wss://10.99.0.1/ws/agent",
		DashboardURL:  "https://dash.example.com",
		AgentVersion:  "1.0.0",
		ConfigVersion: 1,
	}

	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	// These fields must NOT appear in the payload
	secretFields := []string{"wsToken", "ws_token", "wireguardPrivKey", "wireguard_private_key", "wireguardPubKey", "wireguard_public_key", "dashboardPubKey", "dashboard_public_key"}
	for _, field := range secretFields {
		if _, exists := raw[field]; exists {
			t.Errorf("ConfigBackupPayload must not contain secret field %q", field)
		}
	}

	// These fields must be present
	expectedFields := []string{"agentId", "assignedIp", "wsEndpoint", "dashboardUrl", "agentVersion", "configVersion"}
	for _, field := range expectedFields {
		if _, exists := raw[field]; !exists {
			t.Errorf("ConfigBackupPayload missing expected field %q", field)
		}
	}
}

func TestConfigBackupPayload_RoundTrip(t *testing.T) {
	payload := ConfigBackupPayload{
		AgentID:       "ag_roundtrip",
		AssignedIP:    "10.99.0.10",
		WSEndpoint:    "wss://10.99.0.1:443/ws/agent",
		DashboardURL:  "https://dashboard.kenitech.io",
		AgentVersion:  "1.2.3",
		ConfigVersion: 1,
	}

	msg, err := NewMessage(TypeConfigBackup, payload)
	if err != nil {
		t.Fatalf("NewMessage error: %v", err)
	}

	if msg.Type != TypeConfigBackup {
		t.Errorf("expected type %s, got %s", TypeConfigBackup, msg.Type)
	}
	if !strings.HasPrefix(msg.ID, "config_backup_") {
		t.Errorf("expected ID prefix config_backup_, got %s", msg.ID)
	}

	var decoded ConfigBackupPayload
	if err := json.Unmarshal(msg.Payload, &decoded); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}

	if decoded.AgentID != payload.AgentID {
		t.Errorf("AgentID = %s, want %s", decoded.AgentID, payload.AgentID)
	}
	if decoded.AssignedIP != payload.AssignedIP {
		t.Errorf("AssignedIP = %s, want %s", decoded.AssignedIP, payload.AssignedIP)
	}
	if decoded.AgentVersion != payload.AgentVersion {
		t.Errorf("AgentVersion = %s, want %s", decoded.AgentVersion, payload.AgentVersion)
	}
	if decoded.ConfigVersion != payload.ConfigVersion {
		t.Errorf("ConfigVersion = %d, want %d", decoded.ConfigVersion, payload.ConfigVersion)
	}
}

func TestConfigUpdatePayload_OmitsEmpty(t *testing.T) {
	payload := ConfigUpdatePayload{
		WSEndpoint:   "",
		WSToken:      "wst_new",
		DashboardURL: "",
		RestartAfter: true,
	}

	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	var raw map[string]interface{}
	json.Unmarshal(data, &raw)

	if _, exists := raw["wsEndpoint"]; exists {
		t.Error("empty wsEndpoint should be omitted from JSON")
	}
	if _, exists := raw["dashboardUrl"]; exists {
		t.Error("empty dashboardUrl should be omitted from JSON")
	}
	if _, exists := raw["wsToken"]; !exists {
		t.Error("non-empty wsToken should be present in JSON")
	}
	if _, exists := raw["restartAfter"]; !exists {
		t.Error("restartAfter should be present in JSON")
	}
}

func TestNewMessage(t *testing.T) {
	payload := HeartbeatPayload{Uptime: 100, AgentVersion: "0.1.0"}
	msg, err := NewMessage(TypeHeartbeat, payload)
	if err != nil {
		t.Fatalf("NewMessage error: %v", err)
	}

	if msg.Type != TypeHeartbeat {
		t.Errorf("expected type %s, got %s", TypeHeartbeat, msg.Type)
	}
	if msg.ID == "" {
		t.Error("expected non-empty ID")
	}
	if msg.Timestamp == "" {
		t.Error("expected non-empty timestamp")
	}
	if !strings.HasPrefix(msg.ID, "heartbeat_") {
		t.Errorf("expected ID prefix heartbeat_, got %s", msg.ID)
	}

	// Verify timestamp is valid RFC3339
	_, err = time.Parse(time.RFC3339, msg.Timestamp)
	if err != nil {
		t.Errorf("timestamp is not valid RFC3339: %s", msg.Timestamp)
	}
}
