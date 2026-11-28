#!/usr/bin/env bash
#
# Produces the Starlette certificates: boots cert_app_starlette under uvicorn
# (the Starlette adapter with a SQLite sandbox discovered from app.state), points
# oumv-certify at each binding and writes the reports.
#
#   ./certify-starlette.sh
#   REPORT_DIR=/tmp/certs ./certify-starlette.sh
#
# Writes:
#   <REPORT_DIR>/python-starlette.json           request/response suite (HTTP)
#   <REPORT_DIR>/python-starlette-readonly.json  §8.5 read-only deployment
#   <REPORT_DIR>/transports/python-starlette-ws.json
#   <REPORT_DIR>/transports/python-starlette-sse.json
#
# Environment:
#   OUMV_REPO       agent-oumv checkout (default: ../../agent-oumv)
#   REPORT_DIR      report directory (default: <repo>/certification)
#   PY_PORT         listen port (default: 18092)
#   PYTHON          interpreter with starlette/sqlalchemy/uvicorn
#                   (default: python/.venv/bin/python)
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(cd "$HERE/.." && pwd)"
PROJECTS="$(cd "$REPO/.." && pwd)"
OUMV="${OUMV_REPO:-$PROJECTS/agent-oumv}"
REPORT_DIR="${REPORT_DIR:-$REPO/certification}"
TRANSPORT_DIR="${TRANSPORT_DIR:-$REPORT_DIR/transports}"
MARKERS="$HERE/cert_markers.json"
CERTIFY="$OUMV/packages/conformance/dist/cli/certify.js"
FIXTURES="$OUMV/fixtures/conformance-cases.json"
PORT="${PY_PORT:-18092}"
CERT_APP="cert_app_starlette:app"
# §12: the app is started with a token so the auth refusal cases run in the
# transport certificates; the CLI authenticates with the same value.
CERT_TOKEN="${OUMV_CERT_TOKEN:-oumv-cert-token}"
PYTHON="${PYTHON:-$HERE/.venv/bin/python}"
[ -x "$PYTHON" ] || PYTHON="$(command -v python3 || true)"

[ -f "$CERTIFY" ] || { echo "oumv-certify not built: $CERTIFY (set OUMV_REPO and build @oumv/conformance)" >&2; exit 2; }
[ -f "$FIXTURES" ] || { echo "fixtures not found: $FIXTURES" >&2; exit 2; }
[ -f "$MARKERS" ] || { echo "markers not found: $MARKERS" >&2; exit 2; }
[ -n "$PYTHON" ] || { echo "python interpreter not found; set PYTHON" >&2; exit 2; }
command -v node >/dev/null 2>&1 || { echo "node is required for oumv-certify" >&2; exit 2; }

mkdir -p "$REPORT_DIR" "$TRANSPORT_DIR"
WORKSPACE=""
SERVER_PID=""

stop_server() {
  if [ -n "$SERVER_PID" ] && kill -0 "$SERVER_PID" 2>/dev/null; then
    kill "$SERVER_PID" 2>/dev/null || true
    wait "$SERVER_PID" 2>/dev/null || true
  fi
  SERVER_PID=""
}

cleanup() {
  stop_server
  # An EXIT trap's last failing test would become the script's exit status under
  # `set -e`, so the success path must end in an explicit `return 0`.
  if [ -n "$WORKSPACE" ]; then
    if [ "${CERT_DEBUG:-0}" = "1" ]; then
      echo "debug: workspace kept at $WORKSPACE"
    else
      rm -rf "$WORKSPACE"
    fi
  fi
  return 0
}
trap cleanup EXIT

prepare_workspace() {
  WORKSPACE="$(mktemp -d "${TMPDIR:-/tmp}/oumv-starlette-cert.XXXXXX")"
  printf 'value = 0\n' > "$WORKSPACE/conformance_fixture.py"
  printf 'value = 41\n' > "$WORKSPACE/conformance_edit_target.py"
  printf 'def value():\n    return 41\n' > "$WORKSPACE/conformance_symbol_target.py"
  printf 'value = 1\n' > "$WORKSPACE/conformance_delete_target.py"
  printf 'value = 2\n' > "$WORKSPACE/conformance_rename_source.py"
}

