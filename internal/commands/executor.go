package commands

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

var errSystemInfoSpecial = errors.New("system_info: handled specially")

// Result holds the output of a non-streaming command.
type Result struct {
	ExitCode   int
	Stdout     string
	Stderr     string
	DurationMs int64
}

// StreamLine represents a single line of streaming output.
type StreamLine struct {
	Stream string // "stdout" or "stderr"
	Line   string
}

// StreamResult is the final result of a streaming command.
type StreamResult struct {
	ExitCode   int
	DurationMs int64
}

// Allowed service names for service_status action.
var AllowedServices = []string{
	"docker",
	"wg-quick@wg0",
	"keni-agent",
}

var containerNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]+$`)

// Execute runs a whitelisted action and returns the result.
func Execute(ctx context.Context, action string, params json.RawMessage) (*Result, error) {
	start := time.Now()

	cmd, err := buildCommand(ctx, action, params)
	if errors.Is(err, errSystemInfoSpecial) {
		return executeSystemInfo(start)
	}
	if err != nil {
		return nil, err
	}

	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()

	exitCode := 0
	if runErr != nil {
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return nil, fmt.Errorf("executing command: %w", runErr)
		}
	}

	return &Result{
		ExitCode:   exitCode,
		Stdout:     stdout.String(),
		Stderr:     stderr.String(),
		DurationMs: time.Since(start).Milliseconds(),
	}, nil
}

// SystemInfoResult is the structured output for the system_info action.
type SystemInfoResult struct {
	Hostname      string    `json:"hostname"`
	OS            string    `json:"os"`
	Kernel        string    `json:"kernel"`
	Arch          string    `json:"arch"`
	Uptime        string    `json:"uptime"`
	LoadAvg       []float64 `json:"loadAvg"`
}

func executeSystemInfo(start time.Time) (*Result, error) {
	info := SystemInfoResult{
		Arch: runtime.GOARCH,
	}

	info.Hostname, _ = os.Hostname()
	info.OS = readPrettyName()
	info.Kernel = runSimple("uname", "-r")
	info.Uptime = runSimple("uptime", "-p")
	info.LoadAvg = parseLoadAvg()

	data, err := json.Marshal(info)
	if err != nil {
		return nil, fmt.Errorf("marshaling system info: %w", err)
	}

	return &Result{
		ExitCode:   0,
		Stdout:     string(data),
		DurationMs: time.Since(start).Milliseconds(),
	}, nil
}

func readPrettyName() string {
	data, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return runtime.GOOS
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "PRETTY_NAME=") {
			val := strings.TrimPrefix(line, "PRETTY_NAME=")
			return strings.Trim(val, "\"")
		}
	}
	return runtime.GOOS
}

func runSimple(name string, args ...string) string {
	out, err := exec.Command(name, args...).Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

func parseLoadAvg() []float64 {
	out, err := exec.Command("cat", "/proc/loadavg").Output()
	if err != nil {
		return []float64{0, 0, 0}
	}
	parts := strings.Fields(string(out))
	result := make([]float64, 3)
	for i := 0; i < 3 && i < len(parts); i++ {
		val, _ := strconv.ParseFloat(parts[i], 64)
		result[i] = val
	}
	return result
}

// ExecuteStream runs a streaming action, sending output lines to the callback.
// Returns the final result when the command completes.
func ExecuteStream(ctx context.Context, action string, params json.RawMessage, onLine func(StreamLine)) (*StreamResult, error) {
	start := time.Now()

	cmd, err := buildCommand(ctx, action, params)
	if err != nil {
		return nil, err
	}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("creating stdout pipe: %w", err)
	}

	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("creating stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting command: %w", err)
	}

	// Read stdout and stderr concurrently
	done := make(chan struct{}, 2)

	go func() {
		scanner := bufio.NewScanner(stdoutPipe)
		for scanner.Scan() {
			onLine(StreamLine{Stream: "stdout", Line: scanner.Text()})
		}
		done <- struct{}{}
	}()

	go func() {
		scanner := bufio.NewScanner(stderrPipe)
		for scanner.Scan() {
			onLine(StreamLine{Stream: "stderr", Line: scanner.Text()})
		}
		done <- struct{}{}
	}()

	// Wait for both scanners to finish
	<-done
	<-done

	runErr := cmd.Wait()

	exitCode := 0
	if runErr != nil {
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
	}

	return &StreamResult{
		ExitCode:   exitCode,
		DurationMs: time.Since(start).Milliseconds(),
	}, nil
}

// IsStreamingAction returns true if the action should use streaming output.
func IsStreamingAction(action string) bool {
	return action == "backup_trigger"
}

// buildCommand creates the exec.Cmd for a whitelisted action with validated params.
func buildCommand(ctx context.Context, action string, params json.RawMessage) (*exec.Cmd, error) {
	switch action {
	case "container_list":
		return exec.CommandContext(ctx, "docker", "ps", "-a", "--format", "json"), nil

	case "container_stats":
		return exec.CommandContext(ctx, "docker", "stats", "--no-stream", "--format", "json"), nil

	case "container_restart":
		name, err := extractStringParam(params, "name")
		if err != nil {
			return nil, fmt.Errorf("INVALID_PARAMS: %w", err)
		}
		if err := validateContainerName(name); err != nil {
			return nil, fmt.Errorf("INVALID_PARAMS: %w", err)
		}
		return exec.CommandContext(ctx, "docker", "restart", name), nil

	case "backup_snapshots":
		return exec.CommandContext(ctx, "restic", "snapshots", "--json"), nil

	case "backup_stats":
		return exec.CommandContext(ctx, "restic", "stats", "--json"), nil

	case "backup_trigger":
		return exec.CommandContext(ctx, "docker", "start", "-a", "keni-backup"), nil

	case "system_disk":
		return exec.CommandContext(ctx, "df", "-h"), nil

	case "system_memory":
		return exec.CommandContext(ctx, "free", "-m"), nil

	case "system_info":
		// Handled specially in Execute, not as a single command
		return nil, errSystemInfoSpecial

	case "service_status":
		name, err := extractStringParam(params, "name")
		if err != nil {
			return nil, fmt.Errorf("INVALID_PARAMS: %w", err)
		}
		if err := validateServiceName(name); err != nil {
			return nil, fmt.Errorf("INVALID_PARAMS: %w", err)
		}
		return exec.CommandContext(ctx, "systemctl", "is-active", name), nil

	case "wireguard_status":
		return exec.CommandContext(ctx, "wg", "show"), nil

	case "docker_logs":
		name, err := extractStringParam(params, "name")
		if err != nil {
			return nil, fmt.Errorf("INVALID_PARAMS: %w", err)
		}
		if err := validateContainerName(name); err != nil {
			return nil, fmt.Errorf("INVALID_PARAMS: %w", err)
		}
		lines, err := extractIntParam(params, "lines")
		if err != nil {
			return nil, fmt.Errorf("INVALID_PARAMS: %w", err)
		}
		if lines < 1 || lines > 500 {
			return nil, fmt.Errorf("INVALID_PARAMS: lines must be 1-500, got %d", lines)
		}
		return exec.CommandContext(ctx, "docker", "logs", "--tail", strconv.Itoa(lines), name), nil

	default:
		return nil, fmt.Errorf("UNKNOWN_ACTION: action %q is not in the whitelist", action)
	}
}

func extractStringParam(params json.RawMessage, key string) (string, error) {
	if params == nil {
		return "", fmt.Errorf("missing params")
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(params, &m); err != nil {
		return "", fmt.Errorf("parsing params: %w", err)
	}
	raw, ok := m[key]
	if !ok {
		return "", fmt.Errorf("missing required param %q", key)
	}
	var val string
	if err := json.Unmarshal(raw, &val); err != nil {
		return "", fmt.Errorf("param %q must be a string", key)
	}
	return val, nil
}

func extractIntParam(params json.RawMessage, key string) (int, error) {
	if params == nil {
		return 0, fmt.Errorf("missing params")
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(params, &m); err != nil {
		return 0, fmt.Errorf("parsing params: %w", err)
	}
	raw, ok := m[key]
	if !ok {
		return 0, fmt.Errorf("missing required param %q", key)
	}
	var val float64
	if err := json.Unmarshal(raw, &val); err != nil {
		return 0, fmt.Errorf("param %q must be a number", key)
	}
	return int(val), nil
}

// Container name cache with TTL to avoid running docker ps on every command.
var (
	containerCacheMu    sync.Mutex
	containerCacheNames []string
	containerCacheTime  time.Time
	containerCacheTTL   = 10 * time.Second
)

func getCachedContainerNames() ([]string, error) {
	containerCacheMu.Lock()
	defer containerCacheMu.Unlock()

	if time.Since(containerCacheTime) < containerCacheTTL && containerCacheNames != nil {
		return containerCacheNames, nil
	}

	out, err := exec.Command("docker", "ps", "-a", "--format", "{{.Names}}").Output()
	if err != nil {
		return nil, fmt.Errorf("listing containers: %w", err)
	}

	var names []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			names = append(names, line)
		}
	}

	containerCacheNames = names
	containerCacheTime = time.Now()
	return names, nil
}

func validateContainerName(name string) error {
	if !containerNameRe.MatchString(name) {
		return fmt.Errorf("invalid container name %q: must match %s", name, containerNameRe.String())
	}

	names, err := getCachedContainerNames()
	if err != nil {
		return err
	}
	for _, n := range names {
		if n == name {
			return nil
		}
	}
	return fmt.Errorf("container %q does not exist", name)
}

func validateServiceName(name string) error {
	for _, allowed := range AllowedServices {
		if name == allowed {
			return nil
		}
	}
	return fmt.Errorf("service %q is not in the allowlist", name)
}
