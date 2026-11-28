#!/usr/bin/env node
/**
 * Certifies the Next.js browser bridge in a real Chrome.
 *
 * Starts the fixture app (`cert-next/`) with `next dev`, loads it in headless
 * Chrome through playwright-core, waits until the instrumentation client has
 * installed the bridge, and then runs `oumv-certify --browser` against the app.
 * With a real tab attached, observe L1/L4 read the page and `oumv/verify`
 * dispatches synthetic DOM events, so the three `browserCases` are certified
 * rather than claimed.
 *
 *   node certify-next-browser.mjs \
 *     --report ../certification/next-app-router-browser.json \
 *     --certify ../../agent-oumv/packages/conformance/dist/cli/certify.js
 *
 * Needs `cert-next/node_modules` (npm install) and a built `dist/` — the
 * `certify-next-browser.sh` wrapper does both.
 */
import fs from 'node:fs';
import crypto from 'node:crypto';
import net from 'node:net';
import os from 'node:os';
import path from 'node:path';
import { spawn } from 'node:child_process';
import { createRequire } from 'node:module';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const arg = (name, fallback) => {
  const index = process.argv.indexOf(name);
  return index > -1 ? process.argv[index + 1] : fallback;
};

const appDir = path.resolve(arg('--app', path.join(here, 'cert-next')));
const reportPath = path.resolve(arg('--report', path.join(here, '..', 'certification', 'next-app-router-browser.json')));
const certifyPath = path.resolve(
  arg('--certify', path.join(here, '..', '..', 'agent-oumv', 'packages', 'conformance', 'dist', 'cli', 'certify.js'))
);
const fixturesPath = path.resolve(
  arg('--fixtures', path.join(here, '..', '..', 'agent-oumv', 'fixtures', 'conformance-cases.json'))
);
const browserChannel = arg('--channel', 'chrome');

if (!fs.existsSync(path.join(appDir, 'node_modules', 'next'))) {
  console.error(`✗ ${appDir}/node_modules is missing — run certify-next-browser.sh (it installs the fixture app)`);
  process.exit(2);
}
if (!fs.existsSync(certifyPath)) {
  console.error(`✗ conformance CLI not found at ${certifyPath} — build agent-oumv packages/conformance first`);
  process.exit(2);
}
if (!fs.existsSync(path.join(here, 'dist', 'index.js'))) {
  console.error('✗ tokensafe-backend-node is not built — run `pnpm build` in node/ first');
  process.exit(2);
}

const requireFromApp = createRequire(path.join(appDir, 'package.json'));
const { chromium } = requireFromApp('playwright-core');

const freePort = () =>
  new Promise((resolve, reject) => {
    const server = net.createServer();
    server.unref();
    server.on('error', reject);
    server.listen(0, '127.0.0.1', () => {
      const { port } = server.address();
      server.close(() => resolve(port));
    });
  });

const waitForServer = async (url, timeoutMs = 120_000) => {
  const startedAt = Date.now();
  for (;;) {
    try {
      const response = await fetch(url, { signal: AbortSignal.timeout(2000) });
      if (response.ok) return;
    } catch {
      // not up yet
    }
    if (Date.now() - startedAt > timeoutMs) throw new Error(`server did not answer on ${url} within ${timeoutMs} ms`);
    await new Promise((resolve) => setTimeout(resolve, 500));
  }
};

const run = (command, args, options = {}) =>
  new Promise((resolve) => {
    const child = spawn(command, args, { stdio: 'inherit', ...options });
    child.on('exit', (code) => resolve(code ?? 1));
  });

// The fixture files the language-neutral cases edit. They are not imported by
// the app, so writing them does not disturb the dev server.
const FILES = {
  'fixture.js': 'export function value() {\n  return 41;\n}\n',
  'edit_target.js': 'export const value = 41;\n',
  'symbol_target.js': 'export function probe(value) {\n  return 41;\n}\n',
  'delete_target.js': 'export const deleteMe = true;\n',
  'rename_source.js': 'export const renameMe = true;\n',
};
for (const [name, content] of Object.entries(FILES)) {
  fs.writeFileSync(path.join(appDir, name), content, 'utf-8');
}
fs.rmSync(path.join(appDir, 'rename_target.js'), { force: true });
for (const name of ['created_new_file.js', 'batch_a.js', 'batch_b.js']) {
  fs.rmSync(path.join(appDir, name), { force: true });
}

