import type { Server as HttpServer } from 'node:http';
import type { Duplex } from 'node:stream';
import { WebSocketServer, type WebSocket } from 'ws';
import type { OumvRuntime } from '../runtime.js';
import { isAuthorized } from './http.js';
import { bindWsSession } from './ws-core.js';

export interface WsOptions {
  runtime: OumvRuntime;
  authToken?: string;
  requireAuth?: boolean;
}

export interface WsAttachment {
  close: () => Promise<void>;
}

/**
 * WebSocket binding (spec §2.4): JSON-RPC text frames on `<base>/__oumv/rpc`; the
 * connection is bound to the sessionId returned by `oumv/handshake`, and closing it
 * disconnects that session.
 */
export function attachWebSocket(server: HttpServer, options: WsOptions): WsAttachment {
  const runtime = options.runtime;
  const token = options.authToken ?? runtime.authToken ?? '';
  const wss = new WebSocketServer({ noServer: true });

  const onUpgrade = (req: any, socket: Duplex, head: Buffer) => {
    const pathname = String(req.url || '').split('?')[0];
    if (pathname !== runtime.transportPath) return; // another listener may own it
    if (!isAuthorized(req, token, Boolean(options.requireAuth))) {
      socket.write('HTTP/1.1 401 Unauthorized\r\nContent-Length: 0\r\n\r\n');
      socket.destroy();
      return;
    }
    wss.handleUpgrade(req, socket, head, (ws) => wss.emit('connection', ws, req));
  };
  server.on('upgrade', onUpgrade);

  wss.on('connection', (ws: WebSocket) => {
    // The session/broadcast contract is shared with the Bun/Elysia adapter
    // (`ws-core.ts`), so the two push implementations cannot drift apart.
    const binding = bindWsSession(runtime, (payload) => ws.send(JSON.stringify(payload)));
    ws.on('message', (raw) => {
      void binding.handleMessage(raw);
    });
    ws.on('close', () => binding.handleClose());
  });

  return {
    close: () =>
      new Promise<void>((resolve) => {
        server.off('upgrade', onUpgrade);
        for (const client of wss.clients) client.terminate();
        wss.close(() => resolve());
      }),
  };
}
