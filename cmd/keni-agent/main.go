package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/kenitech-io/devops-agent/internal/collector"
	"github.com/kenitech-io/devops-agent/internal/commands"
	"github.com/kenitech-io/devops-agent/internal/config"
	"github.com/kenitech-io/devops-agent/internal/logging"
	"github.com/kenitech-io/devops-agent/internal/metrics"
	"github.com/kenitech-io/devops-agent/internal/register"
	"github.com/kenitech-io/devops-agent/internal/signing"
	"github.com/kenitech-io/devops-agent/internal/update"
	"github.com/kenitech-io/devops-agent/internal/wireguard"
	"github.com/kenitech-io/devops-agent/internal/ws"
)

var version = "dev"

// checkUpdateRecovery detects incomplete self-updates by looking for a .prev
// binary alongside an update-in-progress marker file. If both exist, the
// previous update crashed before completing: restore the old binary and re-exec.
func checkUpdateRecovery() {
	currentPath, err := os.Executable()
	if err != nil {
		slog.Warn("cannot determine executable path for update recovery", "error", err)
		return
	}

	prevPath := currentPath + ".prev"
	markerPath := update.UpdateMarkerPath

	// Both files must exist to trigger recovery
	if _, err := os.Stat(prevPath); os.IsNotExist(err) {
		return
	}
	if _, err := os.Stat(markerPath); os.IsNotExist(err) {
		return
	}

	slog.Warn("detected incomplete update, rolling back to previous binary",
		"current", currentPath, "prev", prevPath)

	// Restore the previous binary over the current one
	if err := os.Rename(prevPath, currentPath); err != nil {
		slog.Error("rollback rename failed", "error", err)
		return
	}

	// Remove the marker so the restored binary does not loop
	os.Remove(markerPath)

	slog.Info("rollback complete, re-executing restored binary")

	// Re-exec the restored binary with the same arguments
	if err := syscall.Exec(currentPath, os.Args, os.Environ()); err != nil {
		slog.Error("re-exec after rollback failed", "error", err)
		os.Exit(1)
	}
}

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "-v") {
		fmt.Printf("keni-agent %s\n", version)
		os.Exit(0)
	}

	logging.Init()
	slog.Info("keni-agent starting", "version", version)

	checkUpdateRecovery()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle shutdown signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		slog.Info("received signal, shutting down", "signal", sig.String())
		cancel()
	}()

	// Load existing config or register
	cfg, err := config.Load()
	if err != nil {
		slog.Info("no existing config found, starting registration", "reason", err.Error())
		cfg, err = runRegistration()
		if err != nil {
			slog.Error("registration failed", "error", err)
			os.Exit(1)
		}
		slog.Info("registration successful", "agent_id", cfg.AgentID, "assigned_ip", cfg.AssignedIP)
		cleanupToken()
	} else {
		slog.Info("loaded existing config", "agent_id", cfg.AgentID)
	}

	// Validate config
	if err := cfg.Validate(); err != nil {
		slog.Error("invalid config", "error", err)
		os.Exit(1)
	}

	// Start metrics and health check server
	metrics.Init(version, cfg.AgentID)
	metrics.StartServer("127.0.0.1:9100")

	// Start WireGuard watchdog
	wireguard.StartWatchdog(ctx)

	// Start the agent loop
	runAgent(ctx, cfg)

	slog.Info("keni-agent stopped")
}

// runAgent connects to the dashboard via WebSocket and runs heartbeat/status/command loops.
func runAgent(ctx context.Context, cfg *config.Config) {
	var cmdMu sync.Mutex
	var cmdWg sync.WaitGroup

	var client *ws.Client
	handler := func(msg *ws.Message) {
		handleDashboardMessage(ctx, client, cfg, msg, &cmdMu, &cmdWg)
	}
	client = ws.NewClient(cfg.WSEndpoint, cfg.AgentID, cfg.WSToken, handler)
	client.SetConnectionCallback(func(connected bool) {
		metrics.SetWSConnected(connected)
		if connected {
			sendConfigBackup(client, cfg)
		} else {
			metrics.WebSocketReconnections.Inc()
		}
	})

	// Start heartbeat ticker (30s)
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		sendHeartbeat(client)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				sendHeartbeat(client)
			}
		}
	}()

	// Start status report ticker (60s)
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		time.Sleep(5 * time.Second)
		sendStatusReport(client)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				sendStatusReport(client)
			}
		}
	}()

	// Run WebSocket client (blocks until context cancelled)
	if err := client.Run(ctx); err != nil && ctx.Err() == nil {
		slog.Error("websocket client exited", "error", err)
	}

	// Wait for in-flight commands to finish
	slog.Info("waiting for in-flight commands to complete")
	done := make(chan struct{})
	go func() {
		cmdWg.Wait()
		close(done)
	}()
	select {
	case <-done:
		slog.Info("all commands completed")
	case <-time.After(30 * time.Second):
		slog.Warn("timed out waiting for commands, forcing shutdown")
	}
}

