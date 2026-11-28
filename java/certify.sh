#!/usr/bin/env bash
#
# Re-certify the JDK-only Spring adapter against the language-neutral OUMV
# fixtures (spec §10) and validate the produced report against
# agent-oumv/schemas/conformance-report.json.
#
# The fixture project is copied to a throwaway directory first, so the git
# worktree is never mutated by the conformance run (applyEdit cases create,
# patch, delete and rename files).
#
#   ./certify.sh            # stdio transport, writes certification/java-spring.json
#   ./certify.sh --http     # also runs the same suite over HTTP/JSON-RPC (temp report)
#
# Environment overrides:
#   OUMV_REPO   path to the agent-oumv checkout (default: ../agent-oumv)
#   REPORT      report path (default: <repo>/certification/java-spring.json)
#
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(cd "$HERE/.." && pwd)"
PROJECTS="$(cd "$REPO/.." && pwd)"
OUMV="${OUMV_REPO:-$PROJECTS/agent-oumv}"
REPORT="${REPORT:-$REPO/certification/java-spring.json}"
MIN_LEVEL="${MIN_LEVEL:-2}"
ADAPTER="$HERE/src/dev/tokensafe/oumv/OumvSpring.java"
MARKERS="$HERE/testdata/markers-spring.json"
FIXTURES="$OUMV/fixtures/conformance-cases.json"
CERTIFY="$OUMV/packages/conformance/dist/cli/certify.js"
VERIFY_FILE="$HERE/testdata/plain/Probe.java"

WITH_HTTP=0
for arg in "$@"; do
  case "$arg" in
    --http) WITH_HTTP=1 ;;
    -h|--help) sed -n '2,20p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "unknown argument: $arg" >&2; exit 2 ;;
  esac
done

command -v java >/dev/null 2>&1 || { echo "java (JDK 17+) is required" >&2; exit 2; }
command -v node >/dev/null 2>&1 || { echo "node is required for oumv-certify" >&2; exit 2; }
[ -f "$CERTIFY" ] || { echo "certify CLI not found: $CERTIFY (set OUMV_REPO)" >&2; exit 2; }
[ -f "$FIXTURES" ] || { echo "fixtures not found: $FIXTURES (set OUMV_REPO)" >&2; exit 2; }
[ -f "$MARKERS" ] || { echo "markers not found: $MARKERS" >&2; exit 2; }
[ -f "$VERIFY_FILE" ] || { echo "verify fixture not found: $VERIFY_FILE" >&2; exit 2; }

WORK="$(mktemp -d "${TMPDIR:-/tmp}/oumv-java-certify.XXXXXX")"
SERVER_PID=""
cleanup() {
  if [ -n "$SERVER_PID" ] && kill -0 "$SERVER_PID" 2>/dev/null; then
    kill "$SERVER_PID" 2>/dev/null || true
    wait "$SERVER_PID" 2>/dev/null || true
  fi
  rm -rf "$WORK"
}
trap cleanup EXIT

cp -R "$HERE/testdata/spring-app" "$WORK/app"
mkdir -p "$WORK/classes"
mkdir -p "$(dirname "$REPORT")"

echo "▸ workspace copy: $WORK/app"
echo "▸ verify command: javac -d $WORK/classes {file}"
echo

STDIO_CMD="java \"$ADAPTER\" --root \"$WORK/app\" --stdio --verify-file \"$VERIFY_FILE\" --verify-cmd \"javac -d $WORK/classes {file}\""

set +e
node "$CERTIFY" \
  --adapter java-spring \
  --framework spring \
  --profile backend \
  --stdio "$STDIO_CMD" \
  --fixtures "$FIXTURES" \
  --markers "@$MARKERS" \
  --report "$REPORT" \
  --min-level "$MIN_LEVEL"
CERTIFY_EXIT=$?
set -e

# oumv-certify exits 1 when any case fails. One Level 3 case
# (observe-l4-returns-recorded-traces) is expected to fail: this sidecar never
# runs the application, so no live trace exists to report. Judgement below is
# therefore made on the report, not on the raw exit code.
if [ "$CERTIFY_EXIT" -ne 0 ] && [ "$CERTIFY_EXIT" -ne 1 ]; then
  echo "✗ oumv-certify failed with exit code $CERTIFY_EXIT" >&2
  exit "$CERTIFY_EXIT"
fi

echo
echo "== validating $REPORT"

node -e '
const fs = require("fs");
const [reportPath, oumvRepo, minLevelRaw] = process.argv.slice(1);
const minLevel = Number(minLevelRaw);
const report = JSON.parse(fs.readFileSync(reportPath, "utf8"));

let schemaOk = true;
try {
  const Ajv2020 = require(oumvRepo + "/node_modules/ajv/dist/2020.js").default;
  const schema = JSON.parse(fs.readFileSync(oumvRepo + "/schemas/conformance-report.json", "utf8"));
  const validate = new Ajv2020({ allErrors: true, strict: false }).compile(schema);
  schemaOk = validate(report);
  if (!schemaOk) {
    console.error("schema errors:", JSON.stringify(validate.errors, null, 1));
  }
} catch (err) {
  console.warn("! ajv unavailable, skipping JSON-schema validation:", err.message);
}

