import crypto from 'node:crypto';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';

/**
 * Workspace discovery registry (spec §9.2). One file per workspace, keyed by
 * profile, so a Vite frontend and this backend never overwrite each other.
 * Written 0600, outside the repository.
 */
export interface RuntimeDescriptor {
  type: 'backend';
  name: string;
  version: string;
  url: string;
  sessionToken?: string;
  workspace?: string;
  capabilities: { observeLevels: string[]; transports: string[] };
  /**
   * Spec §7.4: a server runtime with a browser bridge tells agents where the page
   * side lives, so discovery does not need a second convention.
   */
  frontend?: { bridge: boolean; clientPath?: string };
  startedAt?: string;
}

export function descriptorPath(workspaceDir: string, home?: string): string {
  const root = process.env.TOKENSAFE_HOME || home || path.join(os.homedir(), '.tokensafe');
  const canonical = fs.existsSync(workspaceDir) ? fs.realpathSync(workspaceDir) : path.resolve(workspaceDir);
  const digest = crypto.createHash('sha256').update(canonical).digest('hex').slice(0, 16);
  return path.join(root, 'runtimes', `${digest}.json`);
}

function readRegistry(file: string): Record<string, unknown> {
  try {
    const raw = fs.readFileSync(file, 'utf-8');
    const parsed = JSON.parse(raw);
    return parsed && typeof parsed === 'object' ? parsed : {};
  } catch {
    return {};
  }
}

function writeRegistry(file: string, registry: Record<string, unknown>): void {
  fs.writeFileSync(file, JSON.stringify(registry, null, 2) + '\n', { encoding: 'utf-8', mode: 0o600 });
  fs.chmodSync(file, 0o600);
}

export function writeDescriptor(workspaceDir: string, descriptor: RuntimeDescriptor, home?: string): string {
  const file = descriptorPath(workspaceDir, home);
  fs.mkdirSync(path.dirname(file), { recursive: true });
  const registry = readRegistry(file);
  registry.backend = descriptor;
  registry.workspaceRoot = fs.existsSync(workspaceDir) ? fs.realpathSync(workspaceDir) : path.resolve(workspaceDir);
  registry.pid = process.pid;
  registry.updatedAt = Date.now();
  writeRegistry(file, registry);
  return file;
}

/** Removes only this profile's entry; a frontend entry keeps the file alive. */
export function deleteDescriptor(workspaceDir: string, home?: string): void {
  const file = descriptorPath(workspaceDir, home);
  const registry = readRegistry(file);
  if (Object.keys(registry).length === 0) return;
  delete registry.backend;
  if (registry.frontend) {
    writeRegistry(file, registry);
    return;
  }
  try {
    fs.unlinkSync(file);
  } catch {}
}