func sendHeartbeat(client *ws.Client) {
	hb, err := collector.CollectHeartbeat(version)
	if err != nil {
		slog.Error("collecting heartbeat", "error", err)
		return
	}

	msg, err := ws.NewMessage(ws.TypeHeartbeat, hb)
	if err != nil {
		slog.Error("creating heartbeat message", "error", err)
		return
	}

	if err := client.Send(msg); err != nil {
		slog.Warn("sending heartbeat", "error", err)
	} else {
		metrics.RecordHeartbeat()
	}
}

func sendStatusReport(client *ws.Client) {
	report := collector.CollectStatusReport()

	msg, err := ws.NewMessage(ws.TypeStatusReport, report)
	if err != nil {
		slog.Error("creating status report", "error", err)
		return
	}

	if err := client.Send(msg); err != nil {
		slog.Warn("sending status report", "error", err)
	} else {
		metrics.StatusReportsSent.Inc()
	}
}

func sendConfigBackup(client *ws.Client, cfg *config.Config) {
	payload := ws.ConfigBackupPayload{
		AgentID:       cfg.AgentID,
		AssignedIP:    cfg.AssignedIP,
		WSEndpoint:    cfg.WSEndpoint,
		DashboardURL:  cfg.DashboardURL,
		AgentVersion:  version,
		ConfigVersion: cfg.ConfigVersion,
	}

	msg, err := ws.NewMessage(ws.TypeConfigBackup, payload)
	if err != nil {
		slog.Error("creating config_backup message", "error", err)
		return
	}

	if err := client.Send(msg); err != nil {
		slog.Warn("sending config_backup", "error", err)
	} else {
		slog.Info("sent config_backup to dashboard")
	}
}

func handleConfigUpdate(cfg *config.Config, msg *ws.Message) {
	var payload ws.ConfigUpdatePayload
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		slog.Error("parsing config_update", "error", err)
		return
	}

	changed := cfg.ApplyPartialUpdate(payload.WSEndpoint, payload.WSToken, payload.DashboardURL)
	if !changed {
		slog.Info("config_update received but no fields changed")
		return
	}

	if err := cfg.Save(); err != nil {
		slog.Error("saving updated config", "error", err)
		return
	}

	slog.Info("config updated from dashboard",
		"ws_endpoint_changed", payload.WSEndpoint != "",
		"ws_token_changed", payload.WSToken != "",
		"dashboard_url_changed", payload.DashboardURL != "",
	)

	if payload.RestartAfter {
		slog.Info("restart requested, scheduling restart in 2s")
		go func() {
			time.Sleep(2 * time.Second)
			slog.Info("restarting agent")
			os.Exit(0)
		}()
	}
}

func handleDashboardMessage(ctx context.Context, client *ws.Client, cfg *config.Config, msg *ws.Message, cmdMu *sync.Mutex, cmdWg *sync.WaitGroup) {
	switch msg.Type {
	case ws.TypePing:
		handlePing(client, msg)
	case ws.TypeCommandRequest:
		cmdWg.Add(1)
		go func() {
			defer cmdWg.Done()
			handleCommandRequest(ctx, client, msg, cmdMu)
		}()
	case ws.TypeConfigUpdate:
		handleConfigUpdate(cfg, msg)
	case ws.TypeUpdateAvailable:
		go handleUpdateAvailable(msg)
	default:
		slog.Warn("unknown message type from dashboard", "type", msg.Type)
	}
}

func handlePing(client *ws.Client, msg *ws.Message) {
	pong, err := ws.NewMessage(ws.TypePong, ws.PongPayload{PingID: msg.ID})
	if err != nil {
		slog.Error("creating pong", "error", err)
		return
	}
	if err := client.Send(pong); err != nil {
		slog.Warn("sending pong", "error", err)
	}
}

