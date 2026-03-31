#!/bin/sh
set -e

# Keni Agent uninstaller
# Usage: sudo sh uninstall.sh [--remove-user]

AGENT_USER="keni"
REMOVE_USER=false

log() {
    echo "[keni-agent] $1"
}

# Parse arguments
while [ $# -gt 0 ]; do
    case "$1" in
        --remove-user)
            REMOVE_USER=true
            shift
            ;;
        *)
            echo "Usage: $0 [--remove-user]" >&2
            exit 1
            ;;
    esac
done

# Must run as root
if [ "$(id -u)" -ne 0 ]; then
    echo "[keni-agent] ERROR: This script must be run as root (use sudo)" >&2
    exit 1
fi

# Stop and disable the service
if systemctl is-active --quiet keni-agent 2>/dev/null; then
    log "Stopping keni-agent service..."
    systemctl stop keni-agent
fi

if systemctl is-enabled --quiet keni-agent 2>/dev/null; then
    log "Disabling keni-agent service..."
    systemctl disable keni-agent
fi

# Remove service file
if [ -f /etc/systemd/system/keni-agent.service ]; then
    rm -f /etc/systemd/system/keni-agent.service
    systemctl daemon-reload
    log "Removed systemd service"
fi

# Bring down WireGuard interface
if ip link show wg0 >/dev/null 2>&1; then
    log "Bringing down wg0 interface..."
    wg-quick down wg0 2>/dev/null || true
fi

# Remove WireGuard config
if [ -f /etc/wireguard/wg0.conf ]; then
    rm -f /etc/wireguard/wg0.conf
    log "Removed WireGuard config"
fi

# Remove agent binary and rollback backup
if [ -f /usr/local/bin/keni-agent ]; then
    rm -f /usr/local/bin/keni-agent
    log "Removed agent binary"
fi
if [ -f /usr/local/bin/keni-agent.prev ]; then
    rm -f /usr/local/bin/keni-agent.prev
    log "Removed rollback backup binary"
fi

# Remove config directory
if [ -d /etc/keni-agent ]; then
    rm -rf /etc/keni-agent
    log "Removed config directory"
fi

# Optionally remove the keni user
if [ "$REMOVE_USER" = true ]; then
    if id "$AGENT_USER" >/dev/null 2>&1; then
        # Remove sudoers file
        rm -f "/etc/sudoers.d/$AGENT_USER"
        # Remove user and home directory
        userdel -r "$AGENT_USER" 2>/dev/null || true
        log "Removed user $AGENT_USER"
    fi
else
    log "User $AGENT_USER was not removed. Run with --remove-user to remove it."
fi

log "Uninstall complete."
