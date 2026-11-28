#!/usr/bin/env bash
#
# Re-certifies the PHP sidecar against the language-neutral OUMV conformance
# fixtures (spec §10) and writes certification/php-laravel.json.
#
#   ./certify.sh
#
# What it does:
#   1. copies testdata/laravel-app into a throw-away directory (the fixtures
#      mutate files: create, patch, delete, rename);
#   2. installs the fixture's Composer dependencies and starts it as a REAL
#      Laravel application on the PHP built-in server;
#   3. runs the agent-oumv conformance CLI over stdio against that copy with
#      --app-url, so oumv/verify dispatches GET /health to the live app and the
#      real response becomes the L4 trace evidence (Level 3);
#   4. validates the produced report against schemas/conformance-report.json.
#
# The sidecar itself stays dependency-free — Composer and Laravel exist only in
# this harness, pinned and cached under ~/.tokensafe/cache.
#
# Environment:
#   PHP        php binary (default: `php` on PATH, then /opt/homebrew/bin/php,
#              then /usr/local/bin/php)
#   NODE       node binary (default: `node` on PATH)
#   OUMV_REPO  agent-oumv checkout (default: ../agent-oumv next to this repo)
#   COMPOSER_HOME  Composer cache (default: ~/.tokensafe/cache/composer)

set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(cd "$HERE/.." && pwd)"
ADAPTER="$HERE/bin/oumv-php"
MARKERS="$HERE/testdata/markers-php.json"
REPORT="${REPORT:-$REPO/certification/php-laravel.json}"
REPORT_DIR="$(dirname "$REPORT")"
OUMV_REPO="${OUMV_REPO:-$(cd "$HERE/../.." && pwd)/agent-oumv}"

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
NODE="${NODE:-$(command -v node || true)}"
if [ -z "${NODE:-}" ]; then
  echo "node not found: needed for the agent-oumv conformance CLI" >&2
  exit 2
fi
if [ ! -d "$OUMV_REPO" ]; then
  echo "agent-oumv checkout not found at $OUMV_REPO (set OUMV_REPO)" >&2
  exit 2
fi

FIXTURES="$OUMV_REPO/fixtures/conformance-cases.json"
CERTIFY="$OUMV_REPO/packages/conformance/dist/cli/certify.js"
REPORT_SCHEMA="$OUMV_REPO/schemas/conformance-report.json"
for required in "$FIXTURES" "$CERTIFY" "$REPORT_SCHEMA" "$MARKERS"; do
  if [ ! -f "$required" ]; then
    echo "missing required file: $required" >&2
    exit 2
  fi
done

TMP="$(mktemp -d "${TMPDIR:-/tmp}/oumv-php-certify.XXXXXX")"
APP_PID=""
cleanup() {
  if [ -n "$APP_PID" ] && kill -0 "$APP_PID" 2>/dev/null; then
    kill "$APP_PID" 2>/dev/null || true
    wait "$APP_PID" 2>/dev/null || true
  fi
  rm -rf "$TMP"
}
trap cleanup EXIT

# --- Composer: from PATH, else a pinned phar (the same managed-download pattern
# the engine uses for its Python runtime). The sidecar never needs it; only this
# harness does, to install the real Laravel application.
COMPOSER_VERSION="2.8.4"
COMPOSER_SHA256="c4c4e2e1beab0ea04e0bd042a5dbba9feda1fbf5eda0d36203958edd343c0a8a"
CACHE="${TOKENSAFE_CACHE_DIR:-$HOME/.tokensafe/cache}"
COMPOSER=""
if command -v composer >/dev/null 2>&1; then
  COMPOSER="$(command -v composer)"
else
  PHAR="$CACHE/composer-$COMPOSER_VERSION.phar"
  if [ ! -f "$PHAR" ]; then
    command -v curl >/dev/null 2>&1 || { echo "curl is required to fetch Composer $COMPOSER_VERSION" >&2; exit 2; }
    mkdir -p "$CACHE"
    curl -fsSL -o "$PHAR" "https://getcomposer.org/download/$COMPOSER_VERSION/composer.phar"
    if command -v sha256sum >/dev/null 2>&1; then
      echo "$COMPOSER_SHA256  $PHAR" | sha256sum -c - >/dev/null
    else
      echo "$COMPOSER_SHA256  $PHAR" | shasum -a 256 -c - >/dev/null
    fi
  fi
  COMPOSER="$PHP $PHAR"
fi
export COMPOSER_HOME="${COMPOSER_HOME:-$CACHE/composer}"

cp -R "$HERE/testdata/laravel-app" "$TMP/app"
echo "▸ php: $("$PHP" -r 'echo PHP_VERSION;') ($PHP)"
echo "▸ workspace: $TMP/app (throw-away copy of testdata/laravel-app)"
echo "▸ installing the fixture's Composer dependencies (Composer: $COMPOSER)"
# The fixture ships a composer.lock, so this is a resolved install (no version
# resolution against a moving Packagist) and COMPOSER_NO_AUDIT keeps advisory
# noise from failing a certification that is about the OUMV wire, not the fixture.
( cd "$TMP/app" && COMPOSER_NO_AUDIT=1 $COMPOSER install --no-interaction --no-progress )

APP_PORT="$("$NODE" -e 'const s=require("net").createServer();s.listen(0,"127.0.0.1",()=>{console.log(s.address().port);s.close();});')"
APP_LOG="$(dirname "$TMP")/$(basename "$TMP").laravel.log"
echo "▸ starting the Laravel app on 127.0.0.1:$APP_PORT"
# The fixture has no database: array-backed session/cache keep the framework from
# resolving a SQLite file it does not ship (Laravel defaults to the database driver).
( cd "$TMP/app" && SESSION_DRIVER=array CACHE_STORE=array QUEUE_CONNECTION=sync \
    exec "$PHP" -S "127.0.0.1:$APP_PORT" -t public ) >"$APP_LOG" 2>&1 &
