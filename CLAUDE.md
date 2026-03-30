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

## Feature build order

Build each feature complete and functional before moving to the next:

1. Install script and systemd service
2. Registration flow
3. WebSocket connection and heartbeat
4. Status reporting
5. Command execution
6. Streaming commands
7. Self-update

## Implementation issue

https://github.com/kenitech-io/devops-agent/issues/1

## Conventions

- No em dash in any text, document, or code. Use period, comma, or colon.
- Git user: martin@kenitech.io
- Always add gitleaks CI workflow
- Never commit secrets to git
- Test end-to-end before calling anything done
- Use Go idioms: error handling, interfaces, table-driven tests

## Related repos

- devops-keni: the dashboard this agent connects to
- devops-infra: Ansible roles that run over the WireGuard tunnel to this server
- devops-docs: design docs and protocol spec
