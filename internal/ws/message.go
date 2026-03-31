package ws

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Message types sent by the agent.
const (
	TypeHeartbeat       = "heartbeat"
	TypeStatusReport    = "status_report"
	TypeCommandResult   = "command_result"
	TypeCommandStream   = "command_stream"
	TypeCommandComplete = "command_complete"
	TypePong            = "pong"
	TypeError           = "error"
)

// Message types received from the dashboard.
const (
	TypeCommandRequest  = "command_request"
	TypeUpdateAvailable = "update_available"
	TypePing            = "ping"
)

// Message is the envelope for all WebSocket messages.
type Message struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Timestamp string          `json:"timestamp"`
	Payload   json.RawMessage `json:"payload"`
}

// HeartbeatPayload is sent every 30 seconds.
type HeartbeatPayload struct {
	Uptime       int64     `json:"uptime"`
	LoadAvg      []float64 `json:"loadAvg"`
	MemoryUsedMb int64     `json:"memoryUsedMb"`
	MemoryTotalMb int64    `json:"memoryTotalMb"`
	DiskUsedGb   float64   `json:"diskUsedGb"`
	DiskTotalGb  float64   `json:"diskTotalGb"`
	AgentVersion string    `json:"agentVersion"`
}

// ContainerInfo describes a running container.
type ContainerInfo struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	Image         string  `json:"image"`
	State         string  `json:"state"`
	Health        string  `json:"health"`
	CPUPercent    float64 `json:"cpuPercent"`
	MemoryUsageMb float64 `json:"memoryUsageMb"`
	MemoryLimitMb float64 `json:"memoryLimitMb"`
	Uptime        int64   `json:"uptime"`
}

// BackupInfo describes backup status.
type BackupInfo struct {
	LastSnapshot  string  `json:"lastSnapshot"`
	SnapshotCount int     `json:"snapshotCount"`
	TotalSizeGb   float64 `json:"totalSizeGb"`
	LastStatus    string  `json:"lastStatus"`
}

// WireGuardInfo describes the WireGuard tunnel status.
type WireGuardInfo struct {
	Interface       string `json:"interface"`
	PublicKey       string `json:"publicKey"`
	LatestHandshake string `json:"latestHandshake"`
	TransferRx      int64  `json:"transferRx"`
	TransferTx      int64  `json:"transferTx"`
}

// StatusReportPayload is the full status snapshot sent every 60 seconds.
type StatusReportPayload struct {
	Containers []ContainerInfo `json:"containers"`
	Backups    BackupInfo      `json:"backups"`
	WireGuard  WireGuardInfo   `json:"wireguard"`
}

// CommandRequestPayload is received from the dashboard.
type CommandRequestPayload struct {
	Action  string          `json:"action"`
	Params  json.RawMessage `json:"params"`
	Stream  bool            `json:"stream"`
	Timeout int             `json:"timeout"`
}

// CommandResultPayload is the response to a non-streaming command.
type CommandResultPayload struct {
	RequestID  string `json:"requestId"`
	ExitCode   int    `json:"exitCode"`
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	DurationMs int64  `json:"durationMs"`
}

// CommandStreamPayload streams a single line of output.
type CommandStreamPayload struct {
	RequestID string `json:"requestId"`
	Stream    string `json:"stream"`
	Line      string `json:"line"`
}

// CommandCompletePayload signals a streamed command has finished.
type CommandCompletePayload struct {
	RequestID  string `json:"requestId"`
	ExitCode   int    `json:"exitCode"`
	DurationMs int64  `json:"durationMs"`
}

// UpdateAvailablePayload is received when a new agent version is available.
type UpdateAvailablePayload struct {
	Version     string `json:"version"`
	DownloadURL string `json:"downloadUrl"`
	Checksum    string `json:"checksum"`
}

// PingPayload is empty.
type PingPayload struct{}

// PongPayload responds to a ping.
type PongPayload struct {
	PingID string `json:"pingId"`
}

// ErrorPayload is sent when the agent encounters an error processing a command.
type ErrorPayload struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"requestId,omitempty"`
}

// NewMessage creates a new message with a generated ID and current timestamp.
func NewMessage(msgType string, payload interface{}) (*Message, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshaling payload: %w", err)
	}

	return &Message{
		Type:      msgType,
		ID:        newMessageID(msgType),
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Payload:   data,
	}, nil
}

func newMessageID(prefix string) string {
	return fmt.Sprintf("%s_%s", prefix, uuid.New().String()[:8])
}