APP_PID=$!
APP_READY=0
for _ in $(seq 1 120); do
  if ! kill -0 "$APP_PID" 2>/dev/null; then
    echo "✗ the Laravel app exited early; log:" >&2
    tail -40 "$APP_LOG" >&2
    exit 1
  fi
  if "$PHP" -r 'exit(@file_get_contents("http://127.0.0.1:" . $argv[1] . "/health") === false ? 1 : 0);' "$APP_PORT" 2>/dev/null; then
    APP_READY=1
    break
  fi
  sleep 0.25
done
[ "$APP_READY" -eq 1 ] || { echo "✗ the Laravel app did not answer /health; log:" >&2; tail -40 "$APP_LOG" >&2; exit 1; }
APP_URL="http://127.0.0.1:$APP_PORT"
echo "▸ app under test: $APP_URL (verify dispatches here; each response is an L4 trace)"
echo "▸ fixtures: $FIXTURES"
echo "▸ adapter: $ADAPTER"
echo

mkdir -p "$REPORT_DIR"

set +e
"$NODE" "$CERTIFY" \
  --adapter php-laravel \
  --framework laravel \
  --profile backend \
  --fixtures "$FIXTURES" \
  --stdio "$PHP $ADAPTER --root $TMP/app --stdio --app-url $APP_URL" \
  --markers "@$MARKERS" \
  --report "$REPORT" \
  --min-level 3
certify_exit=$?
set -e

if [ ! -f "$REPORT" ]; then
  echo "✗ the certifier produced no report (exit $certify_exit)" >&2
  exit 1
fi

# Validate the artifact against schemas/conformance-report.json and enforce the
# honest claim: with --app-url the L4 trace case has real evidence, so every case
# must pass and the attained level is 3.
set +e
"$NODE" -e '
const fs = require("node:fs");
const [reportPath, schemaPath, ajvPath] = process.argv.slice(1);
const ajvModule = require(ajvPath);
const Ajv2020 = ajvModule.default?.default ?? ajvModule.default ?? ajvModule;
const report = JSON.parse(fs.readFileSync(reportPath, "utf8"));
const schema = JSON.parse(fs.readFileSync(schemaPath, "utf8"));
const validate = new Ajv2020({ allErrors: true, strict: false }).compile(schema);
if (!validate(report)) {
  console.error("✗ report violates schemas/conformance-report.json:");
  for (const error of validate.errors ?? []) console.error(`  - ${error.instancePath || "/"}: ${error.message}`);
  process.exit(2);
}
console.log("▸ report schema: valid");
const failures = report.cases.filter((c) => c.status !== "passed");
for (const c of failures) {
  console.log(`  ✗ L${c.level} ${c.id}${c.error ? " — " + c.error : ""}`);
}
for (const level of [1, 2, 3]) {
  const cases = report.cases.filter((c) => c.level === level);
  const green = cases.every((c) => c.status === "passed");
  console.log(`▸ level ${level}: ${green ? "passed" : "failed"} (${cases.filter((c) => c.status === "passed").length}/${cases.length})`);
}
console.log(`▸ attainedLevel: ${report.attainedLevel} · ${report.passed}/${report.total} cases passed`);
if (failures.length || report.attainedLevel < 3) process.exit(1);
' -- "$REPORT" "$REPORT_SCHEMA" "$OUMV_REPO/node_modules/ajv/dist/2020.js"
schema_exit=$?
set -e

echo
echo "== read-only profile (§8.5): method absence, proven by the read-only-* cases"
cp -R "$HERE/testdata/laravel-app" "$TMP/ro-app"
RO_REPORT="${REPORT%.json}-readonly.json"
set +e
"$NODE" "$CERTIFY" \
  --adapter php-laravel \
  --framework laravel \
  --profile backend \
  --fixtures "$FIXTURES" \
  --stdio "$PHP $ADAPTER --root $TMP/ro-app --stdio --read-only" \
  --markers "@$MARKERS" \
  --report "$RO_REPORT" \
  --min-level 1 --quiet >/dev/null 2>&1
set -e
"$NODE" -e '
const r = require(process.argv[1]);
const ro = r.cases.filter((c) => c.id.startsWith("read-only-"));
const failed = ro.filter((c) => c.status !== "passed");
const unexpected = r.cases.filter((c) => c.status !== "passed");
console.log("  read-only: level " + r.attainedLevel + " · " + r.passed + "/" + r.total + " · " + ro.length + " read-only cases");
if (ro.length < 6) { console.error("✗ the read-only cases are missing from the report"); process.exit(1); }
if (failed.length) { console.error("✗ read-only failures: " + failed.map((c) => c.id + ": " + c.error).join("; ")); process.exit(1); }
if (unexpected.length) { console.error("✗ unexpected failures: " + unexpected.map((c) => c.id).join(", ")); process.exit(1); }
' -- "$RO_REPORT"

echo
echo "report: $REPORT"
if [ "$schema_exit" -ne 0 ]; then
  echo "✗ certification failed its own level/schema gate" >&2
  exit 1
fi
if [ "$certify_exit" -ne 0 ]; then
  echo "✓ certified at the level recorded in the report (certifier exit $certify_exit reflects the recorded failures)"
else
  echo "✓ certified at the level recorded in the report"
fi
exit 0
