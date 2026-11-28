import crypto from 'node:crypto';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { createRequire } from 'node:module';

const require = createRequire(import.meta.url);

/** Fixtures + runner live in the OUMV repo: a sibling checkout locally, OUMV_REPO in CI. */
const AGENT_OUMV = process.env.OUMV_REPO
  ? path.resolve(process.env.OUMV_REPO)
  : path.resolve(import.meta.dir, '../../../agent-oumv');
export const CONFORMANCE_ENTRY = path.join(AGENT_OUMV, 'packages/conformance/dist/index.cjs');
export const FIXTURES_PATH = path.join(AGENT_OUMV, 'fixtures/conformance-cases.json');
export const hasConformanceRunner = fs.existsSync(CONFORMANCE_ENTRY) && fs.existsSync(FIXTURES_PATH);

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

/** Temp workspace with the files the fixtures edit (same layout as the Node/Next certs). */
export function createWorkspace(): Workspace {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'oumv-bun-cert-'));
  fs.writeFileSync(path.join(dir, 'fixture.js'), FIXTURE_SOURCE);
  fs.writeFileSync(path.join(dir, 'edit_target.js'), EDIT_SOURCE);
  fs.writeFileSync(path.join(dir, 'symbol_target.js'), SYMBOL_SOURCE);
  fs.writeFileSync(path.join(dir, 'delete_target.js'), DELETE_SOURCE);
  fs.writeFileSync(path.join(dir, 'rename_source.js'), RENAME_SOURCE);
  const overrides = {
    fixtureFile: 'fixture.js',
    missingFile: 'missing.js',
    editTarget: 'edit_target.js',
    newFile: 'created_new_file.js',
    editVersion: sha(EDIT_SOURCE),
    editContent: EDIT_REPLACEMENT,
    editContentVersion: sha(EDIT_REPLACEMENT),
    patchReplacement: `${EDIT_REPLACEMENT}\n// patched by conformance\n`,
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
    sandboxTarget: 'POST /__oumv_sandbox_probe',
    // `.js` names so the syntax pre-flight actually parses the batch files.
    batchFileA: 'batch_a.js',
    batchFileB: 'batch_b.js',
    batchContent: 'export const batch = true;\n',
  };
  const defaults = hasConformanceRunner ? require(CONFORMANCE_ENTRY).defaultMarkers() : {};
  return {
    dir,
    markers: { ...defaults, ...overrides } as Record<string, string>,
    cleanup: () => fs.rmSync(dir, { recursive: true, force: true }),
  };
}

/** Writes a certification report under CERT_OUT_DIR when set (CI/release runs). */
export function writeReport(name: string, report: unknown): void {
  const outDir = process.env.CERT_OUT_DIR;
  if (!outDir) return;
  const target = path.join(outDir, name);
  fs.mkdirSync(path.dirname(target), { recursive: true });
  fs.writeFileSync(target, JSON.stringify(report, null, 2) + '\n');
}
