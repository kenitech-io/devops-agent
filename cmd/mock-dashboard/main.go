// mock-dashboard is a test server that simulates the Keni Dashboard.
// It implements the registration endpoint and WebSocket protocol so the
// real keni-agent can be tested end-to-end without the full dashboard.
//
// Usage:
//
//	go run ./cmd/mock-dashboard --listen :8080 --wg-pubkey <key> --wg-endpoint 1.2.3.4:51820
//
// Then install the agent pointing at this server:
//
//	KENI_AGENT_TOKEN=keni_testtoken KENI_DASHBOARD_URL=http://localhost:8080 keni-agent
package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	agentWs "github.com/kenitech-io/devops-agent/internal/ws"
)

var (
	listenAddr  = flag.String("listen", ":8080", "address to listen on")
	wgPubKey    = flag.String("wg-pubkey", "mock-dashboard-pubkey-base64", "WireGuard public key to return to agents")
	wgEndpoint  = flag.String("wg-endpoint", "127.0.0.1:51820", "WireGuard endpoint to return to agents")
	nextIP      uint32 = 2
	agents      = make(map[string]*agentConn)
	agentsMu    sync.Mutex
	validTokens = map[string]bool{
		"keni_testtoken": true,
	}
	tokensMu sync.Mutex
)

type agentConn struct {
	ID       string
	Hostname string
	OS       string
	Conn     *websocket.Conn
}

// Registration types matching the protocol spec.
type registerRequest struct {
	Token         string `json:"token"`
	PublicKey     string `json:"publicKey"`
	Hostname      string `json:"hostname"`
	OS            string `json:"os"`
	Arch          string `json:"arch"`
	DockerVersion string `json:"dockerVersion"`
	KernelVersion string `json:"kernelVersion"`
}

type registerResponse struct {
	AgentID            string `json:"agentId"`
	AssignedIP         string `json:"assignedIp"`
	DashboardPublicKey string `json:"dashboardPublicKey"`
	DashboardEndpoint  string `json:"dashboardEndpoint"`
	WSEndpoint         string `json:"wsEndpoint"`
	WSToken            string `json:"wsToken"`
}

var (
	wsTokens   = make(map[string]string) // agentID -> wsToken
	wsTokensMu sync.Mutex
)

type errorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func main() {
	flag.Parse()
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/agent/register", handleRegister)
	mux.HandleFunc("/ws/agent", handleWebSocket)
	mux.HandleFunc("/", handleIndex)

	log.Printf("mock dashboard listening on %s", *listenAddr)
	log.Printf("valid tokens: keni_testtoken")
	log.Printf("use --listen, --wg-pubkey, --wg-endpoint to configure")

	// Start interactive CLI in background
	go startCLI()

	if err := http.ListenAndServe(*listenAddr, mux); err != nil {
		log.Fatalf("listen error: %v", err)
	}
}

func handleIndex(w http.ResponseWriter, r *http.Request) {
	agentsMu.Lock()
	count := len(agents)
	var list []string
	for _, a := range agents {
		list = append(list, fmt.Sprintf("  %s (%s, %s)", a.ID, a.Hostname, a.OS))
	}
	agentsMu.Unlock()

	fmt.Fprintf(w, "Keni Mock Dashboard\n")
	fmt.Fprintf(w, "Connected agents: %d\n", count)
	for _, s := range list {
		fmt.Fprintf(w, "%s\n", s)
	}
}

func handleRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req registerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Code: "INVALID_REQUEST", Message: err.Error()})
		return
	}

	log.Printf("registration request from %s (token=%s...)", req.Hostname, truncate(req.Token, 12))

	// Validate token
	tokensMu.Lock()
	valid, exists := validTokens[req.Token]
	if exists && valid {
		validTokens[req.Token] = false // consume the token
	}
	tokensMu.Unlock()

	if !exists {
		writeJSON(w, http.StatusUnauthorized, errorResponse{Code: "INVALID_TOKEN", Message: "Invalid or expired token"})
		return
	}
	if !valid {
		writeJSON(w, http.StatusConflict, errorResponse{Code: "TOKEN_USED", Message: "Token already used"})
		return
	}

	// Assign IP
	ip := atomic.AddUint32(&nextIP, 1) - 1
	assignedIP := fmt.Sprintf("10.99.0.%d", ip)
	agentID := generateAgentID()
	wsToken := generateWSToken()

	// Store the ws_token for validation on WS connect
	wsTokensMu.Lock()
	wsTokens[agentID] = wsToken
	wsTokensMu.Unlock()

	// Determine the WebSocket URL based on the listening address
	host, port, _ := net.SplitHostPort(*listenAddr)
	if host == "" || host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	wsEndpoint := fmt.Sprintf("ws://%s:%s/ws/agent", host, port)

	resp := registerResponse{
		AgentID:            agentID,
		AssignedIP:         assignedIP,
		DashboardPublicKey: *wgPubKey,
		DashboardEndpoint:  *wgEndpoint,
		WSEndpoint:         wsEndpoint,
		WSToken:            wsToken,
	}

	log.Printf("registered agent %s: hostname=%s, ip=%s, pubkey=%s...",
		agentID, req.Hostname, assignedIP, truncate(req.PublicKey, 12))

	writeJSON(w, http.StatusOK, resp)
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

