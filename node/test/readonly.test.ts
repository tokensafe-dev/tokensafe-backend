import { describe, expect, it } from 'vitest';
import { OumvRuntime } from '../src/runtime.js';
import { createReadOnlyRuntime } from '../src/readonly.js';
import { Dispatcher } from '../src/transport/dispatcher.js';

const ABSENT: Array<[string, Record<string, unknown>]> = [
  ['oumv/applyEdit', { path: 'app.js', replacement: 'x = 1' }],
  ['oumv/previewEdit', { path: 'app.js', replacement: 'x = 1' }],
  ['oumv/verify', { target: 'GET /' }],
  ['oumv/build', { command: 'echo ok' }],
];

describe('read-only deployment (spec §8.5)', () => {
  it('does not register any writing method', async () => {
    const dispatcher = new Dispatcher(createReadOnlyRuntime({ workspaceDir: process.cwd() }));

    for (const [method, params] of ABSENT) {
      const response = await dispatcher.dispatch({ jsonrpc: '2.0', id: 1, method, params });
      expect(response.error?.code, method).toBe(-32601);
    }
  });

  it('answers -32601 before session validation, without leaking that the method exists', async () => {
    const dispatcher = new Dispatcher(createReadOnlyRuntime({ workspaceDir: process.cwd() }));

    const response = await dispatcher.dispatch({
      jsonrpc: '2.0',
      id: 1,
      method: 'oumv/applyEdit',
      params: { path: 'app.js', replacement: 'x = 1', sessionId: 'sess-unknown' },
    });

    expect(response.error?.code).toBe(-32601);
  });

  it('keeps the read surface registered', async () => {
    const dispatcher = new Dispatcher(createReadOnlyRuntime({ workspaceDir: process.cwd() }));

    const observe = await dispatcher.dispatch({
      jsonrpc: '2.0',
      id: 1,
      method: 'oumv/observe',
      params: { level: 'L0' },
    });
    expect(observe.error).toBeUndefined();

    // A registered getSource answers RESOURCE_NOT_FOUND for a missing file; only an
    // absent method answers -32601.
    const source = await dispatcher.dispatch({
      jsonrpc: '2.0',
      id: 2,
      method: 'oumv/getSource',
      params: { path: 'definitely-not-here.js' },
    });
    expect(source.error?.code).toBe(-32009);
  });

  it('advertises readOnly and drops mutate/verify from the handshake', () => {
    const runtime = createReadOnlyRuntime({ workspaceDir: process.cwd() });
    const handshake: any = runtime.handshake({ protocolVersion: '1.6.0' });

    expect(handshake.capabilities.readOnly).toBe(true);
    expect(handshake.capabilities.mutate).toBeUndefined();
    expect(handshake.capabilities.verify).toBeUndefined();
    expect(handshake.capabilities.observe.levels).toHaveLength(5);
  });

  it('leaves the default deployment writable', async () => {
    const runtime = new OumvRuntime({ workspaceDir: process.cwd() });
    const handshake: any = runtime.handshake({});

    expect(handshake.capabilities.readOnly).toBeUndefined();
    expect(handshake.capabilities.mutate.occ).toBe(true);

    const dispatcher = new Dispatcher(runtime);
    const response = await dispatcher.dispatch({
      jsonrpc: '2.0',
      id: 1,
      method: 'oumv/applyEdit',
      params: { path: 'missing.js', replacement: 'x = 1', expectedVersion: 'sha256:' + '0'.repeat(64) },
    });
    expect(response.error?.code).toBe(-32009);
  });

  it('gates build on the verify capability', async () => {
    const verifyless = new OumvRuntime({ workspaceDir: process.cwd(), capabilities: { verify: false } });
    const dispatcher = new Dispatcher(verifyless);

    const response = await dispatcher.dispatch({ jsonrpc: '2.0', id: 1, method: 'oumv/build', params: { command: 'echo ok' } });
    expect(response.error?.code).toBe(-32601);
  });
});
