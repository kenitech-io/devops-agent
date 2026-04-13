package commands

import (
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

var errUFWStatusSpecial = errors.New("ufw_status: handled specially")

// UFWExpectedRule is a canonical rule the agent's hardening should have put in
// place (see setup.sh apply_hardening). Surfaced via the dashboard UFW tab so
// operators can spot drift between declared and actual state.
type UFWExpectedRule struct {
	Port    string `json:"port"`
	Proto   string `json:"proto"`   // "tcp" | "udp"
	From    string `json:"from"`    // "Anywhere" or CIDR
	Purpose string `json:"purpose"`
}

// ExpectedUFWRules mirrors setup.sh apply_hardening. Keep both in sync.
var ExpectedUFWRules = []UFWExpectedRule{
	{Port: "22", Proto: "tcp", From: "Anywhere", Purpose: "SSH"},
	{Port: "80", Proto: "tcp", From: "Anywhere", Purpose: "HTTP"},
	{Port: "443", Proto: "tcp", From: "Anywhere", Purpose: "HTTPS"},
	{Port: "51820", Proto: "udp", From: "Anywhere", Purpose: "WireGuard"},
	{Port: "9100", Proto: "tcp", From: "10.99.0.0/24", Purpose: "node-exporter"},
	{Port: "8080", Proto: "tcp", From: "10.99.0.0/24", Purpose: "cadvisor"},
	{Port: "3100", Proto: "tcp", From: "10.99.0.0/24", Purpose: "loki"},
}

// UFWActualRule is a parsed tuple line from /etc/ufw/user.rules.
type UFWActualRule struct {
	Port   string `json:"port"`
	Proto  string `json:"proto"`
	Action string `json:"action"` // "ALLOW" | "DENY" | "REJECT" | "LIMIT"
	From   string `json:"from"`
}

// UFWStatusResult is the full payload returned to the dashboard.
type UFWStatusResult struct {
	Installed bool              `json:"installed"`
	Enabled   bool              `json:"enabled"`
	RawOutput string            `json:"rawOutput"`
	Actual    []UFWActualRule   `json:"actual"`
	Expected  []UFWExpectedRule `json:"expected"`
	Matched   []UFWExpectedRule `json:"matched"`
	Missing   []UFWExpectedRule `json:"missing"`
	Extra     []UFWActualRule   `json:"extra"`
}

// tuple format in /etc/ufw/user.rules:
//
//	### tuple ### allow tcp 9100 0.0.0.0/0 any 10.99.0.0/24 in
//	               [1]   [2] [3]  [4]    [5]  [6]         [7]
var ufwTupleRe = regexp.MustCompile(`^###\s+tuple\s+###\s+(\S+)\s+(\S+)\s+(\S+)\s+(\S+)\s+(\S+)\s+(\S+)\s+(\S+)`)

// sudoCat reads a root-owned file via sudo. The keni user has NOPASSWD sudo
// from install.sh, and this avoids the ufw(1) lockfile which /run read-only
// under ProtectSystem=strict blocks.
func sudoCat(path string) (string, error) {
	out, err := exec.Command("sudo", "-n", "cat", path).Output()
	if err != nil {
		return "", fmt.Errorf("sudo cat %s: %w", path, err)
	}
	return string(out), nil
}

func parseUFWConfEnabled(conf string) bool {
	for _, line := range strings.Split(conf, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, "ENABLED=") {
			val := strings.Trim(strings.TrimPrefix(trimmed, "ENABLED="), "\"'")
			return strings.EqualFold(val, "yes")
		}
	}
	return false
}

func parseUFWRules(rules string) []UFWActualRule {
	var out []UFWActualRule
	seen := make(map[string]bool)
	for _, line := range strings.Split(rules, "\n") {
		m := ufwTupleRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		action := strings.ToUpper(m[1])
		proto := m[2]
		port := m[3]
		// m[4] daddr, m[5] sport, m[6] saddr, m[7] direction/iface.
		saddr := m[6]
		direction := m[7]
		from := saddr
		if saddr == "0.0.0.0/0" || saddr == "::/0" {
			from = "Anywhere"
		}
		// direction is "in", "out", or "in_<iface>" / "out_<iface>". When
		// an interface is named, scope the rule to that interface in the
		// display so a "0.0.0.0/0 in_tailscale0" rule doesn't look like a
		// public allow-any.
		if strings.HasPrefix(direction, "in_") {
			from = from + " on " + strings.TrimPrefix(direction, "in_")
		} else if strings.HasPrefix(direction, "out_") {
			from = from + " out " + strings.TrimPrefix(direction, "out_")
		}
		key := port + "/" + proto + "/" + action + "/" + from
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, UFWActualRule{
			Port:   port,
			Proto:  proto,
			Action: action,
			From:   from,
		})
	}
	return out
}

func executeUFWStatus(start time.Time) (*Result, error) {
	if _, err := exec.LookPath("ufw"); err != nil {
		data, _ := json.Marshal(UFWStatusResult{
			Installed: false,
			Expected:  ExpectedUFWRules,
			Actual:    []UFWActualRule{},
			Matched:   []UFWExpectedRule{},
			Missing:   []UFWExpectedRule{},
			Extra:     []UFWActualRule{},
		})
		return &Result{ExitCode: 0, Stdout: string(data), DurationMs: time.Since(start).Milliseconds()}, nil
	}

	result := UFWStatusResult{
		Installed: true,
		Expected:  ExpectedUFWRules,
		Actual:    []UFWActualRule{},
		Matched:   []UFWExpectedRule{},
		Missing:   []UFWExpectedRule{},
		Extra:     []UFWActualRule{},
	}

	conf, confErr := sudoCat("/etc/ufw/ufw.conf")
	if confErr == nil {
		result.Enabled = parseUFWConfEnabled(conf)
	}

	rules, rulesErr := sudoCat("/etc/ufw/user.rules")
	if rulesErr == nil {
		result.Actual = parseUFWRules(rules)
		result.RawOutput = rules
	} else {
		result.RawOutput = fmt.Sprintf("failed to read /etc/ufw/user.rules: %s", rulesErr)
	}

	// Diff expected vs actual.
	isMatch := func(e UFWExpectedRule, a UFWActualRule) bool {
		return e.Port == a.Port && e.Proto == a.Proto && a.Action == "ALLOW" && a.From == e.From
	}

	usedActual := make([]bool, len(result.Actual))
	for _, e := range result.Expected {
		matched := false
		for i, a := range result.Actual {
			if usedActual[i] {
				continue
			}
			if isMatch(e, a) {
				result.Matched = append(result.Matched, e)
				usedActual[i] = true
				matched = true
				break
			}
		}
		if !matched {
			result.Missing = append(result.Missing, e)
		}
	}
	for i, a := range result.Actual {
		if !usedActual[i] {
			result.Extra = append(result.Extra, a)
		}
	}

	data, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	return &Result{ExitCode: 0, Stdout: string(data), DurationMs: time.Since(start).Milliseconds()}, nil
}
