import { describe, expect, it } from 'vitest';
import fs from 'node:fs';
import path from 'node:path';
import { createRequire } from 'node:module';
import { createServer, type IncomingMessage, type Server } from 'node:http';
import type { AddressInfo } from 'node:net';
import { createNextRouteHandlers } from '../src/index.js';
import {
  CONFORMANCE_ENTRY,
  FIXTURES_PATH,
  createWorkspace,
  hasConformanceRunner,
} from './helpers/workspace.js';

const require = createRequire(import.meta.url);

// §12: the certificate runs against a token-guarded binding.
const CERT_TOKEN = 'oumv-cert-token';

/**
 * Next.js App Router certificate (server profile, spec §10).
 *
 * The adapter is a pair of Route Handlers speaking Web Request/Response. Next
 * itself is only the host, so the harness serves the handlers through a bare
 * node:http bridge and certifies the wire over real HTTP — the same transports
 * Next calls. The browser bridge for client components is not part of this
 * profile and is not claimed by the report.
 */
describe('Next.js App Router adapter', () => {
  function bridge(handlers: ReturnType<typeof createNextRouteHandlers>): Promise<{ server: Server; url: string }> {
    const readBody = (req: IncomingMessage) =>
      new Promise<Buffer>((resolve) => {
        const chunks: Buffer[] = [];
        req.on('data', (chunk) => chunks.push(Buffer.from(chunk)));
        req.on('end', () => resolve(Buffer.concat(chunks)));
      });

    const server = createServer(async (req, res) => {
      const base = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
      const headers = new Headers();
      for (const [key, value] of Object.entries(req.headers)) {
        if (typeof value === 'string') headers.set(key, value);
      }
      const body = req.method === 'POST' ? await readBody(req) : undefined;
      const request = new Request(`${base}${req.url}`, { method: req.method, headers, body });
      const response = req.method === 'POST' ? await handlers.POST(request) : handlers.GET(request);
      res.statusCode = response.status;
      response.headers.forEach((value, key) => res.setHeader(key, value));
      if (response.body) {
        const reader = response.body.getReader();
        // Drain just the first chunk for SSE probes; the fixture suite uses POST.
        const { value } = await reader.read();
        if (value) res.write(Buffer.from(value));
        await reader.cancel().catch(() => undefined);
      }
      res.end();
    });

    return new Promise((resolve) => {
      server.listen(0, '127.0.0.1', () => {
        const port = (server.address() as AddressInfo).port;
        resolve({ server, url: `http://127.0.0.1:${port}/__oumv/rpc` });
      });
    });
  }

  function seedNextApp(workspace: ReturnType<typeof createWorkspace>) {
    const routeDir = path.join(workspace.dir, 'app', 'api', 'orders');
    fs.mkdirSync(routeDir, { recursive: true });
    fs.writeFileSync(
      path.join(routeDir, 'route.ts'),
      'export async function GET() {\n  return Response.json({ orders: [] });\n}\n' +
        'export async function POST() {\n  return Response.json({ id: 1 }, { status: 201 });\n}\n',
      'utf-8'
    );
    const groupDir = path.join(workspace.dir, 'app', '(marketing)', 'pricing');
    fs.mkdirSync(groupDir, { recursive: true });
    fs.writeFileSync(path.join(groupDir, 'route.ts'), 'export function GET() { return new Response("ok"); }\n', 'utf-8');
  }

  it('scans App Router route files and keeps route groups out of the path', () => {
    const workspace = createWorkspace();
    try {
      seedNextApp(workspace);
      const handlers = createNextRouteHandlers({
        workspaceDir: workspace.dir,
        authToken: CERT_TOKEN,
        descriptor: false,
      });
      return handlers
        .POST(
          new Request('http://127.0.0.1/__oumv/rpc', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json', 'X-OUMV-Session-Token': CERT_TOKEN },
            body: JSON.stringify({ jsonrpc: '2.0', id: 1, method: 'oumv/observe', params: { level: 'L1' } }),
          })
        )
        .then(async (response) => {
          expect(response.status).toBe(200);
          const payload: any = await response.json();
          const endpoints = payload.result.data.endpoints.map((entry: any) => `${entry.method} ${entry.path}`);
          expect(endpoints).toContain('GET /api/orders');
          expect(endpoints).toContain('POST /api/orders');
          expect(endpoints).toContain('GET /pricing');
          await handlers.dispose();
        });
    } finally {
      workspace.cleanup();
    }
  });

  it('refuses a request without the configured token', async () => {
    const workspace = createWorkspace();
    try {
      const handlers = createNextRouteHandlers({ workspaceDir: workspace.dir, authToken: CERT_TOKEN, descriptor: false });
      const refused = await handlers.POST(
        new Request('http://127.0.0.1/__oumv/rpc', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ jsonrpc: '2.0', id: 1, method: 'oumv/observe', params: { level: 'L0' } }),
        })
      );
      expect(refused.status).toBe(401);
      expect(((await refused.json()) as any).error.code).toBe(-32007);
      await handlers.dispose();
    } finally {
      workspace.cleanup();
    }
  });

  it.runIf(hasConformanceRunner)(
    'certifies the server profile over HTTP at Level 3',
    async () => {
      const workspace = createWorkspace();
      seedNextApp(workspace);
      const handlers = createNextRouteHandlers({
        workspaceDir: workspace.dir,
        authToken: CERT_TOKEN,
        descriptor: false,
        // The cert app's route table, dispatched in-process exactly as an adopter
        // would wire its own Route Handler modules for oumv/verify.
        verifyFetch: async (request) => {
          const url = new URL(request.url);
          if (request.method === 'GET' && url.pathname === '/api/orders') {
            return Response.json({ orders: [] });
          }
          if (request.method === 'POST' && url.pathname === '/api/orders') {
            return Response.json({ id: 1 }, { status: 201 });
          }
          return new Response(JSON.stringify({ error: 'not found' }), { status: 404 });
        },
      });
      const { server, url } = await bridge(handlers);
      try {
        const { loadFixtures, runFixtureSuite, httpTransport } = require(CONFORMANCE_ENTRY);
        const transport = httpTransport({ url, token: CERT_TOKEN });
        const handshake = await transport.request({
          jsonrpc: '2.0',
          id: 'certify-handshake',
          method: 'oumv/handshake',
          params: {},
        });
        const report = await runFixtureSuite(transport, loadFixtures(FIXTURES_PATH), workspace.markers, {
          adapterName: 'next-app-router',
          framework: 'nextjs',
          environment: { transport: 'http', auth: 'required', runtime: 'next-app-router-route-handler', node: process.version },
          capabilities: (handshake.result as any)?.capabilities,
          negotiatedVersion: (handshake.result as any)?.protocolVersion,
          auth: true,
        });
        if (report.failed > 0) console.log(report.cases.filter((entry: any) => entry.status !== 'passed'));
        expect(report.failed).toBe(0);
        expect(report.attainedLevel).toBe(3);
        expect(report.cases.filter((entry: any) => entry.id.startsWith('auth-'))).toHaveLength(3);
        const outDir = process.env.CERT_OUT_DIR;
        if (outDir) {
          fs.mkdirSync(outDir, { recursive: true });
          fs.writeFileSync(path.join(outDir, 'next-app-router.json'), JSON.stringify(report, null, 2) + '\n');
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
