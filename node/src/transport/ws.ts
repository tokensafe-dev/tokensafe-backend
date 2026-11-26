import type { Server as HttpServer } from 'node:http';
import type { Duplex } from 'node:stream';
import { WebSocketServer, type WebSocket } from 'ws';
import type { OumvRuntime } from '../runtime.js';
import { Dispatcher, dispatchAll, failure, type JsonRpcRequest } from './dispatcher.js';
import { isAuthorized } from './http.js';

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
  const dispatcher = new Dispatcher(runtime);
  const wss = new WebSocketServer({ noServer: true });
  const sessions = new WeakMap<WebSocket, string>();

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
    ws.on('message', async (raw) => {
      let payload: JsonRpcRequest | JsonRpcRequest[];
      try {
        payload = JSON.parse(String(raw));
      } catch (error: any) {
        ws.send(JSON.stringify(failure(null, -32700, `Parse error: ${error.message}`)));
        return;
      }
      const isNotification = !Array.isArray(payload) && payload && typeof payload === 'object' && payload.id === undefined;
      const response = await dispatchAll(dispatcher, payload);
      if (isNotification && !Array.isArray(payload) && !(response as any).error) return;
      if (Array.isArray(payload) && Array.isArray(response) && response.length === 0) return;
      ws.send(JSON.stringify(response));
      if (!Array.isArray(payload) && payload?.method === 'oumv/handshake') {
        const sessionId = (response as any)?.result?.sessionId;
        if (sessionId) sessions.set(ws, sessionId);
      }
    });
    ws.on('close', () => {
      const sessionId = sessions.get(ws);
      if (sessionId) runtime.sessions.delete(sessionId);
      sessions.delete(ws);
    });
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
