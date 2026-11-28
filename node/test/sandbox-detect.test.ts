import { describe, expect, it } from 'vitest';
import express from 'express';
import Fastify from 'fastify';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import type { AddressInfo } from 'node:net';
import type { Server } from 'node:http';
import { OumvRuntime, mountTokensafe, registerTokensafe } from '../src/index.js';
import { detectSandbox, prismaSandbox } from '../src/sandbox-detect.js';

/**
 * ORM autodetect (the .NET `AddDbContext` discovery is the template): an app that
 * already holds a db hook must get the sandbox without a second wiring line, and
 * an app that holds nothing must not pretend.
 */
describe('DB sandbox autodetect', () => {
  const workspace = () => fs.mkdtempSync(path.join(os.tmpdir(), 'oumv-sandbox-detect-'));

  it('finds an app.locals db hook and reports its source', () => {
    const app: any = express();
    app.locals.db = { run: async (fn: any) => ({ result: await fn(), rolledBack: true, queries: 1 }) };
    const detected = detectSandbox(app);
    expect(detected?.source).toBe('app.locals.db');
    expect(typeof detected?.hook.db?.run).toBe('function');
  });

  it('wraps a Prisma client with a rollback-only transaction', async () => {
    let rolledBack = false;
    const client = {
      $transaction: async (fn: any) => {
        try {
          await fn();
        } catch (error) {
          rolledBack = true;
          throw error;
        }
      },
    };
    const hook = prismaSandbox(client as any);
    const outcome = await hook.db!.run(async () => 42);
    expect(outcome.result).toBe(42);
    expect(outcome.rolledBack).toBe(true);
    expect(rolledBack).toBe(true);
  });

  it('advertises verify.sandboxRollback on Express when the app holds a hook', async () => {
    const dir = workspace();
    const app = express();
    (app as any).locals = { db: { run: async (fn: any) => ({ result: await fn(), rolledBack: true, queries: 1 }) } };
    const adapter = mountTokensafe(app, { workspaceDir: dir });
    const server: Server = app.listen(0, '127.0.0.1');
    await new Promise<void>((resolve) => server.once('listening', resolve));
    try {
      const port = (server.address() as AddressInfo).port;
      const response = await fetch(`http://127.0.0.1:${port}/__oumv/rpc`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ jsonrpc: '2.0', id: 1, method: 'oumv/handshake', params: {} }),
      });
      const payload: any = await response.json();
      expect(payload.result.capabilities.verify.sandboxRollback).toBe(true);
    } finally {
      await adapter.dispose();
      await new Promise<void>((resolve) => server.close(() => resolve()));
      fs.rmSync(dir, { recursive: true, force: true });
    }
  });

  it('adopts a Fastify decoration and stays honest without one', async () => {
    const dir = workspace();
    const decorated = Fastify({ logger: false }) as any;
    decorated.decorate('db', { run: async (fn: any) => ({ result: await fn(), rolledBack: true }) });
    expect(detectSandbox(decorated)?.source).toBe('fastify.decorator:db');
    const adapter = await registerTokensafe(decorated, { workspaceDir: dir });
    const handshake = async (app: any) => {
      const response = await app.inject({
        method: 'POST',
        url: '/__oumv/rpc',
        payload: { jsonrpc: '2.0', id: 1, method: 'oumv/handshake', params: {} },
      });
      return (response.json() as any).result.capabilities.verify.sandboxRollback;
    };
    expect(await handshake(decorated)).toBe(true);
    await adapter.dispose();

    const plain = Fastify({ logger: false });
    const plainAdapter = await registerTokensafe(plain, { workspaceDir: dir });
    expect(await handshake(plain)).toBe(false);
    await plainAdapter.dispose();
    fs.rmSync(dir, { recursive: true, force: true });
  });
});
