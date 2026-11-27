#!/usr/bin/env bash
#
# Produces the Go transport certificates: boots cmd/certserver (net/http adapter
# with SSE + WS + a database/sql sandbox), points oumv-certify at it over each
# binding and writes the reports. A transport claim without a report is not made.
#
#   ./certify-transports.sh                 # writes certification/transports/*.json
#   REPORT_DIR=/tmp/certs ./certify-transports.sh
#
# Environment:
#   OUMV_REPO   agent-oumv checkout (default: ../../agent-oumv)
#   REPORT_DIR  report directory (default: <repo>/certification/transports)
#   GO_PORT     listen port (default: 18090)
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(cd "$HERE/.." && pwd)"
PROJECTS="$(cd "$REPO/.." && pwd)"
OUMV="${OUMV_REPO:-$PROJECTS/agent-oumv}"
REPORT_DIR="${REPORT_DIR:-$REPO/certification/transports}"
MARKERS="$HERE/cmd/certserver/markers.json"
CERTIFY="$OUMV/packages/conformance/dist/cli/certify.js"
PORT="${GO_PORT:-18090}"

[ -f "$CERTIFY" ] || { echo "oumv-certify not built: $CERTIFY (set OUMV_REPO and build @oumv/conformance)" >&2; exit 2; }
[ -f "$MARKERS" ] || { echo "markers not found: $MARKERS" >&2; exit 2; }
command -v node >/dev/null 2>&1 || { echo "node is required for oumv-certify" >&2; exit 2; }
command -v go >/dev/null 2>&1 || { echo "go is required for the certserver" >&2; exit 2; }

WORKSPACE=""
SERVER_PID=""
BUILD_DIR="$(mktemp -d "${TMPDIR:-/tmp}/oumv-go-cert-build.XXXXXX")"
mkdir -p "$REPORT_DIR"

stop_server() {
  if [ -n "$SERVER_PID" ] && kill -0 "$SERVER_PID" 2>/dev/null; then
    kill "$SERVER_PID" 2>/dev/null || true
    wait "$SERVER_PID" 2>/dev/null || true
  fi
  SERVER_PID=""
}

cleanup() {
  stop_server
  [ -n "$WORKSPACE" ] && rm -rf "$WORKSPACE"
  rm -rf "$BUILD_DIR"
}
trap cleanup EXIT

echo "▸ building certserver"
( cd "$REPO/go" && go build -o "$BUILD_DIR/certserver" ./cmd/certserver )

# Each certificate runs against a fresh workspace: the fixture suite edits,
# deletes and renames files, so a second run on the same tree would see stale OCC
# versions and fail for the wrong reason.
prepare_workspace() {
  WORKSPACE="$(mktemp -d "${TMPDIR:-/tmp}/oumv-go-cert.XXXXXX")"
  printf 'package cert\n' > "$WORKSPACE/cert_fixture.go"
  printf 'package cert\n\nfunc Value() int {\n\treturn 41\n}\n' > "$WORKSPACE/cert_edit_target.go"
  printf 'package cert\n\nfunc Symbol() int {\n\treturn 41\n}\n' > "$WORKSPACE/cert_symbol_target.go"
  printf 'package cert\n\nvar DeleteMe = true\n' > "$WORKSPACE/cert_delete_target.go"
  printf 'package cert\n\nvar RenameMe = true\n' > "$WORKSPACE/cert_rename_source.go"
  cat > "$WORKSPACE/go.mod" <<'MOD'
module cert.local/app

go 1.21
MOD
}

start_server() {
  SERVER_LOG="$WORKSPACE/certserver.log"
  "$BUILD_DIR/certserver" -addr "127.0.0.1:$PORT" -workspace "$WORKSPACE" >"$SERVER_LOG" 2>&1 &
  SERVER_PID=$!
  for _ in $(seq 1 120); do
    if node -e "const s=require('net').connect($PORT,'127.0.0.1');s.on('connect',()=>process.exit(0));s.on('error',()=>process.exit(1))" >/dev/null 2>&1; then
      return 0
    fi
    sleep 0.5
  done
  echo "✗ certserver did not start; log:" >&2
  tail -30 "$SERVER_LOG" >&2
  return 1
}

echo "▸ certifying WebSocket binding"
prepare_workspace
start_server
node "$CERTIFY" --adapter go-stdhttp-ws --framework net/http --profile backend \
  --ws "ws://127.0.0.1:$PORT/__oumv/rpc" --markers "@$MARKERS" --fixtures "$OUMV/fixtures/conformance-cases.json" \
  --report "$REPORT_DIR/go-stdhttp-ws.json" --min-level 3
stop_server
rm -rf "$WORKSPACE"
WORKSPACE=""

echo "▸ certifying SSE binding"
prepare_workspace
start_server
node "$CERTIFY" --adapter go-stdhttp-sse --framework net/http --profile backend \
  --url "http://127.0.0.1:$PORT/__oumv/rpc" --sse "http://127.0.0.1:$PORT/__oumv/events" \
  --markers "@$MARKERS" --fixtures "$OUMV/fixtures/conformance-cases.json" \
  --report "$REPORT_DIR/go-stdhttp-sse.json" --min-level 3
stop_server
rm -rf "$WORKSPACE"
WORKSPACE=""

echo "✓ Go transport certificates written to $REPORT_DIR"
