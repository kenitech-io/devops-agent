#!/bin/sh
set -e

# Keni IDP Setup Script
# Bootstraps a server into a Keni-managed IDP node.
# Installs Docker, hardens the OS, installs the Keni Agent.
# After this, everything is managed remotely via the dashboard.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/kenidevops/devops-agent/main/setup.sh | sh -s -- \
#     --role CORE \
#     --token keni_xxxxx \
#     --repo-url https://github.com/kenidevops/idp-acme-corp \
#     --deploy-token ghp_xxxxx
#
# Roles: CORE (IDP control plane), PROD (production), STG (staging), DEV (development)
# Options:
#   --token        (required) Registration token from the dashboard
#   --role         (required) Server role: CORE, PROD, STG, DEV
#   --dashboard-url Dashboard URL (default: https://dashboard.kenitech.io)
#   --repo-url     Client IDP repo URL for GitOps
#   --deploy-token GitHub token for repo access
#   --ssh-key      Dashboard SSH public key for remote access
#   --skip-docker  Skip Docker installation
#   --skip-hardening Skip OS hardening

AGENT_USER="keni"
INSTALL_DIR="/usr/local/bin"
CONFIG_DIR="/etc/keni-agent"
SERVICE_FILE="/etc/systemd/system/keni-agent.service"
GITHUB_REPO="kenidevops/devops-agent"
VERSION="${KENI_AGENT_VERSION:-latest}"

# Defaults
TOKEN=""
ROLE=""
DASHBOARD_URL=""
SSH_KEY=""
REPO_URL=""
DEPLOY_TOKEN=""
SKIP_DOCKER=false
SKIP_HARDENING=false

log() { echo "[keni] $1"; }
error() { echo "[keni] ERROR: $1" >&2; exit 1; }

# Parse arguments
while [ $# -gt 0 ]; do
    case "$1" in
        --token)       TOKEN="$2"; shift 2 ;;
        --role)        ROLE=$(echo "$2" | tr '[:lower:]' '[:upper:]'); shift 2 ;;
        --dashboard-url) DASHBOARD_URL="$2"; shift 2 ;;
        --ssh-key)     SSH_KEY="$2"; shift 2 ;;
        --repo-url)    REPO_URL="$2"; shift 2 ;;
        --deploy-token) DEPLOY_TOKEN="$2"; shift 2 ;;
        --skip-docker) SKIP_DOCKER=true; shift ;;
        --skip-hardening) SKIP_HARDENING=true; shift ;;
        *)             error "Unknown argument: $1" ;;
    esac
done

[ -z "$TOKEN" ] && error "Missing required argument: --token"
[ -z "$ROLE" ] && error "Missing required argument: --role (CORE, PROD, STG, DEV)"
[ -z "$DASHBOARD_URL" ] && DASHBOARD_URL="https://dashboard.kenitech.io"

case "$ROLE" in
    CORE|PROD|STG|DEV) ;;
    *) error "Invalid role: $ROLE. Must be CORE, PROD, STG, or DEV" ;;
esac

[ "$(id -u)" -ne 0 ] && error "This script must be run as root (use sudo)"

detect_arch() {
    case "$(uname -m)" in
        x86_64|amd64) echo "amd64" ;;
        aarch64|arm64) echo "arm64" ;;
        *) error "Unsupported architecture: $(uname -m)" ;;
    esac
}

detect_os() {
    [ -f /etc/os-release ] || error "Cannot detect OS"
    . /etc/os-release
    echo "$ID"
}

OS_ID=$(detect_os)

# ============================================================
# Phase 1: Docker
# ============================================================
install_docker() {
    if command -v docker >/dev/null 2>&1; then
        log "Docker already installed: $(docker --version)"
        return
    fi

    log "Installing Docker..."
    case "$OS_ID" in
        ubuntu|debian)
            apt-get update -qq
            apt-get install -y -qq ca-certificates curl gnupg
            install -m 0755 -d /etc/apt/keyrings
            curl -fsSL "https://download.docker.com/linux/${OS_ID}/gpg" | gpg --dearmor -o /etc/apt/keyrings/docker.gpg
            chmod a+r /etc/apt/keyrings/docker.gpg
            echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] https://download.docker.com/linux/${OS_ID} $(. /etc/os-release && echo "$VERSION_CODENAME") stable" > /etc/apt/sources.list.d/docker.list
            apt-get update -qq
            apt-get install -y -qq docker-ce docker-ce-cli containerd.io docker-compose-plugin
            ;;
        centos|rhel|rocky|alma)
            yum install -y yum-utils
            yum-config-manager --add-repo https://download.docker.com/linux/centos/docker-ce.repo
            yum install -y docker-ce docker-ce-cli containerd.io docker-compose-plugin
            ;;
        fedora)
            dnf install -y dnf-plugins-core
            dnf config-manager --add-repo https://download.docker.com/linux/fedora/docker-ce.repo
            dnf install -y docker-ce docker-ce-cli containerd.io docker-compose-plugin
            ;;
        *) error "Unsupported OS for Docker install: $OS_ID" ;;
    esac

    systemctl enable docker
    systemctl start docker
    log "Docker installed: $(docker --version)"
}

