import { Elysia } from 'elysia';
import { deleteDescriptor, writeDescriptor } from '../descriptor.js';
import { createReadOnlyRuntime } from '../readonly.js';
import { OumvRuntime, type OumvRuntimeOptions } from '../runtime.js';
import { createFetchHandlers, isAuthorizedFetchRequest, webHandlerToNode } from '../transport/fetch.js';
import { bindWsSession, type WsSessionBinding } from '../transport/ws-core.js';

/**
 * Elysia / Bun adapter.
 *
 * Elysia handlers are Web `Request`/`Response` functions on top of `Bun.serve`, so
 * the runtime is mounted through the fetch binding (`transport/fetch.ts`) rather
 * than the Node `IncomingMessage` one. The L1 route table comes from the composed
 * app in `onStart` — Elysia has no `onRoute` hook, but `onStart` receives the host
 * app with `routes` populated and `server.port` already bound, which is also when
 * the zero-footprint descriptor is written.
 *
 *   import { oumv } from 'tokensafe-backend-node/elysia';
 *   const app = new Elysia().use(oumv());
 *   app.listen(3000);
 *
 * Read-only deployments use `oumv({ readOnly: true })`: mutating methods are not
 * registered at all (spec §8.5), the same shape as `createReadOnlyRuntime`.
 */
export interface ElysiaAdapterOptions extends OumvRuntimeOptions {
  /** Reuse an existing runtime instead of creating one. */
  runtime?: OumvRuntime;
  /** Public JSON-RPC URL; defaults to `http://127.0.0.1:<port><transportPath>` on start. */
  publicUrl?: string;
  /** Set false to skip the descriptor even when the URL is known. */
  descriptor?: boolean;
  /** §8.5: register a read-only runtime (no applyEdit/previewEdit/verify/build). */
  readOnly?: boolean;
  /** Extra host routes when the composed app is not introspectable (tests, custom routers). */
  routes?: Array<{ method: string; path: string; handlerName?: string }>;
}

export interface ElysiaAdapter {
  runtime: OumvRuntime;
  plugin: Elysia;
  dispose: () => Promise<void>;
}

const SKIP_METHODS = new Set(['WS', 'HEAD', 'OPTIONS']);

export function createElysiaPlugin(options: ElysiaAdapterOptions = {}): ElysiaAdapter {
  const workspaceDir = options.workspaceDir ?? process.cwd();
  const runtime =
    options.runtime ??
    (options.readOnly
      ? createReadOnlyRuntime({ ...options, workspaceDir })
      : new OumvRuntime({ ...options, workspaceDir }));
  const token = options.authToken ?? runtime.authToken ?? '';
  const rpcPath = runtime.transportPath;
  const eventsPath = rpcPath.replace(/\/rpc$/, '/events');
  const handlers = createFetchHandlers({
    runtime,
    authToken: options.authToken,
    maxBodyBytes: options.maxBodyBytes,
  });

  // One binding per socket: the session/broadcast contract is shared with the Node
  // `ws` adapter, so the push fixtures certify the same wire on both runtimes.
  const bindings = new WeakMap<object, WsSessionBinding>();
  const bindingFor = (ws: { send: (data: string) => void }): WsSessionBinding => {
    let binding = bindings.get(ws);
    if (!binding) {
      binding = bindWsSession(runtime, (payload) => ws.send(JSON.stringify(payload)));
      bindings.set(ws, binding);
    }
    return binding;
  };

  const plugin = new Elysia({ name: 'tokensafe-oumv' })
    .options(rpcPath, () => handlers.OPTIONS())
    .post(rpcPath, ({ request }) => handlers.POST(request))
    .get(eventsPath, ({ request }) => handlers.GET(request))
    .ws(rpcPath, {
      beforeHandle({ request, set }) {
        // §12: a configured token is required on the upgrade too, before the socket opens.
        if (token && !isAuthorizedFetchRequest(request, token)) {
          set.status = 401;
          return { error: 'PERMISSION_DENIED: invalid OUMV session token' };
        }
      },
      message(ws, message) {
        void bindingFor(ws as { send: (data: string) => void }).handleMessage(message);
      },
      close(ws) {
        bindings.get(ws as object)?.handleClose();
      },
    })
    .onStart((app: any) => {
      const routes = [...(options.routes ?? [])];
      for (const route of app?.routes ?? []) {
        const method = String(route?.method ?? '').toUpperCase();
        const path = String(route?.path ?? '');
        if (!method || SKIP_METHODS.has(method) || !path) continue;
        if (path === rpcPath || path === eventsPath) continue;
        routes.push({ method, path, handlerName: (route?.handler as any)?.name });
      }
      for (const route of routes) {
        runtime.registerRoute(route.method, route.path, route.handlerName || 'handler');
      }
      // `verify` dispatches through the composed app, exactly like the Next adapter;
      // no loopback server is started under Bun.
      runtime.setHandler(webHandlerToNode((request: Request) => app.handle(request)));
      runtime.setTransports(['http', 'sse', 'ws']);

      const port = app?.server?.port;
      const publicUrl = options.publicUrl ?? (port ? `http://127.0.0.1:${port}${rpcPath}` : '');
      if (publicUrl && options.descriptor !== false) {
        writeDescriptor(workspaceDir, {
          type: 'backend',
          name: runtime.name,
          version: runtime.version,
          url: publicUrl,
          ...(token ? { sessionToken: token } : {}),
          workspace: workspaceDir,
          capabilities: { observeLevels: ['L0', 'L1', 'L2', 'L3', 'L4'], transports: ['http', 'sse', 'ws'] },
          startedAt: new Date().toISOString(),
        });
      }
    })
    .onStop(async () => {
      deleteDescriptor(workspaceDir);
      await runtime.dispose();
    });

  return {
    runtime,
    plugin,
    dispose: async () => {
      deleteDescriptor(workspaceDir);
      await runtime.dispose();
    },
  };
}

/** `app.use(oumv())` shorthand; the full handle is `createElysiaPlugin`. */
export function oumv(options: ElysiaAdapterOptions = {}): Elysia {
  return createElysiaPlugin(options).plugin;
}
