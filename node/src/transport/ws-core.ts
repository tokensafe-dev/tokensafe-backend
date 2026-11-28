import type { OumvRuntime } from '../runtime.js';
import { Dispatcher, dispatchAll, failure, type JsonRpcRequest } from './dispatcher.js';

/**
 * Socket-agnostic WebSocket session binding (spec §2.4).
 *
 * The Node adapter (`ws.ts`, the `ws` package) and the Bun/Elysia plugin hand over
 * different socket objects but must answer the same wire: JSON-RPC text frames, the
 * connection bound to the `sessionId` returned by `oumv/handshake`, server pushes as
 * `notifications/oumv.state|diagnostic|trace`, and the session dropped on close.
 * Keeping that logic here means the two push implementations cannot drift apart —
 * the same `pushCases` certificate runs against both.
 */
export interface WsSessionBinding {
  handleMessage(raw: unknown): Promise<void>;
  handleClose(): void;
}

export function bindWsSession(runtime: OumvRuntime, sendJson: (payload: unknown) => void): WsSessionBinding {
  const dispatcher = new Dispatcher(runtime);
  let boundSession = '';

  const unsubscribe = runtime.subscribeEvents((event) => {
    if (!boundSession) return;
    const method =
      {
        state: 'notifications/oumv.state',
        diagnostic: 'notifications/oumv.diagnostic',
      }[event.type as string] ?? 'notifications/oumv.trace';
    try {
      sendJson({ jsonrpc: '2.0', method, params: { ...event.data, sessionId: boundSession } });
    } catch {
      // The socket is closing; handleClose removes the subscriber.
    }
  });

  return {
    async handleMessage(raw: unknown): Promise<void> {
      let payload: JsonRpcRequest | JsonRpcRequest[];
      try {
        // `ws` hands over a Buffer, Bun/Elysia hands over an already-parsed object.
        if (typeof raw === 'string') {
          payload = JSON.parse(raw);
        } else if (raw instanceof Uint8Array) {
          payload = JSON.parse(new TextDecoder().decode(raw));
        } else if (raw && typeof raw === 'object') {
          payload = raw as JsonRpcRequest | JsonRpcRequest[];
        } else {
          throw new Error('unsupported frame');
        }
      } catch (error: any) {
        sendJson(failure(null, -32700, `Parse error: ${error.message}`));
        return;
      }
      const isNotification =
        !Array.isArray(payload) && payload && typeof payload === 'object' && payload.id === undefined;
      const response = await dispatchAll(dispatcher, payload);
      if (isNotification && !Array.isArray(payload) && !(response as any).error) return;
      if (Array.isArray(payload) && Array.isArray(response) && response.length === 0) return;
      sendJson(response);
      if (!Array.isArray(payload) && payload?.method === 'oumv/handshake') {
        const sessionId = (response as any)?.result?.sessionId;
        if (sessionId) boundSession = sessionId;
      }
    },
    handleClose(): void {
      unsubscribe();
      if (boundSession) runtime.sessions.delete(boundSession);
      boundSession = '';
    },
  };
}
