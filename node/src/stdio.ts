#!/usr/bin/env node
/**
 * Headless OUMV runtime over stdio (spec §2.5).
 *
 *   tokensafe-oumv-stdio --workspace /path/to/app
 *   tokensafe-oumv-stdio --app ./src/app.js        # imports an Express/Fastify app
 *
 * One JSON-RPC object per line on stdin, one response per line on stdout;
 * diagnostics stay on stderr. Without `--app` the runtime still serves the
 * file-based pillar (observe L3, getSource, applyEdit); with it, routes and
 * in-process verify work exactly as they do behind HTTP.
 */
import path from 'node:path';
import { pathToFileURL } from 'node:url';
import { OumvRuntime } from './runtime.js';
import { attachStdio } from './transport/stdio.js';
import { mountTokensafe } from './adapters/express.js';
import { registerTokensafe } from './adapters/fastify.js';

const flag = (name: string): string | undefined => {
  const index = process.argv.indexOf(name);
  return index > -1 ? process.argv[index + 1] : undefined;
};

const isFastify = (value: any): boolean =>
  Boolean(value) &&
  typeof value.ready === 'function' &&
  typeof value.inject === 'function' &&
  typeof value.register === 'function';

const main = async () => {
  const appPath = flag('--app');
  const configuredWorkspace = flag('--workspace') || process.env.TOKENSAFE_WORKSPACE;
  const workspaceDir = path.resolve(
    configuredWorkspace || (appPath ? path.dirname(path.resolve(appPath)) : process.cwd())
  );

  let runtime: OumvRuntime;
  if (appPath) {
    const module = await import(pathToFileURL(path.resolve(appPath)).href);
    const app = module.default ?? module.app ?? module;
    if (isFastify(app)) {
      runtime = new OumvRuntime({ workspaceDir });
      await registerTokensafe(app, { runtime });
      await app.ready();
    } else {
      runtime = mountTokensafe(app, { workspaceDir }).runtime;
    }
  } else {
    runtime = new OumvRuntime({ workspaceDir });
  }

  runtime.setTransports(['stdio']);
  const attachment = attachStdio(runtime);
  const shutdown = async () => {
    attachment.close();
    await runtime.dispose();
    process.exit(0);
  };
  process.on('SIGINT', () => void shutdown());
  process.on('SIGTERM', () => void shutdown());
};

void main().catch((error: any) => {
  process.stderr.write(`tokensafe-oumv-stdio: ${error?.stack ?? error}\n`);
  process.exit(1);
});
