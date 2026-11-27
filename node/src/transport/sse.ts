import type { IncomingMessage, ServerResponse } from 'node:http';
import type { OumvRuntime } from '../runtime.js';
import { isAuthorized } from './http.js';

export interface SseOptions {
  runtime: OumvRuntime;
  authToken?: string;
  requireAuth?: boolean;
  heartbeatMs?: number;
  pollMs?: number;
}

/**
 * SSE binding (spec §2.3): `GET <base>/__oumv/events` streams L4 traces with a
 * monotonic `id`, so `Last-Event-ID` replays what the client missed.
 */
export function createSseHandler(options: SseOptions) {
  const runtime = options.runtime;
  const heartbeatMs = options.heartbeatMs ?? 15_000;
  const pollMs = options.pollMs ?? 300;
  const eventsPath = runtime.transportPath.replace(/\/rpc$/, '/events');
  const token = options.authToken ?? runtime.authToken ?? '';

  return async function handle(req: IncomingMessage, res: ServerResponse): Promise<boolean> {
    // Express strips the mount prefix from req.url; originalUrl keeps it.
    const pathname = String((req as any).originalUrl ?? req.url ?? '').split('?')[0];
    if (pathname !== eventsPath) return false;

    if (req.method !== 'GET') {
      res.statusCode = 405;
      res.end(JSON.stringify({ error: 'SSE requires GET' }));
      return true;
    }
    if (!isAuthorized(req, token, Boolean(options.requireAuth))) {
      res.statusCode = 401;
      res.setHeader('Content-Type', 'application/json');
      res.end(JSON.stringify({ jsonrpc: '2.0', id: null, error: { code: -32007, message: 'PERMISSION_DENIED' } }));
      return true;
    }

    res.writeHead(200, {
      'Content-Type': 'text/event-stream',
      'Cache-Control': 'no-cache',
      Connection: 'keep-alive',
      'X-Accel-Buffering': 'no',
    });
    res.write(': connected\n\n');

    let cursor = Number.parseInt(String(req.headers['last-event-id'] ?? '0'), 10);
    if (!Number.isFinite(cursor) || cursor < 0) cursor = 0;

    const flush = () => {
      // `id` is the event's monotonic sequence, shared by trace and state events,
      // so `Last-Event-ID` replays exactly what the client missed (spec §2.3).
      const events = runtime.eventsSince(cursor);
      for (const event of events) {
        res.write(`id: ${event.id}\nevent: ${event.type}\ndata: ${JSON.stringify(event.data)}\n\n`);
        cursor = event.id;
      }
    };
    flush();

    const poll = setInterval(flush, pollMs);
    const heartbeat = setInterval(() => {
      res.write(`event: heartbeat\ndata: ${JSON.stringify(new Date().toISOString())}\n\n`);
    }, heartbeatMs);
    const stop = () => {
      clearInterval(poll);
      clearInterval(heartbeat);
      res.end();
    };
    req.on('close', stop);
    return true;
  };
}
