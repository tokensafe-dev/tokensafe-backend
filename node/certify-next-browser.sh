#!/usr/bin/env bash
# Certifies the Next.js browser bridge in a real Chrome.
#
# Builds the adapter if needed, installs the fixture app's dependencies once, and
# runs the headless-Chrome harness. The app is never part of the package.
#
#   ./certify-next-browser.sh [report-path]
#
# Needs: node, npm, and a Chrome install (playwright-core drives the system
# channel, so no browser download is involved). Takes a few minutes the first run.
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
app="$here/cert-next"
report="${1:-$here/../certification/next-app-router-browser.json}"

if [ ! -f "$here/dist/index.js" ]; then
  echo "▸ building tokensafe-backend-node"
  (cd "$here" && npm run build)
fi

if [ ! -d "$app/node_modules/next" ]; then
  echo "▸ installing the Next fixture app (first run only)"
  (cd "$app" && npm install --no-audit --no-fund --loglevel=error)
fi

shift || true
exec node "$here/certify-next-browser.mjs" --report "$report" "$@"