func handleCommandRequest(ctx context.Context, client *ws.Client, msg *ws.Message, cmdMu *sync.Mutex) {
	var req ws.CommandRequestPayload
	if err := json.Unmarshal(msg.Payload, &req); err != nil {
		sendError(client, "INVALID_PARAMS", "invalid command request payload", msg.ID)
		return
	}

	slog.Info("executing command", "action", req.Action, "request_id", msg.ID)

	if !cmdMu.TryLock() {
		slog.Warn("agent busy, rejecting command", "action", req.Action)
		sendError(client, "BUSY", "agent is already executing a command", msg.ID)
		return
	}
	defer cmdMu.Unlock()

	timeout := time.Duration(req.Timeout) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	cmdCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()

	if req.Stream || commands.IsStreamingAction(req.Action) {
		result, err := commands.ExecuteStream(cmdCtx, req.Action, req.Params, func(line commands.StreamLine) {
			streamMsg, _ := ws.NewMessage(ws.TypeCommandStream, ws.CommandStreamPayload{
				RequestID: msg.ID,
				Stream:    line.Stream,
				Line:      line.Line,
			})
			client.Send(streamMsg)
		})
		if err != nil {
			metrics.CommandsExecuted.WithLabelValues(req.Action, "error").Inc()
			slog.Error("command failed", "action", req.Action, "error", err)
			sendError(client, extractErrorCode(err), err.Error(), msg.ID)
			return
		}

		metrics.CommandsExecuted.WithLabelValues(req.Action, "success").Inc()
		metrics.CommandDuration.WithLabelValues(req.Action).Observe(float64(result.DurationMs))
		slog.Info("command completed", "action", req.Action, "exit_code", result.ExitCode, "duration_ms", result.DurationMs)

		completeMsg, _ := ws.NewMessage(ws.TypeCommandComplete, ws.CommandCompletePayload{
			RequestID:  msg.ID,
			ExitCode:   result.ExitCode,
			DurationMs: result.DurationMs,
		})
		client.Send(completeMsg)
	} else {
		result, err := commands.Execute(cmdCtx, req.Action, req.Params)
		if err != nil {
			metrics.CommandsExecuted.WithLabelValues(req.Action, "error").Inc()
			slog.Error("command failed", "action", req.Action, "error", err)
			sendError(client, extractErrorCode(err), err.Error(), msg.ID)
			return
		}

		metrics.CommandsExecuted.WithLabelValues(req.Action, "success").Inc()
		metrics.CommandDuration.WithLabelValues(req.Action).Observe(float64(result.DurationMs))
		slog.Info("command completed", "action", req.Action, "exit_code", result.ExitCode, "duration_ms", result.DurationMs)

		resultMsg, _ := ws.NewMessage(ws.TypeCommandResult, ws.CommandResultPayload{
			RequestID:  msg.ID,
			ExitCode:   result.ExitCode,
			Stdout:     result.Stdout,
			Stderr:     result.Stderr,
			DurationMs: result.DurationMs,
		})
		client.Send(resultMsg)
	}
}

func handleUpdateAvailable(msg *ws.Message) {
	var payload ws.UpdateAvailablePayload
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		slog.Error("parsing update_available", "error", err)
		return
	}

	slog.Info("update available", "version", payload.Version)

	// Verify release signature before proceeding
	if err := signing.VerifyChecksum(payload.Checksum, payload.Signature); err != nil {
		slog.Error("release signature verification failed, rejecting update", "error", err, "version", payload.Version)
		return
	}

	if err := update.Update(payload.DownloadURL, payload.Checksum); err != nil {
		slog.Error("self-update failed", "error", err)
	}
}

func sendError(client *ws.Client, code, message, requestID string) {
	errMsg, _ := ws.NewMessage(ws.TypeError, ws.ErrorPayload{
		Code:      code,
		Message:   message,
		RequestID: requestID,
	})
	if err := client.Send(errMsg); err != nil {
		slog.Error("sending error message", "error", err)
	}
}

func extractErrorCode(err error) string {
	msg := err.Error()
	for _, code := range []string{"UNKNOWN_ACTION", "INVALID_PARAMS", "TIMEOUT"} {
		if strings.HasPrefix(msg, code) {
			return code
		}
	}
	return "EXECUTION_FAILED"
}

