import crypto from 'node:crypto';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { createRequire } from 'node:module';

const require = createRequire(import.meta.url);

/** Fixtures + runner live in the OUMV repo: a sibling checkout locally, OUMV_REPO in CI. */
const AGENT_OUMV = process.env.OUMV_REPO
  ? path.resolve(process.env.OUMV_REPO)
  : path.resolve(__dirname, '../../../../agent-oumv');
const CONFORMANCE_CJS = path.join(AGENT_OUMV, 'packages/conformance/dist/index.cjs');
const FIXTURES = path.join(AGENT_OUMV, 'fixtures/conformance-cases.json');

export const hasConformanceRunner = fs.existsSync(CONFORMANCE_CJS) && fs.existsSync(FIXTURES);

export const EDIT_SOURCE = 'export const value = 41;\n';
export const EDIT_REPLACEMENT = 'export const value = 42;\n';
export const BROKEN_SOURCE = 'export function Broken( {\n';
export const FIXTURE_SOURCE = 'export function value() {\n  return 41;\n}\n';
export const SYMBOL_SOURCE = 'export function value() {\n  return 41;\n}\n';
export const SYMBOL_REPLACEMENT = 'export function value() {\n  return 42;\n}\n';
export const DELETE_SOURCE = 'export const deleteMe = true;\n';
export const RENAME_SOURCE = 'export const renameMe = true;\n';

export const sha = (content: string) =>
  'sha256:' + crypto.createHash('sha256').update(content, 'utf-8').digest('hex');

export interface Workspace {
  dir: string;
  markers: Record<string, string>;
  cleanup: () => void;
}

/** Temp workspace with the files the fixtures edit (reset per suite). */
export function createWorkspace(): Workspace {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'oumv-node-cert-'));
  fs.writeFileSync(path.join(dir, 'fixture.js'), FIXTURE_SOURCE);
  fs.writeFileSync(path.join(dir, 'edit_target.js'), EDIT_SOURCE);
  fs.writeFileSync(path.join(dir, 'symbol_target.js'), SYMBOL_SOURCE);
  fs.writeFileSync(path.join(dir, 'delete_target.js'), DELETE_SOURCE);
  fs.writeFileSync(path.join(dir, 'rename_source.js'), RENAME_SOURCE);
  return {
    dir,
    markers: {
      fixtureFile: 'fixture.js',
      missingFile: 'missing.js',
      editTarget: 'edit_target.js',
      newFile: 'created_new_file.js',
      editVersion: sha(EDIT_SOURCE),
      editContent: EDIT_REPLACEMENT,
      brokenContent: BROKEN_SOURCE,
      l2Target: 'GET /api/orders',
      l3Target: 'edit_target.js',
      verifyTarget: 'GET /api/orders',
      symbolFile: 'symbol_target.js',
      symbolName: 'value',
      symbolReplacement: SYMBOL_REPLACEMENT,
      symbolVersion: sha(SYMBOL_SOURCE),
      symbolFileContent: SYMBOL_REPLACEMENT,
      deleteFile: 'delete_target.js',
      deleteVersion: sha(DELETE_SOURCE),
      renameSource: 'rename_source.js',
      renameTarget: 'rename_target.js',
      renameVersion: sha(RENAME_SOURCE),
    },
    cleanup: () => fs.rmSync(dir, { recursive: true, force: true }),
  };
}

export interface FixtureRun {
  report: any;
}

/** Runs the language-neutral fixtures over HTTP and writes the report when asked. */
export async function runFixtures(
  url: string,
  markers: Record<string, string>,
  adapterName: string,
  framework: string
): Promise<FixtureRun> {
  const { loadFixtures, runFixtureSuite, httpTransport } = require(CONFORMANCE_CJS);
  const report = await runFixtureSuite(httpTransport({ url }), loadFixtures(FIXTURES), markers, {
    adapterName,
    framework,
    environment: { transport: 'http', node: process.version },
  });
  const outDir = process.env.CERT_OUT_DIR;
  if (outDir) {
    fs.mkdirSync(outDir, { recursive: true });
    fs.writeFileSync(path.join(outDir, `${adapterName.replace(/[/\\]/g, '-')}.json`), JSON.stringify(report, null, 2) + '\n');
  }
  return { report };
}

export function reportPath(adapterName: string): string {
  return path.join(process.env.CERT_OUT_DIR ?? '', `${adapterName}.json`);
}
