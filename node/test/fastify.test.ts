import { afterAll, beforeAll, describe, expect, it } from 'vitest';
import Fastify from 'fastify';
import fs from 'node:fs';
import path from 'node:path';
import { createHash } from 'node:crypto';
import type { AddressInfo } from 'node:net';
import WebSocket from 'ws';
import { OumvRuntime, registerTokensafe } from '../src/index.js';
import { createWorkspace, hasConformanceRunner, runFixtures, type Workspace } from './helpers/workspace.js';

async function waitFor<T>(read: () => T | undefined, timeoutMs = 2000): Promise<T> {
  const start = Date.now();
  for (;;) {
    const value = read();
    if (value !== undefined) return value;
    if (Date.now() - start > timeoutMs) throw new Error('timed out waiting for a WebSocket frame');
    await new Promise((resolve) => setTimeout(resolve, 20));
  }
}

describe('Fastify adapter', () => {
  let workspace: Workspace;
  let app: ReturnType<typeof Fastify>;
  let rpcUrl: string;
  let dispose: () => Promise<void>;
  let closeWs: () => Promise<void>;
  let probeRows = 0;

  beforeAll(async () => {
    workspace = createWorkspace();
    app = Fastify({ logger: false });
    const runtime = new OumvRuntime({
      workspaceDir: workspace.dir,
      sandbox: {
        db: {
          run: async <T>(fn: () => Promise<T> | T) => {
            const before = probeRows;
            const result = await fn();
            probeRows = before; // the sandbox rolls back whatever the probe wrote
            return { result, rolledBack: true, queries: 2 };
          },
        },
      },
    });
    const adapter = await registerTokensafe(app, { runtime });
    app.get('/api/orders', async () => ({ orders: [] }));
    app.post('/api/orders', async (_request, reply) => reply.code(201).send({ id: 1 }));
    app.post('/__oumv_sandbox_probe', async (request, reply) => {
      if (request.headers['x-tokensafe-dry-run'] !== 'true') {
        return reply.code(400).send({ error: 'dry-run header required' });
      }
      probeRows += 1;
      return { ok: true };
    });

    await app.listen({ port: 0, host: '127.0.0.1' });
    const port = (app.server.address() as AddressInfo).port;
    rpcUrl = `http://127.0.0.1:${port}/__oumv/rpc`;
    dispose = adapter.dispose;
    closeWs = () => Promise.resolve();
    void closeWs;
  }, 30_000);

  afterAll(async () => {
    await dispose?.();
    await app?.close();
    workspace?.cleanup();
  });

  it('exposes only application routes in the L1 surface', async () => {
    const response = await fetch(rpcUrl, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ jsonrpc: '2.0', id: 1, method: 'oumv/observe', params: { level: 'L1' } }),
    });
    const endpoints = ((await response.json()) as any).result.data.endpoints;
    const paths = endpoints.map((endpoint: any) => `${endpoint.method} ${endpoint.path}`).sort();
    // The sandbox probe route is mounted alongside the application's routes.
    expect(paths).toEqual(
      expect.arrayContaining(['GET /api/orders', 'POST /api/orders', 'POST /__oumv_sandbox_probe'])
    );
  });

  it.runIf(hasConformanceRunner)(
    'passes the language-neutral fixtures at level 3',
    async () => {
      const { report } = await runFixtures(rpcUrl, workspace.markers, 'node-fastify', 'fastify');
      if (report.failed > 0) {
        console.log(report.cases.filter((c: any) => c.status !== 'passed'));
      }
      expect(report.passed).toBe(report.total);
      expect(report.attainedLevel).toBe(3);
    },
    30_000
  );

  it('serves JSON-RPC over the WebSocket binding and binds the session', async () => {
    const { registerTokensafe: register } = await import('../src/index.js');
    const wsApp = Fastify({ logger: false });
    const wsRuntime = new OumvRuntime({ workspaceDir: workspace.dir });
    const wsAdapter = await register(wsApp, { runtime: wsRuntime });
    await wsApp.listen({ port: 0, host: '127.0.0.1' });
    const port = (wsApp.server.address() as AddressInfo).port;
    const attachment = wsAdapter.attachWebSocket();
    const socket = new WebSocket(`ws://127.0.0.1:${port}/__oumv/rpc`);
    try {
      await new Promise<void>((resolve, reject) => {
        socket.once('open', () => resolve());
        socket.once('error', reject);
      });
      const messages: any[] = [];
      socket.on('message', (data) => messages.push(JSON.parse(String(data))));
      socket.send(JSON.stringify({ jsonrpc: '2.0', id: 1, method: 'oumv/handshake', params: {} }));
      await new Promise((resolve) => setTimeout(resolve, 150));
      expect(messages[0].result.profile).toBe('backend');
      expect(messages[0].result.sessionId).toMatch(/^sess-/);

      // A notification frame gets no reply: the next message belongs to id=2.
      socket.send(JSON.stringify({ jsonrpc: '2.0', method: 'oumv/observe', params: { level: 'L0' } }));
      socket.send(JSON.stringify({ jsonrpc: '2.0', id: 2, method: 'oumv/observe', params: { level: 'L0' } }));
      await new Promise((resolve) => setTimeout(resolve, 150));
      const reply = messages.find((message) => message.id === 2);
      expect(reply.result.level).toBe('L0');
      expect(messages.some((message) => message.id === undefined && message.result)).toBe(false);
    } finally {
      socket.close();
      await attachment.close();
      await wsAdapter.dispose();
      await wsApp.close();
    }
  }, 20_000);

  it('pushes notifications/oumv.state to the bound socket after an applied edit', async () => {
    const { registerTokensafe: register } = await import('../src/index.js');
    const wsApp = Fastify({ logger: false });
    const wsRuntime = new OumvRuntime({ workspaceDir: workspace.dir });
    const wsAdapter = await register(wsApp, { runtime: wsRuntime });
    await wsApp.listen({ port: 0, host: '127.0.0.1' });
    const port = (wsApp.server.address() as AddressInfo).port;
    const attachment = wsAdapter.attachWebSocket();
    const socket = new WebSocket(`ws://127.0.0.1:${port}/__oumv/rpc`);
    try {
      await new Promise<void>((resolve, reject) => {
        socket.once('open', () => resolve());
        socket.once('error', reject);
      });
      const messages: any[] = [];
      socket.on('message', (data) => messages.push(JSON.parse(String(data))));

      socket.send(JSON.stringify({ jsonrpc: '2.0', id: 1, method: 'oumv/handshake', params: {} }));
      const handshake = await waitFor(() => messages.find((message) => message.id === 1));
      const sessionId = handshake.result.sessionId as string;
      expect(sessionId).toMatch(/^sess-/);

      const relativePath = 'notify_target.js';
      const content = 'const value = 1;\n';
      fs.writeFileSync(path.join(workspace.dir, relativePath), content);
      socket.send(
        JSON.stringify({
          jsonrpc: '2.0',
          id: 2,
          method: 'oumv/applyEdit',
          params: {
            sessionId,
            path: relativePath,
            mode: 'patch',
            expectedVersion: 'sha256:' + createHash('sha256').update(content).digest('hex'),
            hunks: [{ target: 'value = 1', replacement: 'value = 2' }],
          },
        })
      );

      const notification = await waitFor(() =>
        messages.find((message) => message.method === 'notifications/oumv.state')
      );
      expect(notification.params.sessionId).toBe(sessionId);
      expect(notification.params.path).toBe(relativePath);
      expect(notification.params.operation).toBe('patch');
      expect(notification.params.version).toMatch(/^sha256:[0-9a-f]{64}$/);
    } finally {
      socket.close();
      await attachment.close();
      await wsAdapter.dispose();
      await wsApp.close();
    }
  }, 20_000);
});
