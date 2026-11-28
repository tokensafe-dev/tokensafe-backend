import { afterEach, describe, expect, it } from 'vitest';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { BridgeBroker, OumvRuntime, createNextBridgeHandlers, createNextRouteHandlers, installNextBridge, resolveAppDir } from '../src/index.js';

/**
 * Spec §7.4 browser bridge: a server runtime gains the live page through a
 * dev-only, same-origin channel. The page registers for a short-lived token,
 * pushes a snapshot and traces, and answers verify commands addressed to it.
 * Everything below runs without a browser — the scripted client plays the tab.
 */

const snapshot = {
  framework: 'react',
  route: '/',
  title: 'Bridge test app',
  elementCount: 4,
  mountedComponents: [
    { id: 'frontend.component:Counter', name: 'Counter', instances: 1, propsKeys: ['step'], hooksCount: 1 },
  ],
  interactiveSurface: [
    { id: 'btn-inc', tag: 'button', role: 'button', label: 'Increment', action: 'click', component: 'Counter' },
  ],
  contracts: [{ componentId: 'Counter', id: 'counter.inc', constraints: ['step > 0'] }],
  errorsCount: 0,
  errors: [],
  tracesCount: 0,
  timestamp: Date.now(),
};

describe('browser bridge broker', () => {
  it('registers a session, refuses a wrong token, and keeps the token constant-time', () => {
    const broker = new BridgeBroker();
    const registration = broker.register({ url: 'http://localhost:3000/', title: 'tab' });
    expect(registration.sessionId).toMatch(/^bs_/);
    expect(registration.token.length).toBeGreaterThan(20);

    expect(broker.authorize(registration.sessionId, registration.token)).not.toBeNull();
    expect(broker.authorize(registration.sessionId, 'wrong-token-of-equal-len!')).toBeNull();
    expect(broker.authorize('bs_missing', registration.token)).toBeNull();
  });

  it('is connected only while a snapshot and a live stream exist, and expires idle sessions', () => {
    let now = 1_000_000;
    const broker = new BridgeBroker({ now: () => now, connectedWindowMs: 1000, ttlMs: 5000 });
    const registration = broker.register({});
    expect(broker.connected).toBe(false);

    broker.recordMessage(registration.sessionId, { kind: 'snapshot', snapshot });
    expect(broker.connected).toBe(false); // no stream: nobody is listening
    const close = broker.openStream(registration.sessionId, () => {});
    expect(broker.connected).toBe(true);

    now += 1500;
    expect(broker.connected).toBe(false); // stale tab
    now += 5000;
    expect(broker.sessionsInfo()).toHaveLength(0); // idle past the TTL, swept
    close();
  });

  it('records browser traces as runtime traces so L4 and causality see them', () => {
    const runtime = new OumvRuntime({ workspaceDir: process.cwd() });
    const broker = new BridgeBroker({ runtime });
    runtime.setBrowserBridge(broker);
    const registration = broker.register({});
    broker.openStream(registration.sessionId, () => {});
    broker.recordMessage(registration.sessionId, { kind: 'snapshot', snapshot });
    broker.recordMessage(registration.sessionId, {
      kind: 'trace',
      trace: { type: 'verify:click', target: 'btn-inc', route: '/', timestamp: Date.now() },
    });

    const l4 = runtime.observe({ level: 'L4' }) as any;
    expect(l4.data.browserConnected).toBe(true);
    expect(l4.data.traces.some((trace: any) => trace.handler === 'browser:verify:click:btn-inc')).toBe(true);
  });

  it('dispatches a verify into the attached page and maps the reply to §4.5', async () => {
    const runtime = new OumvRuntime({ workspaceDir: process.cwd() });
    const broker = new BridgeBroker({ runtime, commandTimeoutMs: 1000 });
    runtime.setBrowserBridge(broker);
    const registration = broker.register({});
    broker.recordMessage(registration.sessionId, { kind: 'snapshot', snapshot });
    const commands: any[] = [];
    broker.openStream(registration.sessionId, (event, data) => {
      if (event !== 'command') return;
      const command = JSON.parse(data);
      commands.push(command);
      broker.recordMessage(registration.sessionId, {
        kind: 'verify-res',
        requestId: command.requestId,
        status: 'passed',
        target: command.target,
        action: command.action,
        diff: { text: { before: '1', after: '2' } },
        surfaceDiff: [{ op: 'text', target: 'btn-inc', before: '1', after: '2' }],
        durationMs: 6,
        latency: { actionMs: 1, totalMs: 6 },
      });
    });

    const result: any = await runtime.verify({ target: 'btn-inc', action: 'click' });
    expect(commands).toHaveLength(1);
    expect(commands[0]).toMatchObject({ kind: 'verify', target: 'btn-inc', action: 'click' });
    expect(result.status).toBe('passed');
    expect(result.surfaceDiff).toHaveLength(1);
    expect(result.timings.t_total_ms).toBe(6);
    expect(result.scope.notSupported).toEqual([]);
  });

  it('answers -32008 for a DOM target while no page is attached', async () => {
    const runtime = new OumvRuntime({ workspaceDir: process.cwd() });
    const broker = new BridgeBroker({ runtime, commandTimeoutMs: 100 });
    runtime.setBrowserBridge(broker);
    // The page published a snapshot on load, then the tab went away: the id is
    // known to be a DOM target, but nobody is listening any more.
    const registration = broker.register({});
    broker.recordMessage(registration.sessionId, { kind: 'snapshot', snapshot });
    await expect(runtime.verify({ target: 'btn-inc' })).rejects.toThrow(/no browser page is attached/);
  });

  it('does not claim a bridge capability without a bridge', () => {
    const runtime = new OumvRuntime({ workspaceDir: process.cwd() });
    const handshake = runtime.handshake({}) as any;
    expect(handshake.capabilities.mutate.ephemeralDispatch).toBeUndefined();
    expect(handshake.capabilities.verify.surfaceDiff).toBe(false);

    const withBridge = new OumvRuntime({ workspaceDir: process.cwd() });
    withBridge.setBrowserBridge(new BridgeBroker());
    const bridged = withBridge.handshake({}) as any;
    expect(bridged.capabilities.mutate.ephemeralDispatch).toBe(true);
    expect(bridged.capabilities.verify.surfaceDiff).toBe(true);
  });
});

