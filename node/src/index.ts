/**
 * OUMV backend adapter for Node.js (Express, Fastify).
 *
 * The runtime is framework-neutral: adapters only mount transports and teach it the
 * route table, so every Node framework answers the same wire shapes (spec §4).
 */
export { OumvRuntime, PROTOCOL_VERSION, OBSERVE_LEVELS } from './runtime.js';
export type {
  OumvRuntimeOptions,
  ObserveLevel,
  RouteInfo,
  RuntimeCapabilities,
  TraceEntry,
  VerifyScope,
} from './runtime.js';
export { OumvError, ERROR_CODES } from './errors.js';
export { SessionRegistry } from './sessions.js';
export { resolveWorkspacePath, splitSourceTarget } from './paths.js';
export { writeDescriptor, deleteDescriptor, descriptorPath } from './descriptor.js';
export type { RuntimeDescriptor } from './descriptor.js';
export { Dispatcher, success, failure, parsePayload, dispatchAll } from './transport/dispatcher.js';
export { createHttpHandler, isAuthorized } from './transport/http.js';
export { createSseHandler } from './transport/sse.js';
export { attachWebSocket } from './transport/ws.js';
export { attachStdio } from './transport/stdio.js';
export type { StdioAttachment, StdioOptions } from './transport/stdio.js';
export { mountTokensafe, tracingMiddleware } from './adapters/express.js';
export type { ExpressAdapter, ExpressAdapterOptions } from './adapters/express.js';
export { registerTokensafe } from './adapters/fastify.js';
export { createNextRouteHandlers, scanAppRouterRoutes } from './adapters/next.js';
export { detectSandbox, prismaSandbox, withDetectedSandbox } from './sandbox-detect.js';
export type { NextAdapterOptions, NextRouteHandlers } from './adapters/next.js';
export type { FastifyAdapter, FastifyAdapterOptions } from './adapters/fastify.js';
