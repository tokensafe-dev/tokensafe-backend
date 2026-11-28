import fs from 'node:fs';
import type http from 'node:http';
import path from 'node:path';
import { OumvRuntime, type OumvRuntimeOptions } from '../runtime.js';
import { deleteDescriptor, writeDescriptor } from '../descriptor.js';
import { ERROR_CODES } from '../errors.js';
import { Dispatcher, dispatchAll, failure, parsePayload, type JsonRpcRequest } from '../transport/dispatcher.js';

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
 * The browser-side bridge is a separate piece of work: this adapter certifies the
 * server profile (observe/understand/verify/mutate over JSON-RPC), not DOM
 * mutation of client components.
 */

export interface NextAdapterOptions extends OumvRuntimeOptions {
  /** Workspace root the path jail and route scan are anchored to (default: cwd). */
  workspaceDir?: string;
  /** Next app directory (default: `<workspaceDir>/app`). */
  appDir?: string;
  /** Public JSON-RPC URL; when set, the discovery descriptor is written. */
  publicUrl?: string;
  /** Set false to skip the descriptor even with `publicUrl`. */
  descriptor?: boolean;
  /** Reuse an existing runtime instead of creating one. */
  runtime?: OumvRuntime;
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
  dispose: () => Promise<void>;
}

const ROUTE_FILE = /^route\.(ts|tsx|js|jsx)$/;
const HTTP_METHODS = ['GET', 'POST', 'PUT', 'PATCH', 'DELETE', 'HEAD', 'OPTIONS'] as const;

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

/** Spec §12: a configured token is always required; no token stays open for dev. */
function isAuthorizedRequest(request: Request, token: string): boolean {
  if (!token) return true;
  const authorization = request.headers.get('authorization') ?? '';
  const provided =
    request.headers.get('x-oumv-session-token') ??
    (authorization.toLowerCase().startsWith('bearer ') ? authorization.slice(7).trim() : '');
  return Boolean(provided) && provided === token;
}

const jsonResponse = (payload: unknown, status = 200): Response =>
  new Response(JSON.stringify(payload), { status, headers: { 'Content-Type': 'application/json' } });

/** Bridges a Web handler into the Node listener the runtime dispatches verify through. */
function webHandlerToNode(fetchHandler: (request: Request) => Promise<Response>): http.RequestListener {
  return (req, res) => {
    void (async () => {
      const chunks: Buffer[] = [];
      for await (const chunk of req) chunks.push(Buffer.from(chunk));
      const headers = new Headers();
      for (const [key, value] of Object.entries(req.headers)) {
        if (typeof value === 'string') headers.set(key, value);
      }
      const request = new Request(`http://${req.headers.host ?? '127.0.0.1'}${req.url ?? '/'}`, {
        method: req.method,
        headers,
        ...(chunks.length > 0 && req.method !== 'GET' && req.method !== 'HEAD'
          ? { body: Buffer.concat(chunks) }
          : {}),
      });
      const response = await fetchHandler(request);
      res.statusCode = response.status;
      response.headers.forEach((value, key) => res.setHeader(key, value));
      res.end(Buffer.from(await response.arrayBuffer()));
    })().catch(() => {
      if (!res.headersSent) res.statusCode = 500;
      res.end();
    });
  };
}

export function createNextRouteHandlers(options: NextAdapterOptions = {}): NextRouteHandlers {
  const workspaceDir = options.workspaceDir ?? process.cwd();
  const runtime =
    options.runtime ??
    new OumvRuntime({
      ...options,
      workspaceDir,
    });
  const dispatcher = new Dispatcher(runtime);
  const token = options.authToken ?? runtime.authToken ?? '';

  for (const route of scanAppRouterRoutes(options.appDir ?? path.join(workspaceDir, 'app'))) {
    runtime.registerRoute(route.method, route.path, 'route');
  }
  if (options.verifyFetch) runtime.setHandler(webHandlerToNode(options.verifyFetch));
  runtime.setTransports(['http', 'sse']);

  if (options.publicUrl && options.descriptor !== false) {
    writeDescriptor(workspaceDir, {
      type: 'backend',
      name: runtime.name,
      version: runtime.version,
      url: options.publicUrl,
      ...(token ? { sessionToken: token } : {}),
      workspace: workspaceDir,
      capabilities: { observeLevels: ['L0', 'L1', 'L2', 'L3', 'L4'], transports: ['http', 'sse'] },
      startedAt: new Date().toISOString(),
    });
  }

  const POST = async (request: Request): Promise<Response> => {
    if (!isAuthorizedRequest(request, token)) {
      return jsonResponse(
        failure(null, ERROR_CODES.PERMISSION_DENIED, 'PERMISSION_DENIED: invalid OUMV session token'),
        401
      );
    }
    let payload: JsonRpcRequest | JsonRpcRequest[];
    try {
      payload = parsePayload(await request.text());
    } catch (error: any) {
      return jsonResponse(failure(null, ERROR_CODES.PARSE_ERROR, error?.message ?? 'PARSE_ERROR'));
    }
    try {
      return jsonResponse(await dispatchAll(dispatcher, payload));
    } catch (error: any) {
      return jsonResponse(
        failure(null, error?.code ?? ERROR_CODES.INTERNAL_ERROR, error?.message ?? 'INTERNAL_ERROR')
      );
    }
  };

  const GET = (request: Request): Response => {
    if (!isAuthorizedRequest(request, token)) {
      return jsonResponse(
        failure(null, ERROR_CODES.PERMISSION_DENIED, 'PERMISSION_DENIED: invalid OUMV session token'),
        401
      );
    }
    const encoder = new TextEncoder();
    const rawCursor = request.headers.get('last-event-id') ?? '0';
    let cursor = Number.parseInt(rawCursor, 10);
    if (!Number.isFinite(cursor) || cursor < 0) cursor = 0;

    const stream = new ReadableStream<Uint8Array>({
      start(controller) {
        let closed = false;
        const write = (text: string) => {
          if (closed) return;
          try {
            controller.enqueue(encoder.encode(text));
          } catch {
            closed = true;
          }
        };
        write(': connected\n\n');
        for (const event of runtime.eventsSince(cursor)) {
          write(`id: ${event.id}\nevent: ${event.type}\ndata: ${JSON.stringify(event.data)}\n\n`);
          cursor = event.id;
        }
        const unsubscribe = runtime.subscribeEvents((event) => {
          if (event.id <= cursor) return;
          cursor = event.id;
          write(`id: ${event.id}\nevent: ${event.type}\ndata: ${JSON.stringify(event.data)}\n\n`);
        });
        const heartbeat = setInterval(() => write(': heartbeat\n\n'), 15_000);
        if (typeof heartbeat === 'object' && 'unref' in heartbeat) heartbeat.unref();
        const close = () => {
          if (closed) return;
          closed = true;
          clearInterval(heartbeat);
          unsubscribe();
          try {
            controller.close();
          } catch {
            // already closed by the consumer
          }
        };
        request.signal.addEventListener('abort', close);
      },
    });

    return new Response(stream, {
      status: 200,
      headers: {
        'Content-Type': 'text/event-stream',
        'Cache-Control': 'no-cache',
        Connection: 'keep-alive',
        'X-Accel-Buffering': 'no',
      },
    });
  };

  return {
    runtime,
    POST,
    GET,
    dispose: async () => {
      deleteDescriptor(workspaceDir);
      await runtime.dispose();
    },
  };
}
