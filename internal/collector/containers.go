package collector

import (
	"encoding/json"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/kenitech-io/devops-agent/internal/ws"
)

// dockerPSEntry matches the JSON output of docker ps --format json.
type dockerPSEntry struct {
	ID      string `json:"ID"`
	Names   string `json:"Names"`
	Image   string `json:"Image"`
	State   string `json:"State"`
	Status  string `json:"Status"`
	RunningFor string `json:"RunningFor"`
}

// dockerStatsEntry matches the JSON output of docker stats --no-stream --format json.
type dockerStatsEntry struct {
	Name     string `json:"Name"`
	CPUPerc  string `json:"CPUPerc"`
	MemUsage string `json:"MemUsage"`
	MemPerc  string `json:"MemPerc"`
}

// CollectContainers gathers container info from docker ps and docker stats.
func CollectContainers() ([]ws.ContainerInfo, error) {
	psOut, err := exec.Command("docker", "ps", "-a", "--format", "json").Output()
	if err != nil {
		return nil, err
	}

	var psEntries []dockerPSEntry
	for _, line := range strings.Split(strings.TrimSpace(string(psOut)), "\n") {
		if line == "" {
			continue
		}
		var entry dockerPSEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		psEntries = append(psEntries, entry)
	}

	// Collect stats for running containers
	statsMap := collectStats()

	containers := make([]ws.ContainerInfo, 0, len(psEntries))
	for _, ps := range psEntries {
		info := ws.ContainerInfo{
			ID:    truncateID(ps.ID),
			Name:  ps.Names,
			Image: ps.Image,
			State: ps.State,
		}

		// Parse health from status string
		info.Health = parseHealth(ps.Status)

		// Parse uptime from RunningFor
		info.Uptime = parseUptime(ps.RunningFor)

		// Add stats if available
		if stats, ok := statsMap[ps.Names]; ok {
			info.CPUPercent = parsePercent(stats.CPUPerc)
			info.MemoryUsageMb, info.MemoryLimitMb = parseMemUsage(stats.MemUsage)
		}

		// Collect Traefik labels for URL auto-discovery
		info.Labels = collectTraefikLabels(ps.ID)

		containers = append(containers, info)
	}

	return containers, nil
}

// collectTraefikLabels extracts Traefik routing labels from a container.
// Only returns labels starting with "traefik.http.routers." to keep payload small.
func collectTraefikLabels(containerID string) map[string]string {
	out, err := exec.Command("docker", "inspect", "--format", "{{json .Config.Labels}}", containerID).Output()
	if err != nil {
		return nil
	}

	var allLabels map[string]string
	if err := json.Unmarshal(out, &allLabels); err != nil {
		return nil
	}

	// Filter to only Traefik routing labels
	result := make(map[string]string)
	for k, v := range allLabels {
		if strings.HasPrefix(k, "traefik.http.routers.") && strings.HasSuffix(k, ".rule") {
			result[k] = v
		}
		if k == "traefik.enable" {
			result[k] = v
		}
	}

	if len(result) == 0 {
		return nil
	}
	return result
}

func collectStats() map[string]dockerStatsEntry {
	out, err := exec.Command("docker", "stats", "--no-stream", "--format", "json").Output()
	if err != nil {
		return nil
	}

	result := make(map[string]dockerStatsEntry)
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		var entry dockerStatsEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		result[entry.Name] = entry
	}
	return result
}

func truncateID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func parseHealth(status string) string {
	lower := strings.ToLower(status)
	if strings.Contains(lower, "(healthy)") {
		return "healthy"
	}
	if strings.Contains(lower, "(unhealthy)") {
		return "unhealthy"
	}
	return ""
}

func parsePercent(s string) float64 {
	s = strings.TrimSuffix(strings.TrimSpace(s), "%")
	val, _ := strconv.ParseFloat(s, 64)
	return val
}

func parseMemUsage(s string) (usageMb, limitMb float64) {
	parts := strings.Split(s, "/")
	if len(parts) != 2 {
		return 0, 0
	}
	usageMb = parseMemValue(strings.TrimSpace(parts[0]))
	limitMb = parseMemValue(strings.TrimSpace(parts[1]))
	return usageMb, limitMb
}

func parseMemValue(s string) float64 {
	s = strings.TrimSpace(s)
	multiplier := 1.0

	if strings.HasSuffix(s, "GiB") {
		multiplier = 1024
		s = strings.TrimSuffix(s, "GiB")
	} else if strings.HasSuffix(s, "MiB") {
		multiplier = 1
		s = strings.TrimSuffix(s, "MiB")
	} else if strings.HasSuffix(s, "KiB") {
		multiplier = 1.0 / 1024
		s = strings.TrimSuffix(s, "KiB")
	} else if strings.HasSuffix(s, "GB") {
		multiplier = 1000
		s = strings.TrimSuffix(s, "GB")
	} else if strings.HasSuffix(s, "MB") {
		multiplier = 1
		s = strings.TrimSuffix(s, "MB")
	}

	val, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return val * multiplier
}

// parseUptime tries to convert Docker's RunningFor string to seconds.
func parseUptime(runningFor string) int64 {
	// Docker RunningFor format: "2 hours ago", "3 days ago", "About a minute ago"
	runningFor = strings.TrimSuffix(strings.TrimSpace(runningFor), " ago")
	runningFor = strings.TrimPrefix(runningFor, "About ")

	// Try to parse duration
	d := parseDurationApprox(runningFor)
	return int64(d.Seconds())
}

func parseDurationApprox(s string) time.Duration {
	parts := strings.Fields(s)
	if len(parts) < 2 {
		if s == "a minute" || s == "an hour" {
			if strings.Contains(s, "minute") {
				return time.Minute
			}
			return time.Hour
		}
		return 0
	}

	num, err := strconv.Atoi(parts[0])
	if err != nil {
		if parts[0] == "a" || parts[0] == "an" {
			num = 1
		} else {
			return 0
		}
	}

	unit := strings.TrimSuffix(parts[1], "s")
	switch unit {
	case "second":
		return time.Duration(num) * time.Second
	case "minute":
		return time.Duration(num) * time.Minute
	case "hour":
		return time.Duration(num) * time.Hour
	case "day":
		return time.Duration(num) * 24 * time.Hour
	case "week":
		return time.Duration(num) * 7 * 24 * time.Hour
	case "month":
		return time.Duration(num) * 30 * 24 * time.Hour
	}
	return 0
}
