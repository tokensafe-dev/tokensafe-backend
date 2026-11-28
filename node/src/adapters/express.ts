import type { IncomingMessage, Server } from 'node:http';
import type { Express } from 'express';
import path from 'node:path';
import { deleteDescriptor, writeDescriptor } from '../descriptor.js';
import { OumvRuntime, type OumvRuntimeOptions } from '../runtime.js';
import { createHttpHandler } from '../transport/http.js';
import { createSseHandler } from '../transport/sse.js';
import { withDetectedSandbox } from '../sandbox-detect.js';
import { attachWebSocket, type WsAttachment } from '../transport/ws.js';

export interface ExpressAdapterOptions extends OumvRuntimeOptions {
  /** Reuse an existing runtime (created before routes when tracing must wrap them). */
  runtime?: OumvRuntime;
  /** Workspace root for path jails and OCC (defaults to cwd). */
  workspaceDir?: string;
  /** Public JSON-RPC URL; when set, the discovery descriptor is written. */
  publicUrl?: string;
  requireAuth?: boolean;
  descriptor?: boolean;
}

export interface ExpressAdapter {
  runtime: OumvRuntime;
  /** Mounts the WebSocket upgrade on the http server that serves this app. */
  attachWebSocket: (server: Server) => WsAttachment;
  dispose: () => Promise<void>;
}

interface ExpressLayer {
  route?: { path: string; methods: Record<string, boolean>; stack?: Array<{ name?: string }> };
  name?: string;
  handle?: { stack?: ExpressLayer[] };
}

/** Best-effort route introspection for Express 4 (`_router`) and 5 (`router`). */
function collectExpressRoutes(app: Express): Array<{ method: string; path: string; handlerName?: string }> {
  const anyApp = app as any;
  // Express 4.22 exposes `app.router` as a throwing deprecation getter; `_router`
  // is the stable internal for 4.x, `router` is the public one in 5.x.
  let stack: ExpressLayer[] = [];
  try {
    stack = anyApp._router?.stack ?? [];
  } catch {}
  if (stack.length === 0) {
    try {
      stack = anyApp.router?.stack ?? [];
    } catch {}
  }
  const routes: Array<{ method: string; path: string; handlerName?: string }> = [];
  for (const layer of stack) {
    if (layer.route) {
      const methods = Object.keys(layer.route.methods ?? {}).filter((m) => layer.route!.methods[m]);
      const handlerName = layer.route.stack?.find((entry) => entry?.name && entry.name !== 'router')?.name;
      for (const method of methods) {
        routes.push({ method: method.toUpperCase(), path: layer.route.path, handlerName });
      }
    }
  }
  return routes;
}

/**
 * Mounts the OUMV runtime on an Express app:
 *
 *   const oumv = mountTokensafe(app, { publicUrl: 'http://127.0.0.1:3000/__oumv/rpc' });
 *   const server = app.listen(3000); oumv.attachWebSocket(server);
 *
 * Must be called before the routes you want in L1, or those routes are registered
 * lazily on the first observe (see `runtime.registerRoute`).
 */
export function mountTokensafe(app: Express, options: ExpressAdapterOptions = {}): ExpressAdapter {
  const runtime =
    options.runtime ??
    new OumvRuntime({
      // Explicit sandbox wiring wins; otherwise adopt the db/prisma the app holds.
      ...withDetectedSandbox(app, options),
      workspaceDir: options.workspaceDir ?? process.cwd(),
      handler: (app as unknown) as ExpressAdapterOptions['handler'],
    });

  for (const route of collectExpressRoutes(app)) {
    runtime.registerRoute(route.method, route.path, route.handlerName);
  }
  runtime.setHandler(app as any);

  const httpHandler = createHttpHandler({ runtime, authToken: options.authToken, requireAuth: options.requireAuth });
  const sseHandler = createSseHandler({ runtime, authToken: options.authToken, requireAuth: options.requireAuth });

  // Express matches by prefix: guard inside each handler on the exact path.
  app.use(runtime.transportPath, (req, res, next) => {
    void httpHandler(req, res).then((handled) => {
      if (!handled) next();
    });
  });
  const eventsPath = runtime.transportPath.replace(/\/rpc$/, '/events');
  app.use(eventsPath, (req, res, next) => {
    void sseHandler(req, res).then((handled) => {
      if (!handled) next();
    });
  });

  runtime.setTransports(['http', 'sse']);

  if (options.publicUrl && options.descriptor !== false) {
    writeDescriptor(runtime.workspaceDir, {
      type: 'backend',
      name: runtime.name,
      version: runtime.version,
      url: options.publicUrl,
      ...(options.authToken ? { sessionToken: options.authToken } : {}),
      workspace: runtime.workspaceDir,
      capabilities: { observeLevels: ['L0', 'L1', 'L2', 'L3', 'L4'], transports: ['http', 'sse'] },
      startedAt: new Date().toISOString(),
    });
  }

  return {
    runtime,
    attachWebSocket: (server: Server) => {
      const attachment = attachWebSocket(server, { runtime, authToken: options.authToken, requireAuth: options.requireAuth });
      runtime.setTransports(['http', 'sse', 'ws']);
      return attachment;
    },
    dispose: async () => {
      await runtime.dispose();
      deleteDescriptor(runtime.workspaceDir);
    },
  };
}

/** Records every request as an L4 trace. Mount before your routes. */
export function tracingMiddleware(runtime: OumvRuntime) {
  return function oumvTracing(req: IncomingMessage, res: any, next: () => void) {
    const startedAt = process.hrtime.bigint();
    res.once('finish', () => {
      if (String(req.url || '').startsWith(runtime.transportPath)) return;
      const durationMs = Number(process.hrtime.bigint() - startedAt) / 1_000_000;
      runtime.recordTrace({
        method: String(req.method || 'GET'),
        path: String(req.url || '/').split('?')[0],
        route: String(req.url || '/').split('?')[0],
        handler: 'express',
        statusCode: Number(res.statusCode) || 200,
        durationMs,
      });
    });
    next();
  };
}
