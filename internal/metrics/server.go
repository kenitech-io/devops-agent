package metrics

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// State holds the runtime state exposed via the health endpoint.
type State struct {
	startTime          time.Time
	version            string
	agentID            string
	wsConnected        atomic.Bool
	lastHBTime         atomic.Int64
	lastGoroutines     atomic.Int64
	lastHeapAllocBytes atomic.Int64
}

var state = &State{
	startTime: time.Now(),
}

// Init sets up the metrics state with agent info.
func Init(version, agentID string) {
	state.version = version
	state.agentID = agentID
	AgentInfo.WithLabelValues(version, agentID).Set(1)
}

// SetWSConnected updates the WebSocket connection status.
func SetWSConnected(connected bool) {
	state.wsConnected.Store(connected)
	if connected {
		WebSocketConnected.Set(1)
	} else {
		WebSocketConnected.Set(0)
	}
}

// RecordHeartbeat updates the last heartbeat timestamp.
func RecordHeartbeat() {
	now := time.Now().Unix()
	state.lastHBTime.Store(now)
	LastHeartbeatTimestamp.Set(float64(now))
	HeartbeatsSent.Inc()
}

// healthResponse is the JSON body for GET /healthz.
type healthResponse struct {
	Status        string  `json:"status"`
	Version       string  `json:"version"`
	AgentID       string  `json:"agentId"`
	UptimeSeconds int64   `json:"uptimeSeconds"`
	WSConnected   bool    `json:"wsConnected"`
	LastHeartbeat int64   `json:"lastHeartbeat"`
	Goroutines    int     `json:"goroutines"`
	HeapAllocMb   float64 `json:"heapAllocMb"`
}

// StartServer starts the metrics and health HTTP server on the given address.
// Typically called with "127.0.0.1:9100" to listen only on localhost.
// It also starts a background goroutine that updates runtime metrics every 30 seconds.
func StartServer(addr string) {
	// Collect runtime metrics once immediately, then every 30 seconds.
	UpdateRuntimeMetrics()
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			UpdateRuntimeMetrics()
		}
	}()

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/healthz", handleHealthz)

	slog.Info("metrics server listening", "addr", addr)
	go func() {
		if err := http.ListenAndServe(addr, mux); err != nil {
			slog.Error("metrics server error", "error", err)
		}
	}()
}

func handleHealthz(w http.ResponseWriter, r *http.Request) {
	connected := state.wsConnected.Load()
	lastHB := state.lastHBTime.Load()

	status := "ok"
	statusCode := http.StatusOK
	if !connected {
		status = "degraded"
		statusCode = http.StatusServiceUnavailable
	}

	goroutines := state.lastGoroutines.Load()
	heapBytes := state.lastHeapAllocBytes.Load()

	resp := healthResponse{
		Status:        status,
		Version:       state.version,
		AgentID:       state.agentID,
		UptimeSeconds: int64(time.Since(state.startTime).Seconds()),
		WSConnected:   connected,
		LastHeartbeat: lastHB,
		Goroutines:    int(goroutines),
		HeapAllocMb:   float64(heapBytes) / 1024 / 1024,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(resp)
}
