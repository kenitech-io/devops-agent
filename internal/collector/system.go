package collector

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/kenitech-io/devops-agent/internal/ws"
)

var startTime = time.Now()

// CollectHeartbeat gathers system metrics for the heartbeat payload.
func CollectHeartbeat(version string) (*ws.HeartbeatPayload, error) {
	uptime := int64(time.Since(startTime).Seconds())
	loadAvg := getLoadAverage()
	memUsed, memTotal := getMemoryInfo()
	diskUsed, diskTotal := getDiskInfo()

	return &ws.HeartbeatPayload{
		Uptime:        uptime,
		LoadAvg:       loadAvg,
		MemoryUsedMb:  memUsed,
		MemoryTotalMb: memTotal,
		DiskUsedGb:    diskUsed,
		DiskTotalGb:   diskTotal,
		AgentVersion:  version,
	}, nil
}

// getLoadAverage reads /proc/loadavg or uses uptime command.
func getLoadAverage() []float64 {
	out, err := exec.Command("cat", "/proc/loadavg").Output()
	if err != nil {
		// Fallback to uptime parsing
		return getLoadFromUptime()
	}
	return parseLoadAvgProc(string(out))
}

// parseLoadAvgProc parses /proc/loadavg content.
func parseLoadAvgProc(content string) []float64 {
	parts := strings.Fields(content)
	if len(parts) < 3 {
		return []float64{0, 0, 0}
	}

	result := make([]float64, 3)
	for i := 0; i < 3; i++ {
		val, err := strconv.ParseFloat(parts[i], 64)
		if err == nil {
			result[i] = val
		}
	}
	return result
}

func getLoadFromUptime() []float64 {
	out, err := exec.Command("uptime").Output()
	if err != nil {
		return []float64{0, 0, 0}
	}
	return parseUptimeOutput(string(out))
}

// parseUptimeOutput parses the output of the uptime command to extract load averages.
func parseUptimeOutput(line string) []float64 {
	idx := strings.Index(line, "load average:")
	if idx == -1 {
		// Some systems use "load averages:"
		idx = strings.Index(line, "load averages:")
		if idx == -1 {
			return []float64{0, 0, 0}
		}
		idx += len("load averages:")
	} else {
		idx += len("load average:")
	}

	parts := strings.Split(strings.TrimSpace(line[idx:]), ",")
	result := make([]float64, 3)
	for i := 0; i < 3 && i < len(parts); i++ {
		val, err := strconv.ParseFloat(strings.TrimSpace(parts[i]), 64)
		if err == nil {
			result[i] = val
		}
	}
	return result
}

// getMemoryInfo reads memory from /proc/meminfo or free -m.
func getMemoryInfo() (usedMb, totalMb int64) {
	out, err := exec.Command("free", "-m").Output()
	if err != nil {
		return 0, 0
	}
	return parseFreeOutput(string(out))
}

// parseFreeOutput parses the output of 'free -m' to extract used and total memory.
func parseFreeOutput(output string) (usedMb, totalMb int64) {
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "Mem:") {
			fields := strings.Fields(line)
			if len(fields) >= 3 {
				total, _ := strconv.ParseInt(fields[1], 10, 64)
				used, _ := strconv.ParseInt(fields[2], 10, 64)
				return used, total
			}
		}
	}
	return 0, 0
}

// getDiskInfo reads disk usage from df for the root filesystem.
func getDiskInfo() (usedGb, totalGb float64) {
	out, err := exec.Command("df", "-BG", "--output=size,used", "/").Output()
	if err != nil {
		return 0, 0
	}
	return parseDfOutput(string(out))
}

// parseDfOutput parses the output of 'df -BG --output=size,used'.
func parseDfOutput(output string) (usedGb, totalGb float64) {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) < 2 {
		return 0, 0
	}

	// Second line has the values
	fields := strings.Fields(lines[1])
	if len(fields) >= 2 {
		totalStr := strings.TrimSuffix(fields[0], "G")
		usedStr := strings.TrimSuffix(fields[1], "G")
		total, _ := strconv.ParseFloat(totalStr, 64)
		used, _ := strconv.ParseFloat(usedStr, 64)
		return used, total
	}
	return 0, 0
}

// GetSystemUptime returns system uptime in seconds by reading /proc/uptime.
func GetSystemUptime() (int64, error) {
	out, err := exec.Command("cat", "/proc/uptime").Output()
	if err != nil {
		return 0, fmt.Errorf("reading /proc/uptime: %w", err)
	}

	parts := strings.Fields(string(out))
	if len(parts) < 1 {
		return 0, fmt.Errorf("unexpected /proc/uptime format")
	}

	uptime, err := strconv.ParseFloat(parts[0], 64)
	if err != nil {
		return 0, fmt.Errorf("parsing uptime: %w", err)
	}

	return int64(uptime), nil
}
