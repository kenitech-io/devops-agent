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

	client := NewClient(wsURL, "ag_test123", handler)

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
	clientRef = NewClient(wsURL, "ag_test", handler)

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
