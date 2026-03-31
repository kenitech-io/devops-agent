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

// TestIntegration_CommandRequestResponse simulates the full flow:
// dashboard sends command_request, agent handler processes it, sends command_result back.
func TestIntegration_CommandRequestResponse(t *testing.T) {
	var receivedResult *Message
	var resultMu sync.Mutex
	resultCh := make(chan struct{}, 1)

	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade error: %v", err)
			return
		}
		defer conn.Close()

		// Send a command_request to the agent
		cmdPayload := CommandRequestPayload{
			Action:  "system_disk",
			Params:  nil,
			Stream:  false,
			Timeout: 10,
		}
		cmdMsg, _ := NewMessage(TypeCommandRequest, cmdPayload)
		data, _ := json.Marshal(cmdMsg)
		conn.WriteMessage(websocket.TextMessage, data)

		// Read response
		for {
			_, respData, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var msg Message
			if json.Unmarshal(respData, &msg) == nil {
				if msg.Type == TypeCommandResult || msg.Type == TypeError {
					resultMu.Lock()
					receivedResult = &msg
					resultMu.Unlock()
					resultCh <- struct{}{}
					return
				}
			}
		}
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")

	var clientRef *Client
	handler := func(msg *Message) {
		// Simulate agent command handling: for this test, just echo back a result
		if msg.Type == TypeCommandRequest {
			var req CommandRequestPayload
			json.Unmarshal(msg.Payload, &req)

			result, _ := NewMessage(TypeCommandResult, CommandResultPayload{
				RequestID:  msg.ID,
				ExitCode:   0,
				Stdout:     "disk info here",
				Stderr:     "",
				DurationMs: 42,
			})
			clientRef.Send(result)
		}
	}
	clientRef = NewClient(wsURL, "ag_integration", "test-ws-token", handler)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go clientRef.Run(ctx)

	// Wait for the result
	select {
	case <-resultCh:
		// Got result
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for command result")
	}

	resultMu.Lock()
	defer resultMu.Unlock()

	if receivedResult == nil {
		t.Fatal("no result received")
	}
	if receivedResult.Type != TypeCommandResult {
		t.Errorf("expected type command_result, got %s", receivedResult.Type)
	}

	var cmdResult CommandResultPayload
	if err := json.Unmarshal(receivedResult.Payload, &cmdResult); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	if cmdResult.ExitCode != 0 {
		t.Errorf("expected exit code 0, got %d", cmdResult.ExitCode)
	}
	if cmdResult.Stdout != "disk info here" {
		t.Errorf("expected stdout 'disk info here', got %q", cmdResult.Stdout)
	}
	if cmdResult.DurationMs != 42 {
		t.Errorf("expected duration 42ms, got %d", cmdResult.DurationMs)
	}

	cancel()
}

// TestIntegration_StreamingCommand simulates the dashboard sending a streaming command
// and receiving command_stream lines followed by command_complete.
func TestIntegration_StreamingCommand(t *testing.T) {
	var receivedMsgs []Message
	var msgMu sync.Mutex
	doneCh := make(chan struct{}, 1)

	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade error: %v", err)
			return
		}
		defer conn.Close()

		// Send a streaming command request
		cmdPayload := CommandRequestPayload{
			Action:  "backup_trigger",
			Params:  nil,
			Stream:  true,
			Timeout: 30,
		}
		cmdMsg, _ := NewMessage(TypeCommandRequest, cmdPayload)
		data, _ := json.Marshal(cmdMsg)
		conn.WriteMessage(websocket.TextMessage, data)

		// Collect responses
		for {
			_, respData, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var msg Message
			if json.Unmarshal(respData, &msg) == nil {
				msgMu.Lock()
				receivedMsgs = append(receivedMsgs, msg)
				if msg.Type == TypeCommandComplete || msg.Type == TypeError {
					msgMu.Unlock()
					doneCh <- struct{}{}
					return
				}
				msgMu.Unlock()
			}
		}
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")

	var clientRef *Client
	handler := func(msg *Message) {
		if msg.Type == TypeCommandRequest {
			// Simulate streaming: send 3 lines, then complete
			for i, line := range []string{"Step 1...", "Step 2...", "Step 3..."} {
				_ = i
				streamMsg, _ := NewMessage(TypeCommandStream, CommandStreamPayload{
					RequestID: msg.ID,
					Stream:    "stdout",
					Line:      line,
				})
				clientRef.Send(streamMsg)
			}

			completeMsg, _ := NewMessage(TypeCommandComplete, CommandCompletePayload{
				RequestID:  msg.ID,
				ExitCode:   0,
				DurationMs: 500,
			})
			clientRef.Send(completeMsg)
		}
	}
	clientRef = NewClient(wsURL, "ag_stream", "test-ws-token", handler)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go clientRef.Run(ctx)

	select {
	case <-doneCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for streaming command completion")
	}

	msgMu.Lock()
	defer msgMu.Unlock()

	// Should have 3 stream messages + 1 complete
	streamCount := 0
	completeCount := 0
	for _, msg := range receivedMsgs {
		switch msg.Type {
		case TypeCommandStream:
			streamCount++
		case TypeCommandComplete:
			completeCount++
		}
	}

	if streamCount != 3 {
		t.Errorf("expected 3 stream messages, got %d", streamCount)
	}
	if completeCount != 1 {
		t.Errorf("expected 1 complete message, got %d", completeCount)
	}

	// Verify the complete message
	lastMsg := receivedMsgs[len(receivedMsgs)-1]
	if lastMsg.Type != TypeCommandComplete {
		t.Errorf("last message should be command_complete, got %s", lastMsg.Type)
	}

	var complete CommandCompletePayload
	json.Unmarshal(lastMsg.Payload, &complete)
	if complete.ExitCode != 0 {
		t.Errorf("expected exit code 0, got %d", complete.ExitCode)
	}

	cancel()
}

