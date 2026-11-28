import { afterEach, describe, expect, it } from 'vitest';
import express from 'express';
import Fastify from 'fastify';
import fs from 'node:fs';
import path from 'node:path';
import { createRequire } from 'node:module';
import type { AddressInfo } from 'node:net';
import type { Server } from 'node:http';
import { OumvRuntime, mountTokensafe, registerTokensafe } from '../src/index.js';
import {
  CONFORMANCE_ENTRY,
  FIXTURES_PATH,
  createWorkspace,
  hasConformanceRunner,
  type Workspace,
} from './helpers/workspace.js';

const require = createRequire(import.meta.url);

// §12: the certificates prove the refusal, so every live binding runs token-guarded.
const CERT_TOKEN = 'oumv-cert-token';

/**
 * Transport certificates (spec §2.3/§2.4): the same fixture suite over the SSE
 * and WebSocket bindings, plus the fixture v8 push cases. A transport claim is
 * only made with a report, so each run writes one to CERT_OUT_DIR.
 */
describe('Express transport certificates', () => {
  let cleanup: (() => Promise<void>) | undefined;
  afterEach(async () => {
    await cleanup?.();
    cleanup = undefined;
  });

  interface LiveServer {
    workspace: Workspace;
    rpcUrl: string;
    eventsUrl: string;
    wsUrl: string;
  }

  async function startServer(): Promise<LiveServer> {
    const workspace = createWorkspace();
    const app = express();
    app.use(express.json());
    // Mount-time introspection learns these, which is what the L1/L2 cases assert.
    app.get('/api/orders', (_req, res) => {
      res.json({ orders: [] });
    });
    app.post('/api/orders', (_req, res) => {
      res.status(201).json({ id: 1 });
    });
    app.post('/__oumv_sandbox_probe', (req, res) => {
      if (req.headers['x-tokensafe-dry-run'] !== 'true') {
        res.status(400).json({ error: 'dry-run header required' });
        return;
      }
      res.json({ ok: true });
    });
    const runtime = new OumvRuntime({
      workspaceDir: workspace.dir,
      authToken: CERT_TOKEN,
      sandbox: { db: { run: async (fn) => ({ result: await fn(), rolledBack: true, queries: 1 }) } },
    });
    const adapter = mountTokensafe(app, { runtime });
    const server: Server = app.listen(0, '127.0.0.1');
    await new Promise<void>((resolve) => server.once('listening', resolve));
    const port = (server.address() as AddressInfo).port;
    const attachment = adapter.attachWebSocket(server);
    cleanup = async () => {
      await attachment.close();
      await adapter.dispose();
      await new Promise<void>((resolve) => server.close(() => resolve()));
      workspace.cleanup();
    };
    return {
      workspace,
      rpcUrl: `http://127.0.0.1:${port}/__oumv/rpc`,
      eventsUrl: `http://127.0.0.1:${port}/__oumv/events`,
      wsUrl: `ws://127.0.0.1:${port}/__oumv/rpc`,
    };
  }

  function writeReport(name: string, report: unknown): void {
    const outDir = process.env.CERT_OUT_DIR;
    if (!outDir) return;
    const target = path.join(outDir, name);
    fs.mkdirSync(path.dirname(target), { recursive: true });
    fs.writeFileSync(target, JSON.stringify(report, null, 2) + '\n');
  }

  it.runIf(hasConformanceRunner)(
    'certifies the SSE binding (46 cases + push with Last-Event-ID replay)',
    async () => {
      const live = await startServer();
      const { loadFixtures, runFixtureSuite, httpTransport, sseEventSource } = require(CONFORMANCE_ENTRY);
      const source = sseEventSource({ url: live.eventsUrl, token: CERT_TOKEN });
      try {
        await source.start();
        const http = httpTransport({ url: live.rpcUrl, token: CERT_TOKEN });
        const handshake = await http.request({
          jsonrpc: '2.0',
          id: 'certify-handshake',
          method: 'oumv/handshake',
          params: {},
        });
        const report = await runFixtureSuite(
          http,
          loadFixtures(FIXTURES_PATH),
          live.workspace.markers,
          {
            adapterName: 'node-express-sse',
            framework: 'express',
            environment: { transport: 'sse', auth: 'required', node: process.version },
            auth: true,
            capabilities: (handshake.result as any)?.capabilities,
            negotiatedVersion: (handshake.result as any)?.protocolVersion,
            push: source,
            pushTimeoutMs: 5_000,
          }
        );
        if (report.failed > 0) console.log(report.cases.filter((entry: any) => entry.status !== 'passed'));
        expect(report.failed).toBe(0);
        expect(report.total).toBe(50);
        writeReport('transports/node-express-sse.json', report);
      } finally {
        await source.close();
      }
    },
    30_000
  );

  it.runIf(hasConformanceRunner)(
    'certifies the WebSocket binding (46 cases + state notification push)',
    async () => {
      const live = await startServer();
      const { loadFixtures, runFixtureSuite, wsTransport } = require(CONFORMANCE_ENTRY);
      const transport = wsTransport({ url: live.wsUrl, token: CERT_TOKEN });
      try {
        const handshake = await transport.request({
          jsonrpc: '2.0',
          id: 'certify-handshake',
          method: 'oumv/handshake',
          params: {},
        });
        const report = await runFixtureSuite(transport, loadFixtures(FIXTURES_PATH), live.workspace.markers, {
          adapterName: 'node-express-ws',
          framework: 'express',
          environment: { transport: 'ws', node: process.version },
          capabilities: (handshake.result as any)?.capabilities,
          negotiatedVersion: (handshake.result as any)?.protocolVersion,
          push: transport,
          pushTimeoutMs: 5_000,
        });
        if (report.failed > 0) console.log(report.cases.filter((entry: any) => entry.status !== 'passed'));
        expect(report.failed).toBe(0);
        expect(report.total).toBe(47);
        writeReport('transports/node-express-ws.json', report);
      } finally {
        await transport.close?.();
      }
    },
    30_000
  );
});