# ============================================================
# Phase 2: OS Hardening
# ============================================================
apply_hardening() {
    log "Applying security hardening..."

    # SSH: disable root login and password auth
    SSHD_CONFIG="/etc/ssh/sshd_config"
    if [ -f "$SSHD_CONFIG" ]; then
        sed -i 's/^#\?PermitRootLogin.*/PermitRootLogin prohibit-password/' "$SSHD_CONFIG"
        sed -i 's/^#\?PasswordAuthentication.*/PasswordAuthentication no/' "$SSHD_CONFIG"
        sed -i 's/^#\?PubkeyAuthentication.*/PubkeyAuthentication yes/' "$SSHD_CONFIG"
        systemctl reload sshd 2>/dev/null || systemctl reload ssh 2>/dev/null || true
        log "SSH hardened: root login via key only, password auth disabled"
    fi

    # Firewall: allow SSH + HTTP + HTTPS + WireGuard
    if command -v ufw >/dev/null 2>&1; then
        ufw --force reset >/dev/null 2>&1
        ufw default deny incoming >/dev/null
        ufw default allow outgoing >/dev/null
        ufw allow 22/tcp >/dev/null    # SSH
        ufw allow 80/tcp >/dev/null    # HTTP
        ufw allow 443/tcp >/dev/null   # HTTPS
        ufw allow 51820/udp >/dev/null # WireGuard
        ufw --force enable >/dev/null
        log "UFW firewall configured: SSH, HTTP, HTTPS, WireGuard"
    elif command -v firewall-cmd >/dev/null 2>&1; then
        firewall-cmd --permanent --add-service=ssh >/dev/null
        firewall-cmd --permanent --add-service=http >/dev/null
        firewall-cmd --permanent --add-service=https >/dev/null
        firewall-cmd --permanent --add-port=51820/udp >/dev/null
        firewall-cmd --reload >/dev/null
        log "Firewalld configured: SSH, HTTP, HTTPS, WireGuard"
    else
        log "No firewall found (ufw/firewalld), skipping"
    fi

    # Automatic security updates
    case "$OS_ID" in
        ubuntu|debian)
            apt-get install -y -qq unattended-upgrades >/dev/null 2>&1
            log "Unattended upgrades enabled"
            ;;
    esac
}

# ============================================================
# Phase 3: Git
# ============================================================
install_git() {
    if command -v git >/dev/null 2>&1; then
        log "Git already installed: $(git --version)"
        return
    fi

    log "Installing git..."
    case "$OS_ID" in
        ubuntu|debian) apt-get install -y -qq git ;;
        centos|rhel|rocky|alma) yum install -y git ;;
        fedora) dnf install -y git ;;
        *) error "Unsupported OS for git install: $OS_ID" ;;
    esac
    log "Git installed: $(git --version)"
}

# ============================================================
# Phase 4: WireGuard (renumbered from 3)
# ============================================================
install_wireguard() {
    if command -v wg >/dev/null 2>&1; then
        log "WireGuard already installed"
        return
    fi

    log "Installing WireGuard..."
    case "$OS_ID" in
        ubuntu|debian) apt-get install -y -qq wireguard wireguard-tools ;;
        centos|rhel|rocky|alma) yum install -y epel-release && yum install -y wireguard-tools ;;
        fedora) dnf install -y wireguard-tools ;;
        *) error "Unsupported OS for WireGuard: $OS_ID" ;;
    esac
    log "WireGuard installed"
}

# ============================================================
# Phase 5: Keni Agent
# ============================================================
install_agent() {
    # Always download latest binary (handles upgrades on re-install)
    rm -f "${INSTALL_DIR}/keni-agent"

    ARCH=$(detect_arch)

    if [ "$VERSION" = "latest" ]; then
        log "Fetching latest release..."
        VERSION=$(curl -fsSL "https://api.github.com/repos/${GITHUB_REPO}/releases/latest" | grep '"tag_name"' | head -1 | sed 's/.*"tag_name": *"//;s/".*//')
        [ -z "$VERSION" ] && error "Could not determine latest version"
        log "Latest version: ${VERSION}"
    fi

    TARBALL="keni-agent_${VERSION#v}_linux_${ARCH}.tar.gz"
    URL="https://github.com/${GITHUB_REPO}/releases/download/${VERSION}/${TARBALL}"

    log "Downloading keni-agent ${VERSION} for linux/${ARCH}..."
    TMPDIR=$(mktemp -d)
    curl -fsSL -o "${TMPDIR}/${TARBALL}" "$URL"
    tar -xzf "${TMPDIR}/${TARBALL}" -C "${TMPDIR}"
    mv "${TMPDIR}/keni-agent" "${INSTALL_DIR}/keni-agent"
    rm -rf "${TMPDIR}"
    chmod 755 "${INSTALL_DIR}/keni-agent"
    log "Agent installed: ${VERSION}"
}

