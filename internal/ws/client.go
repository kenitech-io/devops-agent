package ws

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// MessageHandler is called when a message is received from the dashboard.
type MessageHandler func(msg *Message)

// ConnectionCallback is called when the WebSocket connection state changes.
type ConnectionCallback func(connected bool)

// Rate limiting constants for incoming commands.
const (
	maxCommandsPerMinute = 60
	rateLimitWindow      = time.Minute
)

// Client manages the WebSocket connection to the dashboard.
type Client struct {
	wsEndpoint string
	agentID    string
	wsToken    string
	handler    MessageHandler
	onConnect  ConnectionCallback

	conn   *websocket.Conn
	connMu sync.Mutex

	sendCh chan []byte

	// Rate limiter for incoming messages.
	rateMu         sync.Mutex
	rateTimestamps []time.Time

	// Buffered messages for reconnect. When the send channel is full and the
	// message is a heartbeat or status_report, we store it here so it can be
	// flushed immediately after reconnecting.
	pendingMu           sync.Mutex
	pendingHeartbeat    []byte
	pendingStatusReport []byte

	// Backoff state
	backoffStep       int
	disconnectedAt    time.Time
	warnedUnreachable bool
}

// NewClient creates a new WebSocket client.
func NewClient(wsEndpoint, agentID, wsToken string, handler MessageHandler) *Client {
	return &Client{
		wsEndpoint: wsEndpoint,
		agentID:    agentID,
		wsToken:    wsToken,
		handler:    handler,
		sendCh:     make(chan []byte, 64),
	}
}

// SetConnectionCallback registers a callback for connection state changes.
func (c *Client) SetConnectionCallback(cb ConnectionCallback) {
	c.onConnect = cb
}

// Run connects to the dashboard and maintains the connection with reconnection logic.
// It blocks until the context is cancelled.
func (c *Client) Run(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		err := c.connectAndServe(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			slog.Warn("websocket connection error", "error", err)
		}

		if !c.reconnectBackoff(ctx) {
			return ctx.Err()
		}
	}
}

// Send sends a message to the dashboard. If the send buffer is full and the
// message is a heartbeat or status_report, it is stored as a pending message
// so it can be flushed on the next successful reconnect.
func (c *Client) Send(msg *Message) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshaling message: %w", err)
	}

	select {
	case c.sendCh <- data:
		return nil
	default:
		// Channel full. Buffer heartbeat/status_report for reconnect flush.
		if msg.Type == TypeHeartbeat || msg.Type == TypeStatusReport {
			c.pendingMu.Lock()
			if msg.Type == TypeHeartbeat {
				c.pendingHeartbeat = data
			} else {
				c.pendingStatusReport = data
			}
			c.pendingMu.Unlock()
			slog.Debug("buffered message for reconnect", "type", msg.Type)
			return nil
		}
		return fmt.Errorf("send buffer full, dropping message type=%s", msg.Type)
	}
}

// SendDirect writes a message directly to the WebSocket connection, bypassing
// the send channel. This is used during shutdown when the write loop may have
// already exited. Returns an error if no connection is available.
func (c *Client) SendDirect(msg *Message) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshaling message: %w", err)
	}

	c.connMu.Lock()
	conn := c.conn
	c.connMu.Unlock()

	if conn == nil {
		return fmt.Errorf("no active connection")
	}

	return conn.WriteMessage(websocket.TextMessage, data)
}

func (c *Client) connectAndServe(ctx context.Context) error {
	url := fmt.Sprintf("%s?agentId=%s&protocolVersion=%d", c.wsEndpoint, c.agentID, ProtocolVersion)
	slog.Info("connecting to dashboard", "url", url)

	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
	}

	headers := http.Header{}
	if c.wsToken != "" {
		headers.Set("Authorization", "Bearer "+c.wsToken)
	}

	conn, _, err := dialer.DialContext(ctx, url, headers)
	if err != nil {
		return fmt.Errorf("dialing %s: %w", url, err)
	}

	// Limit incoming message size to 16 MB to prevent DoS via oversized messages.
	conn.SetReadLimit(16 * 1024 * 1024)

	c.connMu.Lock()
	c.conn = conn
	c.connMu.Unlock()

	defer func() {
		conn.Close()
		c.connMu.Lock()
		c.conn = nil
		c.connMu.Unlock()
		if c.onConnect != nil {
			c.onConnect(false)
		}
	}()

	slog.Info("connected to dashboard")

	c.resetBackoff()

	if c.onConnect != nil {
		c.onConnect(true)
	}

	// Flush any buffered heartbeat/status_report from while disconnected.
	c.flushPending()

	closeCh := make(chan struct{})
	errCh := make(chan error, 2)

	go func() {
		errCh <- c.readLoop(ctx, conn)
	}()

	go func() {
		errCh <- c.writeLoop(ctx, conn, closeCh)
	}()

	var firstErr error
	select {
	case firstErr = <-errCh:
		// One goroutine errored. Close the connection to unblock the other.
		conn.Close()
	case <-ctx.Done():
		close(closeCh)
		conn.Close()
		firstErr = ctx.Err()
	}

	// Wait for the second goroutine to exit to prevent leaks.
	<-errCh
	return firstErr
}