describe('Fastify transport certificates', () => {
  let cleanup: (() => Promise<void>) | undefined;
  afterEach(async () => {
    await cleanup?.();
    cleanup = undefined;
  });

  async function startServer() {
    const workspace = createWorkspace();
    const app = Fastify({ logger: false });
    const runtime = new OumvRuntime({
      workspaceDir: workspace.dir,
      authToken: CERT_TOKEN,
      sandbox: { db: { run: async (fn) => ({ result: await fn(), rolledBack: true, queries: 1 }) } },
    });
    const adapter = await registerTokensafe(app, { runtime });
    // The adapter learns routes through an onRoute hook, so they are added after
    // registration (same order as the adapter's own suite).
    app.get('/api/orders', async () => ({ orders: [] }));
    app.post('/api/orders', async (_request, reply) => reply.code(201).send({ id: 1 }));
    app.post('/__oumv_sandbox_probe', async (request, reply) => {
      if (request.headers['x-tokensafe-dry-run'] !== 'true') {
        return reply.code(400).send({ error: 'dry-run header required' });
      }
      return { ok: true };
    });
    await app.listen({ port: 0, host: '127.0.0.1' });
    const port = (app.server.address() as AddressInfo).port;
    const attachment = adapter.attachWebSocket();
    cleanup = async () => {
      await attachment.close();
      await adapter.dispose();
      await app.close();
      workspace.cleanup();
    };
    return {
      workspace,
      rpcUrl: `http://127.0.0.1:${port}/__oumv/rpc`,
      eventsUrl: `http://127.0.0.1:${port}/__oumv/events`,
      wsUrl: `ws://127.0.0.1:${port}/__oumv/rpc`,
    };
  }

  function writeReport(name: string, report: unknown): void {
    const outDir = process.env.CERT_OUT_DIR;
    if (!outDir) return;
    const target = path.join(outDir, name);
    fs.mkdirSync(path.dirname(target), { recursive: true });
    fs.writeFileSync(target, JSON.stringify(report, null, 2) + '\n');
  }

  it.runIf(hasConformanceRunner)(
    'certifies the SSE binding (46 cases + push with Last-Event-ID replay)',
    async () => {
      const live = await startServer();
      const { loadFixtures, runFixtureSuite, httpTransport, sseEventSource } = require(CONFORMANCE_ENTRY);
      const source = sseEventSource({ url: live.eventsUrl, token: CERT_TOKEN });
      try {
        await source.start();
        const http = httpTransport({ url: live.rpcUrl, token: CERT_TOKEN });
        const handshake = await http.request({
          jsonrpc: '2.0',
          id: 'certify-handshake',
          method: 'oumv/handshake',
          params: {},
        });
        const report = await runFixtureSuite(
          http,
          loadFixtures(FIXTURES_PATH),
          live.workspace.markers,
          {
            adapterName: 'node-fastify-sse',
            framework: 'fastify',
            environment: { transport: 'sse', auth: 'required', node: process.version },
            auth: true,
            capabilities: (handshake.result as any)?.capabilities,
            negotiatedVersion: (handshake.result as any)?.protocolVersion,
            push: source,
            pushTimeoutMs: 5_000,
          }
        );
        if (report.failed > 0) console.log(report.cases.filter((entry: any) => entry.status !== 'passed'));
        expect(report.failed).toBe(0);
        expect(report.total).toBe(50);
        writeReport('transports/node-fastify-sse.json', report);
      } finally {
        await source.close();
      }
    },
    30_000
  );

  it.runIf(hasConformanceRunner)(
    'certifies the WebSocket binding (46 cases + state notification push)',
    async () => {
      const live = await startServer();
      const { loadFixtures, runFixtureSuite, wsTransport } = require(CONFORMANCE_ENTRY);
      const transport = wsTransport({ url: live.wsUrl, token: CERT_TOKEN });
      try {
        const handshake = await transport.request({
          jsonrpc: '2.0',
          id: 'certify-handshake',
          method: 'oumv/handshake',
          params: {},
        });
        const report = await runFixtureSuite(transport, loadFixtures(FIXTURES_PATH), live.workspace.markers, {
          adapterName: 'node-fastify-ws',
          framework: 'fastify',
          environment: { transport: 'ws', node: process.version },
          capabilities: (handshake.result as any)?.capabilities,
          negotiatedVersion: (handshake.result as any)?.protocolVersion,
          push: transport,
          pushTimeoutMs: 5_000,
        });
        if (report.failed > 0) console.log(report.cases.filter((entry: any) => entry.status !== 'passed'));
        expect(report.failed).toBe(0);
        expect(report.total).toBe(47);
        writeReport('transports/node-fastify-ws.json', report);
      } finally {
        await transport.close?.();
      }
    },
    30_000
  );
});