setup_config() {
    mkdir -p "$CONFIG_DIR"
    chmod 750 "$CONFIG_DIR"

    # Environment file with token, dashboard URL, server role, and GitOps config
    cat > "${CONFIG_DIR}/env" <<EOF
KENI_AGENT_TOKEN=${TOKEN}
KENI_DASHBOARD_URL=${DASHBOARD_URL}
KENI_SERVER_ROLE=${ROLE}
EOF

    # Add optional GitOps config
    if [ -n "$REPO_URL" ]; then
        echo "KENI_IDP_REPO_URL=${REPO_URL}" >> "${CONFIG_DIR}/env"
    fi
    if [ -n "$DEPLOY_TOKEN" ]; then
        echo "KENI_DEPLOY_TOKEN=${DEPLOY_TOKEN}" >> "${CONFIG_DIR}/env"
    fi

    chmod 600 "${CONFIG_DIR}/env"
}

setup_user() {
    if ! id "$AGENT_USER" >/dev/null 2>&1; then
        useradd -r -m -s /bin/bash "$AGENT_USER"
        log "Created user $AGENT_USER"
    fi

    # Sudo access for remote management
    cat > "/etc/sudoers.d/$AGENT_USER" <<EOF
${AGENT_USER} ALL=(ALL) NOPASSWD: ALL
EOF
    chmod 440 "/etc/sudoers.d/$AGENT_USER"

    # Docker group access
    usermod -aG docker "$AGENT_USER" 2>/dev/null || true

    # SSH key for dashboard access
    if [ -n "$SSH_KEY" ]; then
        KENI_HOME=$(eval echo "~${AGENT_USER}")
        SSH_DIR="${KENI_HOME}/.ssh"
        mkdir -p "$SSH_DIR"
        chmod 700 "$SSH_DIR"
        AUTHORIZED_KEYS="${SSH_DIR}/authorized_keys"
        if [ -f "$AUTHORIZED_KEYS" ] && grep -qF "$SSH_KEY" "$AUTHORIZED_KEYS" 2>/dev/null; then
            log "SSH key already configured"
        else
            echo "$SSH_KEY" >> "$AUTHORIZED_KEYS"
            log "Added dashboard SSH key"
        fi
        chmod 600 "$AUTHORIZED_KEYS"
        chown -R "${AGENT_USER}:${AGENT_USER}" "$SSH_DIR"
    fi
}

install_service() {
    cat > "$SERVICE_FILE" <<'SERVICEEOF'
[Unit]
Description=Keni Agent
After=network-online.target docker.service
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
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
ReadWritePaths=/etc/keni-agent /etc/wireguard /var/lib/keni-agent
PrivateTmp=yes

[Install]
WantedBy=multi-user.target
SERVICEEOF

    systemctl daemon-reload
    systemctl enable keni-agent
}

# ============================================================
# Main
# ============================================================
main() {
    log "================================================"
    log "  Keni IDP Setup"
    log "  Role: ${ROLE}"
    log "  Dashboard: ${DASHBOARD_URL}"
    log "================================================"

    # Clean up previous installation if present
    if systemctl is-active --quiet keni-agent 2>/dev/null; then
        log "Stopping existing agent..."
        systemctl stop keni-agent
    fi
    if [ -f "${CONFIG_DIR}/config.yml" ]; then
        log "Removing old registration config (will re-register with new token)"
        rm -f "${CONFIG_DIR}/config.yml"
    fi

    # Phase 1: Docker
    if [ "$SKIP_DOCKER" = "false" ]; then
        install_docker
    fi

    # Phase 2: Hardening
    if [ "$SKIP_HARDENING" = "false" ]; then
        apply_hardening
    fi

    # Phase 3: Git (required for GitOps)
    install_git

    # Phase 4: WireGuard
    install_wireguard

    # Phase 5: Agent
    install_agent
    setup_config
    setup_user

    # Create GitOps data directory
    mkdir -p /var/lib/keni-agent
    chown "${AGENT_USER}:${AGENT_USER}" /var/lib/keni-agent

    install_service

    # Start
    systemctl start keni-agent
    sleep 3

    if systemctl is-active --quiet keni-agent; then
        log "Agent running and connected"
    else
        log "WARNING: agent may not have started. Check: journalctl -u keni-agent -f"
    fi

    log ""
    log "Setup complete. Server will appear in the dashboard shortly."
    log "Status: systemctl status keni-agent"
    log "Logs:   journalctl -u keni-agent -f"
}

main
