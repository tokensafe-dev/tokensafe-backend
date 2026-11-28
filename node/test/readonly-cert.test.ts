import { describe, expect, it } from 'vitest';
import express from 'express';
import Fastify from 'fastify';
import fs from 'node:fs';
import path from 'node:path';
import { createRequire } from 'node:module';
import { createServer, type IncomingMessage, type Server } from 'node:http';
import type { AddressInfo } from 'node:net';
import { createNextRouteHandlers, createReadOnlyRuntime, mountTokensafe, registerTokensafe, tracingMiddleware } from '../src/index.js';
import {
  CONFORMANCE_ENTRY,
  FIXTURES_PATH,
  createWorkspace,
  hasConformanceRunner,
  runFixtures,
} from './helpers/workspace.js';

const require = createRequire(import.meta.url);

/**
 * §8.5 read-only certificates (backend profile, spec §10).
 *
 * Same fixtures and same transports as the writable certificates, but the runtime
 * is built with `createReadOnlyRuntime`: the writing pillars are absent from the
 * registry, the handshake advertises `capabilities.readOnly` and the runner drops
 * every case whose capability is unadvertised. The report therefore proves the
 * read-only shape with the `read-only-*` cases instead of asserting it in prose.
 */
describe('read-only deployment certificates (§8.5)', () => {
  it.runIf(hasConformanceRunner)(
    'certifies Express with no writing methods registered',
    async () => {
      const workspace = createWorkspace();
      const app = express();
      app.use(express.json());
      const runtime = createReadOnlyRuntime({ workspaceDir: workspace.dir });
      app.use(tracingMiddleware(runtime));
      app.get('/api/orders', (_request, response) => {
        response.json({ orders: [] });
      });
      const adapter = mountTokensafe(app, { runtime });
      const server = app.listen(0, '127.0.0.1');
      await new Promise<void>((resolve) => server.once('listening', resolve));
      const port = (server.address() as AddressInfo).port;
      try {
        const { report } = await runFixtures(
          `http://127.0.0.1:${port}/__oumv/rpc`,
          workspace.markers,
          'node-express',
          'express',
          'node-express-readonly'
        );
        expect(report.failed).toBe(0);
        expect(report.cases.filter((entry: any) => entry.id.startsWith('read-only-'))).toHaveLength(6);
      } finally {
        await adapter.dispose();
        await new Promise<void>((resolve) => server.close(() => resolve()));
        workspace.cleanup();
      }
    },
    30_000
  );

  it.runIf(hasConformanceRunner)(
    'certifies Fastify with no writing methods registered',
    async () => {
      const workspace = createWorkspace();
      const app = Fastify({ logger: false });
      const runtime = createReadOnlyRuntime({ workspaceDir: workspace.dir });
      const adapter = await registerTokensafe(app, { runtime });
      app.get('/api/orders', async () => ({ orders: [] }));
      await app.listen({ port: 0, host: '127.0.0.1' });
      const port = (app.server.address() as AddressInfo).port;
      try {
        const { report } = await runFixtures(
          `http://127.0.0.1:${port}/__oumv/rpc`,
          workspace.markers,
          'node-fastify',
          'fastify',
          'node-fastify-readonly'
        );
        expect(report.failed).toBe(0);
        expect(report.cases.filter((entry: any) => entry.id.startsWith('read-only-'))).toHaveLength(6);
      } finally {
        await adapter.dispose();
        await app.close();
        workspace.cleanup();
      }
    },
    30_000
  );

  it.runIf(hasConformanceRunner)(
    'certifies the Next.js Route Handlers with no writing methods registered',
    async () => {
      const workspace = createWorkspace();
      const routeDir = path.join(workspace.dir, 'app', 'api', 'orders');
      fs.mkdirSync(routeDir, { recursive: true });
      fs.writeFileSync(
        path.join(routeDir, 'route.ts'),
        'export async function GET() {\n  return Response.json({ orders: [] });\n}\n',
        'utf-8'
      );
      const runtime = createReadOnlyRuntime({ workspaceDir: workspace.dir });
      const handlers = createNextRouteHandlers({ runtime, workspaceDir: workspace.dir, descriptor: false });

      const readBody = (request: IncomingMessage) =>
        new Promise<Buffer>((resolve) => {
          const chunks: Buffer[] = [];
          request.on('data', (chunk) => chunks.push(Buffer.from(chunk)));
          request.on('end', () => resolve(Buffer.concat(chunks)));
        });
      const server: Server = createServer(async (request, response) => {
        const base = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
        const headers = new Headers();
        for (const [key, value] of Object.entries(request.headers)) {
          if (typeof value === 'string') headers.set(key, value);
        }
        const body = request.method === 'POST' ? await readBody(request) : undefined;
        const web = new Request(`${base}${request.url}`, { method: request.method, headers, body });
        const answer = request.method === 'POST' ? await handlers.POST(web) : handlers.GET(web);
        response.statusCode = answer.status;
        answer.headers.forEach((value, key) => response.setHeader(key, value));
        if (answer.body) {
          const reader = answer.body.getReader();
          const { value } = await reader.read();
          if (value) response.write(Buffer.from(value));
          await reader.cancel().catch(() => undefined);
        }
        response.end();
      });
      await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve));
      const port = (server.address() as AddressInfo).port;
      try {
        const { loadFixtures, runFixtureSuite, httpTransport } = require(CONFORMANCE_ENTRY);
        const transport = httpTransport({ url: `http://127.0.0.1:${port}/__oumv/rpc` });
        const handshake = await transport.request({
          jsonrpc: '2.0',
          id: 'certify-handshake',
          method: 'oumv/handshake',
          params: {},
        });
        const report = await runFixtureSuite(transport, loadFixtures(FIXTURES_PATH), workspace.markers, {
          adapterName: 'next-app-router',
          framework: 'nextjs',
          environment: { transport: 'http', runtime: 'next-app-router-route-handler', readOnly: true, node: process.version },
          capabilities: (handshake.result as any)?.capabilities,
          negotiatedVersion: (handshake.result as any)?.protocolVersion,
        });
        expect(report.failed).toBe(0);
        expect(report.cases.filter((entry: any) => entry.id.startsWith('read-only-'))).toHaveLength(6);
        const outDir = process.env.CERT_OUT_DIR;
        if (outDir) {
          fs.mkdirSync(outDir, { recursive: true });
          fs.writeFileSync(path.join(outDir, 'next-app-router-readonly.json'), JSON.stringify(report, null, 2) + '\n');
        }
      } finally {
        await new Promise<void>((resolve) => server.close(() => resolve()));
        await handlers.dispose();
        workspace.cleanup();
      }
    },
    60_000
  );
});
