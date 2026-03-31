package collector

import (
	"log/slog"

	"github.com/kenitech-io/devops-agent/internal/ws"
)

// CollectStatusReport gathers a full status snapshot.
func CollectStatusReport() *ws.StatusReportPayload {
	containers, err := CollectContainers()
	if err != nil {
		slog.Error("collecting containers", "error", err)
		containers = nil
	}

	backups := CollectBackups()
	wgInfo := CollectWireGuard()

	return &ws.StatusReportPayload{
		Containers: containers,
		Backups:    backups,
		WireGuard:  wgInfo,
	}
}
