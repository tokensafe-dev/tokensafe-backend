import type { FastifyInstance, FastifyReply, FastifyRequest } from 'fastify';
import { deleteDescriptor, writeDescriptor } from '../descriptor.js';
import { OumvRuntime, type OumvRuntimeOptions } from '../runtime.js';
import { createHttpHandler } from '../transport/http.js';
import { createSseHandler } from '../transport/sse.js';
import { withDetectedSandbox } from '../sandbox-detect.js';
import { attachWebSocket, type WsAttachment } from '../transport/ws.js';

export interface FastifyAdapterOptions extends OumvRuntimeOptions {
  /** Reuse an existing runtime (one instance per app, mounted before/after routes). */
  runtime?: OumvRuntime;
  publicUrl?: string;
  requireAuth?: boolean;
  descriptor?: boolean;
}

export interface FastifyAdapter {
  runtime: OumvRuntime;
  attachWebSocket: () => WsAttachment;
  dispose: () => Promise<void>;
}

/**
 * Registers the OUMV runtime on a Fastify instance. Routes are captured with the
 * `onRoute` hook (no private APIs), and verification dispatches through Fastify's
 * own request pipeline.
 */
export async function registerTokensafe(
  fastify: FastifyInstance,
  options: FastifyAdapterOptions = {}
): Promise<FastifyAdapter> {
  const runtime =
    options.runtime ??
    new OumvRuntime({
      // Explicit sandbox wiring wins; otherwise adopt a db/prisma decoration.
      ...withDetectedSandbox(fastify, options),
      workspaceDir: options.workspaceDir ?? process.cwd(),
    });

  const eventsPath = runtime.transportPath.replace(/\/rpc$/, '/events');
  fastify.addHook('onRoute', (route) => {
    // Never advertise the adapter's own endpoints as application surface.
    if (route.url === runtime.transportPath || route.url === eventsPath) return;
    const methods = Array.isArray(route.method) ? route.method : [route.method];
    for (const method of methods) {
      if (String(method).toUpperCase() === 'HEAD') continue; // Fastify adds HEAD for GET
      runtime.registerRoute(String(method), route.url, (route.handler as any)?.name);
    }
  });

  runtime.setHandler((req, res) => {
    (fastify as any).routing(req, res);
  });

  const httpHandler = createHttpHandler({ runtime, authToken: options.authToken, requireAuth: options.requireAuth });
  const sseHandler = createSseHandler({ runtime, authToken: options.authToken, requireAuth: options.requireAuth });

  const bridge = (handler: (req: any, res: any) => Promise<boolean>) => async (request: FastifyRequest, reply: FastifyReply) => {
    // Fastify's JSON parser already consumed the stream; hand the parsed body to
    // the transport, which honours it instead of re-reading (spec §2.2 batch too).
    if (request.body !== undefined) (request.raw as any).body = request.body;
    const handled = await handler(request.raw, reply.raw);
    if (!handled) {
      reply.callNotFound();
      return reply;
    }
    return reply;
  };

  fastify.route({
    method: ['POST', 'OPTIONS'],
    url: runtime.transportPath,
    handler: bridge(httpHandler),
  });
  fastify.route({
    method: 'GET',
    url: eventsPath,
    handler: bridge(sseHandler),
  });

  runtime.setTransports(['http', 'sse']);

  if (options.publicUrl && options.descriptor !== false) {
    writeDescriptor(runtime.workspaceDir, {
      type: 'backend',
      name: runtime.name,
      version: runtime.version,
      url: options.publicUrl,
      ...(options.authToken ? { sessionToken: options.authToken } : {}),
      workspace: runtime.workspaceDir,
      capabilities: { observeLevels: ['L0', 'L1', 'L2', 'L3', 'L4'], transports: ['http', 'sse'] },
      startedAt: new Date().toISOString(),
    });
  }

  return {
    runtime,
    attachWebSocket: () => {
      const attachment = attachWebSocket(fastify.server, {
        runtime,
        authToken: options.authToken,
        requireAuth: options.requireAuth,
      });
      runtime.setTransports(['http', 'sse', 'ws']);
      return attachment;
    },
    dispose: async () => {
      await runtime.dispose();
      deleteDescriptor(runtime.workspaceDir);
    },
  };
}
