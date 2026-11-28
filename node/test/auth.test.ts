import { afterEach, describe, expect, it } from 'vitest';
import express from 'express';
import type { AddressInfo } from 'node:net';
import type { Server } from 'node:http';
import { OumvRuntime, mountTokensafe } from '../src/index.js';

/**
 * Spec §12 transport auth: a configured token is never optional (no fail-open),
 * and SSE follows the same rule as the RPC POST.
 */
describe('transport auth', () => {
  let cleanup: (() => Promise<void>) | undefined;
  afterEach(async () => {
    await cleanup?.();
    cleanup = undefined;
  });

  async function start(options: { authToken?: string; requireAuth?: boolean }) {
    const app = express();
    app.use(express.json());
    const runtime = new OumvRuntime({ workspaceDir: process.cwd(), authToken: options.authToken });
    const adapter = mountTokensafe(app, { runtime, requireAuth: options.requireAuth });
    const server: Server = app.listen(0, '127.0.0.1');
    await new Promise<void>((resolve) => server.once('listening', resolve));
    const port = (server.address() as AddressInfo).port;
    cleanup = async () => {
      await adapter.dispose();
      await new Promise<void>((resolve) => server.close(() => resolve()));
    };
    return {
      rpcUrl: `http://127.0.0.1:${port}/__oumv/rpc`,
      eventsUrl: `http://127.0.0.1:${port}/__oumv/events`,
    };
  }

  const observe = (headers: Record<string, string> = {}, url: string) =>
    fetch(url, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', ...headers },
      body: JSON.stringify({ jsonrpc: '2.0', id: 1, method: 'oumv/observe', params: { level: 'L0' } }),
    });

  it('stays open on loopback when no token is configured', async () => {
    const live = await start({});
    expect((await observe({}, live.rpcUrl)).status).toBe(200);
  });

  it('requires a configured token even without requireAuth (fail closed)', async () => {
    const live = await start({ authToken: 'secret-token' });

    const missing = await observe({}, live.rpcUrl);
    expect(missing.status).toBe(401);
    expect(((await missing.json()) as any).error.code).toBe(-32007);

    const invalid = await observe({ 'X-OUMV-Session-Token': 'nope' }, live.rpcUrl);
    expect(invalid.status).toBe(401);

    const wrongScheme = await observe({ Authorization: 'Basic c2VjcmV0LXRva2Vu' }, live.rpcUrl);
    expect(wrongScheme.status).toBe(401);
  });

  it('dispatches the request that carries the token', async () => {
    const live = await start({ authToken: 'secret-token' });
    expect((await observe({ 'X-OUMV-Session-Token': 'secret-token' }, live.rpcUrl)).status).toBe(200);
    expect((await observe({ Authorization: 'Bearer secret-token' }, live.rpcUrl)).status).toBe(200);
  });

  it('refuses the SSE stream without a credential', async () => {
    const live = await start({ authToken: 'secret-token' });
    const refused = await fetch(live.eventsUrl);
    expect(refused.status).toBe(401);
    expect(((await refused.json()) as any).error.code).toBe(-32007);
  });

  it('requireAuth without a token refuses everything (no credential to compare)', async () => {
    const live = await start({ requireAuth: true });
    expect((await observe({}, live.rpcUrl)).status).toBe(401);
  });
});