// The only failures this adapter is allowed to have are the documented Level 3
// gaps. Anything at Level <= 2 failing means the adapter is not certified.
const DOCUMENTED_GAPS = new Set(["observe-l4-returns-recorded-traces"]);
const failures = report.cases.filter((c) => c.status !== "passed");
const undocumented = failures.filter((c) => c.level <= minLevel || !DOCUMENTED_GAPS.has(c.id));
const levelOk = report.attainedLevel >= minLevel;
const certified = schemaOk && undocumented.length === 0 && levelOk;

console.log("  attainedLevel : " + report.attainedLevel + " (required >= " + minLevel + ")");
console.log("  passed/total  : " + report.passed + "/" + report.total);
console.log("  levels        : " + JSON.stringify(report.levels));
console.log("  schema valid  : " + schemaOk);
for (const c of failures) {
  console.log("  documented L3 gap: " + c.id + " — " + c.error);
}
for (const c of undocumented) {
  console.log("  UNDOCUMENTED FAILURE (" + c.level + "): " + c.id + " — " + c.error);
}
if (!certified) {
  console.error("✗ certification claim not met");
  process.exit(1);
}
console.log("✓ certified: adapter java-spring attains OUMV Level " + report.attainedLevel +
  " with " + report.passed + "/" + report.total + " fixtures v" + report.fixtureVersion);
' "$REPORT" "$OUMV" "$MIN_LEVEL"

if [ "$WITH_HTTP" -eq 1 ]; then
  echo
  echo "== HTTP parity run (fresh fixture copy, temp report, not committed)"
  # The stdio run above mutated $WORK/app (create/patch/delete/rename cases), so the
  # HTTP run needs its own pristine copy of the fixture project.
  cp -R "$HERE/testdata/spring-app" "$WORK/app-http"
  mkdir -p "$WORK/classes-http"
  java "$ADAPTER" --root "$WORK/app-http" --port 0 \
    --verify-file "$VERIFY_FILE" --verify-cmd "javac -d $WORK/classes-http {file}" \
    >"$WORK/http.out" 2>"$WORK/http.err" &
  SERVER_PID=$!
  for _ in $(seq 1 100); do
    grep -q "url=" "$WORK/http.err" 2>/dev/null && break
    sleep 0.1
  done
  URL="$(grep -o 'url=http://[^ ]*' "$WORK/http.err" | head -1 | cut -d= -f2)"
  if [ -z "$URL" ]; then
    echo "✗ could not start the HTTP server" >&2
    cat "$WORK/http.err" >&2
    exit 1
  fi
  echo "  endpoint: $URL"
  set +e
  node "$CERTIFY" --adapter java-spring --framework spring --profile backend \
    --url "$URL" --quiet --fixtures "$FIXTURES" --markers "@$MARKERS" \
    --report "$WORK/http-report.json" --min-level "$MIN_LEVEL"
  HTTP_EXIT=$?
  set -e
  node -e '
const r = require(process.argv[1]);
const bad = r.cases.filter((c) => c.status !== "passed" && c.id !== "observe-l4-returns-recorded-traces");
console.log("  HTTP: level " + r.attainedLevel + " · " + r.passed + "/" + r.total +
  (bad.length ? " · UNEXPECTED: " + bad.map((c) => c.id).join(", ") : " · only the documented L4 gap"));
if (r.attainedLevel < Number(process.argv[2]) || bad.length) process.exit(1);
' "$WORK/http-report.json" "$MIN_LEVEL"
  kill "$SERVER_PID" 2>/dev/null || true
  wait "$SERVER_PID" 2>/dev/null || true
  SERVER_PID=""
  echo "  exit code from the non-zero run (expected 1): $HTTP_EXIT"
fi

echo
echo "== read-only profile (§8.5): method absence, proven by the read-only-* cases"
cp -R "$HERE/testdata/spring-app" "$WORK/app-ro"
RO_REPORT="${REPORT%.json}-readonly.json"
RO_STDIO_CMD="java \"$ADAPTER\" --root \"$WORK/app-ro\" --stdio --read-only --verify-file \"$VERIFY_FILE\""
set +e
node "$CERTIFY" --adapter java-spring --framework spring --profile backend \
  --stdio "$RO_STDIO_CMD" --fixtures "$FIXTURES" --markers "@$MARKERS" \
  --report "$RO_REPORT" --min-level 1 --quiet >/dev/null 2>&1
set -e
node -e '
const r = require(process.argv[1]);
const ro = r.cases.filter((c) => c.id.startsWith("read-only-"));
const failed = ro.filter((c) => c.status !== "passed");
const unexpected = r.cases.filter((c) => c.status !== "passed" && c.id !== "observe-l4-returns-recorded-traces");
console.log("  read-only: level " + r.attainedLevel + " · " + r.passed + "/" + r.total +
  " · " + ro.length + " read-only cases");
if (ro.length < 6) { console.error("✗ the read-only cases are missing from the report"); process.exit(1); }
if (failed.length) { console.error("✗ read-only failures: " + failed.map((c) => c.id + ": " + c.error).join("; ")); process.exit(1); }
if (unexpected.length) { console.error("✗ unexpected failures: " + unexpected.map((c) => c.id).join(", ")); process.exit(1); }
' "$RO_REPORT"

echo
echo "report: $REPORT"
