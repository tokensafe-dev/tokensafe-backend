import { PassThrough } from 'node:stream';
import { afterEach, describe, expect, it } from 'vitest';
import { OumvRuntime, attachStdio } from '../src/index.js';
import { createWorkspace, type Workspace } from './helpers/workspace.js';

const waitFor = async (predicate: () => boolean, timeout = 2000) => {
  const started = Date.now();
  while (!predicate()) {
    if (Date.now() - started > timeout) throw new Error('timed out waiting for stdio output');
    await new Promise((resolve) => setTimeout(resolve, 10));
  }
};

describe('stdio transport (§2.5)', () => {
  let workspace: Workspace | undefined;

  afterEach(() => {
    workspace?.cleanup();
    workspace = undefined;
  });

  it('answers requests in order, skips notifications and reports parse errors', async () => {
    workspace = createWorkspace();
    const runtime = new OumvRuntime({ workspaceDir: workspace.dir });
    runtime.setTransports(['stdio']);
    const input = new PassThrough();
    const output = new PassThrough();
    const lines: any[] = [];
    output.on('data', (chunk) => {
      for (const line of String(chunk).split('\n').filter(Boolean)) lines.push(JSON.parse(line));
    });
    const attachment = attachStdio(runtime, { input, output });

    try {
      input.write(JSON.stringify({ jsonrpc: '2.0', id: 1, method: 'oumv/handshake', params: {} }) + '\n');
      await waitFor(() => lines.length === 1);
      expect(lines[0].result.profile).toBe('backend');
      const sessionId = lines[0].result.sessionId;

      input.write(
        JSON.stringify({ jsonrpc: '2.0', id: 2, method: 'oumv/observe', params: { level: 'L0', sessionId } }) +
          '\n'
      );
      await waitFor(() => lines.length === 2);
      expect(lines[1].result.level).toBe('L0');

      // A notification gets no frame; the following parse error must be reply #3.
      input.write(JSON.stringify({ jsonrpc: '2.0', method: 'oumv/observe', params: { level: 'L0' } }) + '\n');
      input.write('{not json}\n');
      await waitFor(() => lines.length === 3);
      expect(lines[2].error.code).toBe(-32700);

      await new Promise((resolve) => setTimeout(resolve, 50));
      expect(lines).toHaveLength(3);
    } finally {
      attachment.close();
      await runtime.dispose();
    }
  });

  it('accepts a batch line and drops notification entries', async () => {
    workspace = createWorkspace();
    const runtime = new OumvRuntime({ workspaceDir: workspace.dir });
    runtime.setTransports(['stdio']);
    const input = new PassThrough();
    const output = new PassThrough();
    const lines: any[] = [];
    output.on('data', (chunk) => {
      for (const line of String(chunk).split('\n').filter(Boolean)) lines.push(JSON.parse(line));
    });
    const attachment = attachStdio(runtime, { input, output });

    try {
      input.write(
        JSON.stringify([
          { jsonrpc: '2.0', id: 1, method: 'oumv/handshake', params: {} },
          { jsonrpc: '2.0', method: 'oumv/observe', params: { level: 'L0' } },
          { jsonrpc: '2.0', id: 2, method: 'oumv/observe', params: { level: 'L0' } },
        ]) + '\n'
      );
      await waitFor(() => lines.length === 1);
      expect(Array.isArray(lines[0])).toBe(true);
      expect(lines[0]).toHaveLength(2);
      expect(lines[0][0].id).toBe(1);
      expect(lines[0][1].result.level).toBe('L0');
    } finally {
      attachment.close();
      await runtime.dispose();
    }
  });
});