const sha = (text) => 'sha256:' + crypto.createHash('sha256').update(text, 'utf-8').digest('hex');

const markers = {
  fixtureFile: 'fixture.js',
  missingFile: 'missing.js',
  editTarget: 'edit_target.js',
  newFile: 'created_new_file.js',
  editVersion: sha(FILES['edit_target.js']),
  editContent: 'export const value = 42;\n',
  editContentVersion: sha('export const value = 42;\n'),
  patchReplacement: 'export const value = 42;\n// patched by conformance\n',
  brokenContent: 'export function Broken( {\n',
  l2Target: 'GET /api/orders',
  l3Target: 'edit_target.js',
  verifyTarget: 'GET /api/orders',
  symbolFile: 'symbol_target.js',
  symbolName: 'probe',
  symbolReplacement: 'export function probe(value) {\n  return 42;\n}\n',
  symbolVersion: sha(FILES['symbol_target.js']),
  symbolFileContent: 'export function probe(value) {\n  return 42;\n}\n',
  deleteFile: 'delete_target.js',
  deleteVersion: sha(FILES['delete_target.js']),
  renameSource: 'rename_source.js',
  renameTarget: 'rename_target.js',
  renameVersion: sha(FILES['rename_source.js']),
  batchFileA: 'batch_a.js',
  batchFileB: 'batch_b.js',
  batchContent: 'export const batch = true;\n',
  browserTarget: 'probe-btn',
  browserAction: 'click',
};
const markersFile = path.join(os.tmpdir(), `oumv-next-browser-markers-${process.pid}.json`);
fs.writeFileSync(markersFile, JSON.stringify(markers, null, 2));

const port = await freePort();
const baseUrl = `http://127.0.0.1:${port}`;
console.log(`▸ next dev: ${baseUrl}`);
const nextDev = spawn(process.execPath, [path.join(appDir, 'node_modules', 'next', 'dist', 'bin', 'next'), 'dev', '-p', String(port), '-H', '127.0.0.1'], {
  cwd: appDir,
  env: { ...process.env, NODE_ENV: 'development' },
  stdio: ['ignore', 'inherit', 'inherit'],
});

let exitCode = 1;
let browser;
try {
  await waitForServer(`${baseUrl}/`);
  browser = await chromium.launch({ channel: browserChannel, headless: true });
  const page = await browser.newPage();
  page.on('console', (message) => {
    if (message.type() === 'error') console.log(`  [browser] ${message.text()}`);
  });
  await page.goto(`${baseUrl}/`, { waitUntil: 'load' });
  await page.waitForFunction(() => Boolean(window.__OUMV_REACT_BRIDGE_INSTALLED__), { timeout: 60_000 });
  // Registration, the first snapshot and the command stream need a moment.
  await page.waitForTimeout(1000);

  fs.rmSync(reportPath, { force: true });
  const code = await run(process.execPath, [
    certifyPath,
    '--adapter',
    'next-app-router-browser',
    '--framework',
    'nextjs',
    '--profile',
    'backend',
    '--url',
    `${baseUrl}/api/tokensafe`,
    '--fixtures',
    fixturesPath,
    '--markers',
    `@${markersFile}`,
    '--report',
    reportPath,
    '--min-level',
    '1',
    '--browser',
    '--timeout',
    process.env.OUMV_CERT_TIMEOUT || '60000',
  ]);
  fs.rmSync(markersFile, { force: true });

  if (fs.existsSync(reportPath)) {
    const report = JSON.parse(fs.readFileSync(reportPath, 'utf-8'));
    report.environment = {
      ...(report.environment || {}),
      transport: 'http',
      browser: `${browserChannel} (headless)`,
      next: '15.5.29 (next dev)',
      runtime: 'next-app-router-route-handler + instrumentation-client',
      note: 'real Chrome tab connected: the instrumentation client registered a bridge session; verify dispatched a synthetic DOM click and L4 traces come from the page',
    };
    fs.writeFileSync(reportPath, JSON.stringify(report, null, 2) + '\n');
    console.log(`\nlevel ${report.attainedLevel} · ${report.passed}/${report.total} · report: ${reportPath}`);
  }
  exitCode = code;
} finally {
  await browser?.close().catch(() => undefined);
  nextDev.kill('SIGTERM');
  setTimeout(() => nextDev.kill('SIGKILL'), 5000).unref();
}
process.exit(exitCode);
