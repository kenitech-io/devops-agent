package collector

import (
	"encoding/json"
	"os/exec"
	"time"

	"github.com/kenitech-io/devops-agent/internal/ws"
)

// resticSnapshot matches the JSON output of restic snapshots --json.
type resticSnapshot struct {
	Time string `json:"time"`
	Paths []string `json:"paths"`
	Tags  []string `json:"tags"`
}

// resticStats matches the JSON output of restic stats --json.
type resticStats struct {
	TotalSize      int64 `json:"total_size"`
	TotalFileCount int   `json:"total_file_count"`
}

// CollectBackups gathers backup info from restic.
func CollectBackups() ws.BackupInfo {
	info := ws.BackupInfo{
		LastStatus: "unknown",
	}

	// Get snapshots
	snapOut, err := exec.Command("restic", "snapshots", "--json", "--latest", "1").Output()
	if err != nil {
		info.LastStatus = "error"
		return info
	}

	var snapshots []resticSnapshot
	if err := json.Unmarshal(snapOut, &snapshots); err != nil {
		info.LastStatus = "error"
		return info
	}

	if len(snapshots) > 0 {
		info.LastSnapshot = snapshots[0].Time
		info.LastStatus = "success"

		// Check if last snapshot is older than 25 hours (stale)
		snapshotTime, err := time.Parse(time.RFC3339, snapshots[0].Time)
		if err == nil && time.Since(snapshotTime) > 25*time.Hour {
			info.LastStatus = "stale"
		}
	}

	// Get total snapshot count (separate call without --latest)
	countOut, err := exec.Command("restic", "snapshots", "--json").Output()
	if err == nil {
		var allSnapshots []resticSnapshot
		if json.Unmarshal(countOut, &allSnapshots) == nil {
			info.SnapshotCount = len(allSnapshots)
		}
	}

	// Get stats
	statsOut, err := exec.Command("restic", "stats", "--json").Output()
	if err == nil {
		var stats resticStats
		if json.Unmarshal(statsOut, &stats) == nil {
			info.TotalSizeGb = float64(stats.TotalSize) / (1024 * 1024 * 1024)
		}
	}

	return info
}
