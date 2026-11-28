import type { IncomingMessage, ServerResponse } from 'node:http';
import { ERROR_CODES } from '../errors.js';
import type { OumvRuntime } from '../runtime.js';
import { Dispatcher, dispatchAll, failure, parsePayload, type JsonRpcRequest } from './dispatcher.js';

export interface HttpHandlerOptions {
  runtime: OumvRuntime;
  authToken?: string;
  requireAuth?: boolean;
  maxBodyBytes?: number;
}

const LOOPBACK = new Set(['127.0.0.1', '::1', '::ffff:127.0.0.1', 'localhost']);

/** True when the request carries the expected `X-OUMV-Session-Token`/Bearer token. */
export function isAuthorized(req: IncomingMessage, token: string, requireAuth: boolean): boolean {
  const provided = String(
    req.headers['x-oumv-session-token'] ??
      (typeof req.headers.authorization === 'string' && req.headers.authorization.toLowerCase().startsWith('bearer ')
        ? req.headers.authorization.slice(7).trim()
        : '') ??
      ''
  );
  // A configured token is always required (spec §12): accepting an empty
  // credential because `requireAuth` was not passed is the classic fail-open.
  if (token) return Boolean(provided) && provided === token;
  return !requireAuth;
}

/**
 * HTTP binding (spec §2.2): POST /__oumv/rpc, single object or batch, 200 for every
 * well-formed request including JSON-RPC errors; 4xx only for transport failures.
 */
export function createHttpHandler(options: HttpHandlerOptions) {
  const runtime = options.runtime;
  const dispatcher = new Dispatcher(runtime);
  const token = options.authToken ?? runtime.authToken ?? '';
  const maxBodyBytes = options.maxBodyBytes ?? runtime.maxBodyBytes;

  return async function handle(req: IncomingMessage, res: ServerResponse): Promise<boolean> {
    // Express strips the mount prefix from req.url; originalUrl keeps it.
    const pathname = String((req as any).originalUrl ?? req.url ?? '').split('?')[0];
    if (pathname !== runtime.transportPath) return false;

    res.setHeader('Access-Control-Allow-Origin', '*');
    res.setHeader('Access-Control-Allow-Headers', 'Content-Type, Authorization, X-OUMV-Session-Token');
    res.setHeader('Access-Control-Allow-Methods', 'POST, OPTIONS');

    if (req.method === 'OPTIONS') {
      res.statusCode = 204;
      res.end();
      return true;
    }
    if (req.method !== 'POST') {
      res.statusCode = 405;
      res.setHeader('Content-Type', 'application/json');
      res.end(JSON.stringify({ error: 'Method not allowed' }));
      return true;
    }
    if (!isAuthorized(req, token, Boolean(options.requireAuth))) {
      res.statusCode = 401;
      res.setHeader('Content-Type', 'application/json');
      res.end(JSON.stringify(failure(null, ERROR_CODES.PERMISSION_DENIED, 'PERMISSION_DENIED: invalid OUMV session token')));
      return true;
    }

    // Express/Fastify body parsers drain the stream before middleware sees it:
    // honour an already-parsed body, otherwise read ours (limit enforced there).
    const preParsed = (req as any).body;
    const chunks: Buffer[] = [];
    let size = 0;
    if (preParsed === undefined) {
      for await (const chunk of req) {
        const buffer = Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk);
        size += buffer.length;
        if (size > maxBodyBytes) {
          res.statusCode = 413;
          res.setHeader('Content-Type', 'application/json');
          res.end(JSON.stringify(failure(null, ERROR_CODES.INVALID_REQUEST, 'INVALID_REQUEST: body too large')));
          return true;
        }
        chunks.push(buffer);
      }
    }

    res.setHeader('Content-Type', 'application/json');
    try {
      const payload =
        preParsed !== undefined && typeof preParsed === 'object'
          ? (preParsed as JsonRpcRequest | JsonRpcRequest[])
          : parsePayload(Buffer.concat(chunks).toString('utf-8'));
      const response = await dispatchAll(dispatcher, payload);
      res.statusCode = 200;
      res.end(JSON.stringify(response));
    } catch (error: any) {
      res.statusCode = 200;
      res.end(
        JSON.stringify(
          failure(null, error?.code ?? ERROR_CODES.INTERNAL_ERROR, error?.message ?? 'INTERNAL_ERROR: unreadable body')
        )
      );
    }
    return true;
  };
}

export function isLoopbackRequest(req: IncomingMessage): boolean {
  return LOOPBACK.has(String(req.socket?.remoteAddress ?? ''));
}
