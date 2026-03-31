# Keni Agent

Lightweight Go agent that runs on client servers, connects to the [Keni Dashboard](https://github.com/kenitech-io/devops-keni) over WireGuard, and serves as the single entry point for platform management.

## Architecture

```
Dashboard Server (VPS)              Client Server
+----------------------------+      +----------------------------+
| Keni Dashboard (Next.js)   |      | Keni Agent (Go binary)     |
| PostgreSQL                 |      | wg0: 10.99.0.X             |
| WireGuard hub (10.99.0.1)  |<---->| Docker, Traefik, etc.      |
| Ansible                    | WG   |                            |
+----------------------------+      +----------------------------+
```

- Agent always initiates connections outbound
- Registration over HTTPS (public internet)
- After registration: WebSocket over WireGuard for heartbeats, status, commands
- All commands are whitelisted actions with validated parameters, no shell interpretation

## Installation

On the client server (requires root):

```bash
curl -fsSL https://raw.githubusercontent.com/kenitech-io/devops-agent/main/install.sh | sh -s -- \
  --token keni_abc123... \
  --dashboard-url https://dashboard.kenitech.io \
  --ssh-key "ssh-ed25519 AAAA... keni-dashboard"
```

To install a specific version:

```bash
KENI_AGENT_VERSION=v0.1.0 curl -fsSL https://raw.githubusercontent.com/kenitech-io/devops-agent/main/install.sh | sh -s -- \
  --token keni_abc123... \
  --dashboard-url https://dashboard.kenitech.io \
  --ssh-key "ssh-ed25519 AAAA... keni-dashboard"
```

The install script:
1. Installs WireGuard if not present
2. Downloads the agent binary for the correct architecture (amd64/arm64)
3. Creates `/etc/keni-agent/` config directory with the registration token
4. Creates a `keni` user with sudo access and adds the dashboard SSH key
5. Installs and starts the systemd service
6. The agent registers with the dashboard, configures wg0, and begins reporting

## Uninstall

```bash
sudo sh uninstall.sh             # keeps the keni user
sudo sh uninstall.sh --remove-user  # also removes the keni user
```

## Configuration

After registration, the agent stores its config at `/etc/keni-agent/config.yml`:

```yaml
agent_id: ag_abc123
assigned_ip: 10.99.0.5
dashboard_endpoint: 203.0.113.10:51820
ws_endpoint: wss://10.99.0.1:443/ws/agent
wireguard_private_key: <base64>
wireguard_public_key: <base64>
dashboard_public_key: <base64>
dashboard_url: https://dashboard.kenitech.io
```

## Agent Actions

| Action | Params | Streaming | Description |
|--------|--------|-----------|-------------|
| `container_list` | none | no | List all containers with status |
| `container_stats` | none | no | Container resource usage |
| `container_restart` | `name` | no | Restart a specific container |
| `backup_snapshots` | none | no | List Restic snapshots |
| `backup_stats` | none | no | Backup repo size and health |
| `backup_trigger` | none | yes | Start manual backup, stream output |
| `system_disk` | none | no | Disk usage |
| `system_memory` | none | no | Memory usage |
| `system_info` | none | no | OS, kernel, uptime, load (JSON) |
| `service_status` | `name` | no | Check systemd service status |
| `wireguard_status` | none | no | WireGuard interface status |
| `docker_logs` | `name`, `lines` | no | Last N lines of container logs |

## Development

### Prerequisites

- Go 1.21+
- Make

### Build

```bash
make build            # build for current platform
make build-all        # cross-compile linux/amd64 + linux/arm64
make test             # run tests with race detector
make lint             # go vet
make clean            # remove build artifacts
```

### Testing with mock dashboard

The repo includes a mock dashboard server for end-to-end testing:

```bash
# Terminal 1: start mock dashboard
go run ./cmd/mock-dashboard --listen :8080

# Terminal 2: run the agent (skip WireGuard for local testing)
KENI_AGENT_TOKEN=keni_testtoken KENI_DASHBOARD_URL=http://localhost:8080 go run ./cmd/keni-agent

# In mock dashboard terminal, send commands:
> list
> send ag_abc123 container_list
> send ag_abc123 system_info
> send ag_abc123 docker_logs name=traefik lines=50
> ping ag_abc123
```

### Automated E2E test

```bash
./scripts/e2e-test.sh
```

Builds both binaries, starts mock dashboard, registers agent, verifies heartbeat, health endpoint, and metrics. Exits 0 on success.

### Project structure

```
cmd/
  keni-agent/          - Main agent binary
  mock-dashboard/      - Test dashboard server with interactive CLI
internal/
  config/              - YAML config persistence
  register/            - Registration flow (POST /api/agent/register)
  wireguard/           - WireGuard keypair generation, wg0 config, watchdog
  ws/                  - WebSocket client, message types, reconnection
  collector/           - System metrics, container, backup, WireGuard data
  commands/            - Whitelisted command execution with parameter validation
  update/              - Self-update: download, verify, replace, restart
  metrics/             - Prometheus metrics and health check endpoint
scripts/
  e2e-test.sh          - Automated end-to-end test
```

## Observability

### Health check

```bash
curl http://localhost:9100/healthz
```

Returns JSON with agent status, version, uptime, WebSocket connection state, and last heartbeat timestamp. Returns 200 when connected, 503 when degraded.

### Prometheus metrics

```bash
curl http://localhost:9100/metrics
```

Exposed metrics:
- `keni_agent_heartbeats_total` - heartbeats sent
- `keni_agent_status_reports_total` - status reports sent
- `keni_agent_commands_total{action,status}` - commands executed (by action and success/error)
- `keni_agent_command_duration_ms{action}` - command execution time histogram
- `keni_agent_websocket_connected` - 1 when connected, 0 when disconnected
- `keni_agent_websocket_reconnections_total` - reconnection attempts
- `keni_agent_last_heartbeat_timestamp` - unix timestamp of last heartbeat
- `keni_agent_info{version,agent_id}` - agent metadata

### Release

Tag a version to trigger the release workflow:

```bash
git tag v0.1.0
git push origin v0.1.0
```

GitHub Actions runs tests, then Goreleaser builds static binaries for linux/amd64 and linux/arm64 and creates a GitHub release.

## Protocol

See [agent-protocol-spec.md](https://github.com/kenitech-io/devops-docs/blob/main/platform/agent-protocol-spec.md) for the full protocol specification.

## Design

See [keni-dashboard-design.md](https://github.com/kenitech-io/devops-docs/blob/main/platform/keni-dashboard-design.md) for the overall architecture and design decisions.
