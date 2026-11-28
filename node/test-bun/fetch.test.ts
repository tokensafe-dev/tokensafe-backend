import { afterAll, beforeAll, describe, expect, it } from 'bun:test';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { OumvRuntime } from '../src/runtime.js';
import { createFetchHandlers } from '../src/transport/fetch.js';

const home = mkdtempSync(join(tmpdir(), 'oumv-fetch-home-'));
const workspace = mkdtempSync(join(tmpdir(), 'oumv-fetch-ws-'));
process.env.TOKENSAFE_HOME = home;

const handshake = { jsonrpc: '2.0', id: 1, method: 'oumv/handshake', params: {} };

function runtime(options = {}) {
  return new OumvRuntime({ workspaceDir: workspace, name: 'bun-fetch', version: '0.0.0', ...options });
}

function rpc(handlers, body, headers = {}) {
  return handlers.POST(
    new Request('http://127.0.0.1/__oumv/rpc', {
      method: 'POST',
      headers: { 'content-type': 'application/json', ...headers },
      body: JSON.stringify(body),
    })
  );
}

describe('fetch binding (spec §2.2)', () => {
  it('answers the CORS preflight with 204 and the OUMV headers', () => {
    const handlers = createFetchHandlers({ runtime: runtime() });
    const res = handlers.OPTIONS();
    expect(res.status).toBe(204);
    expect(res.headers.get('access-control-allow-origin')).toBe('*');
    expect(res.headers.get('access-control-allow-headers')).toContain('X-OUMV-Session-Token');
  });

  it('dispatches a single request and answers 200 with the result', async () => {
    const handlers = createFetchHandlers({ runtime: runtime() });
    const res = await rpc(handlers, handshake);
    expect(res.status).toBe(200);
    const payload = (await res.json()) as any;
    expect(payload.result.protocolVersion).toBe('1.6.0');
    expect(typeof payload.result.sessionId).toBe('string');
  });

  it('dispatches a batch in order', async () => {
    const handlers = createFetchHandlers({ runtime: runtime() });
    const res = await rpc(handlers, [handshake, { ...handshake, id: 2 }]);
    const payload = (await res.json()) as any[];
    expect(Array.isArray(payload)).toBe(true);
    expect(payload.map((entry) => entry.id)).toEqual([1, 2]);
  });

  it('keeps 200 for a malformed body and reports -32700', async () => {
    const handlers = createFetchHandlers({ runtime: runtime() });
    const res = await handlers.POST(
      new Request('http://127.0.0.1/__oumv/rpc', { method: 'POST', body: '{not json' })
    );
    expect(res.status).toBe(200);
    const payload = (await res.json()) as any;
    expect(payload.error.code).toBe(-32700);
  });

  it('refuses an oversized body with 413', async () => {
    const handlers = createFetchHandlers({ runtime: runtime(), maxBodyBytes: 8 });
    const res = await rpc(handlers, handshake);
    expect(res.status).toBe(413);
  });

  it('requires the configured token before dispatch', async () => {
    const handlers = createFetchHandlers({ runtime: runtime({ authToken: 'sekret' }) });
    const denied = await rpc(handlers, handshake);
    expect(denied.status).toBe(401);
    expect(((await denied.json()) as any).error.code).toBe(-32007);
    const allowed = await rpc(handlers, handshake, { 'x-oumv-session-token': 'sekret' });
    expect(allowed.status).toBe(200);
  });
});

describe('fetch SSE binding (spec §2.3)', () => {
  it('streams a connected frame, live events, and replays them from Last-Event-ID', async () => {
    const rt = runtime();
    const handlers = createFetchHandlers({ runtime: rt });
    const controller = new AbortController();
    const res = handlers.GET(
      new Request('http://127.0.0.1/__oumv/events', { headers: { 'last-event-id': '0' }, signal: controller.signal })
    );
    expect(res.status).toBe(200);
    expect(res.headers.get('content-type')).toBe('text/event-stream');

    const reader = res.body!.getReader();
    const decoder = new TextDecoder();
    let buffer = '';
    const waitFor = async (needle: string): Promise<void> => {
      while (!buffer.includes(needle)) {
        const chunk = await reader.read();
        if (chunk.done) throw new Error(`stream ended before ${needle}`);
        buffer += decoder.decode(chunk.value, { stream: true });
      }
    };

    await waitFor(': connected');
    rt.recordState('/api/v1/orders', 'v1', 'update');
    await waitFor('event: state');
    await waitFor('data:');
    expect(buffer).toContain('event: state');
    const eventId = Number(/id: (\d+)/.exec(buffer)?.[1]);
    expect(eventId).toBeGreaterThan(0);
    controller.abort();

    // A second subscriber with Last-Event-ID must see the same event replayed.
    const replay = handlers.GET(new Request('http://127.0.0.1/__oumv/events', { headers: { 'last-event-id': '0' } }));
    const replayReader = replay.body!.getReader();
    let replayText = '';
    while (!replayText.includes('event: state')) {
      const chunk = await replayReader.read();
      if (chunk.done) break;
      replayText += decoder.decode(chunk.value, { stream: true });
    }
    expect(replayText).toContain('event: state');
    await replayReader.cancel();
  });
});

afterAll(() => {
  rmSync(home, { recursive: true, force: true });
  rmSync(workspace, { recursive: true, force: true });
});
