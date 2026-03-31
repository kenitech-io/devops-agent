# devops-agent: Keni Agent

## What this is

Lightweight Go agent that runs on client servers. Connects to the Keni Dashboard over WireGuard. Single entry point for all platform management on client infrastructure.

## Design docs (source of truth)

- Design document: ~/projects/keni/devops-docs/platform/keni-dashboard-design.md
- Agent protocol spec: ~/projects/keni/devops-docs/platform/agent-protocol-spec.md
- Also on GitHub: https://github.com/kenitech-io/devops-docs/blob/main/platform/

Read both docs before making any architectural decisions.

## Stack

- Go (latest stable)
- Single static binary, cross-compiled for linux/amd64 and linux/arm64
- No external runtime dependencies
- Runs as systemd service (keni-agent.service)

## Architecture

- Agent always initiates connections outbound. Dashboard never connects to agent.
- Registration: POST to dashboard's public HTTPS endpoint with token and WireGuard public key.
- After registration: WebSocket connection over WireGuard for heartbeats, status reports, commands.
- Commands are fixed actions with validated parameters. No shell interpretation ever. Use exec.Command with argument arrays.
- Config file: /etc/keni-agent/config.yml
- Logs to stdout/journald

## Feature build order (all complete)

1. Install script and systemd service
2. Registration flow
3. WebSocket connection and heartbeat
4. Status reporting
5. Command execution
6. Streaming commands
7. Self-update

## Implementation issue

https://github.com/kenitech-io/devops-agent/issues/1 (closed)

## Dev mode

For development over Tailscale without WireGuard, set these env vars in /etc/keni-agent/env:
- `KENI_SKIP_WIREGUARD=true` - skips keypair generation and wg0 setup
- `KENI_WS_ENDPOINT=ws://<tailscale-ip>:8080/ws/agent` - connects directly via Tailscale
- `KENI_DASHBOARD_URL=http://<tailscale-ip>:3002` - points to dev dashboard

The dashboard auto-detects localhost and includes --dashboard-url in the install command.

## Deployment

- Repo is public (agent binary has no secrets)
- Binaries published via GitHub Releases (goreleaser on tag push)
- Install script downloads from GitHub Releases, defaults to dashboard.kenitech.io
- Pi5 demo: agent deployed via Tailscale, connected to local dashboard
- SSH key for Ansible: ~/.ssh/keni_dashboard (ed25519), public key in dashboard .env as DASHBOARD_SSH_PUBLIC_KEY

## Conventions

- No em dash in any text, document, or code. Use period, comma, or colon.
- Git user: martin@kenitech.io
- Always add gitleaks CI workflow
- Never commit secrets to git
- Test end-to-end before calling anything done
- Use Go idioms: error handling, interfaces, table-driven tests

## Local paths

All work runs from the Google Drive tech department directory. Repo clones are at:

- This repo: ~/projects/keni/devops-agent
- devops-keni: ~/projects/keni/devops-keni
- devops-infra: ~/projects/keni/devops-infra
- devops-docs: ~/projects/keni/devops-docs
- devops-monitoring: ~/projects/keni/devops-monitoring
- devops-cd: ~/projects/keni/devops-cd
- devops-ci: ~/projects/keni/devops-ci
- devops-tools: ~/projects/keni/devops-tools
- devops-iac: ~/projects/keni/devops-iac
- devops-secrets: ~/projects/keni/devops-secrets
- devops-backup-tools: ~/projects/keni/devops-backup-tools

## Related repos

- devops-keni: the dashboard this agent connects to
- devops-infra: Ansible roles that run over the WireGuard tunnel to this server
- devops-docs: design docs and protocol spec