// TestIntegration_BusyRejectsSecondCommand verifies that when a command is running,
// a second command gets an error response.
func TestIntegration_BusyRejectsSecondCommand(t *testing.T) {
	var responses []Message
	var respMu sync.Mutex
	doneCh := make(chan struct{}, 1)

	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}

	// Use a channel to ensure the first command is holding the lock before we send the second
	firstCmdStarted := make(chan struct{})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		// Send first command
		cmdPayload := CommandRequestPayload{
			Action:  "system_disk",
			Timeout: 10,
		}
		cmdMsg, _ := NewMessage(TypeCommandRequest, cmdPayload)
		data, _ := json.Marshal(cmdMsg)
		conn.WriteMessage(websocket.TextMessage, data)

		// Wait until the first command handler has acquired the lock
		<-firstCmdStarted

		// Now send the second command (will get BUSY)
		cmdMsg2, _ := NewMessage(TypeCommandRequest, cmdPayload)
		data2, _ := json.Marshal(cmdMsg2)
		conn.WriteMessage(websocket.TextMessage, data2)

		// Collect two responses
		for {
			_, respData, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var msg Message
			if json.Unmarshal(respData, &msg) == nil {
				respMu.Lock()
				responses = append(responses, msg)
				if len(responses) >= 2 {
					respMu.Unlock()
					doneCh <- struct{}{}
					return
				}
				respMu.Unlock()
			}
		}
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")

	var cmdMu sync.Mutex
	var clientRef *Client
	handler := func(msg *Message) {
		if msg.Type == TypeCommandRequest {
			// Dispatch concurrently, like the real agent does in main.go
			go func() {
				if !cmdMu.TryLock() {
					errMsg, _ := NewMessage(TypeError, ErrorPayload{
						Code:      "BUSY",
						Message:   "agent is busy",
						RequestID: msg.ID,
					})
					clientRef.Send(errMsg)
					return
				}

				// Signal that the first command is now holding the lock
				select {
				case firstCmdStarted <- struct{}{}:
				default:
				}

				// Hold the lock for a while to ensure the second command sees BUSY
				time.Sleep(300 * time.Millisecond)

				result, _ := NewMessage(TypeCommandResult, CommandResultPayload{
					RequestID:  msg.ID,
					ExitCode:   0,
					Stdout:     "ok",
					DurationMs: 300,
				})
				clientRef.Send(result)
				cmdMu.Unlock()
			}()
		}
	}
	clientRef = NewClient(wsURL, "ag_busy", "test-ws-token", handler)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go clientRef.Run(ctx)

	select {
	case <-doneCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for responses")
	}

	respMu.Lock()
	defer respMu.Unlock()

	hasResult := false
	hasBusy := false
	for _, msg := range responses {
		if msg.Type == TypeCommandResult {
			hasResult = true
		}
		if msg.Type == TypeError {
			var errPayload ErrorPayload
			json.Unmarshal(msg.Payload, &errPayload)
			if errPayload.Code == "BUSY" {
				hasBusy = true
			}
		}
	}

	if !hasResult {
		t.Error("expected at least one successful command_result")
	}
	if !hasBusy {
		t.Error("expected BUSY error for second concurrent command")
	}

	cancel()
}
