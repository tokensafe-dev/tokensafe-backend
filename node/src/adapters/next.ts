import fs from 'node:fs';
import path from 'node:path';
import { OumvRuntime, type OumvRuntimeOptions } from '../runtime.js';
import { deleteDescriptor, writeDescriptor } from '../descriptor.js';
import { createFetchHandlers, webHandlerToNode } from '../transport/fetch.js';
import { getOrCreateBridgeBroker } from '../browser/bridge.js';
import { createNextBridgeHandlers, type NextBridgeHandlers } from '../browser/handlers.js';

/**
 * Next.js App Router adapter (server profile).
 *
 * Next has no Vite dev server, no `configureServer` and no app object to
 * introspect: the runtime is mounted as a Route Handler and the L1 surface is
 * read from the `app/**\/route.{ts,tsx,js,jsx}` files, which is the only route
 * table Next actually has. The handlers speak Web `Request`/`Response`, so the
 * same module works behind `next dev`, `next start`, or any server that can
 * translate a Request.
 *
 *   // app/api/tokensafe/route.ts
 *   import { createNextRouteHandlers } from 'tokensafe-backend-node';
 *   const handlers = createNextRouteHandlers();
 *   export const POST = handlers.POST;
 *   export const GET = handlers.GET;
 *
 * The browser-side bridge (spec §7.4) is opt-in: `bridge: true` mounts the
 * Bridge Route Handlers and binds the runtime to the page broker, so client
 * components become observable and DOM-target verify works. It stays a
 * development tool — the handlers answer 404 in a production build.
 */

export interface NextAdapterOptions extends OumvRuntimeOptions {
  /** Workspace root the path jail and route scan are anchored to (default: cwd). */
  workspaceDir?: string;
  /** Next app directory (default: `<workspaceDir>/app`, then `<workspaceDir>/src/app`). */
  appDir?: string;
  /** Public JSON-RPC URL; when set, the discovery descriptor is written. */
  publicUrl?: string;
  /** Set false to skip the descriptor even with `publicUrl`. */
  descriptor?: boolean;
  /** Reuse an existing runtime instead of creating one. */
  runtime?: OumvRuntime;
  /**
   * Spec §7.4: mount the browser bridge for client components. Returns `BRIDGE`
   * (export it from `app/api/tokensafe/bridge/route.ts`) and advertises
   * `mutate.ephemeralDispatch` + `verify.surfaceDiff` while a page is attached.
   */
  bridge?: boolean;
  /**
   * In-process dispatcher for `oumv/verify` and causality traces. Next has no
   * application object to call, so the adopter supplies one Web handler that
   * routes to its own Route Handler modules (e.g. `(request) => GET(request)`);
   * without it verify answers RUNTIME_UNAVAILABLE, which is the honest answer.
   */
  verifyFetch?: (request: Request) => Promise<Response>;
}

export interface NextRouteHandlers {
  runtime: OumvRuntime;
  /** JSON-RPC over POST (spec §2.2), 200 for well-formed requests, 401 on a bad credential. */
  POST: (request: Request) => Promise<Response>;
  /** SSE event stream (spec §2.3) with `Last-Event-ID` replay. */
  GET: (request: Request) => Response;
  /** Present with `bridge: true`; export `GET`/`POST` from the bridge route file. */
  BRIDGE?: NextBridgeHandlers;
  dispose: () => Promise<void>;
}

const ROUTE_FILE = /^route\.(ts|tsx|js|jsx)$/;
const HTTP_METHODS = ['GET', 'POST', 'PUT', 'PATCH', 'DELETE', 'HEAD', 'OPTIONS'] as const;

/**
 * Next projects keep their App Router either at `app/` or at `src/app/`; both are
 * first-class, so the adapter probes instead of assuming.
 */
export function resolveAppDir(workspaceDir: string, explicit?: string): string {
  if (explicit) return explicit;
  const srcApp = path.join(workspaceDir, 'src', 'app');
  if (fs.existsSync(srcApp)) return srcApp;
  return path.join(workspaceDir, 'app');
}

/** Reads the exported method handlers out of the app directory. */
export function scanAppRouterRoutes(appDir: string): Array<{ method: string; path: string }> {
  const routes: Array<{ method: string; path: string }> = [];
  if (!fs.existsSync(appDir)) return routes;

  const walk = (dir: string, prefix: string): void => {
    let entries: fs.Dirent[];
    try {
      entries = fs.readdirSync(dir, { withFileTypes: true });
    } catch {
      return;
    }
    for (const entry of entries) {
      if (entry.name.startsWith('.')) continue;
      const full = path.join(dir, entry.name);
      if (entry.isDirectory()) {
        // Route groups `(marketing)` do not contribute a path segment.
        const segment = entry.name.startsWith('(') && entry.name.endsWith(')') ? '' : entry.name;
        walk(full, segment ? `${prefix}/${segment}` : prefix);
        continue;
      }
      if (!ROUTE_FILE.test(entry.name)) continue;
      let source = '';
      try {
        source = fs.readFileSync(full, 'utf-8');
      } catch {
        continue;
      }
      for (const method of HTTP_METHODS) {
        const exported = new RegExp(`export\\s+(?:async\\s+)?(?:function\\s+|const\\s+|let\\s+)?${method}\\b`);
        if (exported.test(source)) routes.push({ method, path: prefix || '/' });
      }
    }
  };

  walk(appDir, '');
  return routes;
}

export function createNextRouteHandlers(options: NextAdapterOptions = {}): NextRouteHandlers {
  const workspaceDir = options.workspaceDir ?? process.cwd();
  const runtime =
    options.runtime ??
    new OumvRuntime({
      ...options,
      workspaceDir,
    });
  const token = options.authToken ?? runtime.authToken ?? '';
  const handlers = createFetchHandlers({
    runtime,
    authToken: options.authToken,
    maxBodyBytes: options.maxBodyBytes,
    // Next Route Handlers are same-origin; the Node/Express binding owns CORS there.
    cors: false,
  });

  for (const route of scanAppRouterRoutes(resolveAppDir(workspaceDir, options.appDir))) {
    runtime.registerRoute(route.method, route.path, 'route');
  }
  if (options.verifyFetch) runtime.setHandler(webHandlerToNode(options.verifyFetch));
  runtime.setTransports(['http', 'sse']);

  let bridge: NextBridgeHandlers | undefined;
  if (options.bridge) {
    // One broker per workspace per process: the bridge route compiles into its own
    // bundle, so this must be a process-global lookup, not a module singleton.
    const broker = getOrCreateBridgeBroker(workspaceDir, runtime);
    runtime.setBrowserBridge(broker);
    bridge = createNextBridgeHandlers({ workspaceDir, broker, runtime });
  }

  if (options.publicUrl && options.descriptor !== false) {
    writeDescriptor(workspaceDir, {
      type: 'backend',
      name: runtime.name,
      version: runtime.version,
      url: options.publicUrl,
      ...(token ? { sessionToken: token } : {}),
      workspace: workspaceDir,
      capabilities: { observeLevels: ['L0', 'L1', 'L2', 'L3', 'L4'], transports: ['http', 'sse'] },
      ...(bridge ? { frontend: { bridge: true, clientPath: '/api/tokensafe/bridge?client=1' } } : {}),
      startedAt: new Date().toISOString(),
    });
  }

  return {
    runtime,
    POST: handlers.POST,
    GET: handlers.GET,
    ...(bridge ? { BRIDGE: bridge } : {}),
    dispose: async () => {
      deleteDescriptor(workspaceDir);
      await runtime.dispose();
    },
  };
}
