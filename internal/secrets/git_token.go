package secrets

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// GitTokenResponse is the API response from the dashboard git-token endpoint.
type GitTokenResponse struct {
	Token    string `json:"token"`
	RepoURL  string `json:"repoUrl"`
	Username string `json:"username"`
	Error    string `json:"error,omitempty"`
}

// FetchGitToken retrieves a fresh GitHub installation token from the dashboard.
// The token is short-lived (1 hour) and should be fetched before each git operation.
func FetchGitToken(dashboardURL, agentID, wsToken string) (*GitTokenResponse, error) {
	url := fmt.Sprintf("%s/api/agent/git-token?agentId=%s", strings.TrimRight(dashboardURL, "/"), agentID)

	client := &http.Client{Timeout: 15 * time.Second}

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating git-token request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+wsToken)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching git token: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return nil, fmt.Errorf("reading git-token response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("git-token API returned %d: %s", resp.StatusCode, string(body))
	}

	var result GitTokenResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parsing git-token response: %w", err)
	}

	return &result, nil
}
