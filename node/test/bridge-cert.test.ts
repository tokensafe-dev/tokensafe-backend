import { describe, expect, it } from 'vitest';
import fs from 'node:fs';
import path from 'node:path';
import { createRequire } from 'node:module';
import { createServer, type IncomingMessage, type Server } from 'node:http';
import type { AddressInfo } from 'node:net';
import { createNextRouteHandlers } from '../src/index.js';
import { CONFORMANCE_ENTRY, FIXTURES_PATH, createWorkspace, hasConformanceRunner } from './helpers/workspace.js';

const require = createRequire(import.meta.url);
const CERT_TOKEN = 'oumv-cert-token';

/**
 * Next.js App Router certificate WITH the browser bridge (spec §7.4, fixtures v15).
 *
 * Next itself is only the host: the harness serves both Route Handler pairs (RPC
 * and bridge) through a bare node:http bridge, and a **scripted client** speaks
 * the page side of the bridge protocol over real HTTP — register, snapshot, SSE
 * command stream, verify-res, traces. That proves the server side end to end
 * without a browser; the real-Chrome certificate is `next-app-router-browser.sh`.
 */

const SCRIPTED_SNAPSHOT = {
  framework: 'react',
  route: '/',
  title: 'Scripted Next page',
  elementCount: 6,
  mountedComponents: [
    { id: 'frontend.component:Counter', name: 'Counter', instances: 1, propsKeys: ['step'], hooksCount: 2 },
  ],
  interactiveSurface: [
    { id: 'btn-inc', tag: 'button', role: 'button', label: 'Increment', action: 'click', component: 'Counter' },
  ],
  contracts: [],
  errorsCount: 0,
  errors: [],
  tracesCount: 0,
  timestamp: 0,
};

function serveHandlers(handlers: ReturnType<typeof createNextRouteHandlers>): Promise<{ server: Server; base: string }> {
  const port = () => (server.address() as AddressInfo).port;
  const server = createServer(async (req: IncomingMessage, res) => {
    const base = `http://127.0.0.1:${port()}`;
    const url = new URL(req.url ?? '/', base);
    const bridgeHandlers = handlers.BRIDGE;
    const target = url.pathname === '/api/tokensafe/bridge' && bridgeHandlers ? bridgeHandlers : handlers;
    const headers = new Headers();
    for (const [key, value] of Object.entries(req.headers)) {
      if (typeof value === 'string') headers.set(key, value);
    }
    const chunks: Buffer[] = [];
    if (req.method === 'POST') {
      for await (const chunk of req) chunks.push(Buffer.from(chunk));
    }
    const request = new Request(`${base}${req.url}`, {
      method: req.method,
      headers,
      body: req.method === 'POST' ? Buffer.concat(chunks) : undefined,
    });
    const response = req.method === 'POST' ? await target.POST(request) : await target.GET(request);
    res.statusCode = response.status;
    response.headers.forEach((value, key) => res.setHeader(key, value));
    if (!response.body) {
      res.end();
      return;
    }
    const reader = response.body.getReader();
    req.on('close', () => {
      reader.cancel().catch(() => undefined);
    });
    try {
      for (;;) {
        const { value, done } = await reader.read();
        if (done) break;
        if (value) res.write(Buffer.from(value));
      }
    } catch {
      // client went away
    }
    res.end();
  });
  return new Promise((resolve) => {
    server.listen(0, '127.0.0.1', () => resolve({ server, base: `http://127.0.0.1:${port()}` }));
  });
}

interface ScriptedClient {
  stop: () => Promise<void>;
  posts: number;
}

