#!/usr/bin/env node
/**
 * Vendors the Next.js browser bridge module into the backend.
 *
 * The generator lives in agent-dom (`tokensafe-ui/next`, built from
 * `packages/vite/src/bridge-core.ts`); the backend serves the resulting string
 * from its Bridge Route Handler. Vendoring keeps the server package free of a
 * runtime dependency on the client package while the shared core stays the one
 * source of truth.
 *
 *   node scripts/gen-next-bridge-client.mjs --from ../agent-dom/packages/tokensafe-ui/dist/next.js
 *   node scripts/gen-next-bridge-client.mjs --from <...> --check
 *
 * Without `--from` neither mode runs: CI that does not have the client source
 * checks nothing rather than checking against itself. The committed file is the
 * reviewed artifact.
 */
import { readFileSync, writeFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const root = join(dirname(fileURLToPath(import.meta.url)), '..');
const target = join(root, 'src/browser/client-code.ts');
const arg = (name) => {
  const index = process.argv.indexOf(name);
  return index > -1 ? process.argv[index + 1] : undefined;
};

const from = arg('--from') ?? process.env.TOKENSAFE_UI_NEXT;
if (!from) {
  console.log('gen-next-bridge-client: no --from source; nothing to do (the committed file is authoritative).');
  process.exit(0);
}

const source = resolve(from);
const mod = await import(pathToFileURL(source).href);
if (typeof mod.getNextReactBridgeClientCode !== 'function') {
  console.error(`gen-next-bridge-client: ${source} does not export getNextReactBridgeClientCode`);
  process.exit(2);
}
const code = mod.getNextReactBridgeClientCode();

const header = `/**
 * GENERATED FILE — do not edit.
 *
 * The Next.js bridge client, generated from \`tokensafe-ui/next\`
 * (\`packages/vite/src/bridge-core.ts\` in agent-dom). It is vendored so the Route
 * Handler can serve the browser module without a build step and without a
 * runtime dependency on the client package. Regenerate with:
 *
 *   node scripts/gen-next-bridge-client.mjs --from <path-to-tokensafe-ui/dist/next.js>
 *
 * \`--check\` compares this file with the given source; CI without the source skips.
 */
export const NEXT_BRIDGE_CLIENT_CODE: string = ${JSON.stringify(code)};
`;

if (process.argv.includes('--check')) {
  let onDisk = '';
  try {
    onDisk = readFileSync(target, 'utf-8');
  } catch {
    console.error('gen-next-bridge-client: src/browser/client-code.ts is missing');
    process.exit(1);
  }
  if (onDisk !== header) {
    console.error('client-code.ts is stale: run `node scripts/gen-next-bridge-client.mjs --from <source>`');
    process.exit(1);
  }
  console.log('client-code.ts is up to date.');
} else {
  writeFileSync(target, header);
  console.log(`Wrote src/browser/client-code.ts (${code.length} bytes of client code).`);
}
