#!/usr/bin/env bash
#
# Certifies the §14.2 workspace policy on the Go net/http certserver: the runtime
# runs with Permissions.QueryDisabled, keeps observeQuery.readOnly advertised and
# still registers oumv/query, so the refusal is -32007 query_disabled — the shape
# §3.1 rule 5 reserves for policy, not the method absence of a read-only build.
#
#   ./certify-policy.sh                 # writes certification/go-stdhttp-query-disabled.json
#   REPORT_DIR=/tmp/certs ./certify-policy.sh
#
# Environment:
#   OUMV_REPO   agent-oumv checkout (default: ../../agent-oumv)
#   REPORT_DIR  report directory (default: <repo>/certification)
#   GO_PORT     listen port (default: 18094)
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(cd "$HERE/.." && pwd)"
PROJECTS="$(cd "$REPO/.." && pwd)"
OUMV="${OUMV_REPO:-$PROJECTS/agent-oumv}"
REPORT_DIR="${REPORT_DIR:-$REPO/certification}"
MARKERS="$HERE/cmd/certserver/markers.json"
CERTIFY="$OUMV/packages/conformance/dist/cli/certify.js"
FIXTURES="$OUMV/fixtures/conformance-cases.json"
PORT="${GO_PORT:-18094}"
CERT_TOKEN="${OUMV_CERT_TOKEN:-oumv-cert-token}"

for required in "$CERTIFY" "$MARKERS" "$FIXTURES"; do
  [ -f "$required" ] || { echo "missing $required (set OUMV_REPO and build @oumv/conformance)" >&2; exit 2; }
done
command -v node >/dev/null 2>&1 || { echo "node is required for oumv-certify" >&2; exit 2; }
command -v go >/dev/null 2>&1 || { echo "go is required for the certserver" >&2; exit 2; }

BUILD_DIR="$(mktemp -d "${TMPDIR:-/tmp}/oumv-go-policy-build.XXXXXX")"
WORKSPACE="$(mktemp -d "${TMPDIR:-/tmp}/oumv-go-policy.XXXXXX")"
SERVER_PID=""
cleanup() {
  if [ -n "$SERVER_PID" ] && kill -0 "$SERVER_PID" 2>/dev/null; then
    kill "$SERVER_PID" 2>/dev/null || true
    wait "$SERVER_PID" 2>/dev/null || true
  fi
  rm -rf "$WORKSPACE" "$BUILD_DIR"
}
trap cleanup EXIT

printf 'package cert\n' > "$WORKSPACE/cert_fixture.go"
printf 'package cert\n\nfunc Value() int {\n\treturn 41\n}\n' > "$WORKSPACE/cert_edit_target.go"
printf 'package cert\n\nfunc Symbol() int {\n\treturn 41\n}\n' > "$WORKSPACE/cert_symbol_target.go"
printf 'package cert\n\nvar DeleteMe = true\n' > "$WORKSPACE/cert_delete_target.go"
printf 'package cert\n\nvar RenameMe = true\n' > "$WORKSPACE/cert_rename_source.go"
printf 'module cert.local/app\n\ngo 1.21\n' > "$WORKSPACE/go.mod"

mkdir -p "$REPORT_DIR"
echo "▸ building certserver"
( cd "$REPO/go" && go build -o "$BUILD_DIR/certserver" ./cmd/certserver )

echo "▸ certifying the §14.2 workspace policy (query disabled)"
TOKENSAFE_CERT_QUERY_DISABLED=1 "$BUILD_DIR/certserver" \
  -addr "127.0.0.1:$PORT" -workspace "$WORKSPACE" -token "$CERT_TOKEN" \
  >"$WORKSPACE/certserver.log" 2>&1 &
SERVER_PID=$!
for _ in $(seq 1 120); do
  if node -e "const s=require('net').connect($PORT,'127.0.0.1');s.on('connect',()=>process.exit(0));s.on('error',()=>process.exit(1))" >/dev/null 2>&1; then
    break
  fi
  sleep 0.5
done

node "$CERTIFY" --adapter go-stdhttp --framework net/http --profile backend \
  --url "http://127.0.0.1:$PORT/__oumv/rpc" --token "$CERT_TOKEN" \
  --markers "@$MARKERS" --fixtures "$FIXTURES" --policy \
  --report "$REPORT_DIR/go-stdhttp-query-disabled.json" --min-level 1
echo "✓ policy certificate written to $REPORT_DIR"
