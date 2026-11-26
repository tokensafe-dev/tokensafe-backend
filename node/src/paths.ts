import fs from 'node:fs';
import path from 'node:path';
import { securityViolation } from './errors.js';

const SENSITIVE = /(^|[\\/])(\.env(\..*)?|id_rsa|id_ed25519|\.npmrc|\.pypirc|credentials\.json|secrets\.ya?ml)$/i;

/**
 * Realpath jail (spec §4.6): every file operation is confined to the workspace,
 * symlinks are resolved first, credential files are refused. Runs before any other
 * guard, which is why the error is always `-32003`.
 */
export function resolveWorkspacePath(inputPath: string, workspaceDir: string): string {
  let canonicalRoot = path.resolve(workspaceDir);
  try {
    if (fs.existsSync(canonicalRoot)) canonicalRoot = fs.realpathSync(canonicalRoot);
  } catch {}

  let resolved = path.isAbsolute(inputPath)
    ? path.resolve(inputPath)
    : path.resolve(canonicalRoot, inputPath);
  try {
    if (fs.existsSync(resolved)) resolved = fs.realpathSync(resolved);
  } catch {}

  const rootWithSep = canonicalRoot.endsWith(path.sep) ? canonicalRoot : canonicalRoot + path.sep;
  if (resolved !== canonicalRoot && !resolved.startsWith(rootWithSep)) {
    throw securityViolation(`SECURITY_VIOLATION: path traversal blocked: ${JSON.stringify(inputPath)} escapes the workspace`);
  }
  if (SENSITIVE.test(resolved)) {
    throw securityViolation(`SECURITY_VIOLATION: access to a credential file is blocked: ${JSON.stringify(inputPath)}`);
  }
  return resolved;
}

/** Splits `"path#symbol"` into its parts (spec §4.1 target grammar). */
export function splitSourceTarget(target: string): { path: string; symbol?: string } {
  const [rawPath, symbol] = target.split('#');
  return { path: rawPath.trim(), symbol: symbol?.trim() || undefined };
}
