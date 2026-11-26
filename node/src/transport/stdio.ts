import readline from 'node:readline';
import { failure, parsePayload, Dispatcher, type JsonRpcRequest, type JsonRpcResponse } from './dispatcher.js';
import type { OumvRuntime } from '../runtime.js';

export interface StdioOptions {
  input?: NodeJS.ReadableStream;
  output?: NodeJS.WritableStream;
}

export interface StdioAttachment {
  close: () => void;
}

const isNotification = (request: JsonRpcRequest) =>
  Boolean(request) && typeof request === 'object' && request.id === undefined;

/**
 * §2.5 stdio binding: newline-delimited JSON-RPC, one message per line.
 *
 * stdout carries responses only (diagnostics belong on stderr), notifications get
 * no reply, and lines are processed in order so a handshake is never overtaken by
 * the observe that follows it. Batching is optional on stdio but accepted.
 */
export function attachStdio(runtime: OumvRuntime, options: StdioOptions = {}): StdioAttachment {
  const dispatcher = new Dispatcher(runtime);
  const output = options.output ?? process.stdout;
  const input = options.input ?? process.stdin;

  const write = (body: unknown) => {
    if (body !== undefined && body !== null) output.write(JSON.stringify(body) + '\n');
  };

  const handleLine = async (line: string) => {
    let payload: JsonRpcRequest | JsonRpcRequest[];
    try {
      payload = parsePayload(line);
    } catch (error: any) {
      write(failure(null, error?.code ?? -32700, error?.message ?? 'Parse error'));
      return;
    }

    if (Array.isArray(payload)) {
      const responses: JsonRpcResponse[] = [];
      for (const request of payload) {
        const response = await dispatcher.dispatch(request);
        if (!isNotification(request)) responses.push(response);
      }
      if (responses.length > 0) write(responses);
      return;
    }

    if (isNotification(payload)) {
      await dispatcher.dispatch(payload);
      return;
    }
    write(await dispatcher.dispatch(payload));
  };

  const rl = readline.createInterface({ input: input as any, crlfDelay: Infinity });
  let queue: Promise<void> = Promise.resolve();
  rl.on('line', (line) => {
    const trimmed = line.trim();
    if (!trimmed) return;
    queue = queue.then(() => handleLine(trimmed)).catch((error: any) => {
      process.stderr.write(`tokensafe-oumv-stdio: ${error?.stack ?? error}\n`);
    });
  });

  return { close: () => rl.close() };
}
