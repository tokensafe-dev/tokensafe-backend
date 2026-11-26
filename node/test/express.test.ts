import { afterAll, beforeAll, describe, expect, it } from 'vitest';
import express from 'express';
import type { AddressInfo } from 'node:net';
import type { Server } from 'node:http';
import { OumvRuntime, mountTokensafe, tracingMiddleware } from '../src/index.js';
import { createWorkspace, hasConformanceRunner, runFixtures, type Workspace } from './helpers/workspace.js';

describe('Express adapter', () => {
  let workspace: Workspace;
  let server: Server;
  let baseUrl: string;
  let rpcUrl: string;
  let dispose: () => Promise<void>;

  beforeAll(async () => {
    workspace = createWorkspace();
    const app = express();
    app.use(express.json());
    // Runtime first, so tracing can wrap the app routes, then mount: mount-time
    // introspection picks those routes up for L1.
    const runtime = new OumvRuntime({ workspaceDir: workspace.dir });
    app.use(tracingMiddleware(runtime));
    app.get('/api/orders', (req, res) => {
      res.json({ orders: [] });
    });
    app.post('/api/orders', (req, res) => {
      res.status(201).json({ id: 1 });
    });
    const adapter = mountTokensafe(app, { runtime });

    server = app.listen(0, '127.0.0.1');
    await new Promise<void>((resolve) => server.once('listening', resolve));
    const port = (server.address() as AddressInfo).port;
    baseUrl = `http://127.0.0.1:${port}`;
    rpcUrl = `${baseUrl}/__oumv/rpc`;
    dispose = adapter.dispose;
  }, 30_000);

  afterAll(async () => {
    await dispose?.();
    await new Promise<void>((resolve) => server?.close(() => resolve()));
    workspace?.cleanup();
  });

  it.runIf(hasConformanceRunner)(
    'passes the language-neutral fixtures at level 3',
    async () => {
      const { report } = await runFixtures(rpcUrl, workspace.markers, 'node-express', 'express');
      if (report.failed > 0) {
        console.log(report.cases.filter((c: any) => c.status !== 'passed'));
      }
      expect(report.passed).toBe(report.total);
      expect(report.attainedLevel).toBe(3);
    },
    30_000
  );

  it('answers JSON-RPC batches and skips notifications', async () => {
    const response = await fetch(rpcUrl, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify([
        { jsonrpc: '2.0', id: 1, method: 'oumv/handshake', params: {} },
        { jsonrpc: '2.0', method: 'oumv/observe', params: { level: 'L0' } },
        { jsonrpc: '2.0', id: 2, method: 'oumv/observe', params: { level: 'L0' } },
      ]),
    });
    const body = (await response.json()) as any[];
    expect(response.status).toBe(200);
    expect(body).toHaveLength(2);
    expect(body[0].id).toBe(1);
    expect(body[1].result.level).toBe('L0');
  });

  it('streams recorded traces over SSE', async () => {
    const controller = new AbortController();
    const response = await fetch(`${baseUrl}/__oumv/events`, { signal: controller.signal });
    expect(response.headers.get('content-type')).toContain('text/event-stream');
    const reader = response.body!.getReader();
    const decoder = new TextDecoder();
    let text = '';
    const timeout = setTimeout(() => controller.abort(), 3000);
    try {
      while (!text.includes('event: trace')) {
        const { value, done } = await reader.read();
        if (done) break;
        text += decoder.decode(value, { stream: true });
      }
    } finally {
      clearTimeout(timeout);
      controller.abort();
    }
    expect(text).toContain(': connected');
    expect(text).toContain('event: trace');
  });

  it('rejects a wrong session token with 401', async () => {
    const app = express();
    const adapter = mountTokensafe(app, { workspaceDir: workspace.dir, authToken: 'secret', requireAuth: true });
    const local = app.listen(0, '127.0.0.1');
    await new Promise<void>((resolve) => local.once('listening', resolve));
    const port = (local.address() as AddressInfo).port;
    try {
      const denied = await fetch(`http://127.0.0.1:${port}/__oumv/rpc`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ jsonrpc: '2.0', id: 1, method: 'oumv/handshake', params: {} }),
      });
      expect(denied.status).toBe(401);

      const allowed = await fetch(`http://127.0.0.1:${port}/__oumv/rpc`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'X-OUMV-Session-Token': 'secret' },
        body: JSON.stringify({ jsonrpc: '2.0', id: 2, method: 'oumv/handshake', params: {} }),
      });
      expect(allowed.status).toBe(200);
      expect(((await allowed.json()) as any).result.profile).toBe('backend');
    } finally {
      await adapter.dispose();
      await new Promise<void>((resolve) => local.close(() => resolve()));
    }
  });
});
