#!/usr/bin/env bash
#
# JDK-only self-test for the Spring OUMV adapter.
#
# It copies the fixture project to a throwaway directory (the repository is never
# touched), compiles the adapter + Selftest with javac, and exercises handshake,
# observe L0-L3, getSource, applyEdit patch/OCC/guards, verify, understand and the
# session lifecycle over the stdio transport. No Node, no Maven, no Gradle.
#
#   ./selftest.sh
#
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SRC="$HERE/src/dev/tokensafe/oumv"

command -v java >/dev/null 2>&1 || { echo "java (JDK 17+) is required" >&2; exit 2; }
command -v javac >/dev/null 2>&1 || { echo "javac (JDK 17+) is required" >&2; exit 2; }

WORK="$(mktemp -d "${TMPDIR:-/tmp}/oumv-java-selftest.XXXXXX")"
cleanup() {
  rm -rf "$WORK"
}
trap cleanup EXIT

cp -R "$HERE/testdata/spring-app" "$WORK/app"
cp -R "$HERE/testdata/plain" "$WORK/plain"
mkdir -p "$WORK/classes" "$WORK/adapter-classes"

echo "== compiling adapter + selftest (javac, no dependencies)"
javac -d "$WORK/adapter-classes" "$SRC/OumvSpring.java" "$SRC/Selftest.java"

echo "== running selftest against a temp copy of testdata/spring-app"
java -cp "$WORK/adapter-classes" dev.tokensafe.oumv.Selftest \
  "$WORK/app" "$WORK/plain/Probe.java" "$SRC/OumvSpring.java"

echo
echo "fixture copy was discarded: $WORK"