describe('Next bridge route handlers', () => {
  afterEach(() => {
    delete process.env.NODE_ENV;
  });

  it('serves the client module, registers a session and stores a snapshot', async () => {
    const broker = new BridgeBroker();
    const handlers = createNextBridgeHandlers({ broker, enabled: true, clientCode: 'console.log("bridge")', workspaceDir: os.tmpdir() });

    const client = await handlers.GET(new Request('http://localhost:3000/api/tokensafe/bridge?client=1'));
    expect(client.status).toBe(200);
    expect(await client.text()).toContain('bridge');

    const registration = await handlers.POST(
      new Request('http://localhost:3000/api/tokensafe/bridge', {
        method: 'POST',
        body: JSON.stringify({ kind: 'register', url: 'http://localhost:3000/', title: 'tab' }),
      })
    );
    expect(registration.status).toBe(200);
    const info = (await registration.json()) as any;
    expect(info.sessionId).toMatch(/^bs_/);

    const snapshotPost = await handlers.POST(
      new Request(`http://localhost:3000/api/tokensafe/bridge?session=${info.sessionId}&t=${info.token}`, {
        method: 'POST',
        body: JSON.stringify({ kind: 'snapshot', snapshot }),
      })
    );
    expect(snapshotPost.status).toBe(204);
    expect(broker.latestSnapshot()?.route).toBe('/');

    const refused = await handlers.POST(
      new Request(`http://localhost:3000/api/tokensafe/bridge?session=${info.sessionId}&t=nope`, {
        method: 'POST',
        body: JSON.stringify({ kind: 'snapshot', snapshot }),
      })
    );
    expect(refused.status).toBe(401);
  });

  it('streams commands over SSE and answers 404 outside development', async () => {
    const broker = new BridgeBroker();
    const handlers = createNextBridgeHandlers({ broker, enabled: true, workspaceDir: os.tmpdir() });
    const info = broker.register({});
    broker.recordMessage(info.sessionId, { kind: 'snapshot', snapshot });

    const response = await handlers.GET(
      new Request(`http://localhost:3000/api/tokensafe/bridge?session=${info.sessionId}&t=${info.token}`)
    );
    expect(response.status).toBe(200);
    expect(response.headers.get('content-type')).toContain('text/event-stream');
    const reader = response.body!.getReader();
    const first = await reader.read();
    expect(new TextDecoder().decode(first.value)).toContain('event: ready');

    // A command written by the broker must reach this stream.
    const pending = broker.requestVerify({ target: 'btn-inc', action: 'click' }, { sessionId: info.sessionId, timeoutMs: 2000 });
    const second = await reader.read();
    const frame = new TextDecoder().decode(second.value);
    expect(frame).toContain('event: command');
    const command = JSON.parse(frame.split('data: ')[1]);
    broker.recordMessage(info.sessionId, {
      kind: 'verify-res',
      requestId: command.requestId,
      status: 'passed',
      target: 'btn-inc',
      surfaceDiff: [],
      durationMs: 1,
      latency: { actionMs: 1, totalMs: 1 },
    });
    await expect(pending).resolves.toMatchObject({ status: 'passed' });
    await reader.cancel();

    process.env.NODE_ENV = 'production';
    const disabled = createNextBridgeHandlers({ broker, workspaceDir: os.tmpdir() });
    expect((await disabled.GET(new Request('http://localhost:3000/api/tokensafe/bridge?client=1'))).status).toBe(404);
    expect(
      (
        await disabled.POST(
          new Request('http://localhost:3000/api/tokensafe/bridge', {
            method: 'POST',
            body: JSON.stringify({ kind: 'register' }),
          })
        )
      ).status
    ).toBe(404);
  });

  it('mounts the bridge from createNextRouteHandlers and detects src/app', () => {
    const workspace = fs.mkdtempSync(path.join(os.tmpdir(), 'oumv-next-bridge-'));
    try {
      fs.mkdirSync(path.join(workspace, 'src', 'app', 'api', 'health'), { recursive: true });
      fs.writeFileSync(
        path.join(workspace, 'src', 'app', 'api', 'health', 'route.ts'),
        'export async function GET() { return new Response("ok"); }\n',
        'utf-8'
      );
      expect(resolveAppDir(workspace)).toBe(path.join(workspace, 'src', 'app'));

      const handlers = createNextRouteHandlers({ workspaceDir: workspace, bridge: true });
      expect(handlers.BRIDGE).toBeDefined();
      expect(handlers.runtime.browserConnected).toBe(false);
      const handshake = handlers.runtime.handshake({}) as any;
      expect(handshake.capabilities.mutate.ephemeralDispatch).toBe(true);
      expect(handshake.capabilities.verify.surfaceDiff).toBe(true);
    } finally {
      fs.rmSync(workspace, { recursive: true, force: true });
    }
  });
});

describe('installNextBridge', () => {
  it('injects the client module once, and never in production', () => {
    const appended: Array<{ id: string; type: string; src: string }> = [];
    const fakeDocument = {
      getElementById: (id: string) => appended.find((node) => node.id === id) ?? null,
      createElement: (_tag: string) => ({ id: '', type: '', async: false, src: '' }),
      head: { appendChild: (node: any) => appended.push(node) },
    };
    installNextBridge({ document: fakeDocument });
    installNextBridge({ document: fakeDocument });
    expect(appended).toHaveLength(1);
    expect(appended[0].src).toBe('/api/tokensafe/bridge?client=1');

    appended.length = 0;
    const previous = process.env.NODE_ENV;
    process.env.NODE_ENV = 'production';
    try {
      installNextBridge({ document: fakeDocument });
      expect(appended).toHaveLength(0);
    } finally {
      process.env.NODE_ENV = previous;
    }
  });
});
