package metrics

import (
	"log/slog"
	"runtime"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// GoroutineWarningThreshold is the goroutine count above which a warning is logged.
// Can be overridden before calling Init() or UpdateRuntimeMetrics().
var GoroutineWarningThreshold = 100

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

	// Goroutines tracks the current number of goroutines.
	Goroutines = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "keni_agent_goroutines",
		Help: "Current number of goroutines",
	})

	// HeapAllocBytes tracks current heap allocation in bytes.
	HeapAllocBytes = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "keni_agent_heap_alloc_bytes",
		Help: "Current heap allocation in bytes",
	})

	// HeapSysBytes tracks heap system bytes.
	HeapSysBytes = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "keni_agent_heap_sys_bytes",
		Help: "Heap system bytes obtained from the OS",
	})

	// GCPauseNs tracks the last GC pause duration in nanoseconds.
	GCPauseNs = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "keni_agent_gc_pause_ns",
		Help: "Last GC pause duration in nanoseconds",
	})
)

// UpdateRuntimeMetrics reads Go runtime stats and updates the Prometheus gauges.
// If the goroutine count exceeds GoroutineWarningThreshold, a warning is logged.
func UpdateRuntimeMetrics() {
	numGoroutines := runtime.NumGoroutine()
	Goroutines.Set(float64(numGoroutines))

	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	HeapAllocBytes.Set(float64(m.HeapAlloc))
	HeapSysBytes.Set(float64(m.HeapSys))

	// Last GC pause: PauseNs is a circular buffer, NumGC is total count.
	if m.NumGC > 0 {
		lastPause := m.PauseNs[(m.NumGC+255)%256]
		GCPauseNs.Set(float64(lastPause))
	}

	// Store latest values for the health endpoint.
	state.lastGoroutines.Store(int64(numGoroutines))
	state.lastHeapAllocBytes.Store(int64(m.HeapAlloc))

	if numGoroutines > GoroutineWarningThreshold {
		// Log every time it exceeds, but use a separate once-guard for the first occurrence.
		slog.Warn("goroutine count exceeds threshold",
			"count", numGoroutines,
			"threshold", GoroutineWarningThreshold,
		)
	}
}
