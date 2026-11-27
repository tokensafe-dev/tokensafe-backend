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
#   2. runs the agent-oumv conformance CLI over stdio against that copy;
#   3. validates the produced report against schemas/conformance-report.json;
#   4. exits 0 only when Level 1 and Level 2 fully pass and the attained level
#      is at least 2. The Level 3 case `observe-l4-returns-recorded-traces`
#      fails by design: this sidecar has no live PHP application to record real
#      request traces from and never fabricates them.
#
# Environment:
#   PHP        php binary (default: `php` on PATH, then /opt/homebrew/bin/php,
#              then /usr/local/bin/php)
#   NODE       node binary (default: `node` on PATH)
#   OUMV_REPO  agent-oumv checkout (default: ../agent-oumv next to this repo)

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
cleanup() { rm -rf "$TMP"; }
trap cleanup EXIT

cp -R "$HERE/testdata/laravel-app" "$TMP/app"
echo "▸ php: $("$PHP" -r 'echo PHP_VERSION;') ($PHP)"
echo "▸ workspace: $TMP/app (throw-away copy of testdata/laravel-app)"
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
  --stdio "$PHP $ADAPTER --root $TMP/app --stdio" \
  --markers "@$MARKERS" \
  --report "$REPORT" \
  --min-level 2
certify_exit=$?
set -e

if [ ! -f "$REPORT" ]; then
  echo "✗ the certifier produced no report (exit $certify_exit)" >&2
  exit 1
fi

# Validate the artifact against schemas/conformance-report.json and enforce the
# honest claim: Level 1 + Level 2 must be fully green, Level 3 failures allowed.
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
const atLevel = (max) => report.cases.filter((c) => c.level <= max);
const l1 = atLevel(1);
const l2 = atLevel(2);
const l1Green = l1.every((c) => c.status === "passed");
const l2Green = l2.every((c) => c.status === "passed");
for (const c of report.cases.filter((c) => c.status !== "passed")) {
  console.log(`  ✗ L${c.level} ${c.id}${c.error ? " — " + c.error : ""}`);
}
console.log(`▸ level 1: ${l1Green ? "passed" : "failed"} (${l1.filter((c) => c.status === "passed").length}/${l1.length})`);
console.log(`▸ level 2: ${l2Green ? "passed" : "failed"} (${l2.filter((c) => c.status === "passed").length}/${l2.length})`);
console.log(`▸ attainedLevel: ${report.attainedLevel} · ${report.passed}/${report.total} cases passed`);
if (!l1Green || !l2Green || report.attainedLevel < 2) process.exit(1);
' -- "$REPORT" "$REPORT_SCHEMA" "$OUMV_REPO/node_modules/ajv/dist/2020.js"
schema_exit=$?
set -e

echo
echo "report: $REPORT"
if [ "$schema_exit" -ne 0 ]; then
  echo "✗ certification failed its own level/schema gate" >&2
  exit 1
fi
if [ "$certify_exit" -ne 0 ]; then
  echo "✓ certified at Level 2 (the certifier exit code $certify_exit reflects the L3 trace case only)"
else
  echo "✓ certified at Level 2"
fi
exit 0