// checkRateLimit returns true if the message should be allowed, false if rate limited.
func (c *Client) checkRateLimit() bool {
	c.rateMu.Lock()
	defer c.rateMu.Unlock()

	now := time.Now()
	cutoff := now.Add(-rateLimitWindow)

	// Remove timestamps outside the window.
	valid := 0
	for _, ts := range c.rateTimestamps {
		if ts.After(cutoff) {
			c.rateTimestamps[valid] = ts
			valid++
		}
	}
	c.rateTimestamps = c.rateTimestamps[:valid]

	if len(c.rateTimestamps) >= maxCommandsPerMinute {
		return false
	}

	c.rateTimestamps = append(c.rateTimestamps, now)
	return true
}

func (c *Client) readLoop(ctx context.Context, conn *websocket.Conn) error {
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		_, data, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("reading message: %w", err)
		}

		var msg Message
		if err := json.Unmarshal(data, &msg); err != nil {
			slog.Warn("invalid message from dashboard", "error", err)
			continue
		}

		// Rate limit incoming command messages.
		if msg.Type == TypeCommandRequest {
			if !c.checkRateLimit() {
				slog.Warn("rate limit exceeded, dropping command", "type", msg.Type)
				continue
			}
		}

		c.handler(&msg)
	}
}

func (c *Client) writeLoop(ctx context.Context, conn *websocket.Conn, closeCh <-chan struct{}) error {
	for {
		select {
		case <-closeCh:
			conn.WriteMessage(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, "agent shutting down"))
			return ctx.Err()
		case <-ctx.Done():
			return ctx.Err()
		case data := <-c.sendCh:
			if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
				return fmt.Errorf("writing message: %w", err)
			}
		}
	}
}

// flushPending drains any buffered heartbeat or status_report into the send
// channel so they are delivered immediately after reconnecting.
func (c *Client) flushPending() {
	c.pendingMu.Lock()
	hb := c.pendingHeartbeat
	sr := c.pendingStatusReport
	c.pendingHeartbeat = nil
	c.pendingStatusReport = nil
	c.pendingMu.Unlock()

	if hb != nil {
		select {
		case c.sendCh <- hb:
			slog.Debug("flushed pending heartbeat on reconnect")
		default:
			slog.Warn("could not flush pending heartbeat, send buffer full")
		}
	}
	if sr != nil {
		select {
		case c.sendCh <- sr:
			slog.Debug("flushed pending status_report on reconnect")
		default:
			slog.Warn("could not flush pending status_report, send buffer full")
		}
	}
}

var backoffDelays = []time.Duration{
	1 * time.Second,
	2 * time.Second,
	4 * time.Second,
	8 * time.Second,
	16 * time.Second,
	30 * time.Second,
}

func (c *Client) resetBackoff() {
	c.backoffStep = 0
	c.disconnectedAt = time.Time{}
	c.warnedUnreachable = false
}

func (c *Client) reconnectBackoff(ctx context.Context) bool {
	if c.disconnectedAt.IsZero() {
		c.disconnectedAt = time.Now()
	}

	if !c.warnedUnreachable && time.Since(c.disconnectedAt) >= 5*time.Minute {
		slog.Error("unable to reach dashboard for 5 minutes, continuing retry at 30s intervals")
		c.warnedUnreachable = true
	}

	delay := backoffDelays[c.backoffStep]
	if c.backoffStep < len(backoffDelays)-1 {
		c.backoffStep++
	}

	slog.Info("reconnecting", "delay", delay.String())

	select {
	case <-time.After(delay):
		return true
	case <-ctx.Done():
		return false
	}
}
