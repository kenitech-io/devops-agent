package collector

import (
	"bufio"
	"context"
	"encoding/json"
	"log/slog"
	"os/exec"
	"strings"
	"time"
)

// DockerEvent represents a parsed Docker daemon event.
type DockerEvent struct {
	Status string `json:"status"`
	ID     string `json:"id"`
	Type   string `json:"Type"`
	Action string `json:"Action"`
	Actor  struct {
		Attributes map[string]string `json:"Attributes"`
	} `json:"Actor"`
}

// ContainerName returns the container name from the event attributes.
func (e DockerEvent) ContainerName() string {
	return e.Actor.Attributes["name"]
}

// DockerEventCallback is called when a relevant container event is received.
type DockerEventCallback func(event DockerEvent)

// WatchDockerEvents listens to the Docker daemon event stream for container
// die, oom, and kill events. Calls the callback for each event.
// Automatically reconnects with backoff if the stream disconnects.
// Blocks until the context is cancelled.
func WatchDockerEvents(ctx context.Context, callback DockerEventCallback) {
	backoff := time.Second

	for {
		if ctx.Err() != nil {
			return
		}

		err := streamEvents(ctx, callback)
		if ctx.Err() != nil {
			return
		}

		slog.Warn("docker events stream ended, reconnecting", "error", err, "backoff", backoff)

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}

		// Increase backoff up to 30s
		backoff = backoff * 2
		if backoff > 30*time.Second {
			backoff = 30 * time.Second
		}
	}
}

func streamEvents(ctx context.Context, callback DockerEventCallback) error {
	cmd := exec.CommandContext(ctx, "docker", "events",
		"--format", "{{json .}}",
		"--filter", "type=container",
		"--filter", "event=die",
		"--filter", "event=oom",
		"--filter", "event=kill",
	)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}

	if err := cmd.Start(); err != nil {
		return err
	}

	slog.Info("docker events stream connected")

	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var event DockerEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			slog.Debug("docker events parse error", "error", err)
			continue
		}

		slog.Info("docker event",
			"action", event.Action,
			"container", event.ContainerName(),
		)
		callback(event)
	}

	return cmd.Wait()
}