func cleanupToken() {
	envFile := "/etc/keni-agent/env"
	data, err := os.ReadFile(envFile)
	if err != nil {
		return
	}

	var kept []string
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "KENI_AGENT_TOKEN=") {
			continue
		}
		kept = append(kept, line)
	}

	cleaned := strings.Join(kept, "\n")
	if err := os.WriteFile(envFile, []byte(cleaned), 0600); err != nil {
		slog.Warn("could not clean token from env file", "error", err)
	} else {
		slog.Info("removed registration token from env file")
	}
}

func runRegistration() (*config.Config, error) {
	token := os.Getenv("KENI_AGENT_TOKEN")
	if token == "" {
		return nil, fmt.Errorf("KENI_AGENT_TOKEN environment variable not set")
	}

	dashboardURL := os.Getenv("KENI_DASHBOARD_URL")
	if dashboardURL == "" {
		return nil, fmt.Errorf("KENI_DASHBOARD_URL environment variable not set")
	}

	var privKey, pubKey string
	if os.Getenv("KENI_SKIP_WIREGUARD") == "true" {
		privKey = "dev-skip-wireguard"
		pubKey = "dev-skip-wireguard"
	} else {
		var err error
		privKey, pubKey, err = wireguard.GenerateKeyPair()
		if err != nil {
			return nil, fmt.Errorf("generating WireGuard keypair: %w", err)
		}
	}

	info, err := register.GatherSystemInfo()
	if err != nil {
		return nil, fmt.Errorf("gathering system info: %w", err)
	}

	// Retry registration with backoff
	req := register.Request{
		Token:         token,
		PublicKey:     pubKey,
		Hostname:      info.Hostname,
		OS:            info.OS,
		Arch:          info.Arch,
		DockerVersion: info.DockerVersion,
		KernelVersion: info.KernelVersion,
	}

	var resp *register.Response
	backoff := []time.Duration{1, 2, 4, 8, 16, 30, 60}
	maxAttempts := 10
	for attempt := 0; attempt < maxAttempts; attempt++ {
		resp, err = register.Register(dashboardURL, req)
		if err == nil {
			break
		}
		// Don't retry on 401/409 (invalid/used token)
		if strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "409") {
			return nil, err
		}
		if attempt < maxAttempts-1 {
			delay := backoff[attempt]
			if attempt >= len(backoff) {
				delay = backoff[len(backoff)-1]
			}
			slog.Warn("registration failed, retrying", "attempt", attempt+1, "error", err, "retry_in", fmt.Sprintf("%ds", delay))
			time.Sleep(delay * time.Second)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("registering with dashboard after %d attempts: %w", maxAttempts, err)
	}

	// Skip WireGuard in dev mode (KENI_SKIP_WIREGUARD=true)
	if os.Getenv("KENI_SKIP_WIREGUARD") != "true" {
		wgCfg := wireguard.Config{
			PrivateKey:        privKey,
			AssignedIP:        resp.AssignedIP,
			DashboardPubKey:   resp.DashboardPublicKey,
			DashboardEndpoint: resp.DashboardEndpoint,
		}
		if err := wireguard.ConfigureInterface(wgCfg); err != nil {
			return nil, fmt.Errorf("configuring WireGuard: %w", err)
		}
	} else {
		slog.Info("skipping WireGuard setup (dev mode)")
	}

	// Allow overriding the WebSocket endpoint for dev (e.g., Tailscale IP)
	wsEndpoint := resp.WSEndpoint
	if override := os.Getenv("KENI_WS_ENDPOINT"); override != "" {
		slog.Info("overriding wsEndpoint", "from", wsEndpoint, "to", override)
		wsEndpoint = override
	}

	cfg := &config.Config{
		AgentID:           resp.AgentID,
		AssignedIP:        resp.AssignedIP,
		DashboardEndpoint: resp.DashboardEndpoint,
		WSEndpoint:        wsEndpoint,
		WSToken:           resp.WSToken,
		WireGuardPrivKey:  privKey,
		WireGuardPubKey:   pubKey,
		DashboardPubKey:   resp.DashboardPublicKey,
		DashboardURL:      dashboardURL,
	}
	if err := cfg.Save(); err != nil {
		return nil, fmt.Errorf("saving config: %w", err)
	}

	return cfg, nil
}
