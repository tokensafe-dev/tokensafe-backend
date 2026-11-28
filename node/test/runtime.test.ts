import fs from 'node:fs';
import path from 'node:path';
import { afterEach, describe, expect, it } from 'vitest';
import { OumvRuntime, checkSyntax, extractSymbolRange, parseVerifyTarget } from '../src/runtime.js';
import { resolveWorkspacePath } from '../src/paths.js';
import { createWorkspace, sha } from './helpers/workspace.js';

describe('OumvRuntime core', () => {
  let cleanup = () => {};
  afterEach(() => cleanup());

  it('narrows advertised capabilities to the request', () => {
    const runtime = new OumvRuntime({ workspaceDir: process.cwd() });
    const full: any = runtime.handshake({});
    expect(full.capabilities.observe.levels).toHaveLength(5);
    expect(full.sessionId).toMatch(/^sess-/);
    expect(full.profile).toBe('backend');

    const narrowed: any = runtime.handshake({
      requestedCapabilities: { observe: { levels: ['L0', 'L1'], formats: ['json', 'yaml'] } },
    });
    expect(narrowed.capabilities.observe.levels).toEqual(['L0', 'L1']);
    // yaml is not implemented: the request is filtered, not echoed.
    expect(narrowed.capabilities.observe.formats).toEqual(['json']);
  });

  it('refuses unknown observe levels and formats', () => {
    const runtime = new OumvRuntime({ workspaceDir: process.cwd() });
    const codeOf = (fn: () => unknown) => {
      try {
        fn();
        return 0;
      } catch (error: any) {
        return error.code;
      }
    };
    expect(codeOf(() => runtime.observe({ level: 'L9' }))).toBe(-32006);
    expect(codeOf(() => runtime.observe({ level: 'L0', format: 'yaml' }))).toBe(-32006);
  });

  it('enforces the guard order: jail -> missing -> syntax -> version -> OCC', () => {
    const workspace = createWorkspace();
    cleanup = workspace.cleanup;
    const runtime = new OumvRuntime({ workspaceDir: workspace.dir });

    const attempt = (params: Record<string, any>) => {
      try {
        runtime.applyEdit(params);
        return 'ok';
      } catch (error: any) {
        return error.code;
      }
    };

    // A traversal path wins over everything else, even when the file is missing.
    expect(attempt({ path: '../outside.js', replacement: 'x', expectedVersion: 'sha256:' + '0'.repeat(64) })).toBe(-32003);
    expect(attempt({ path: 'missing.js', replacement: 'x', expectedVersion: 'sha256:' + '0'.repeat(64) })).toBe(-32009);
    expect(attempt({ path: 'edit_target.js', replacement: 'export function Broken( {\n' })).toBe(-32002);
    expect(attempt({ path: 'edit_target.js', replacement: 'export const value = 1;\n' })).toBe(-32011);
    expect(
      attempt({ path: 'edit_target.js', replacement: 'export const value = 1;\n', expectedVersion: 'sha256:' + '0'.repeat(64) })
    ).toBe(-32001);

    // The same order holds for patch, symbol and every batch item: a stale version
    // paired with broken syntax answers -32002 (guard 3), never -32001 (guard 5).
    const stale = 'sha256:' + '0'.repeat(64);
    expect(
      attempt({
        path: 'edit_target.js',
        mode: 'patch',
        hunks: [{ target: 'export const value = 41;', replacement: 'export function Broken( {' }],
        expectedVersion: stale,
      })
    ).toBe(-32002);
    expect(
      attempt({
        path: 'symbol_target.js',
        mode: 'symbol',
        symbol: 'value',
        replacement: 'export function Broken( {',
        expectedVersion: stale,
      })
    ).toBe(-32002);
    expect(
      attempt({
        edits: [
          { path: 'edit_target.js', mode: 'replace', replacement: 'export function Broken( {', expectedVersion: stale },
        ],
      })
    ).toBe(-32002);
  });

  it('creates a new file with mode=create and never overwrites', () => {
    const workspace = createWorkspace();
    cleanup = workspace.cleanup;
    const runtime = new OumvRuntime({ workspaceDir: workspace.dir });

    const created: any = runtime.applyEdit({
      path: 'lib/created.js',
      replacement: 'export const created = true;\n',
      mode: 'create',
    });
    expect(created.created).toBe(true);
    expect(created.status).toBe('APPLIED');
    expect(fs.existsSync(`${workspace.dir}/lib/created.js`)).toBe(true);

    let code = 0;
    try {
      runtime.applyEdit({ path: 'lib/created.js', replacement: 'x = 1;\n', mode: 'create' });
    } catch (error: any) {
      code = error.code;
    }
    expect(code).toBe(-32001);

    code = 0;
    try {
      runtime.applyEdit({
        path: 'lib/other.js',
        replacement: 'x = 1;\n',
        mode: 'create',
        expectedVersion: 'sha256:' + '0'.repeat(64),
      });
    } catch (error: any) {
      code = error.code;
    }
    expect(code).toBe(-32011);
  });

  it('rejects credential files even inside the workspace', () => {
    const workspace = createWorkspace();
    cleanup = workspace.cleanup;
    try {
      resolveWorkspacePath('.env', workspace.dir);
      throw new Error('expected a security violation');
    } catch (error: any) {
      expect(error.code).toBe(-32003);
    }
  });

  it('rejects unknown session ids with session_expired data', async () => {
    const { Dispatcher } = await import('../src/transport/dispatcher.js');
    const runtime = new OumvRuntime({ workspaceDir: process.cwd() });
    const response = await new Dispatcher(runtime).dispatch({
      jsonrpc: '2.0',
      id: 1,
      method: 'oumv/observe',
      params: { level: 'L0', sessionId: 'sess-missing' },
    });
    expect(response.error?.code).toBe(-32011);
    expect(response.error?.data?.reason).toBe('session_expired');
  });

  it('answers unknown methods with -32601', async () => {
    const { Dispatcher } = await import('../src/transport/dispatcher.js');
    const runtime = new OumvRuntime({ workspaceDir: process.cwd() });
    const response = await new Dispatcher(runtime).dispatch({ jsonrpc: '2.0', id: 2, method: 'oumv/nope' });
    expect(response.error?.code).toBe(-32601);
  });

  it('slices a symbol declaration and reports its line range', () => {
    const source = ['// header', 'export function value() {', '  return 41;', '}', '', 'export const other = 1;'].join('\n');
    const slice = extractSymbolRange(source, 'value');
    expect(slice?.source).toContain('return 41');
    expect(slice?.startLine).toBe(2);
    expect(slice?.endLine).toBe(4);
    expect(extractSymbolRange(source, 'absent')).toBeUndefined();
  });

  it('parses verify targets and keeps the method explicit', () => {
    expect(parseVerifyTarget('GET /api/orders')).toEqual({ method: 'GET', url: '/api/orders' });
    expect(parseVerifyTarget('/health')).toEqual({ method: 'POST', url: '/health' });
  });

  it('skips the syntax guard for non-JS paths', () => {
    expect(checkSyntax('data.json', '{ broken').status).toBe('skipped');
    expect(checkSyntax('app.ts', 'const x: number = 1;\n').status).toBe('passed');
    try {
      checkSyntax('app.ts', 'const = ;');
      throw new Error('expected a syntax error');
    } catch (error: any) {
      expect(error.code).toBe(-32002);
    }
  });

  it('records traces and derives causality only from them', () => {
    const runtime = new OumvRuntime({ workspaceDir: process.cwd() });
    const before: any = runtime.understand({ query: 'causality', target: 'GET /api/orders' });
    expect(before.causality.observed).toBe(false);
    expect(before.causality.traceCount).toBe(0);

    runtime.recordTrace({
      method: 'GET',
      path: '/api/orders',
      route: '/api/orders',
      handler: 'handler',
      statusCode: 200,
      durationMs: 3.5,
    });
    const after: any = runtime.understand({ query: 'causality', target: 'GET /api/orders' });
    expect(after.causality.observed).toBe(true);
    expect(after.causality.rootCause).toBe('GET /api/orders -> 200');

    const l4: any = runtime.observe({ level: 'L4' });
    expect(l4.data.traces).toHaveLength(1);
    expect(l4.data.traceCount).toBe(1);
  });

  it('publishes a state event on an applied edit and replays it by monotonic id', () => {
    const workspace = createWorkspace();
    cleanup = workspace.cleanup;
    const runtime = new OumvRuntime({ workspaceDir: workspace.dir });

    const seen: any[] = [];
    const unsubscribe = runtime.subscribeEvents((event) => seen.push(event));

    const relative = 'state_target.js';
    const content = 'const value = 1;\n';
    fs.writeFileSync(path.join(workspace.dir, relative), content);

    const result: any = runtime.applyEdit({
      path: relative,
      mode: 'patch',
      expectedVersion: sha(content),
      hunks: [{ target: 'value = 1', replacement: 'value = 2' }],
    });

    expect(result.status).toBe('APPLIED');
    expect(seen).toHaveLength(1);
    expect(seen[0]).toMatchObject({
      id: 1,
      type: 'state',
      data: { path: relative, operation: 'patch', version: result.newVersion },
    });
    expect(runtime.eventsSince(0)).toHaveLength(1);
    expect(runtime.eventsSince(1)).toHaveLength(0);

    unsubscribe();
    runtime.recordState(relative, String(result.newVersion), 'patch');
    expect(seen).toHaveLength(1); // a closed subscription receives nothing
  });

  it('runs verify inside the adopter DB sandbox and reports the observed rollback', async () => {
    const workspace = createWorkspace();
    cleanup = workspace.cleanup;
    let executed = 0;
    let rolledBack = false;
    const runtime = new OumvRuntime({
      workspaceDir: workspace.dir,
      sandbox: {
        db: {
          run: async (fn) => {
            const result = await fn();
            rolledBack = true;
            return { result, rolledBack: true, queries: 3 };
          },
        },
      },
    });
    runtime.setHandler((req, res) => {
      executed += 1;
      res.setHeader('x-dry-run', String(req.headers['x-tokensafe-dry-run'] ?? ''));
      res.statusCode = 200;
      res.end('{"ok":true}');
    });

    const result: any = await runtime.verify({
      target: 'POST /api/items',
      sandbox: { mode: 'rollback' },
    });

    expect(executed).toBe(1);
    expect(rolledBack).toBe(true);
    expect(result.rolledBack).toBe(true);
    expect(result.rollbackStatus).toBe('verified');
    expect(result.dbDiff).toEqual({ rollback: true, queries: 3, driver: 'app-hook' });
    expect(result.scope.notExecuted).not.toContain('persistent database state');

    const handshake: any = runtime.handshake({});
    expect(handshake.capabilities.verify.sandboxRollback).toBe(true);
    expect(handshake.capabilities.transports).toContain('http');
  });

  it('refuses a sandbox request when no hook is configured', async () => {
    const runtime = new OumvRuntime({ workspaceDir: process.cwd() });
    runtime.setHandler((_req, res) => {
      res.statusCode = 200;
      res.end('{}');
    });
    await expect(runtime.verify({ target: 'GET /', sandbox: { mode: 'rollback' } })).rejects.toThrow(
      /RUNTIME_UNAVAILABLE/
    );
    const handshake: any = runtime.handshake({});
    expect(handshake.capabilities.verify.sandboxRollback).toBe(false);
  });

  describe('§14.2 query workspace policy (wire 1.6.0)', () => {
    const hook = {
      db: {
        query: async () => ({ columns: ['1'], rows: [[1]] }),
      },
    };

    it('answers columns/rows when no policy disables query', async () => {
      const runtime = new OumvRuntime({ workspaceDir: process.cwd(), sandbox: hook });

      const result: any = await runtime.query({ sql: 'SELECT 1' });

      expect(result.columns).toEqual(['1']);
      expect(result.rows).toEqual([[1]]);
      expect(result.rowCount).toBe(1);
    });

    it('rejects with -32007 query_disabled when the policy forbids it', async () => {
      const runtime = new OumvRuntime({
        workspaceDir: process.cwd(),
        sandbox: hook,
        permissions: { query: false },
      });

      await expect(runtime.query({ sql: 'SELECT 1' })).rejects.toMatchObject({
        code: -32007,
        data: { reason: 'query_disabled' },
      });
    });

    it('checks the policy before the missing-hook gate', async () => {
      const runtime = new OumvRuntime({ workspaceDir: process.cwd(), permissions: { query: false } });

      await expect(runtime.query({ sql: 'SELECT 1' })).rejects.toMatchObject({ code: -32007 });
    });

    it('still answers -32006 when no hook is configured and no policy forbids it', async () => {
      const runtime = new OumvRuntime({ workspaceDir: process.cwd() });

      await expect(runtime.query({ sql: 'SELECT 1' })).rejects.toMatchObject({ code: -32006 });
    });
  });

  describe('§8 result versioning (wire 1.6.0)', () => {
    it('carries a workspace digest and answers ifVersion honestly', () => {
      const workspace = createWorkspace();
      cleanup = workspace.cleanup;
      const runtime = new OumvRuntime({ workspaceDir: workspace.dir });

      const observed: any = runtime.observe({ level: 'L0' });
      const digest = observed.workspaceVersion;
      expect(digest).toMatch(/^sha256:[a-f0-9]{64}$/);

      // Match: the cached copy is fresh, so the answer is the short envelope.
      const short: any = runtime.observe({ level: 'L1', ifVersion: digest });
      expect(short.unchanged).toBe(true);
      expect(short.workspaceVersion).toBe(digest);

      // A touched file invalidates the caller's token with -32010.
      fs.writeFileSync(path.join(workspace.dir, 'touched.js'), 'export const v = 1;\n');
      const fresh: any = runtime.observe({ level: 'L0' });
      expect(fresh.workspaceVersion).not.toBe(digest);
      expect(() => runtime.observe({ level: 'L0', ifVersion: digest })).toThrowError(/STALE_RESOURCE_VERSION/);
      try {
        runtime.observe({ level: 'L0', ifVersion: digest });
      } catch (err: any) {
        expect(err.code).toBe(-32010);
        expect(err.data.reason).toBe('stale_version');
        expect(err.data.current).toBe(fresh.workspaceVersion);
      }

      const understood: any = runtime.understand({ query: 'causality' });
      expect(understood.workspaceVersion).toBe(fresh.workspaceVersion);
      const uShort: any = runtime.understand({ query: 'causality', ifVersion: fresh.workspaceVersion });
      expect(uShort.unchanged).toBe(true);
    });
  });

});
