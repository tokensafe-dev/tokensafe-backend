import { afterAll, describe, expect, it } from 'bun:test';
import { Database } from 'bun:sqlite';
import fs from 'node:fs';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { createRequire } from 'node:module';
import { Elysia } from 'elysia';
import { createElysiaPlugin, type ElysiaAdapter } from '../src/adapters/elysia.js';
import {
  CONFORMANCE_ENTRY,
  FIXTURES_PATH,
  createWorkspace,
  hasConformanceRunner,
  writeReport,
  type Workspace,
} from './helpers.js';

const require = createRequire(import.meta.url);

// §12: the certificates prove the refusal, so the target runs token-guarded.
const CERT_TOKEN = 'oumv-cert-token';

const home = mkdtempSync(join(tmpdir(), 'oumv-bun-cert-home-'));
process.env.TOKENSAFE_HOME = home;

interface CertApp {
  adapter: ElysiaAdapter;
  app: Elysia;
  workspace: Workspace;
  rpcUrl: string;
  eventsUrl: string;
  wsUrl: string;
  cleanup: () => Promise<void>;
}

/**
 * The Elysia/Bun certificate target: a real app on Bun's server with the fixture
 * routes, a token-guarded binding, and a `bun:sqlite` sandbox — the sandbox and the
 * read-only query hook are what let the report claim the 2 Level-3 capability cases
 * a Next.js cert cannot (48 base instead of 46).
 */
async function startCertApp(options: { queryDisabled?: boolean } = {}): Promise<CertApp> {
  const workspace = createWorkspace();
  const db = new Database(':memory:');
  db.run('CREATE TABLE fixture (id INTEGER PRIMARY KEY)');
  const sandbox = {
    db: {
      async run<T>(fn: () => Promise<T> | T) {
        db.run('BEGIN');
        try {
          const result = await fn();
          return { result, rolledBack: true, queries: 1 };
        } finally {
          db.run('ROLLBACK');
        }
      },
      async query(sql: string, params?: unknown[], limit?: number) {
        const statement = db.prepare(sql);
        const rows = statement.values(...((params ?? []) as any[])) as unknown[][];
        return {
          columns: statement.columnNames as string[],
          rows: typeof limit === 'number' ? rows.slice(0, limit) : rows,
        };
      },
    },
  };
  const adapter = createElysiaPlugin({
    workspaceDir: workspace.dir,
    authToken: CERT_TOKEN,
    sandbox,
    ...(options.queryDisabled ? { permissions: { query: false } } : {}),
  });
  const app = new Elysia()
    .get('/api/orders', () => ({ orders: [] }))
    .post('/api/orders', () => new Response(JSON.stringify({ id: 1 }), { status: 201 }))
    .post('/__oumv_sandbox_probe', ({ request }) => {
      if (request.headers.get('x-tokensafe-dry-run') !== 'true') {
        return new Response(JSON.stringify({ error: 'dry-run header required' }), { status: 400 });
      }
      db.run('INSERT INTO fixture (id) VALUES (1)');
      return { ok: true };
    })
    .use(adapter.plugin);
  await app.listen(0);
  const port = (app.server as any).port as number;
  return {
    adapter,
    app,
    workspace,
    rpcUrl: `http://127.0.0.1:${port}/__oumv/rpc`,
    eventsUrl: `http://127.0.0.1:${port}/__oumv/events`,
    wsUrl: `ws://127.0.0.1:${port}/__oumv/rpc`,
    cleanup: async () => {
      await adapter.dispose();
      app.stop();
      workspace.cleanup();
    },
  };
}

