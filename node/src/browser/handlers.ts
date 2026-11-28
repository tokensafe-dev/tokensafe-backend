import fs from 'node:fs';
import path from 'node:path';
import { BridgeBroker, getOrCreateBridgeBroker, peekBridgeBroker } from './bridge.js';
import type { OumvRuntime } from '../runtime.js';

/**
 * Bridge Route Handlers for Next.js App Router (spec §7.4).
 *
 *   // app/api/tokensafe/bridge/route.ts
 *   import { createNextBridgeHandlers } from 'tokensafe-backend-node';
 *   const bridge = createNextBridgeHandlers();
 *   export const GET = bridge.GET;
 *   export const POST = bridge.POST;
 *
 * Three jobs on one same-origin URL:
 *   POST `{kind:"register"}`                    → a short-lived session token
 *   POST `?session=…&t=…`                       → snapshots, traces, results
 *   GET  `?session=…&t=…`                       → the command SSE stream
 *   GET  `?client=1`                            → the browser module itself
 *
 * The whole surface answers 404 outside development. The bridge token is not the
 * transport token: it only proves "this tab registered", never "this caller may
 * use the RPC".
 */

export interface NextBridgeHandlerOptions {
  /** Workspace the broker is keyed by (default: cwd, or the nearest app/ parent). */
  workspaceDir?: string;
  /** Share an existing broker (tests). */
  broker?: BridgeBroker;
  /** Runtime that records browser traces; the RPC route attaches its own if absent. */
  runtime?: OumvRuntime;
  /**
   * Explicit switch. Defaults to `NODE_ENV !== 'production'`: the bridge is a
   * development tool and must not exist in a production build.
   */
  enabled?: boolean;
  /** Override the served browser module (defaults to the vendored client). */
  clientCode?: string;
}

export interface NextBridgeHandlers {
  GET: (request: Request) => Promise<Response>;
  POST: (request: Request) => Promise<Response>;
}

const MAX_BODY_BYTES = 512 * 1024;

function json(status: number, body: Record<string, unknown>): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json', 'Cache-Control': 'no-store' },
  });
}

/**
 * Resolves the workspace the broker is keyed by. Next runs from the project root,
 * so `cwd` is right; a `src/app` project still resolves to the same root.
 */
export function resolveBridgeWorkspace(explicit?: string): string {
  const candidate = path.resolve(explicit ?? process.cwd());
  try {
    return fs.realpathSync(candidate);
  } catch {
    return candidate;
  }
}

export function createNextBridgeHandlers(options: NextBridgeHandlerOptions = {}): NextBridgeHandlers {
  const workspaceDir = resolveBridgeWorkspace(options.workspaceDir);
  const enabled = options.enabled ?? process.env.NODE_ENV !== 'production';

  let broker: BridgeBroker | undefined =
    options.broker ?? peekBridgeBroker(workspaceDir) ?? getOrCreateBridgeBroker(workspaceDir);
  if (options.runtime) broker.attachRuntime(options.runtime);
  const resolvedBroker = (): BridgeBroker => {
    if (!broker) broker = getOrCreateBridgeBroker(workspaceDir, options.runtime);
    return broker;
  };

  const GET = async (request: Request): Promise<Response> => {
    if (!enabled) return json(404, { error: 'bridge_disabled' });
    const url = new URL(request.url);

    if (url.searchParams.get('client') === '1') {
      const code = options.clientCode ?? (await loadClientCode());
      return new Response(code, {
        status: 200,
        headers: {
          'Content-Type': 'text/javascript; charset=utf-8',
          'Cache-Control': 'no-store',
        },
      });
    }

    const sessionId = url.searchParams.get('session') ?? '';
    const token = url.searchParams.get('t') ?? '';
    const active = resolvedBroker().authorize(sessionId, token);
    if (!active) return json(401, { error: 'bridge_unauthorized' });

    const encoder = new TextEncoder();
    let unregister: () => void = () => {};
    let heartbeat: NodeJS.Timeout | undefined;
    let closed = false;
    const stream = new ReadableStream<Uint8Array>({
      start(controller) {
        const close = () => {
          if (closed) return;
          closed = true;
          if (heartbeat) clearInterval(heartbeat);
          unregister();
          try {
            controller.close();
          } catch {
            // already closed by the host
          }
        };
        const send = (event: string, data: string) => {
          if (closed) return;
          try {
            controller.enqueue(encoder.encode(`event: ${event}\ndata: ${data}\n\n`));
          } catch {
            close();
          }
        };
        unregister = resolvedBroker().openStream(active.sessionId, send);
        send('ready', JSON.stringify({ sessionId: active.sessionId, heartbeatMs: 15_000 }));
        heartbeat = setInterval(() => send('ping', '{}'), 15_000);
        // Next cancels the request when the tab navigates away.
        try {
          request.signal?.addEventListener('abort', close, { once: true });
        } catch {
          // hosts without an AbortSignal keep the heartbeat as the liveness probe
        }
      },
      cancel() {
        closed = true;
        if (heartbeat) clearInterval(heartbeat);
        unregister();
      },
    });

    return new Response(stream, {
      status: 200,
      headers: {
        'Content-Type': 'text/event-stream; charset=utf-8',
        'Cache-Control': 'no-cache, no-transform',
        Connection: 'keep-alive',
        'X-Accel-Buffering': 'no',
      },
    });
  };

  const POST = async (request: Request): Promise<Response> => {
    if (!enabled) return json(404, { error: 'bridge_disabled' });
    const url = new URL(request.url);
    let raw: string;
    try {
      raw = await request.text();
    } catch {
      return json(400, { error: 'unreadable_body' });
    }
    if (raw.length > MAX_BODY_BYTES) return json(413, { error: 'body_too_large' });
    let message: Record<string, unknown>;
    try {
      message = JSON.parse(raw);
    } catch {
      return json(400, { error: 'invalid_json' });
    }
    if (!message || typeof message !== 'object') return json(400, { error: 'invalid_message' });

    if (message.kind === 'register') {
      const registration = resolvedBroker().register({
        url: typeof message.url === 'string' ? message.url : '',
        title: typeof message.title === 'string' ? message.title : '',
      });
      return json(200, { ...registration });
    }

    const sessionId = url.searchParams.get('session') ?? '';
    const token = url.searchParams.get('t') ?? '';
    const active = resolvedBroker().authorize(sessionId, token);
    if (!active) return json(401, { error: 'bridge_unauthorized' });
    resolvedBroker().recordMessage(active.sessionId, message);
    return new Response(null, { status: 204 });
  };

  return { GET, POST };
}

/**
 * The browser module is vendored (`src/browser/client-code.ts`, generated from
 * tokensafe-ui/next) so the Route Handler can serve it without a build step and
 * without a runtime dependency on the client package.
 */
export async function loadClientCode(): Promise<string> {
  const module = await import('./client-code.js');
  return module.NEXT_BRIDGE_CLIENT_CODE;
}
