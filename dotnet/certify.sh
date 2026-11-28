#!/usr/bin/env bash
#
# Re-certify the ASP.NET Core adapter against the language-neutral OUMV fixtures
# (spec §10) and write three reports: HTTP, SSE and WebSocket.
#
# The fixture app (minimal APIs + one classic controller + EF Core SQLite) is
# copied to a throwaway workspace first, so the git worktree is never mutated by
# the conformance run (applyEdit cases create, patch, delete and rename files).
#
#   ./certify.sh            # writes certification/dotnet-aspnetcore.json and transports/*
#
# Environment overrides:
#   OUMV_REPO        path to the agent-oumv checkout (default: ../agent-oumv)
#   REPORT           HTTP report path (default: <repo>/certification/dotnet-aspnetcore.json)
#   TRANSPORT_DIR    SSE/WS report directory (default: <repo>/certification/transports)
#   DOTNET           dotnet executable (default: from PATH, then ~/.dotnet-10)
#   PORT             first port to try (default: 18120; the next free one is used)
#   CERT_DEBUG=1     keep the workspace and the server log
#
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(cd "$HERE/.." && pwd)"
PROJECTS="$(cd "$REPO/.." && pwd)"
OUMV="${OUMV_REPO:-$PROJECTS/agent-oumv}"
REPORT="${REPORT:-$REPO/certification/dotnet-aspnetcore.json}"
TRANSPORT_DIR="${TRANSPORT_DIR:-$REPO/certification/transports}"
MIN_LEVEL="${MIN_LEVEL:-3}"
MARKERS="$HERE/certapp/markers.json"
SEEDS="$HERE/certapp/testdata"
FIXTURES="$OUMV/fixtures/conformance-cases.json"
CERTIFY="$OUMV/packages/conformance/dist/cli/certify.js"

if [ -n "${DOTNET:-}" ]; then
  :
elif [ -x "$HOME/.dotnet-10/dotnet" ]; then
  # A local SDK install wins over an older SDK that may shadow it on PATH.
  DOTNET="$HOME/.dotnet-10/dotnet"
  export DOTNET_ROOT="$HOME/.dotnet-10"
elif command -v dotnet >/dev/null 2>&1; then
  DOTNET="$(command -v dotnet)"
else
  echo "dotnet (SDK 10+) is required" >&2
  exit 2
fi

for required in "$CERTIFY" "$FIXTURES" "$MARKERS"; do
  [ -f "$required" ] || { echo "missing $required (set OUMV_REPO)" >&2; exit 2; }
done
command -v node >/dev/null 2>&1 || { echo "node is required for oumv-certify" >&2; exit 2; }

PORT="${PORT:-18120}"
APP_DLL="$HERE/certapp/bin/Release/net10.0/TokenSafe.Oumv.CertApp.dll"
WORKSPACE=""
SERVER_PID=""

is_port_free() {
  node -e "const s=require('net').connect($1,'127.0.0.1');s.on('connect',()=>process.exit(1));s.on('error',()=>process.exit(0))" >/dev/null 2>&1
}

choose_port() {
  local candidate="$PORT"
  for _ in $(seq 1 20); do
    if is_port_free "$candidate"; then PORT="$candidate"; return 0; fi
    candidate=$((candidate + 1))
  done
  echo "no free port near $PORT" >&2
  return 1
}

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
  WORKSPACE="$(mktemp -d "${TMPDIR:-/tmp}/oumv-dotnet-cert.XXXXXX")"
# The server log MUST NOT live inside the workspace: it grows on every request, and
# the adapter's §4.1 digest would then differ between two consecutive calls.
SERVER_LOG="$(dirname "$WORKSPACE")/$(basename "$WORKSPACE").server.log"
  cp "$SEEDS"/*.cs "$WORKSPACE"/
}

start_server() {
  ( cd "$HERE/certapp" && TOKENSAFE_CERT_WORKSPACE="$WORKSPACE" ASPNETCORE_ENVIRONMENT=Production \
      exec "$DOTNET" "$APP_DLL" --urls "http://127.0.0.1:$PORT" ) >"$SERVER_LOG" 2>&1 &
  SERVER_PID=$!
  for _ in $(seq 1 160); do
    if ! kill -0 "$SERVER_PID" 2>/dev/null; then
      echo "✗ cert app exited early; log:" >&2
      tail -40 "$SERVER_LOG" >&2
      return 1
    fi
    if node -e "const s=require('net').connect($PORT,'127.0.0.1');s.on('connect',()=>process.exit(0));s.on('error',()=>process.exit(1))" >/dev/null 2>&1; then
      return 0
    fi
    sleep 0.25
  done
  echo "✗ cert app did not start; log:" >&2
  tail -40 "$SERVER_LOG" >&2
  return 1
}

echo "▸ building TokenSafe.Oumv + cert app (Release)"
"$DOTNET" build "$HERE/certapp/TokenSafe.Oumv.CertApp.csproj" -c Release --nologo -v quiet
[ -f "$APP_DLL" ] || { echo "✗ build did not produce $APP_DLL" >&2; exit 1; }
mkdir -p "$TRANSPORT_DIR"

echo "▸ certifying HTTP binding"
choose_port
prepare_workspace
start_server
node "$CERTIFY" --adapter dotnet-aspnetcore --framework aspnetcore --profile backend \
  --url "http://127.0.0.1:$PORT/__oumv/rpc" --markers "@$MARKERS" --fixtures "$FIXTURES" \
  --report "$REPORT" --min-level "$MIN_LEVEL"
stop_server
rm -rf "$WORKSPACE"
WORKSPACE=""

echo "▸ certifying SSE binding"
choose_port
prepare_workspace
start_server
node "$CERTIFY" --adapter dotnet-aspnetcore-sse --framework aspnetcore --profile backend \
  --url "http://127.0.0.1:$PORT/__oumv/rpc" --sse "http://127.0.0.1:$PORT/__oumv/events" \
  --markers "@$MARKERS" --fixtures "$FIXTURES" \
  --report "$TRANSPORT_DIR/dotnet-aspnetcore-sse.json" --min-level "$MIN_LEVEL"
stop_server
rm -rf "$WORKSPACE"
WORKSPACE=""

echo "▸ certifying WebSocket binding"
choose_port
prepare_workspace
start_server
node "$CERTIFY" --adapter dotnet-aspnetcore-ws --framework aspnetcore --profile backend \
  --ws "ws://127.0.0.1:$PORT/__oumv/rpc" \
  --markers "@$MARKERS" --fixtures "$FIXTURES" \
  --report "$TRANSPORT_DIR/dotnet-aspnetcore-ws.json" --min-level "$MIN_LEVEL"
stop_server
rm -rf "$WORKSPACE"
WORKSPACE=""

echo "✓ ASP.NET Core certificates written to $REPORT and $TRANSPORT_DIR"
