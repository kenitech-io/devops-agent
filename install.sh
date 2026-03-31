#!/bin/sh
set -e

# Keni Agent installer
# Usage: curl -s https://agent.kenitech.io/install.sh | sh -s -- --token <TOKEN> --dashboard-url <URL> --ssh-key <PUBKEY>

AGENT_USER="keni"
INSTALL_DIR="/usr/local/bin"
CONFIG_DIR="/etc/keni-agent"
SERVICE_FILE="/etc/systemd/system/keni-agent.service"
DOWNLOAD_BASE="https://agent.kenitech.io/download"

# Defaults
TOKEN=""
DASHBOARD_URL=""
SSH_KEY=""

log() {
    echo "[keni-agent] $1"
}

error() {
    echo "[keni-agent] ERROR: $1" >&2
    exit 1
}

# Parse arguments
while [ $# -gt 0 ]; do
    case "$1" in
        --token)
            TOKEN="$2"
            shift 2
            ;;
        --dashboard-url)
            DASHBOARD_URL="$2"
            shift 2
            ;;
        --ssh-key)
            SSH_KEY="$2"
            shift 2
            ;;
        *)
            error "Unknown argument: $1"
            ;;
    esac
done

if [ -z "$TOKEN" ]; then
    error "Missing required argument: --token"
fi

if [ -z "$DASHBOARD_URL" ]; then
    error "Missing required argument: --dashboard-url"
fi

if [ -z "$SSH_KEY" ]; then
    error "Missing required argument: --ssh-key (dashboard SSH public key for Ansible access)"
fi

# Must run as root
if [ "$(id -u)" -ne 0 ]; then
    error "This script must be run as root (use sudo)"
fi

# Detect architecture
detect_arch() {
    ARCH=$(uname -m)
    case "$ARCH" in
        x86_64|amd64)
            echo "amd64"
            ;;
        aarch64|arm64)
            echo "arm64"
            ;;
        *)
            error "Unsupported architecture: $ARCH"
            ;;
    esac
}

# Detect OS
detect_os() {
    if [ ! -f /etc/os-release ]; then
        error "Cannot detect OS: /etc/os-release not found. Only Linux is supported."
    fi
    . /etc/os-release
    echo "$ID"
}

# Install WireGuard if not present
install_wireguard() {
    if command -v wg >/dev/null 2>&1; then
        log "WireGuard already installed"
        return
    fi

    log "Installing WireGuard..."
    OS_ID=$(detect_os)

    case "$OS_ID" in
        ubuntu|debian)
            apt-get update -qq
            apt-get install -y -qq wireguard wireguard-tools
            ;;
        centos|rhel|rocky|alma)
            yum install -y epel-release
            yum install -y wireguard-tools
            ;;
        fedora)
            dnf install -y wireguard-tools
            ;;
        *)
            error "Unsupported OS for automatic WireGuard install: $OS_ID. Install WireGuard manually and re-run."
            ;;
    esac

    if ! command -v wg >/dev/null 2>&1; then
        error "WireGuard installation failed"
    fi
    log "WireGuard installed successfully"
}

# Download and install the agent binary
install_binary() {
    ARCH=$(detect_arch)
    BINARY_URL="${DOWNLOAD_BASE}/keni-agent-linux-${ARCH}"

    log "Downloading keni-agent for linux/${ARCH}..."
    if command -v curl >/dev/null 2>&1; then
        curl -fsSL -o "${INSTALL_DIR}/keni-agent" "$BINARY_URL"
    elif command -v wget >/dev/null 2>&1; then
        wget -qO "${INSTALL_DIR}/keni-agent" "$BINARY_URL"
    else
        error "Neither curl nor wget found. Install one and re-run."
    fi

    chmod 755 "${INSTALL_DIR}/keni-agent"
    log "Binary installed to ${INSTALL_DIR}/keni-agent"
}

# Create config directory and environment file
setup_config() {
    mkdir -p "$CONFIG_DIR"
    chmod 750 "$CONFIG_DIR"

    # Write environment file with token and dashboard URL for first run
    cat > "${CONFIG_DIR}/env" <<EOF
KENI_AGENT_TOKEN=${TOKEN}
KENI_DASHBOARD_URL=${DASHBOARD_URL}
EOF
    chmod 600 "${CONFIG_DIR}/env"
    log "Config directory created at ${CONFIG_DIR}"
}

# Install systemd service
install_service() {
    cat > "$SERVICE_FILE" <<'SERVICEEOF'
[Unit]
Description=Keni Agent
Documentation=https://github.com/kenitech-io/devops-agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/keni-agent
Restart=always
RestartSec=5
EnvironmentFile=-/etc/keni-agent/env
StandardOutput=journal
StandardError=journal
SyslogIdentifier=keni-agent

# Security hardening
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
ReadWritePaths=/etc/keni-agent /etc/wireguard
PrivateTmp=yes

[Install]
WantedBy=multi-user.target
SERVICEEOF

    systemctl daemon-reload
    systemctl enable keni-agent
    log "Systemd service installed and enabled"
}

# Create keni user for Ansible SSH access over WireGuard
setup_ssh_access() {
    # Create keni user if it does not exist
    if id "$AGENT_USER" >/dev/null 2>&1; then
        log "User $AGENT_USER already exists"
    else
        useradd -r -m -s /bin/bash "$AGENT_USER"
        log "Created user $AGENT_USER"
    fi

    # Grant sudo access (passwordless, for Ansible)
    cat > "/etc/sudoers.d/$AGENT_USER" <<EOF
${AGENT_USER} ALL=(ALL) NOPASSWD: ALL
EOF
    chmod 440 "/etc/sudoers.d/$AGENT_USER"
    log "Granted sudo access to $AGENT_USER"

    # Set up SSH authorized_keys with the dashboard public key
    KENI_HOME=$(eval echo "~${AGENT_USER}")
    SSH_DIR="${KENI_HOME}/.ssh"
    mkdir -p "$SSH_DIR"
    chmod 700 "$SSH_DIR"

    AUTHORIZED_KEYS="${SSH_DIR}/authorized_keys"

    # Add key if not already present
    if [ -f "$AUTHORIZED_KEYS" ] && grep -qF "$SSH_KEY" "$AUTHORIZED_KEYS"; then
        log "Dashboard SSH key already in authorized_keys"
    else
        echo "$SSH_KEY" >> "$AUTHORIZED_KEYS"
        log "Added dashboard SSH key to authorized_keys"
    fi

    chmod 600 "$AUTHORIZED_KEYS"
    chown -R "${AGENT_USER}:${AGENT_USER}" "$SSH_DIR"
    log "SSH access configured for $AGENT_USER"
}

# Start the agent
start_agent() {
    log "Starting keni-agent..."
    systemctl start keni-agent

    # Wait briefly for the agent to register
    sleep 3

    if systemctl is-active --quiet keni-agent; then
        log "keni-agent is running"
    else
        log "WARNING: keni-agent may not have started correctly. Check logs with: journalctl -u keni-agent -f"
    fi
}

# Main
main() {
    log "Starting Keni Agent installation"
    log "Dashboard: ${DASHBOARD_URL}"

    install_wireguard
    install_binary
    setup_config
    setup_ssh_access
    install_service
    start_agent

    log ""
    log "Installation complete."
    log "The agent will register with the dashboard on first start."
    log "Check status: systemctl status keni-agent"
    log "View logs:    journalctl -u keni-agent -f"
}

main