/** The page side of the bridge, over the same HTTP surface a browser uses. */
async function startScriptedClient(bridgeUrl: string): Promise<ScriptedClient> {
  const registration = (await (
    await fetch(bridgeUrl, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ kind: 'register', url: 'http://127.0.0.1:3000/', title: 'scripted tab' }),
    })
  ).json()) as { sessionId: string; token: string };
  expect(registration.sessionId).toMatch(/^bs_/);

  const sessionUrl = `${bridgeUrl}?session=${encodeURIComponent(registration.sessionId)}&t=${encodeURIComponent(registration.token)}`;
  let posts = 0;
  const post = async (message: Record<string, unknown>): Promise<void> => {
    posts += 1;
    await fetch(sessionUrl, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(message) });
  };

  const stream = await fetch(sessionUrl);
  expect(stream.status).toBe(200);
  const reader = stream.body!.getReader();
  let markReady: () => void = () => {};
  const ready = new Promise<void>((resolve) => {
    markReady = resolve;
  });

  const pump = (async () => {
    const decoder = new TextDecoder();
    let buffer = '';
    try {
      for (;;) {
        const { value, done } = await reader.read();
        if (done) break;
        buffer += decoder.decode(value, { stream: true });
        let index: number;
        while ((index = buffer.indexOf('\n\n')) >= 0) {
          const frame = buffer.slice(0, index);
          buffer = buffer.slice(index + 2);
          const event = /^event: (.+)$/m.exec(frame)?.[1] ?? 'message';
          const data = frame
            .split('\n')
            .filter((line) => line.startsWith('data: '))
            .map((line) => line.slice(6))
            .join('\n');
          if (event === 'ready') markReady();
          if (event !== 'command') continue;
          const command = JSON.parse(data) as { requestId: string; kind: string; target?: string; action?: string };
          if (command.kind !== 'verify') continue;
          await post({
            kind: 'trace',
            trace: { type: 'verify:' + (command.action ?? 'click'), target: command.target, route: '/', timestamp: Date.now() },
          });
          await post({
            kind: 'verify-res',
            requestId: command.requestId,
            status: 'passed',
            target: command.target,
            action: command.action,
            diff: { text: { before: '1', after: '2' } },
            surfaceDiff: [{ op: 'text', target: command.target, before: '1', after: '2' }],
            durationMs: 4,
            latency: { actionMs: 1, totalMs: 4 },
          });
        }
      }
    } catch {
      // stream cancelled by the server
    }
  })();

  await ready;
  await post({ kind: 'snapshot', snapshot: SCRIPTED_SNAPSHOT });
  return {
    stop: async () => {
      await reader.cancel().catch(() => undefined);
      await pump.catch(() => undefined);
    },
    get posts() {
      return posts;
    },
  } as ScriptedClient;
}

describe('Next.js App Router browser bridge', () => {
  it.runIf(hasConformanceRunner)(
    'certifies the bridge with a scripted page over HTTP at Level 3',
    async () => {
      const workspace = createWorkspace();
      fs.mkdirSync(path.join(workspace.dir, 'app', 'api', 'orders'), { recursive: true });
      fs.writeFileSync(
        path.join(workspace.dir, 'app', 'api', 'orders', 'route.ts'),
        'export async function GET() {\n  return Response.json({ orders: [] });\n}\n',
        'utf-8'
      );
      const handlers = createNextRouteHandlers({
        workspaceDir: workspace.dir,
        authToken: CERT_TOKEN,
        descriptor: false,
        bridge: true,
        verifyFetch: async (request) => {
          const url = new URL(request.url);
          if (request.method === 'GET' && url.pathname === '/api/orders') return Response.json({ orders: [] });
          return new Response(JSON.stringify({ error: 'not found' }), { status: 404 });
        },
      });
      const { server, base } = await serveHandlers(handlers);
      const client = await startScriptedClient(`${base}/api/tokensafe/bridge`);
      try {
        const { loadFixtures, runFixtureSuite, httpTransport } = require(CONFORMANCE_ENTRY);
        const transport = httpTransport({ url: `${base}/__oumv/rpc`, token: CERT_TOKEN });
        const handshake = await transport.request({ jsonrpc: '2.0', id: 'certify-handshake', method: 'oumv/handshake', params: {} });
        const capabilities = (handshake.result as any)?.capabilities;
        // §7.4: the two bridge capabilities exist only because a bridge is mounted.
        expect(capabilities?.mutate?.ephemeralDispatch).toBe(true);
        expect(capabilities?.verify?.surfaceDiff).toBe(true);

        const report = await runFixtureSuite(transport, loadFixtures(FIXTURES_PATH), workspace.markers, {
          adapterName: 'next-app-router-bridge-sim',
          framework: 'nextjs',
          environment: {
            transport: 'http',
            auth: 'required',
            browser: 'scripted-client',
            runtime: 'next-app-router-route-handler',
            node: process.version,
          },
          capabilities,
          negotiatedVersion: (handshake.result as any)?.protocolVersion,
          auth: true,
          browser: true,
        });
        if (report.failed > 0) console.log(report.cases.filter((entry: any) => entry.status !== 'passed'));
        expect(report.failed).toBe(0);
        expect(report.attainedLevel).toBe(3);
        const browserCases = report.cases.filter((entry: any) => entry.id.startsWith('browser-'));
        expect(browserCases.map((entry: any) => entry.id).sort()).toEqual([
          'browser-l4-records-dom-traces',
          'browser-observe-l1-lists-elements',
          'browser-verify-dispatches-a-dom-action',
        ]);
        expect(browserCases.every((entry: any) => entry.status === 'passed')).toBe(true);
        // snapshot + one trace + one verify-res: the scripted page really answered.
        expect(client.posts).toBeGreaterThanOrEqual(3);

        const outDir = process.env.CERT_OUT_DIR;
        if (outDir) {
          fs.mkdirSync(outDir, { recursive: true });
          fs.writeFileSync(path.join(outDir, 'next-app-router-bridge-sim.json'), JSON.stringify(report, null, 2) + '\n');
        }
      } finally {
        await client.stop();
        await new Promise<void>((resolve) => server.close(() => resolve()));
        await handlers.dispose();
        workspace.cleanup();
      }
    },
    90_000
  );
});
