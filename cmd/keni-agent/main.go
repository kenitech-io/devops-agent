package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/kenitech-io/devops-agent/internal/collector"
	"github.com/kenitech-io/devops-agent/internal/commands"
	"github.com/kenitech-io/devops-agent/internal/config"
	"github.com/kenitech-io/devops-agent/internal/gitops"
	"github.com/kenitech-io/devops-agent/internal/logging"
	"github.com/kenitech-io/devops-agent/internal/metrics"
	"github.com/kenitech-io/devops-agent/internal/register"
	"github.com/kenitech-io/devops-agent/internal/secrets"
	"github.com/kenitech-io/devops-agent/internal/signing"
	"github.com/kenitech-io/devops-agent/internal/update"
	"github.com/kenitech-io/devops-agent/internal/wireguard"
	"github.com/kenitech-io/devops-agent/internal/ws"
)

var version = "dev"

// checkUpdateRecovery handles leftover files from self-updates.
//
// Marker + .prev = update crashed during binary replacement: rollback.
// .prev only (no marker) = update completed successfully, binary was replaced
// and restart happened: just clean up the stale backup.
func checkUpdateRecovery() {
	currentPath, err := os.Executable()
	if err != nil {
		slog.Warn("cannot determine executable path for update recovery", "error", err)
		return
	}

	prevPath := currentPath + ".prev"
	markerPath := update.UpdateMarkerPath

	hasPrev := false
	if _, err := os.Stat(prevPath); err == nil {
		hasPrev = true
	}
	hasMarker := false
	if _, err := os.Stat(markerPath); err == nil {
		hasMarker = true
	}

	if !hasPrev && !hasMarker {
		return
	}

	if hasMarker && hasPrev {
		// Incomplete update: crashed during binary replacement. Rollback.
		slog.Warn("detected incomplete update, rolling back to previous binary",
			"current", currentPath, "prev", prevPath)

		if err := os.Rename(prevPath, currentPath); err != nil {
			slog.Error("rollback rename failed", "error", err)
			return
		}
		os.Remove(markerPath)

		slog.Info("rollback complete, re-executing restored binary")
		if err := syscall.Exec(currentPath, os.Args, os.Environ()); err != nil {
			slog.Error("re-exec after rollback failed", "error", err)
			os.Exit(1)
		}
		return
	}

	// Clean up stale files from a completed update
	if hasPrev {
		slog.Info("cleaning up stale backup from completed update", "prev", prevPath)
		os.Remove(prevPath)
	}
	if hasMarker {
		slog.Info("cleaning up stale marker file", "marker", markerPath)
		os.Remove(markerPath)
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

	// Handle shutdown signals. The wsClient pointer is set later by runAgent;
	// we capture it here so the signal handler can send a goodbye message.
	var wsClient *ws.Client
	var wsClientMu sync.Mutex

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		slog.Info("received signal, sending goodbye", "signal", sig.String())
		wsClientMu.Lock()
		c := wsClient
		wsClientMu.Unlock()
		if c != nil {
			sendGoodbye(c, "shutdown")
			time.Sleep(1 * time.Second)
		}
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

	// Start the agent loop. Pass the wsClient pointer so the signal handler can use it.
	runAgent(ctx, cfg, &wsClient, &wsClientMu)

	slog.Info("keni-agent stopped")
}

// runAgent connects to the dashboard via WebSocket and runs heartbeat/status/command loops.
func runAgent(ctx context.Context, cfg *config.Config, wsClientPtr **ws.Client, wsClientMu *sync.Mutex) {
	var cmdMu sync.Mutex
	var cmdWg sync.WaitGroup

	// GitOps operator: may be started at boot or later via config_update.
	var gitopsOp *gitops.Operator
	var gitopsMu sync.Mutex // protects gitopsOp pointer

	// startGitOpsOperator creates and starts the operator if repo URL and role are available.
	// Safe to call multiple times: no-op if already running.
	startGitOpsOperator := func(client *ws.Client) {
		gitopsMu.Lock()
		defer gitopsMu.Unlock()

		if gitopsOp != nil {
			return // already running
		}

		repoURL := cfg.GetRepoURL()
		serverRole := cfg.GetServerRole()

		// Try fetching repo URL from dashboard if not in config
		if repoURL == "" && cfg.DashboardURL != "" && cfg.AgentID != "" && cfg.WSToken != "" {
			slog.Info("no repo URL in config, fetching from dashboard")
			gitTokenResp, err := secrets.FetchGitToken(cfg.DashboardURL, cfg.AgentID, cfg.WSToken)
			if err != nil {
				slog.Warn("could not fetch git config from dashboard", "error", err)
			} else if gitTokenResp.RepoURL != "" {
				repoURL = gitTokenResp.RepoURL
				cfg.RepoURL = repoURL
				if saveErr := cfg.Save(); saveErr != nil {
					slog.Warn("could not save repo URL to config", "error", saveErr)
				}
				slog.Info("fetched repo URL from dashboard", "url", repoURL)
			}
		}

		if repoURL == "" || serverRole == "" {
			slog.Info("gitops operator not started (missing repo URL or server role)",
				"has_repo", repoURL != "", "has_role", serverRole != "")
			return
		}

		repo, err := gitops.NewRepo(repoURL, cfg.GetDeployToken(), config.GitOpsDataDir)
		if err != nil {
			slog.Error("invalid gitops repo config", "error", err)
			return
		}

		// Set token func to fetch fresh installation tokens from dashboard
		if cfg.DashboardURL != "" && cfg.AgentID != "" && cfg.WSToken != "" {
			repo.SetTokenFunc(func() (string, error) {
				resp, err := secrets.FetchGitToken(cfg.DashboardURL, cfg.AgentID, cfg.WSToken)
				if err != nil {
					return "", err
				}
				return resp.Token, nil
			})
		}

		gitopsOp = gitops.NewOperator(repo, serverRole)
		gitopsOp.SetSecretsConfig(&gitops.SecretsConfig{
			DashboardURL: cfg.DashboardURL,
			AgentID:      cfg.AgentID,
			WSToken:      cfg.WSToken,
		})

		// Register sync callback to report results to dashboard
		if client != nil {
			gitopsOp.SetSyncCallback(func(result gitops.SyncResult) {
				sendGitSyncReport(client, serverRole, result)
			})
		}

		go func() {
			if err := gitopsOp.Run(ctx); err != nil && ctx.Err() == nil {
				slog.Error("gitops operator exited, clearing reference", "error", err)
				gitopsMu.Lock()
				gitopsOp = nil
				gitopsMu.Unlock()
			}
		}()
		slog.Info("gitops operator started", "repo", repoURL, "role", serverRole)
	}

	// getGitOpsOp returns the current operator (thread-safe).
	getGitOpsOp := func() *gitops.Operator {
		gitopsMu.Lock()
		defer gitopsMu.Unlock()
		return gitopsOp
	}

	// Try starting operator at boot
	startGitOpsOperator(nil)

	var client *ws.Client
	handler := func(msg *ws.Message) {
		handleDashboardMessage(ctx, client, cfg, msg, &cmdMu, &cmdWg, getGitOpsOp, startGitOpsOperator)
	}
	client = ws.NewClient(cfg.WSEndpoint, cfg.AgentID, cfg.WSToken, handler)

	// Re-register sync callback now that client is available, and retry operator start
	// in case it failed at boot due to missing repo URL (now we can fetch it).
	startGitOpsOperator(client)

	// Expose the client to the signal handler for goodbye messages.
	wsClientMu.Lock()
	*wsClientPtr = client
	wsClientMu.Unlock()

	client.SetConnectionCallback(func(connected bool) {
		metrics.SetWSConnected(connected)
		if connected {
			sendConfigBackup(client, cfg)
		} else {
			metrics.WebSocketReconnections.Inc()
		}
	})

	// Start Docker events listener for immediate reaction to container die/oom/kill.
	// Triggers an immediate status report and gitops drift check instead of waiting
	// for the next poll cycle.
	go collector.WatchDockerEvents(ctx, func(event collector.DockerEvent) {
		slog.Info("container event detected, sending immediate status report",
			"action", event.Action, "container", event.ContainerName())
		op := getGitOpsOp()
		sendStatusReport(client, op)
		// Trigger drift check so the operator can remediate
		if op != nil {
			op.TriggerSync()
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
		sendStatusReport(client, getGitOpsOp())
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				sendStatusReport(client, getGitOpsOp())
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

func sendStatusReport(client *ws.Client, gitopsOp *gitops.Operator) {
	report := collector.CollectStatusReport()

	if gitopsOp != nil {
		report.GitOps = gitopsOp.Status()
	}

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

func sendGitSyncReport(client *ws.Client, role string, result gitops.SyncResult) {
	payload := ws.GitSyncPayload{
		CommitSha:  result.CommitHash,
		LastPullAt: time.Now().UTC().Format(time.RFC3339),
		Error:      result.Error,
	}

	for _, c := range result.Components {
		running := c.Status == "running"
		containerCount := 0
		if di, ok := result.DriftInfo[c.Name]; ok {
			containerCount = di.ContainerCount
		}
		payload.Components = append(payload.Components, ws.GitSyncComponent{
			Name:           c.Name,
			Role:           role,
			Running:        running,
			ContainerCount: containerCount,
		})
	}

	msg, err := ws.NewMessage(ws.TypeGitSync, payload)
	if err != nil {
		slog.Error("creating git_sync message", "error", err)
		return
	}
	if err := client.Send(msg); err != nil {
		slog.Warn("sending git_sync message", "error", err)
	} else {
		slog.Info("sent git_sync report", "commit", result.CommitHash[:min(8, len(result.CommitHash))], "error", result.Error)
	}
}

func sendGoodbye(client *ws.Client, reason string) {
	msg, err := ws.NewMessage(ws.TypeAgentGoodbye, ws.AgentGoodbyePayload{Reason: reason})
	if err != nil {
		slog.Error("creating goodbye message", "error", err)
		return
	}
	if err := client.SendDirect(msg); err != nil {
		slog.Warn("sending goodbye message", "error", err)
	} else {
		slog.Info("sent goodbye message", "reason", reason)
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
		PublicIP:      register.PublicIP(),
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

func handleConfigUpdate(cfg *config.Config, msg *ws.Message, gitopsOp *gitops.Operator, startGitOpsOp func(*ws.Client), client *ws.Client) {
	var payload ws.ConfigUpdatePayload
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		slog.Error("parsing config_update", "error", err)
		return
	}

	changed := cfg.ApplyPartialUpdate(payload.WSEndpoint, payload.WSToken, payload.DashboardURL)

	// Handle environment change: update server role in config.
	envChanged := false
	if payload.Environment != "" && payload.Environment != cfg.ServerRole {
		cfg.ServerRole = payload.Environment
		changed = true
		envChanged = true
	}

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
		"environment_changed", envChanged,
	)

	// If environment changed or gitops restart requested, restart operator.
	if envChanged || payload.RestartGitOps {
		if gitopsOp != nil && !payload.RestartGitOps {
			slog.Info("environment changed, triggering gitops sync", "new_environment", payload.Environment)
			gitopsOp.TriggerSync()
		} else {
			reason := "environment set"
			if payload.RestartGitOps {
				reason = "restart requested by dashboard"
			}
			slog.Info("starting gitops operator", "reason", reason)
			startGitOpsOp(client)
		}
	}

	if payload.RestartAfter {
		slog.Info("restart requested, scheduling restart in 2s")
		go func() {
			time.Sleep(2 * time.Second)
			slog.Info("restarting agent")
			os.Exit(0)
		}()
	}
}

func handleDashboardMessage(ctx context.Context, client *ws.Client, cfg *config.Config, msg *ws.Message, cmdMu *sync.Mutex, cmdWg *sync.WaitGroup, getGitOpsOp func() *gitops.Operator, startGitOpsOp func(*ws.Client)) {
	switch msg.Type {
	case ws.TypePing:
		handlePing(client, msg)
	case ws.TypeCommandRequest:
		// Check for restart_gitops before dispatching (needs access to startGitOpsOp)
		var peek ws.CommandRequestPayload
		if err := json.Unmarshal(msg.Payload, &peek); err == nil && peek.Action == "restart_gitops" {
			go func() {
				slog.Info("restart_gitops requested, restarting operator")
				startGitOpsOp(client)
				time.Sleep(3 * time.Second)
				newOp := getGitOpsOp()
				exitCode := 0
				stdout := "GitOps operator restarted"
				if newOp == nil {
					exitCode = 1
					stdout = "GitOps operator failed to start (check repo access)"
				}
				result, _ := ws.NewMessage(ws.TypeCommandResult, ws.CommandResultPayload{
					RequestID: msg.ID,
					ExitCode:  exitCode,
					Stdout:    stdout,
				})
				client.Send(result)
			}()
			return
		}
		cmdWg.Add(1)
		go func() {
			defer cmdWg.Done()
			handleCommandRequest(ctx, client, msg, cmdMu, getGitOpsOp())
		}()
	case ws.TypeConfigUpdate:
		handleConfigUpdate(cfg, msg, getGitOpsOp(), startGitOpsOp, client)
	case ws.TypeUpdateAvailable:
		go handleUpdateAvailable(client, msg)
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

func handleCommandRequest(ctx context.Context, client *ws.Client, msg *ws.Message, cmdMu *sync.Mutex, gitopsOp *gitops.Operator) {
	var req ws.CommandRequestPayload
	if err := json.Unmarshal(msg.Payload, &req); err != nil {
		sendError(client, "INVALID_PARAMS", "invalid command request payload", msg.ID)
		return
	}

	slog.Info("executing command", "action", req.Action, "request_id", msg.ID)

	// gitops_sync: trigger operator and stream progress until complete (no mutex needed)
	if req.Action == "gitops_sync" {
		if gitopsOp == nil {
			sendError(client, "EXECUTION_FAILED", "gitops operator not running", msg.ID)
			return
		}

		sendStream := func(line string) {
			streamMsg, _ := ws.NewMessage(ws.TypeCommandStream, ws.CommandStreamPayload{
				RequestID: msg.ID,
				Stream:    "stdout",
				Line:      line,
			})
			client.Send(streamMsg)
		}

		syncCtx := context.WithoutCancel(ctx)

		result, err := gitopsOp.TriggerSyncWait(syncCtx, sendStream)
		if err != nil {
			sendStream(fmt.Sprintf("Sync failed: %s", err))
			completeMsg, _ := ws.NewMessage(ws.TypeCommandComplete, ws.CommandCompletePayload{
				RequestID:  msg.ID,
				ExitCode:   1,
				DurationMs: 0,
			})
			client.Send(completeMsg)
			return
		}

		exitCode := 0
		if result.Error != "" {
			exitCode = 1
		}

		completeMsg, _ := ws.NewMessage(ws.TypeCommandComplete, ws.CommandCompletePayload{
			RequestID:  msg.ID,
			ExitCode:   exitCode,
			DurationMs: result.DurationMs,
		})
		client.Send(completeMsg)
		return
	}

	// Send goodbye before uninstall so the dashboard knows the agent is leaving.
	if req.Action == "agent_uninstall" {
		sendGoodbye(client, "uninstall")
		time.Sleep(2 * time.Second)
	}

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
			recordCommandError(client, msg.ID, req.Action, err)
			return
		}

		recordCommandSuccess(req.Action, result.DurationMs, result.ExitCode)
		completeMsg, _ := ws.NewMessage(ws.TypeCommandComplete, ws.CommandCompletePayload{
			RequestID:  msg.ID,
			ExitCode:   result.ExitCode,
			DurationMs: result.DurationMs,
		})
		client.Send(completeMsg)
	} else {
		result, err := commands.Execute(cmdCtx, req.Action, req.Params)
		if err != nil {
			recordCommandError(client, msg.ID, req.Action, err)
			return
		}

		recordCommandSuccess(req.Action, result.DurationMs, result.ExitCode)
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

func recordCommandError(client *ws.Client, requestID, action string, err error) {
	metrics.CommandsExecuted.WithLabelValues(action, "error").Inc()
	slog.Error("command failed", "action", action, "error", err)
	sendError(client, extractErrorCode(err), err.Error(), requestID)
}

func recordCommandSuccess(action string, durationMs int64, exitCode int) {
	metrics.CommandsExecuted.WithLabelValues(action, "success").Inc()
	metrics.CommandDuration.WithLabelValues(action).Observe(float64(durationMs))
	slog.Info("command completed", "action", action, "exit_code", exitCode, "duration_ms", durationMs)
}

func handleUpdateAvailable(client *ws.Client, msg *ws.Message) {
	var payload ws.UpdateAvailablePayload
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		slog.Error("parsing update_available", "error", err)
		return
	}

	slog.Info("update available", "version", payload.Version)

	// Send progress events back to the dashboard via WebSocket
	sendProgress := func(step, status, detail string) {
		progressMsg, err := ws.NewMessage(ws.TypeUpdateProgress, ws.UpdateProgressPayload{
			Version: payload.Version,
			Step:    step,
			Status:  status,
			Detail:  detail,
		})
		if err == nil {
			client.Send(progressMsg)
		}
	}

	sendProgress("Verifying signature", "running", "")

	// Verify release signature before proceeding.
	if err := signing.VerifyChecksum(payload.Checksum, payload.Signature); err != nil {
		slog.Error("release signature verification failed, rejecting update", "error", err, "version", payload.Version)
		sendProgress("Verifying signature", "error", err.Error())
		return
	}
	sendProgress("Verifying signature", "done", "")

	// Extract the file-specific checksum from the signed checksums content.
	fileChecksum := payload.Checksum
	if !strings.HasPrefix(fileChecksum, "sha256:") {
		filename := filepath.Base(payload.DownloadURL)
		for _, line := range strings.Split(payload.Checksum, "\n") {
			parts := strings.Fields(line)
			if len(parts) >= 2 && parts[1] == filename {
				fileChecksum = "sha256:" + parts[0]
				break
			}
		}
		if !strings.HasPrefix(fileChecksum, "sha256:") {
			slog.Error("could not extract checksum for file", "filename", filename)
			sendProgress("Extracting checksum", "error", "Could not find checksum for "+filename)
			return
		}
	}

	if err := update.UpdateWithProgress(payload.DownloadURL, fileChecksum, sendProgress); err != nil {
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
		if strings.HasPrefix(line, "KENI_DEPLOY_TOKEN=") {
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
		Role:          os.Getenv("KENI_SERVER_ROLE"),
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

	// Skip WireGuard only in dev mode (KENI_SKIP_WIREGUARD=true)
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

	// Use registration response values, fall back to env vars
	serverRole := resp.ServerRole
	if serverRole == "" {
		serverRole = os.Getenv("KENI_SERVER_ROLE")
	}
	repoURL := resp.GitRepoURL
	if repoURL == "" {
		repoURL = os.Getenv("KENI_IDP_REPO_URL")
	}
	resolvedDashboardURL := resp.DashboardURL
	if resolvedDashboardURL == "" {
		resolvedDashboardURL = dashboardURL
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
		DashboardURL:      resolvedDashboardURL,
		ServerRole:        serverRole,
		RepoURL:           repoURL,
		DeployToken:       firstNonEmpty(resp.DeployToken, os.Getenv("KENI_DEPLOY_TOKEN")),
	}
	if err := cfg.Save(); err != nil {
		return nil, fmt.Errorf("saving config: %w", err)
	}

	return cfg, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
