import { afterAll, describe, expect, it } from 'bun:test';
import { existsSync, mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { Elysia } from 'elysia';
import { descriptorPath } from '../src/descriptor.js';
import { createElysiaPlugin } from '../src/adapters/elysia.js';

const home = mkdtempSync(join(tmpdir(), 'oumv-elysia-home-'));
process.env.TOKENSAFE_HOME = home;

async function start(options = {}, configure?: (app: Elysia) => Elysia) {
  const workspace = options.workspaceDir ?? mkdtempSync(join(tmpdir(), 'oumv-elysia-ws-'));
  const adapter = createElysiaPlugin({ workspaceDir: workspace, ...options });
  let app = new Elysia().get('/api/v1/orders', () => ({ orders: [] }));
  if (configure) app = configure(app);
  app = app.use(adapter.plugin);
  await app.listen(0);
  const port = (app.server as any).port as number;
  return { adapter, app, port, workspace, base: `http://127.0.0.1:${port}` };
}

async function rpc(base: string, body: unknown, headers: Record<string, string> = {}) {
  const res = await fetch(`${base}/__oumv/rpc`, {
    method: 'POST',
    headers: { 'content-type': 'application/json', ...headers },
    body: JSON.stringify(body),
  });
  return { status: res.status, payload: (await res.json()) as any };
}

describe('Elysia plugin', () => {
  it('mounts the wire, reads the host route table and writes the descriptor', async () => {
    const { adapter, port, workspace, base } = await start();
    try {
      const routeKeys = adapter.runtime.listRoutes().map((r) => `${r.method} ${r.path}`);
      expect(routeKeys).toContain('GET /api/v1/orders');
      expect(routeKeys).not.toContain('WS /__oumv/rpc');

      const handshake = await rpc(base, { jsonrpc: '2.0', id: 1, method: 'oumv/handshake', params: {} });
      expect(handshake.status).toBe(200);
      expect(handshake.payload.result.protocolVersion).toBe('1.6.0');
      expect(handshake.payload.result.capabilities.transports).toEqual(['http', 'sse', 'ws']);

      const observe = await rpc(base, {
        jsonrpc: '2.0',
        id: 2,
        method: 'oumv/observe',
        params: { level: 'L1' },
      });
      const routes = (observe.payload.result.data.endpoints ?? []).map((e: any) => e.path ?? e.route);
      expect(routes).toContain('/api/v1/orders');

      const descriptor = JSON.parse(
        await Bun.file(descriptorPath(workspace)).text()
      );
      expect(descriptor.backend.url).toBe(`http://127.0.0.1:${port}/__oumv/rpc`);
    } finally {
      await adapter.dispose();
    }
  });

  it('streams SSE from the Elysia GET binding', async () => {
    const { adapter, base } = await start();
    try {
      const res = await fetch(`${base}/__oumv/events`);
      expect(res.status).toBe(200);
      expect(res.headers.get('content-type')).toBe('text/event-stream');
      const reader = res.body!.getReader();
      const first = await reader.read();
      expect(new TextDecoder().decode(first.value)).toContain(': connected');
      await reader.cancel();
    } finally {
      await adapter.dispose();
    }
  });

  it('binds the WebSocket session and pushes state notifications', async () => {
    const { adapter, port } = await start();
    try {
      const ws = new WebSocket(`ws://127.0.0.1:${port}/__oumv/rpc`);
      const messages: any[] = [];
      const waiters: Array<(value: any) => void> = [];
      ws.addEventListener('message', (event) => {
        const parsed = JSON.parse(String(event.data));
        const waiter = waiters.shift();
        if (waiter) waiter(parsed);
        else messages.push(parsed);
      });
      const nextMessage = () =>
        new Promise<any>((resolve) => {
          if (messages.length) return resolve(messages.shift());
          waiters.push(resolve);
        });
      await new Promise<void>((resolve, reject) => {
        ws.addEventListener('open', () => resolve());
        ws.addEventListener('error', () => reject(new Error('ws failed to open')));
      });
      ws.send(JSON.stringify({ jsonrpc: '2.0', id: 1, method: 'oumv/handshake', params: {} }));
      const handshake = await nextMessage();
      const sessionId = handshake.result.sessionId as string;
      expect(typeof sessionId).toBe('string');

      adapter.runtime.recordState('/api/v1/orders', 'v2', 'update');
      const push = await nextMessage();
      expect(push.method).toBe('notifications/oumv.state');
      expect(push.params.sessionId).toBe(sessionId);
      expect(push.params.operation).toBe('update');
      ws.close();
    } finally {
      await adapter.dispose();
    }
  });

  it('requires the configured token on RPC and on the upgrade', async () => {
    const { adapter, port, base } = await start({ authToken: 'sekret' });
    try {
      const denied = await rpc(base, { jsonrpc: '2.0', id: 1, method: 'oumv/handshake', params: {} });
      expect(denied.status).toBe(401);
      const allowed = await rpc(
        base,
        { jsonrpc: '2.0', id: 1, method: 'oumv/handshake', params: {} },
        { 'x-oumv-session-token': 'sekret' }
      );
      expect(allowed.status).toBe(200);

      const refused = await new Promise<string>((resolve) => {
        const ws = new WebSocket(`ws://127.0.0.1:${port}/__oumv/rpc`);
        ws.addEventListener('open', () => resolve('open'));
        ws.addEventListener('error', () => resolve('error'));
      });
      expect(refused).toBe('error');
    } finally {
      await adapter.dispose();
    }
  });

  it('registers no mutating method in read-only mode', async () => {
    const { adapter, base } = await start({ readOnly: true });
    try {
      const methods = adapter.runtime.registeredMethods();
      expect(methods).not.toContain('oumv/applyEdit');
      expect(methods).not.toContain('oumv/verify');
      const edit = await rpc(base, {
        jsonrpc: '2.0',
        id: 3,
        method: 'oumv/applyEdit',
        params: { path: 'x.ts', expectedVersion: 'sha256:0', source: '' },
      });
      expect(edit.payload.error.code).toBe(-32601);
    } finally {
      await adapter.dispose();
    }
  });

  it('dispatches verify through the composed app', async () => {
    const { adapter, base } = await start(
      {},
      (app) => app.post('/api/v1/orders', () => new Response(JSON.stringify({ id: 7 }), { status: 201 }))
    );
    try {
      const verify = await rpc(base, {
        jsonrpc: '2.0',
        id: 4,
        method: 'oumv/verify',
        params: { target: 'POST /api/v1/orders', payload: { sku: 'x' } },
      });
      expect(verify.payload.result.statusCode).toBe(201);
      expect(verify.payload.result.status).toBe('passed');
    } finally {
      await adapter.dispose();
    }
  });
});

afterAll(() => {
  rmSync(home, { recursive: true, force: true });
});
