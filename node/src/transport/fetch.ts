import type http from 'node:http';
import { ERROR_CODES } from '../errors.js';
import type { OumvRuntime } from '../runtime.js';
import { Dispatcher, dispatchAll, failure, parsePayload, type JsonRpcRequest } from './dispatcher.js';

/**
 * Web `Request`/`Response` binding for runtimes whose host speaks fetch
 * (Next.js Route Handlers, Elysia/Bun, Hono, workerd). The Node `http` binding in
 * `http.ts` stays the lower-level one; both share `Dispatcher` and `parsePayload`,
 * so the wire shapes cannot drift apart.
 */
export interface FetchHandlerOptions {
  runtime: OumvRuntime;
  authToken?: string;
  maxBodyBytes?: number;
  /** `Access-Control-Allow-Origin` value; false disables CORS headers (default '*'). */
  cors?: string | false;
}

export interface FetchHandlers {
  /** JSON-RPC over POST (spec §2.2); `OPTIONS` answers the CORS preflight. */
  POST: (request: Request) => Promise<Response>;
  OPTIONS: () => Response;
  /** SSE event stream (spec §2.3) with `Last-Event-ID` replay. */
  GET: (request: Request) => Response;
}

/** Spec §12: a configured token is always required; no token stays open for dev. */
export function isAuthorizedFetchRequest(request: Request, token: string): boolean {
  if (!token) return true;
  const authorization = request.headers.get('authorization') ?? '';
  const provided =
    request.headers.get('x-oumv-session-token') ??
    (authorization.toLowerCase().startsWith('bearer ') ? authorization.slice(7).trim() : '');
  return Boolean(provided) && provided === token;
}

const JSON_HEADERS = { 'Content-Type': 'application/json' };

export function createFetchHandlers(options: FetchHandlerOptions): FetchHandlers {
  const runtime = options.runtime;
  const dispatcher = new Dispatcher(runtime);
  const token = options.authToken ?? runtime.authToken ?? '';
  const maxBodyBytes = options.maxBodyBytes ?? runtime.maxBodyBytes;
  const corsOrigin = options.cors === false ? null : options.cors ?? '*';

  const withCors = (headers: Record<string, string>): Record<string, string> => {
    if (corsOrigin === null) return headers;
    return {
      ...headers,
      'Access-Control-Allow-Origin': corsOrigin,
      'Access-Control-Allow-Headers': 'Content-Type, Authorization, X-OUMV-Session-Token',
      'Access-Control-Allow-Methods': 'POST, OPTIONS',
    };
  };

  const jsonResponse = (payload: unknown, status = 200): Response =>
    new Response(JSON.stringify(payload), { status, headers: withCors(JSON_HEADERS) });

  const OPTIONS = (): Response => new Response(null, { status: 204, headers: withCors({}) });

  const POST = async (request: Request): Promise<Response> => {
    if (!isAuthorizedFetchRequest(request, token)) {
      return jsonResponse(
        failure(null, ERROR_CODES.PERMISSION_DENIED, 'PERMISSION_DENIED: invalid OUMV session token'),
        401
      );
    }
    const raw = await request.text();
    if (new TextEncoder().encode(raw).length > maxBodyBytes) {
      return jsonResponse(failure(null, ERROR_CODES.INVALID_REQUEST, 'INVALID_REQUEST: body too large'), 413);
    }
    let payload: JsonRpcRequest | JsonRpcRequest[];
    try {
      payload = parsePayload(raw);
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
    if (!isAuthorizedFetchRequest(request, token)) {
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
      headers: withCors({
        'Content-Type': 'text/event-stream',
        'Cache-Control': 'no-cache',
        Connection: 'keep-alive',
        'X-Accel-Buffering': 'no',
      }),
    });
  };

  return { POST, OPTIONS, GET };
}

/** Bridges a Web handler into the Node listener the runtime dispatches verify through. */
export function webHandlerToNode(fetchHandler: (request: Request) => Promise<Response>): http.RequestListener {
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
