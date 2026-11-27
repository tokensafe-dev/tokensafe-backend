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
});
