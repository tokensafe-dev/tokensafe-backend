#!/usr/bin/env bash
#
# Direct self-test for the OUMV PHP sidecar (spec §4/§5), no Composer, no PHPUnit.
#
#   ./selftest.sh            (uses $PHP, then /opt/homebrew/bin/php, then /usr/local/bin/php)
#
# What it proves:
#   1. the adapter and every fixture parse (`php -l`);
#   2. handshake / observe L0-L4 / getSource / applyEdit (patch, symbol, create,
#      delete, rename, OCC, guards) over stdio, against a throw-away copy of the
#      Laravel fixture — selftest.php;
#   3. Symfony #[Route] attribute discovery and L2/L3 resolution — selftest.php
#      in scan mode;
#   4. the HTTP binding (POST /__oumv/rpc, batch, OPTIONS, 405, 415, 401/200 with
#      a token) through PHP's built-in server.

set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ADAPTER="$HERE/bin/oumv-php"

if [ -z "${PHP:-}" ]; then
  PHP="$(command -v php || true)"
fi
if [ -z "${PHP:-}" ]; then
  for candidate in /opt/homebrew/bin/php /usr/local/bin/php; do
    if [ -x "$candidate" ]; then PHP="$candidate"; break; fi
  done
fi
if [ -z "${PHP:-}" ] || [ ! -x "$PHP" ]; then
  echo "php not found: install PHP 8.1+ or set PHP=/path/to/php" >&2
  exit 2
fi

echo "▸ php: $("$PHP" -r 'echo PHP_VERSION;') ($PHP)"
echo "▸ lint: adapter and fixtures"
"$PHP" -l "$ADAPTER" >/dev/null
while IFS= read -r -d '' file; do
  "$PHP" -l "$file" >/dev/null
done < <(find "$HERE/testdata" -name '*.php' -print0)

TMP="$(mktemp -d "${TMPDIR:-/tmp}/oumv-php-selftest.XXXXXX")"
SERVER_PIDS=()
cleanup() {
  for pid in "${SERVER_PIDS[@]:-}"; do
    kill "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
  done
  rm -rf "$TMP"
}
trap cleanup EXIT

cp -R "$HERE/testdata/laravel-app" "$TMP/laravel"
cp -R "$HERE/testdata/symfony-app" "$TMP/symfony"
cp "$HERE/testdata/plain/probe.php" "$TMP/laravel/plain-probe.php"

echo
echo "▸ 1/3 stdio: handshake, observe, getSource, applyEdit, verify, understand (Laravel fixture)"
"$PHP" "$HERE/selftest.php" "$TMP/laravel" "$ADAPTER"

echo
echo "▸ 2/3 static scan: Symfony #[Route] attributes"
"$PHP" "$HERE/selftest.php" "$TMP/symfony" "$ADAPTER" --symfony

echo
echo "▸ 3/3 HTTP: php -S router binding"
PORT=$(( 18400 + (RANDOM % 400) ))
TOKEN_PORT=$(( PORT + 1 ))
BASE="http://127.0.0.1:${PORT}/__oumv/rpc"

"$PHP" -S "127.0.0.1:${PORT}" "$ADAPTER" --root "$TMP/laravel" >"$TMP/http.log" 2>&1 &
SERVER_PIDS+=("$!")
"$PHP" -S "127.0.0.1:${TOKEN_PORT}" "$ADAPTER" --root "$TMP/laravel" --token selftest-token >"$TMP/http-token.log" 2>&1 &
SERVER_PIDS+=("$!")

ready=0
for _ in $(seq 1 50); do
  if curl -fsS "http://127.0.0.1:${PORT}/__oumv/health" >/dev/null 2>&1 \
     && curl -fsS "http://127.0.0.1:${TOKEN_PORT}/__oumv/health" >/dev/null 2>&1; then
    ready=1
    break
  fi
  sleep 0.1
done
if [ "$ready" -ne 1 ]; then
  echo "  FAIL the built-in server did not start" >&2
  cat "$TMP/http.log" >&2 || true
  exit 1
fi

http_failures=0
http_check() {
  local name="$1" expected="$2" actual="$3"
  if [ "$expected" = "$actual" ]; then
    echo "  ok   $name"
  else
    echo "  FAIL $name — expected '$expected', got '$actual'"
    http_failures=$(( http_failures + 1 ))
  fi
}

RPC_BODY='{"jsonrpc":"2.0","id":1,"method":"oumv/handshake","params":{"protocolVersion":"1.3.0"}}'
response="$(curl -sS -X POST "$BASE" -H 'Content-Type: application/json' -d "$RPC_BODY")"
http_check "HTTP handshake returns protocolVersion 1.3.0" "1.3.0" \
  "$(printf '%s' "$response" | "$PHP" -r '$d=json_decode(stream_get_contents(STDIN),true); echo $d["result"]["protocolVersion"] ?? "missing";')"

batch='[{"jsonrpc":"2.0","id":"a","method":"oumv/observe","params":{"level":"L0"}},{"jsonrpc":"2.0","method":"oumv/observe","params":{"level":"L0"}}]'
batch_count="$(curl -sS -X POST "$BASE" -H 'Content-Type: application/json' -d "$batch" \
  | "$PHP" -r '$d=json_decode(stream_get_contents(STDIN),true); echo is_array($d) ? count($d) : "not-array";')"
http_check "HTTP batch returns one entry and drops the notification" "1" "$batch_count"

http_check "HTTP GET is 405" "405" "$(curl -s -o /dev/null -w '%{http_code}' "$BASE")"
http_check "HTTP OPTIONS is 204" "204" "$(curl -s -o /dev/null -w '%{http_code}' -X OPTIONS "$BASE")"
http_check "HTTP wrong Content-Type is 415" "415" \
  "$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE" -H 'Content-Type: text/plain' -d '{}')"
http_check "HTTP malformed JSON answers 200 with -32700" "-32700" \
  "$(curl -sS -X POST "$BASE" -H 'Content-Type: application/json' -d '{oops' \
     | "$PHP" -r '$d=json_decode(stream_get_contents(STDIN),true); echo $d["error"]["code"] ?? "missing";')"

TOKEN_BASE="http://127.0.0.1:${TOKEN_PORT}/__oumv/rpc"
http_check "token server rejects an unauthenticated call with 401" "401" \
  "$(curl -s -o /dev/null -w '%{http_code}' -X POST "$TOKEN_BASE" -H 'Content-Type: application/json' -d "$RPC_BODY")"
http_check "token server accepts a bearer token" "200" \
  "$(curl -s -o /dev/null -w '%{http_code}' -X POST "$TOKEN_BASE" -H 'Content-Type: application/json' \
     -H 'Authorization: Bearer selftest-token' -d "$RPC_BODY")"
http_check "token server accepts X-OUMV-Session-Token" "200" \
  "$(curl -s -o /dev/null -w '%{http_code}' -X POST "$TOKEN_BASE" -H 'Content-Type: application/json' \
     -H 'X-OUMV-Session-Token: selftest-token' -d "$RPC_BODY")"

if [ "$http_failures" -ne 0 ]; then
  echo "HTTP transport: ${http_failures} failure(s)" >&2
  exit 1
fi

echo
echo "✓ selftest passed"
