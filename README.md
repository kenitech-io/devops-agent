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
  --ssh-key "ssh-ed25519 AAAA... keni-dashboard"
```

### Install options

| Flag | Required | Default | Description |
|------|----------|---------|-------------|
| `--token` | yes | - | Registration token from the dashboard |
| `--ssh-key` | yes | - | Dashboard SSH public key for Ansible access |
| `--dashboard-url` | no | `https://dashboard.kenitech.io` | Dashboard URL for registration |

Set `KENI_AGENT_VERSION=v0.1.0` to pin a specific version (defaults to latest release).

If the binary is already at `/usr/local/bin/keni-agent`, the download step is skipped.

### What the installer does

1. Installs WireGuard if not present
2. Downloads the agent binary from GitHub Releases (linux/amd64 or linux/arm64)
3. Creates `/etc/keni-agent/` config directory with the registration token
4. Creates a `keni` user with passwordless sudo and adds the dashboard SSH key
5. Installs and enables the systemd service
6. Starts the agent, which registers with the dashboard, configures wg0, and begins reporting

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

### Environment variables

Set in `/etc/keni-agent/env` (read by the systemd service):

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `KENI_AGENT_TOKEN` | on first run | - | Registration token (removed after registration) |
| `KENI_DASHBOARD_URL` | on first run | - | Dashboard URL for registration |
| `KENI_LOG_FORMAT` | no | `text` | Log format: `text` (dev) or `json` (production) |
| `KENI_LOG_LEVEL` | no | `info` | Log level: `debug`, `info`, `warn`, `error` |
| `KENI_SKIP_WIREGUARD` | no | - | Set to `true` to skip WireGuard setup (dev mode) |
| `KENI_WS_ENDPOINT` | no | - | Override WebSocket URL (dev mode, e.g., Tailscale IP) |

## Dev mode

For development over Tailscale (no WireGuard):

```bash
# /etc/keni-agent/env
KENI_AGENT_TOKEN=keni_abc123...
KENI_DASHBOARD_URL=http://100.x.x.x:3002
KENI_SKIP_WIREGUARD=true
KENI_WS_ENDPOINT=ws://100.x.x.x:8080/ws/agent
KENI_LOG_FORMAT=json
```

This skips WireGuard keypair generation and interface setup, and connects the WebSocket directly to the dashboard's Tailscale IP. The dashboard auto-detects localhost and includes `--dashboard-url` in the install command.

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

## Development

### Prerequisites

- Go 1.21+
- Make

### Build

```bash
make build            # build for current platform
make build-all        # cross-compile linux/amd64 + linux/arm64
make mock-dashboard   # build mock dashboard server
make test             # run tests with race detector
make lint             # go vet
make clean            # remove build artifacts
```

### Testing with mock dashboard

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

### Project structure

```
cmd/
  keni-agent/          - Main agent binary
  mock-dashboard/      - Test dashboard server with interactive CLI
internal/
  config/              - YAML config persistence and validation
  register/            - Registration flow (POST /api/agent/register)
  wireguard/           - WireGuard keypair generation, wg0 config, watchdog
  ws/                  - WebSocket client, message types, reconnection
  collector/           - System metrics, container, backup, WireGuard data
  commands/            - Whitelisted command execution with parameter validation
  update/              - Self-update: download, verify, replace, restart
  metrics/             - Prometheus metrics and health check endpoint
  logging/             - Structured logging (slog) configuration
scripts/
  e2e-test.sh          - Automated end-to-end test
```

### Release

Tag a version to trigger the release workflow:

```bash
git tag v0.2.0
git push origin v0.2.0
```

GitHub Actions runs tests, then Goreleaser builds static binaries for linux/amd64 and linux/arm64 and creates a GitHub release.

## Protocol

See [agent-protocol-spec.md](https://github.com/kenitech-io/devops-docs/blob/main/platform/agent-protocol-spec.md) for the full protocol specification.

## Design

See [keni-dashboard-design.md](https://github.com/kenitech-io/devops-docs/blob/main/platform/keni-dashboard-design.md) for the overall architecture and design decisions.