start_server() {
  local read_only="${1:-0}"
  local query_disabled="${2:-0}"
  if node -e "const s=require('net').connect($PORT,'127.0.0.1');s.on('connect',()=>process.exit(0));s.on('error',()=>process.exit(1))" >/dev/null 2>&1; then
    echo "✗ port $PORT is already in use; kill the stale server first" >&2
    return 1
  fi
  SERVER_LOG="$WORKSPACE/uvicorn.log"
  ( export TOKENSAFE_CERT_WORKSPACE="$WORKSPACE" TOKENSAFE_OUMV_AUTH_TOKEN="$CERT_TOKEN" \
      TOKENSAFE_CERT_READ_ONLY="$read_only" TOKENSAFE_CERT_QUERY_DISABLED="$query_disabled"; cd "$HERE"; \
    exec "$PYTHON" -m uvicorn "$CERT_APP" --host 127.0.0.1 --port "$PORT" --log-level warning ) \
    >"$SERVER_LOG" 2>&1 &
  SERVER_PID=$!
  for _ in $(seq 1 120); do
    if node -e "const s=require('net').connect($PORT,'127.0.0.1');s.on('connect',()=>process.exit(0));s.on('error',()=>process.exit(1))" >/dev/null 2>&1; then
      return 0
    fi
    sleep 0.5
  done
  echo "✗ uvicorn did not start; log:" >&2
  tail -30 "$SERVER_LOG" >&2
  return 1
}

reset_workspace() {
  stop_server
  if [ -n "$WORKSPACE" ]; then rm -rf "$WORKSPACE"; fi
  WORKSPACE=""
}

echo "▸ certifying Starlette request/response suite (HTTP)"
prepare_workspace
start_server 0
node "$CERTIFY" --adapter python-starlette --framework starlette --profile backend \
  --url "http://127.0.0.1:$PORT/__oumv/rpc" --token "$CERT_TOKEN" \
  --markers "@$MARKERS" --fixtures "$FIXTURES" \
  --report "$REPORT_DIR/python-starlette.json" --min-level 3
reset_workspace

echo "▸ certifying Starlette read-only deployment (§8.5)"
prepare_workspace
start_server 1
node "$CERTIFY" --adapter python-starlette --framework starlette --profile backend \
  --url "http://127.0.0.1:$PORT/__oumv/rpc" --token "$CERT_TOKEN" \
  --markers "@$MARKERS" --fixtures "$FIXTURES" \
  --report "$REPORT_DIR/python-starlette-readonly.json" --min-level 3
reset_workspace

echo "▸ certifying Starlette WebSocket binding"
prepare_workspace
start_server 0
node "$CERTIFY" --adapter python-starlette-ws --framework starlette --profile backend \
  --ws "ws://127.0.0.1:$PORT/__oumv/rpc" --token "$CERT_TOKEN" \
  --markers "@$MARKERS" --fixtures "$FIXTURES" \
  --report "$TRANSPORT_DIR/python-starlette-ws.json" --min-level 3
reset_workspace

echo "▸ certifying Starlette SSE binding"
prepare_workspace
start_server 0
node "$CERTIFY" --adapter python-starlette-sse --framework starlette --profile backend \
  --url "http://127.0.0.1:$PORT/__oumv/rpc" --sse "http://127.0.0.1:$PORT/__oumv/events" \
  --token "$CERT_TOKEN" --auth \
  --markers "@$MARKERS" --fixtures "$FIXTURES" \
  --report "$TRANSPORT_DIR/python-starlette-sse.json" --min-level 3
reset_workspace

echo "▸ certifying the §14.2 workspace policy (permissions.query=false)"
# The runtime still advertises observeQuery.readOnly and still registers oumv/query:
# the refusal is -32007 query_disabled, the shape §3.1 rule 5 reserves for policy.
prepare_workspace
start_server 0 1
node "$CERTIFY" --adapter python-starlette --framework starlette --profile backend \
  --url "http://127.0.0.1:$PORT/__oumv/rpc" --token "$CERT_TOKEN" \
  --markers "@$MARKERS" --fixtures "$FIXTURES" --policy \
  --report "$REPORT_DIR/python-starlette-query-disabled.json" --min-level 1 --quiet
reset_workspace

echo "✓ Starlette certificates written to $REPORT_DIR"
