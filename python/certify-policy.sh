#!/usr/bin/env bash
# Certifies the §14.2 workspace policy on the FastAPI cert app: the runtime is
# started with permissions.query=False, keeps observeQuery.readOnly advertised and
# still registers oumv/query, so the refusal is -32007 query_disabled (the shape
# §3.1 rule 5 reserves for policy — the opposite of a read-only deployment).
#
#   ./certify-policy.sh
#   REPORT_DIR=/tmp/certs ./certify-policy.sh
#
#   REPORT_DIR  where python-fastapi-query-disabled.json lands (default: <repo>/certification)
#   PY_PORT     listen port (default: 18093)
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
REPO="$(cd "$HERE/.." && pwd)"
OUMV="${OUMV_REPO:-$(cd "$REPO/.." && pwd)/agent-oumv}"
REPORT_DIR="${REPORT_DIR:-$REPO/certification}"
MARKERS="$HERE/cert_markers.json"
CERTIFY="$OUMV/packages/conformance/dist/cli/certify.js"
FIXTURES="$OUMV/fixtures/conformance-cases.json"
PORT="${PY_PORT:-18093}"
CERT_TOKEN="${OUMV_CERT_TOKEN:-oumv-cert-token}"
PYTHON="${PYTHON:-$HERE/.venv/bin/python}"
[ -x "$PYTHON" ] || PYTHON="$(command -v python3 || true)"

for required in "$CERTIFY" "$FIXTURES" "$MARKERS"; do
  [ -f "$required" ] || { echo "missing $required (set OUMV_REPO and build @oumv/conformance)" >&2; exit 2; }
done

WORKSPACE="$(mktemp -d "${TMPDIR:-/tmp}/oumv-python-policy.XXXXXX")"
cleanup() { [ -n "${SERVER_PID:-}" ] && kill "$SERVER_PID" 2>/dev/null || true; rm -rf "$WORKSPACE"; }
trap cleanup EXIT
printf 'value = 0\n' > "$WORKSPACE/conformance_fixture.py"
printf 'value = 41\n' > "$WORKSPACE/conformance_edit_target.py"
printf 'def value():\n    return 41\n' > "$WORKSPACE/conformance_symbol_target.py"
printf 'value = 1\n' > "$WORKSPACE/conformance_delete_target.py"
printf 'value = 2\n' > "$WORKSPACE/conformance_rename_source.py"

mkdir -p "$REPORT_DIR"
( export TOKENSAFE_CERT_WORKSPACE="$WORKSPACE" TOKENSAFE_OUMV_AUTH_TOKEN="$CERT_TOKEN" \
    TOKENSAFE_CERT_QUERY_DISABLED=1; cd "$HERE"; \
  exec "$PYTHON" -m uvicorn cert_app:app --host 127.0.0.1 --port "$PORT" --log-level warning ) \
  >"$WORKSPACE/uvicorn.log" 2>&1 &
SERVER_PID=$!
for _ in $(seq 1 120); do
  if node -e "const s=require('net').connect($PORT,'127.0.0.1');s.on('connect',()=>process.exit(0));s.on('error',()=>process.exit(1))" >/dev/null 2>&1; then
    break
  fi
  sleep 0.5
done

echo "▸ certifying the §14.2 workspace policy (permissions.query=false)"
node "$CERTIFY" --adapter python-fastapi --framework fastapi --profile backend \
  --url "http://127.0.0.1:$PORT/__oumv/rpc" --token "$CERT_TOKEN" \
  --markers "@$MARKERS" --fixtures "$FIXTURES" --policy \
  --report "$REPORT_DIR/python-fastapi-query-disabled.json" --min-level 1
echo "✓ policy certificate written to $REPORT_DIR"
