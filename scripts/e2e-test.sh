#!/bin/sh
set -e

# E2E test: starts mock dashboard, registers agent, verifies heartbeat and command execution.
# Skips WireGuard (uses localhost WebSocket only).
#
# Usage: ./scripts/e2e-test.sh

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
DASHBOARD_PORT=18080
DASHBOARD_PID=""
AGENT_PID=""
PASSED=0
FAILED=0

cleanup() {
    if [ -n "$AGENT_PID" ]; then
        kill "$AGENT_PID" 2>/dev/null || true
        wait "$AGENT_PID" 2>/dev/null || true
    fi
    if [ -n "$DASHBOARD_PID" ]; then
        kill "$DASHBOARD_PID" 2>/dev/null || true
        wait "$DASHBOARD_PID" 2>/dev/null || true
    fi
    rm -f "$DASHBOARD_LOG" "$AGENT_LOG"
}

trap cleanup EXIT

log() {
    echo "[e2e] $1"
}

pass() {
    PASSED=$((PASSED + 1))
    echo "  PASS: $1"
}

fail() {
    FAILED=$((FAILED + 1))
    echo "  FAIL: $1"
}

# Build binaries
log "Building binaries..."
cd "$PROJECT_DIR"
go build -o /tmp/e2e-mock-dashboard ./cmd/mock-dashboard
go build -o /tmp/e2e-keni-agent ./cmd/keni-agent

DASHBOARD_LOG=$(mktemp)
AGENT_LOG=$(mktemp)

# Start mock dashboard
log "Starting mock dashboard on port $DASHBOARD_PORT..."
/tmp/e2e-mock-dashboard --listen ":$DASHBOARD_PORT" > "$DASHBOARD_LOG" 2>&1 &
DASHBOARD_PID=$!
sleep 1

if ! kill -0 "$DASHBOARD_PID" 2>/dev/null; then
    echo "ERROR: mock dashboard failed to start"
    cat "$DASHBOARD_LOG"
    exit 1
fi

log "Mock dashboard running (PID $DASHBOARD_PID)"

# Test 1: Registration
log "Test 1: Agent registration"
KENI_AGENT_TOKEN=keni_testtoken \
KENI_DASHBOARD_URL="http://127.0.0.1:$DASHBOARD_PORT" \
/tmp/e2e-keni-agent > "$AGENT_LOG" 2>&1 &
AGENT_PID=$!

# Wait for registration
sleep 3

if grep -q "registration successful" "$AGENT_LOG"; then
    pass "Agent registered successfully"
else
    fail "Agent registration"
    echo "  Agent log:"
    cat "$AGENT_LOG" | head -20
fi

# Test 2: Verify agent ID was assigned
if grep -q "agent ID: ag_" "$AGENT_LOG"; then
    pass "Agent ID assigned"
else
    fail "Agent ID assignment"
fi

# Test 3: Verify WebSocket connection
if grep -q "connected to dashboard" "$AGENT_LOG"; then
    pass "WebSocket connected"
else
    fail "WebSocket connection"
fi

# Test 4: Verify heartbeat received by dashboard
sleep 5
if grep -q "heartbeat:" "$DASHBOARD_LOG"; then
    pass "Heartbeat received by dashboard"
else
    fail "Heartbeat not received"
    echo "  Dashboard log:"
    cat "$DASHBOARD_LOG" | head -20
fi

# Test 5: Health check endpoint
log "Test 5: Health check endpoint"
sleep 1
HEALTH_RESPONSE=$(curl -s http://127.0.0.1:9100/healthz 2>/dev/null || echo "FAILED")
if echo "$HEALTH_RESPONSE" | grep -q '"status"'; then
    pass "Health endpoint responds"
else
    fail "Health endpoint"
    echo "  Response: $HEALTH_RESPONSE"
fi

# Test 6: Metrics endpoint
log "Test 6: Metrics endpoint"
METRICS_RESPONSE=$(curl -s http://127.0.0.1:9100/metrics 2>/dev/null || echo "FAILED")
if echo "$METRICS_RESPONSE" | grep -q "keni_agent_heartbeats_total"; then
    pass "Metrics endpoint responds with agent metrics"
else
    fail "Metrics endpoint"
fi

# Cleanup
log "Stopping agent..."
kill "$AGENT_PID" 2>/dev/null || true
wait "$AGENT_PID" 2>/dev/null || true
AGENT_PID=""

log "Stopping dashboard..."
kill "$DASHBOARD_PID" 2>/dev/null || true
wait "$DASHBOARD_PID" 2>/dev/null || true
DASHBOARD_PID=""

# Summary
echo ""
log "Results: $PASSED passed, $FAILED failed"
if [ "$FAILED" -gt 0 ]; then
    exit 1
fi
exit 0
