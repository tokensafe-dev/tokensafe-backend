#!/usr/bin/env bash
#
# Produces the Python transport certificates: boots cert_app under uvicorn (FastAPI
# adapter with SSE + WS + a SQLite sandbox), points oumv-certify at each binding
# and writes the reports.
#
#   ./certify-transports.sh
#   REPORT_DIR=/tmp/certs ./certify-transports.sh
#
# Environment:
#   OUMV_REPO   agent-oumv checkout (default: ../../agent-oumv)
#   REPORT_DIR  report directory (default: <repo>/certification/transports)
#   PY_PORT     listen port (default: 18091)
#   PYTHON      interpreter with fastapi/sqlalchemy/uvicorn (default: python/.venv/bin/python)
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(cd "$HERE/.." && pwd)"
PROJECTS="$(cd "$REPO/.." && pwd)"
OUMV="${OUMV_REPO:-$PROJECTS/agent-oumv}"
REPORT_DIR="${REPORT_DIR:-$REPO/certification/transports}"
MARKERS="$HERE/cert_markers.json"
CERTIFY="$OUMV/packages/conformance/dist/cli/certify.js"
PORT="${PY_PORT:-18091}"
# §12: the app is started with a token so the auth refusal cases run in the
# certificate; the CLI authenticates with the same value.
CERT_TOKEN="${OUMV_CERT_TOKEN:-oumv-cert-token}"
PYTHON="${PYTHON:-$HERE/.venv/bin/python}"
[ -x "$PYTHON" ] || PYTHON="$(command -v python3 || true)"

[ -f "$CERTIFY" ] || { echo "oumv-certify not built: $CERTIFY (set OUMV_REPO and build @oumv/conformance)" >&2; exit 2; }
[ -f "$MARKERS" ] || { echo "markers not found: $MARKERS" >&2; exit 2; }
[ -n "$PYTHON" ] || { echo "python interpreter not found; set PYTHON" >&2; exit 2; }
command -v node >/dev/null 2>&1 || { echo "node is required for oumv-certify" >&2; exit 2; }

mkdir -p "$REPORT_DIR"
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
  WORKSPACE="$(mktemp -d "${TMPDIR:-/tmp}/oumv-python-cert.XXXXXX")"
  printf 'value = 0\n' > "$WORKSPACE/conformance_fixture.py"
  printf 'value = 41\n' > "$WORKSPACE/conformance_edit_target.py"
  printf 'def value():\n    return 41\n' > "$WORKSPACE/conformance_symbol_target.py"
  printf 'value = 1\n' > "$WORKSPACE/conformance_delete_target.py"
  printf 'value = 2\n' > "$WORKSPACE/conformance_rename_source.py"
}

start_server() {
  if node -e "const s=require('net').connect($PORT,'127.0.0.1');s.on('connect',()=>process.exit(0));s.on('error',()=>process.exit(1))" >/dev/null 2>&1; then
    echo "✗ port $PORT is already in use; kill the stale server first" >&2
    return 1
  fi
  SERVER_LOG="$WORKSPACE/uvicorn.log"
  ( export TOKENSAFE_CERT_WORKSPACE="$WORKSPACE" TOKENSAFE_OUMV_AUTH_TOKEN="$CERT_TOKEN"; cd "$HERE"; \
    exec "$PYTHON" -m uvicorn cert_app:app --host 127.0.0.1 --port "$PORT" --log-level warning ) \
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

echo "▸ certifying WebSocket binding"
prepare_workspace
start_server
node "$CERTIFY" --adapter python-fastapi-ws --framework fastapi --profile backend \
  --ws "ws://127.0.0.1:$PORT/__oumv/rpc" --token "$CERT_TOKEN" --markers "@$MARKERS" --fixtures "$OUMV/fixtures/conformance-cases.json" \
  --report "$REPORT_DIR/python-fastapi-ws.json" --min-level 3
stop_server
rm -rf "$WORKSPACE"
WORKSPACE=""

echo "▸ certifying SSE binding"
prepare_workspace
start_server
node "$CERTIFY" --adapter python-fastapi-sse --framework fastapi --profile backend \
  --url "http://127.0.0.1:$PORT/__oumv/rpc" --sse "http://127.0.0.1:$PORT/__oumv/events" \
  --token "$CERT_TOKEN" --auth \
  --markers "@$MARKERS" --fixtures "$OUMV/fixtures/conformance-cases.json" \
  --report "$REPORT_DIR/python-fastapi-sse.json" --min-level 3
stop_server
rm -rf "$WORKSPACE"
WORKSPACE=""

echo "✓ Python transport certificates written to $REPORT_DIR"
