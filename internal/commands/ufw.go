package commands

import (
	"encoding/json"
	"errors"
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
	Proto   string `json:"proto"`            // "tcp" | "udp"
	From    string `json:"from"`             // "Anywhere" or CIDR
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

// UFWActualRule is a parsed line from `ufw status`.
type UFWActualRule struct {
	Port   string `json:"port"`
	Proto  string `json:"proto"`
	Action string `json:"action"` // "ALLOW" | "DENY" | "REJECT"
	From   string `json:"from"`
}

// UFWStatusResult is the full payload returned to the dashboard.
type UFWStatusResult struct {
	Installed bool              `json:"installed"`
	Enabled   bool              `json:"enabled"`
	RawOutput string            `json:"rawOutput"`
	Actual    []UFWActualRule   `json:"actual"`
	Expected  []UFWExpectedRule `json:"expected"`
	// Matched: expected rules present in actual.
	Matched []UFWExpectedRule `json:"matched"`
	// Missing: expected but not present in actual.
	Missing []UFWExpectedRule `json:"missing"`
	// Extra: actual rules not in the expected set (worth auditing).
	Extra []UFWActualRule `json:"extra"`
}

// ufwRuleLine matches lines like "22/tcp   ALLOW IN   Anywhere" and
// "9100/tcp   ALLOW IN   10.99.0.0/24".
var ufwRuleLine = regexp.MustCompile(`^\s*(\d+)/(tcp|udp)\s+(ALLOW|DENY|REJECT|LIMIT)(?:\s+IN)?\s+(.+?)\s*$`)

func executeUFWStatus(start time.Time) (*Result, error) {
	if _, err := exec.LookPath("ufw"); err != nil {
		data, _ := json.Marshal(UFWStatusResult{
			Installed: false,
			Expected:  ExpectedUFWRules,
		})
		return &Result{ExitCode: 0, Stdout: string(data), DurationMs: time.Since(start).Milliseconds()}, nil
	}

	// `ufw status` requires root; agent runs as the keni user. The installer
	// grants keni NOPASSWD sudo, so -n keeps this non-interactive and the
	// call still fails cleanly if sudo is ever locked down.
	out, _ := exec.Command("sudo", "-n", "ufw", "status").CombinedOutput()
	raw := string(out)

	result := UFWStatusResult{
		Installed: true,
		RawOutput: raw,
		Expected:  ExpectedUFWRules,
		// Non-nil slices so the JSON encoder emits [] rather than null,
		// which the dashboard expects when calling .length / .some.
		Actual:  []UFWActualRule{},
		Matched: []UFWExpectedRule{},
		Missing: []UFWExpectedRule{},
		Extra:   []UFWActualRule{},
	}

	for _, line := range strings.Split(raw, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "Status:") {
			result.Enabled = strings.Contains(trimmed, "active")
			continue
		}
		m := ufwRuleLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		result.Actual = append(result.Actual, UFWActualRule{
			Port:   m[1],
			Proto:  m[2],
			Action: m[3],
			From:   strings.TrimSpace(m[4]),
		})
	}

	// Diff expected vs actual. An expected rule matches if ANY actual rule
	// has the same (port, proto, from). Public-facing rules accept "Anywhere"
	// as well as the IPv4-only variant ufw sometimes emits.
	isMatch := func(e UFWExpectedRule, a UFWActualRule) bool {
		if e.Port != a.Port || e.Proto != a.Proto || a.Action != "ALLOW" {
			return false
		}
		if e.From == "Anywhere" {
			return a.From == "Anywhere" || a.From == "Anywhere (v6)"
		}
		return a.From == e.From
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