func handleWebSocket(w http.ResponseWriter, r *http.Request) {
	agentID := r.URL.Query().Get("agentId")
	if agentID == "" {
		http.Error(w, "missing agentId", http.StatusBadRequest)
		return
	}

	// Validate Bearer token
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		http.Error(w, "missing Authorization header", http.StatusUnauthorized)
		return
	}
	token := strings.TrimPrefix(authHeader, "Bearer ")
	if token == authHeader {
		http.Error(w, "invalid Authorization format, expected Bearer token", http.StatusUnauthorized)
		return
	}

	wsTokensMu.Lock()
	expectedToken, exists := wsTokens[agentID]
	wsTokensMu.Unlock()
	if !exists || token != expectedToken {
		http.Error(w, "invalid ws_token", http.StatusUnauthorized)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("websocket upgrade error: %v", err)
		return
	}

	agent := &agentConn{ID: agentID, Conn: conn}
	agentsMu.Lock()
	agents[agentID] = agent
	agentsMu.Unlock()

	log.Printf("agent %s connected via WebSocket", agentID)

	defer func() {
		conn.Close()
		agentsMu.Lock()
		delete(agents, agentID)
		agentsMu.Unlock()
		log.Printf("agent %s disconnected", agentID)
	}()

	// Send a ping every 30 seconds
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			ping, _ := agentWs.NewMessage(agentWs.TypePing, agentWs.PingPayload{})
			data, _ := json.Marshal(ping)
			if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
				return
			}
		}
	}()

	// Read messages from agent
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				log.Printf("agent %s read error: %v", agentID, err)
			}
			return
		}

		var msg agentWs.Message
		if err := json.Unmarshal(data, &msg); err != nil {
			log.Printf("agent %s: invalid message: %v", agentID, err)
			continue
		}

		handleAgentMessage(agentID, conn, &msg)
	}
}

func handleAgentMessage(agentID string, conn *websocket.Conn, msg *agentWs.Message) {
	switch msg.Type {
	case agentWs.TypeHeartbeat:
		var hb agentWs.HeartbeatPayload
		json.Unmarshal(msg.Payload, &hb)
		log.Printf("agent %s heartbeat: uptime=%ds, load=%.1f/%.1f/%.1f, mem=%d/%dMB, disk=%.1f/%.1fGB, version=%s",
			agentID, hb.Uptime, hb.LoadAvg[0], hb.LoadAvg[1], hb.LoadAvg[2],
			hb.MemoryUsedMb, hb.MemoryTotalMb, hb.DiskUsedGb, hb.DiskTotalGb, hb.AgentVersion)

		// Update agent info
		agentsMu.Lock()
		if a, ok := agents[agentID]; ok {
			a.OS = hb.AgentVersion
		}
		agentsMu.Unlock()

	case agentWs.TypeStatusReport:
		var sr agentWs.StatusReportPayload
		json.Unmarshal(msg.Payload, &sr)
		log.Printf("agent %s status: containers=%d, backup_status=%s, wg_interface=%s",
			agentID, len(sr.Containers), sr.Backups.LastStatus, sr.WireGuard.Interface)
		for _, c := range sr.Containers {
			log.Printf("  container: %s (%s) state=%s health=%s cpu=%.1f%% mem=%.0fMB",
				c.Name, c.Image, c.State, c.Health, c.CPUPercent, c.MemoryUsageMb)
		}

	case agentWs.TypeCommandResult:
		var cr agentWs.CommandResultPayload
		json.Unmarshal(msg.Payload, &cr)
		log.Printf("agent %s command result: request=%s exit=%d duration=%dms stdout_len=%d",
			agentID, cr.RequestID, cr.ExitCode, cr.DurationMs, len(cr.Stdout))
		if cr.Stdout != "" {
			// Print first 200 chars of stdout
			out := cr.Stdout
			if len(out) > 200 {
				out = out[:200] + "..."
			}
			log.Printf("  stdout: %s", out)
		}
		if cr.Stderr != "" {
			log.Printf("  stderr: %s", cr.Stderr)
		}

	case agentWs.TypeCommandStream:
		var cs agentWs.CommandStreamPayload
		json.Unmarshal(msg.Payload, &cs)
		log.Printf("agent %s stream [%s]: %s", agentID, cs.Stream, cs.Line)

	case agentWs.TypeCommandComplete:
		var cc agentWs.CommandCompletePayload
		json.Unmarshal(msg.Payload, &cc)
		log.Printf("agent %s command complete: request=%s exit=%d duration=%dms",
			agentID, cc.RequestID, cc.ExitCode, cc.DurationMs)

	case agentWs.TypeConfigBackup:
		var cb agentWs.ConfigBackupPayload
		json.Unmarshal(msg.Payload, &cb)
		log.Printf("agent %s config_backup: assigned_ip=%s, ws_endpoint=%s, dashboard_url=%s, agent_version=%s, config_version=%d",
			agentID, cb.AssignedIP, cb.WSEndpoint, cb.DashboardURL, cb.AgentVersion, cb.ConfigVersion)

	case agentWs.TypePong:
		var pong agentWs.PongPayload
		json.Unmarshal(msg.Payload, &pong)
		log.Printf("agent %s pong (ping=%s)", agentID, pong.PingID)

	case agentWs.TypeError:
		var errPayload agentWs.ErrorPayload
		json.Unmarshal(msg.Payload, &errPayload)
		log.Printf("agent %s error: code=%s message=%s request=%s",
			agentID, errPayload.Code, errPayload.Message, errPayload.RequestID)

	default:
		log.Printf("agent %s: unknown message type %s", agentID, msg.Type)
	}
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func generateAgentID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return "ag_" + hex.EncodeToString(b)
}

func generateWSToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	return "wst_" + hex.EncodeToString(b)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
