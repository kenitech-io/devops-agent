package register

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// Request is the body sent to POST /api/agent/register.
type Request struct {
	Token         string `json:"token"`
	PublicKey     string `json:"publicKey"`
	Hostname      string `json:"hostname"`
	OS            string `json:"os"`
	Arch          string `json:"arch"`
	DockerVersion string `json:"dockerVersion"`
	KernelVersion string `json:"kernelVersion"`
	Role          string `json:"role,omitempty"` // CORE, PROD, STG, DEV
}

// Response is the body returned from POST /api/agent/register.
type Response struct {
	AgentID            string `json:"agentId"`
	AssignedIP         string `json:"assignedIp"`
	DashboardPublicKey string `json:"dashboardPublicKey"`
	DashboardEndpoint  string `json:"dashboardEndpoint"`
	WSEndpoint         string `json:"wsEndpoint"`
	WSToken            string `json:"wsToken"`
	GitRepoURL         string `json:"gitRepoUrl,omitempty"`
	DeployToken        string `json:"deployToken,omitempty"`
	ServerRole         string `json:"serverRole,omitempty"`
	DashboardURL       string `json:"dashboardUrl,omitempty"`
}

// ErrorResponse is returned on registration failure.
type ErrorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// SystemInfo holds information gathered from the local system.
type SystemInfo struct {
	Hostname      string
	OS            string
	Arch          string
	DockerVersion string
	KernelVersion string
}

// Register sends the registration request to the dashboard.
func Register(dashboardURL string, req Request) (*Response, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	url := strings.TrimRight(dashboardURL, "/") + "/api/agent/register"

	client := &http.Client{Timeout: 30 * time.Second}
	httpReq, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("sending request to %s: %w", url, err)
	}
	defer httpResp.Body.Close()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		var errResp ErrorResponse
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Message != "" {
			return nil, fmt.Errorf("registration failed (%d): %s", httpResp.StatusCode, errResp.Message)
		}
		return nil, fmt.Errorf("registration failed (%d): %s", httpResp.StatusCode, string(respBody))
	}

	var resp Response
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, fmt.Errorf("parsing response: %w", err)
	}

	if resp.AgentID == "" || resp.AssignedIP == "" {
		return nil, fmt.Errorf("incomplete registration response: missing agentId or assignedIp")
	}

	return &resp, nil
}

// GatherSystemInfo collects hostname, OS, arch, docker version, and kernel version.
func GatherSystemInfo() (*SystemInfo, error) {
	hostname, err := os.Hostname()
	if err != nil {
		return nil, fmt.Errorf("getting hostname: %w", err)
	}

	osInfo := readOSInfo()
	arch := runtime.GOARCH
	dockerVersion := getDockerVersion()
	kernelVersion := getKernelVersion()

	return &SystemInfo{
		Hostname:      hostname,
		OS:            osInfo,
		Arch:          arch,
		DockerVersion: dockerVersion,
		KernelVersion: kernelVersion,
	}, nil
}

// readOSInfo reads /etc/os-release to get a human-readable OS string.
func readOSInfo() string {
	data, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return runtime.GOOS
	}

	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "PRETTY_NAME=") {
			val := strings.TrimPrefix(line, "PRETTY_NAME=")
			val = strings.Trim(val, "\"")
			return val
		}
	}

	return runtime.GOOS
}

// getDockerVersion returns the Docker version or "unknown".
func getDockerVersion() string {
	out, err := exec.Command("docker", "version", "--format", "{{.Server.Version}}").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

// getKernelVersion returns the kernel version via uname -r.
func getKernelVersion() string {
	out, err := exec.Command("uname", "-r").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

// privateRanges are IP ranges that are NOT public.
var privateRanges []*net.IPNet

func init() {
	for _, cidr := range []string{
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"127.0.0.0/8",
		"169.254.0.0/16",
		"100.64.0.0/10",
		"fc00::/7",
		"fe80::/10",
		"::1/128",
	} {
		_, block, _ := net.ParseCIDR(cidr)
		privateRanges = append(privateRanges, block)
	}
}

func isPublicIP(ip net.IP) bool {
	if ip == nil || ip.IsUnspecified() || ip.IsMulticast() {
		return false
	}
	for _, r := range privateRanges {
		if r.Contains(ip) {
			return false
		}
	}
	return true
}

// PublicIP returns the first public IPv4 address found on network interfaces.
func PublicIP() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil {
				continue
			}
			// Prefer IPv4
			if ip4 := ip.To4(); ip4 != nil && isPublicIP(ip4) {
				return ip4.String()
			}
		}
	}
	return ""
}
