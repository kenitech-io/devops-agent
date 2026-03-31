package collector

import (
	"testing"
)

func TestCollectHeartbeat(t *testing.T) {
	hb, err := CollectHeartbeat("1.0.0-test")
	if err != nil {
		t.Fatalf("CollectHeartbeat returned error: %v", err)
	}
	if hb == nil {
		t.Fatal("CollectHeartbeat returned nil")
	}

	if hb.AgentVersion != "1.0.0-test" {
		t.Errorf("AgentVersion = %q, want %q", hb.AgentVersion, "1.0.0-test")
	}

	if hb.Uptime < 0 {
		t.Errorf("Uptime = %d, want >= 0", hb.Uptime)
	}

	if len(hb.LoadAvg) != 3 {
		t.Errorf("LoadAvg has %d elements, want 3", len(hb.LoadAvg))
	}

	for i, v := range hb.LoadAvg {
		if v < 0 {
			t.Errorf("LoadAvg[%d] = %f, want >= 0", i, v)
		}
	}
}

func TestCollectHeartbeat_VersionPassthrough(t *testing.T) {
	versions := []string{"", "0.1.0", "v2.3.4-rc1", "dev-build-abc123"}
	for _, v := range versions {
		t.Run(v, func(t *testing.T) {
			hb, err := CollectHeartbeat(v)
			if err != nil {
				t.Fatalf("CollectHeartbeat(%q) error: %v", v, err)
			}
			if hb.AgentVersion != v {
				t.Errorf("AgentVersion = %q, want %q", hb.AgentVersion, v)
			}
		})
	}
}

func TestGetLoadAverage(t *testing.T) {
	result := getLoadAverage()
	if len(result) != 3 {
		t.Fatalf("getLoadAverage() returned %d values, want 3", len(result))
	}
	for i, v := range result {
		if v < 0 {
			t.Errorf("getLoadAverage()[%d] = %f, want >= 0", i, v)
		}
	}
}

func TestGetLoadFromUptime(t *testing.T) {
	result := getLoadFromUptime()
	if len(result) != 3 {
		t.Fatalf("getLoadFromUptime() returned %d values, want 3", len(result))
	}
	for i, v := range result {
		if v < 0 {
			t.Errorf("getLoadFromUptime()[%d] = %f, want >= 0", i, v)
		}
	}
}

func TestParseLoadAvgProc(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []float64
	}{
		{
			"normal linux output",
			"0.50 0.75 1.00 1/234 5678",
			[]float64{0.50, 0.75, 1.00},
		},
		{
			"high load",
			"12.50 8.75 6.25 3/456 7890",
			[]float64{12.50, 8.75, 6.25},
		},
		{
			"zero load",
			"0.00 0.00 0.00 1/100 1234",
			[]float64{0, 0, 0},
		},
		{
			"too few fields",
			"0.50",
			[]float64{0, 0, 0},
		},
		{
			"empty",
			"",
			[]float64{0, 0, 0},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseLoadAvgProc(tt.input)
			if len(got) != 3 {
				t.Fatalf("parseLoadAvgProc(%q) returned %d values, want 3", tt.input, len(got))
			}
			for i, v := range got {
				if v != tt.expected[i] {
					t.Errorf("parseLoadAvgProc(%q)[%d] = %f, want %f", tt.input, i, v, tt.expected[i])
				}
			}
		})
	}
}

func TestParseUptimeOutput(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []float64
	}{
		{
			"linux format",
			" 14:30:01 up 5 days,  3:22,  2 users,  load average: 0.50, 0.75, 1.00",
			[]float64{0.50, 0.75, 1.00},
		},
		{
			"macOS format",
			"14:30  up 5 days,  3:22, 2 users, load averages: 2.10, 1.85, 1.50",
			[]float64{2.10, 1.85, 1.50},
		},
		{
			"no load average",
			"14:30:01 up 5 days, 2 users",
			[]float64{0, 0, 0},
		},
		{
			"empty",
			"",
			[]float64{0, 0, 0},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseUptimeOutput(tt.input)
			if len(got) != 3 {
				t.Fatalf("parseUptimeOutput returned %d values, want 3", len(got))
			}
			for i, v := range got {
				diff := v - tt.expected[i]
				if diff > 0.01 || diff < -0.01 {
					t.Errorf("parseUptimeOutput(%q)[%d] = %f, want %f", tt.input, i, v, tt.expected[i])
				}
			}
		})
	}
}

func TestParseFreeOutput(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		expectUsed  int64
		expectTotal int64
	}{
		{
			"normal output",
			"              total        used        free      shared  buff/cache   available\nMem:           7982        3500        1200         200        3282        4000\nSwap:          2047           0        2047\n",
			3500, 7982,
		},
		{
			"high usage",
			"              total        used        free      shared  buff/cache   available\nMem:          16384       15000         384         100        1000        1384\nSwap:          8192        1000        7192\n",
			15000, 16384,
		},
		{
			"no Mem line",
			"              total        used        free\nSwap:          2047           0        2047\n",
			0, 0,
		},
		{
			"empty",
			"",
			0, 0,
		},
		{
			"malformed Mem line",
			"Mem: abc",
			0, 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			used, total := parseFreeOutput(tt.input)
			if used != tt.expectUsed {
				t.Errorf("parseFreeOutput used = %d, want %d", used, tt.expectUsed)
			}
			if total != tt.expectTotal {
				t.Errorf("parseFreeOutput total = %d, want %d", total, tt.expectTotal)
			}
		})
	}
}

func TestParseDfOutput(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		expectUsed  float64
		expectTotal float64
	}{
		{
			"normal output",
			"     Size     Used\n     500G     250G\n",
			250, 500,
		},
		{
			"small disk",
			"     Size     Used\n      50G      30G\n",
			30, 50,
		},
		{
			"no G suffix",
			"     Size     Used\n      500      250\n",
			250, 500,
		},
		{
			"single line only (header)",
			"     Size     Used",
			0, 0,
		},
		{
			"empty",
			"",
			0, 0,
		},
		{
			"not enough fields",
			"     Size     Used\n      500\n",
			0, 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			used, total := parseDfOutput(tt.input)
			if used != tt.expectUsed {
				t.Errorf("parseDfOutput used = %f, want %f", used, tt.expectUsed)
			}
			if total != tt.expectTotal {
				t.Errorf("parseDfOutput total = %f, want %f", total, tt.expectTotal)
			}
		})
	}
}

func TestGetMemoryInfo(t *testing.T) {
	used, total := getMemoryInfo()
	if total < 0 {
		t.Errorf("total memory = %d, want >= 0", total)
	}
	if used < 0 {
		t.Errorf("used memory = %d, want >= 0", used)
	}
	if total > 0 && used > total {
		t.Errorf("used memory %d exceeds total %d", used, total)
	}
}

func TestGetDiskInfo(t *testing.T) {
	used, total := getDiskInfo()
	if total < 0 {
		t.Errorf("total disk = %f, want >= 0", total)
	}
	if used < 0 {
		t.Errorf("used disk = %f, want >= 0", used)
	}
	if total > 0 && used > total {
		t.Errorf("used disk %f exceeds total %f", used, total)
	}
}

func TestGetSystemUptime(t *testing.T) {
	uptime, err := GetSystemUptime()
	if err != nil {
		// Expected on macOS where /proc/uptime does not exist
		t.Logf("GetSystemUptime() error (expected on macOS): %v", err)
		return
	}
	if uptime <= 0 {
		t.Errorf("GetSystemUptime() = %d, want > 0", uptime)
	}
}
