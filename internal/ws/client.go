package ws

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// MessageHandler is called when a message is received from the dashboard.
type MessageHandler func(msg *Message)

// ConnectionCallback is called when the WebSocket connection state changes.
type ConnectionCallback func(connected bool)

// Client manages the WebSocket connection to the dashboard.
type Client struct {
	wsEndpoint string
	agentID    string
	handler    MessageHandler
	onConnect  ConnectionCallback

	conn   *websocket.Conn
	connMu sync.Mutex

	sendCh chan []byte

	// Backoff state
	backoffStep       int
	disconnectedAt    time.Time
	warnedUnreachable bool
}

// NewClient creates a new WebSocket client.
func NewClient(wsEndpoint, agentID string, handler MessageHandler) *Client {
	return &Client{
		wsEndpoint: wsEndpoint,
		agentID:    agentID,
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

// Send sends a message to the dashboard.
func (c *Client) Send(msg *Message) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshaling message: %w", err)
	}

	select {
	case c.sendCh <- data:
		return nil
	default:
		return fmt.Errorf("send buffer full, dropping message type=%s", msg.Type)
	}
}

func (c *Client) connectAndServe(ctx context.Context) error {
	url := fmt.Sprintf("%s?agentId=%s", c.wsEndpoint, c.agentID)
	slog.Info("connecting to dashboard", "url", url)

	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
	}

	conn, _, err := dialer.DialContext(ctx, url, nil)
	if err != nil {
		return fmt.Errorf("dialing %s: %w", url, err)
	}

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

	closeCh := make(chan struct{})
	errCh := make(chan error, 2)

	go func() {
		errCh <- c.readLoop(ctx, conn)
	}()

	go func() {
		errCh <- c.writeLoop(ctx, conn, closeCh)
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		close(closeCh)
		return ctx.Err()
	}
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
