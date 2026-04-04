package secrets

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

// Secret represents a single secret key-value pair from the dashboard.
type Secret struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// SecretsResponse is the API response from the dashboard secrets endpoint.
type SecretsResponse struct {
	Secrets []Secret `json:"secrets"`
}

// varPattern matches ${VARIABLE_NAME} patterns in .env files.
var varPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// FetchSecrets retrieves decrypted secrets from the dashboard API.
func FetchSecrets(dashboardURL, agentID, wsToken string) ([]Secret, error) {
	url := fmt.Sprintf("%s/api/agent/secrets?agentId=%s", strings.TrimRight(dashboardURL, "/"), agentID)

	client := &http.Client{Timeout: 15 * time.Second}

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating secrets request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+wsToken)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching secrets: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("secrets API returned %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1 MB limit
	if err != nil {
		return nil, fmt.Errorf("reading secrets response: %w", err)
	}

	var result SecretsResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parsing secrets response: %w", err)
	}

	slog.Info("fetched secrets from dashboard", "count", len(result.Secrets))
	return result.Secrets, nil
}

// InjectSecrets reads an .env file, replaces ${VAR} patterns with matching
// secret values, and writes the resolved file back. Variables without a
// matching secret are left unchanged.
func InjectSecrets(envFilePath string, secrets []Secret) error {
	data, err := os.ReadFile(envFilePath)
	if err != nil {
		return fmt.Errorf("reading env file %s: %w", envFilePath, err)
	}

	secretMap := make(map[string]string, len(secrets))
	for _, s := range secrets {
		secretMap[s.Name] = s.Value
	}

	content := string(data)
	injected := 0

	resolved := varPattern.ReplaceAllStringFunc(content, func(match string) string {
		// Extract variable name from ${NAME}
		name := match[2 : len(match)-1]
		if val, ok := secretMap[name]; ok {
			injected++
			return val
		}
		return match
	})

	if injected == 0 {
		return nil
	}

	if err := os.WriteFile(envFilePath, []byte(resolved), 0600); err != nil {
		return fmt.Errorf("writing resolved env file %s: %w", envFilePath, err)
	}

	slog.Info("injected secrets into env file", "path", envFilePath, "count", injected)
	return nil
}