describe('Elysia/Bun certificate', () => {
  it(
    'certifies HTTP at Level 3 with transport auth (spec §10)',
    async () => {
      if (!hasConformanceRunner) return; // runner not built in agent-oumv
      const live = await startCertApp();
      try {
        const { loadFixtures, runFixtureSuite, httpTransport } = require(CONFORMANCE_ENTRY);
        const transport = httpTransport({ url: live.rpcUrl, token: CERT_TOKEN });
        const handshake = await transport.request({
          jsonrpc: '2.0',
          id: 'certify-handshake',
          method: 'oumv/handshake',
          params: {},
        });
        const report = await runFixtureSuite(transport, loadFixtures(FIXTURES_PATH), live.workspace.markers, {
          adapterName: 'bun-elysia',
          framework: 'elysia',
          environment: { transport: 'http', auth: 'required', runtime: `bun ${Bun.version}` },
          capabilities: (handshake.result as any)?.capabilities,
          negotiatedVersion: (handshake.result as any)?.protocolVersion,
          auth: true,
        });
        if (report.failed > 0) console.log(report.cases.filter((entry: any) => entry.status !== 'passed'));
        expect(report.failed).toBe(0);
        expect(report.attainedLevel).toBe(3);
        expect(report.cases.filter((entry: any) => entry.id.startsWith('auth-'))).toHaveLength(3);
        writeReport('bun-elysia.json', report);
      } finally {
        await live.cleanup();
      }
    },
    120_000
  );

  it(
    'certifies the SSE binding with Last-Event-ID replay',
    async () => {
      if (!hasConformanceRunner) return; // runner not built in agent-oumv
      const live = await startCertApp();
      try {
        const { loadFixtures, runFixtureSuite, httpTransport, sseEventSource } = require(CONFORMANCE_ENTRY);
        const source = sseEventSource({ url: live.eventsUrl, token: CERT_TOKEN });
        await source.start();
        try {
          const http = httpTransport({ url: live.rpcUrl, token: CERT_TOKEN });
          const handshake = await http.request({
            jsonrpc: '2.0',
            id: 'certify-handshake',
            method: 'oumv/handshake',
            params: {},
          });
          const report = await runFixtureSuite(http, loadFixtures(FIXTURES_PATH), live.workspace.markers, {
            adapterName: 'bun-elysia-sse',
            framework: 'elysia',
            environment: { transport: 'sse', auth: 'required', runtime: `bun ${Bun.version}` },
            auth: true,
            capabilities: (handshake.result as any)?.capabilities,
            negotiatedVersion: (handshake.result as any)?.protocolVersion,
            push: source,
            pushTimeoutMs: 5_000,
          });
          if (report.failed > 0) console.log(report.cases.filter((entry: any) => entry.status !== 'passed'));
          expect(report.failed).toBe(0);
          writeReport('transports/bun-elysia-sse.json', report);
        } finally {
          await source.close();
        }
      } finally {
        await live.cleanup();
      }
    },
    120_000
  );

  it(
    'certifies the WebSocket binding and the state push',
    async () => {
      if (!hasConformanceRunner) return; // runner not built in agent-oumv
      const live = await startCertApp();
      try {
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
            adapterName: 'bun-elysia-ws',
            framework: 'elysia',
            environment: { transport: 'ws', runtime: `bun ${Bun.version}` },
            capabilities: (handshake.result as any)?.capabilities,
            negotiatedVersion: (handshake.result as any)?.protocolVersion,
            push: transport,
            pushTimeoutMs: 5_000,
          });
          if (report.failed > 0) console.log(report.cases.filter((entry: any) => entry.status !== 'passed'));
          expect(report.failed).toBe(0);
          writeReport('transports/bun-elysia-ws.json', report);
        } finally {
          await transport.close?.();
        }
      } finally {
        await live.cleanup();
      }
    },
    120_000
  );
  it(
    'certifies the §14.2 workspace policy (permissions.query=false)',
    async () => {
      if (!hasConformanceRunner) return; // runner not built in agent-oumv
      const live = await startCertApp({ queryDisabled: true });
      try {
        const { loadFixtures, runFixtureSuite, httpTransport } = require(CONFORMANCE_ENTRY);
        const transport = httpTransport({ url: live.rpcUrl, token: CERT_TOKEN });
        const report = await runFixtureSuite(transport, loadFixtures(FIXTURES_PATH), live.workspace.markers, {
          adapterName: 'bun-elysia',
          negotiatedVersion: '1.6.0',
          auth: true,
          policy: true,
          environment: { transport: 'http', policy: 'query_disabled', runtime: `bun ${Bun.version}` },
        });
        const policyCases = report.cases.filter((c: any) => c.id.startsWith('policy-')).map((c: any) => c.id);
        // The refusal is -32007 with the registry reason, and observe is unaffected.
        expect(policyCases.sort()).toEqual(['policy-observe-is-unaffected', 'policy-query-disabled-is-refused']);
        expect(report.cases.filter((c: any) => c.status !== 'passed')).toEqual([]);
        writeReport('bun-elysia-query-disabled.json', report);
      } finally {
        await live.cleanup();
      }
    },
    60000,
  );
});

afterAll(() => {
  rmSync(home, { recursive: true, force: true });
});
