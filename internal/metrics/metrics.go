package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// HeartbeatsSent counts the total number of heartbeats sent.
	HeartbeatsSent = promauto.NewCounter(prometheus.CounterOpts{
		Name: "keni_agent_heartbeats_total",
		Help: "Total number of heartbeats sent to the dashboard",
	})

	// StatusReportsSent counts the total number of status reports sent.
	StatusReportsSent = promauto.NewCounter(prometheus.CounterOpts{
		Name: "keni_agent_status_reports_total",
		Help: "Total number of status reports sent to the dashboard",
	})

	// CommandsExecuted counts the total number of commands executed.
	CommandsExecuted = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "keni_agent_commands_total",
		Help: "Total number of commands executed",
	}, []string{"action", "status"})

	// CommandDuration tracks command execution duration in milliseconds.
	CommandDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "keni_agent_command_duration_ms",
		Help:    "Command execution duration in milliseconds",
		Buckets: []float64{10, 50, 100, 500, 1000, 5000, 10000, 30000},
	}, []string{"action"})

	// WebSocketReconnections counts connection attempts.
	WebSocketReconnections = promauto.NewCounter(prometheus.CounterOpts{
		Name: "keni_agent_websocket_reconnections_total",
		Help: "Total number of WebSocket reconnection attempts",
	})

	// WebSocketConnected is 1 when connected, 0 when disconnected.
	WebSocketConnected = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "keni_agent_websocket_connected",
		Help: "Whether the agent is connected to the dashboard (1=yes, 0=no)",
	})

	// LastHeartbeatTimestamp records the unix timestamp of the last heartbeat.
	LastHeartbeatTimestamp = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "keni_agent_last_heartbeat_timestamp",
		Help: "Unix timestamp of the last heartbeat sent",
	})

	// AgentInfo exposes static agent metadata as labels.
	AgentInfo = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "keni_agent_info",
		Help: "Agent metadata",
	}, []string{"version", "agent_id"})
)
