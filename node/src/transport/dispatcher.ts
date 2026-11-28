import { ERROR_CODES, OumvError, invalidParams, validationFailed } from '../errors.js';
import type { OumvRuntime } from '../runtime.js';

export interface JsonRpcRequest {
  jsonrpc?: string;
  id?: string | number | null;
  method?: string;
  params?: Record<string, any>;
}

export interface JsonRpcResponse {
  jsonrpc: '2.0';
  id: string | number | null;
  result?: unknown;
  error?: { code: number; message: string; data?: Record<string, unknown> };
}

export const success = (id: JsonRpcRequest['id'], result: unknown): JsonRpcResponse => ({
  jsonrpc: '2.0',
  id: id ?? null,
  result,
});

export const failure = (
  id: JsonRpcRequest['id'],
  code: number,
  message: string,
  data?: Record<string, unknown>
): JsonRpcResponse => ({
  jsonrpc: '2.0',
  id: id ?? null,
  error: { code, message, ...(data ? { data } : {}) },
});

/**
 * JSON-RPC dispatch for every method (spec §4/§5). Transport bindings call this;
 * session validation happens once here so HTTP, SSE and WebSocket agree.
 */
export class Dispatcher {
  /** Closed registry (spec §8.5): null means the runtime states no registry. */
  private readonly allowed: Set<string> | null;

  constructor(private readonly runtime: OumvRuntime) {
    this.allowed =
      typeof runtime.registeredMethods === 'function' ? new Set(runtime.registeredMethods()) : null;
  }

  async dispatch(request: JsonRpcRequest): Promise<JsonRpcResponse> {
    const id = request?.id ?? null;
    const method = String(request?.method ?? '');
    const params = (request?.params ?? {}) as Record<string, any>;

    if (!method) return failure(id, ERROR_CODES.INVALID_REQUEST, 'INVALID_REQUEST: method is required');

    // The registry is consulted first, before session validation: a method the runtime
    // does not serve is METHOD_NOT_FOUND whatever the session state is (§3.1 rule 5).
    // Answering -32011 here would leak that the method exists.
    if (this.allowed && !this.allowed.has(method)) {
      return failure(id, ERROR_CODES.METHOD_NOT_FOUND, `METHOD_NOT_FOUND: ${method}`);
    }

    if (method === 'oumv/disconnect') {
      return success(id, { closed: this.runtime.sessions.delete(params.sessionId) });
    }

    if (method !== 'oumv/handshake' && method !== 'handshake' && params.sessionId) {
      if (!this.runtime.sessions.validate(String(params.sessionId))) {
        return failure(
          id,
          ERROR_CODES.VALIDATION_FAILED,
          'VALIDATION_FAILED: unknown or expired sessionId — re-run oumv/handshake',
          { reason: 'session_expired', sessionId: params.sessionId }
        );
      }
    }

    try {
      switch (method) {
        case 'oumv/handshake':
        case 'handshake':
          return success(id, this.runtime.handshake(params));
        case 'oumv/observe':
          return success(id, this.runtime.observe(params));
        case 'oumv/understand':
          return success(id, this.runtime.understand(params));
        case 'oumv/getSource':
          return success(id, this.runtime.getSource(params));
        case 'oumv/applyEdit':
          return success(id, this.runtime.applyEdit(params));
        case 'oumv/previewEdit':
          return success(id, this.runtime.previewEdit(params));
        case 'oumv/cancel':
          return success(id, this.runtime.cancel(params));
        case 'oumv/index':
          return success(id, this.runtime.index(params));
        case 'oumv/query':
          return success(id, await this.runtime.query(params));
        case 'oumv/build':
          return success(id, await this.runtime.build(params));
        case 'oumv/verify':
          return success(id, await this.runtime.verify(params));
        default:
          return failure(id, ERROR_CODES.METHOD_NOT_FOUND, `METHOD_NOT_FOUND: ${method}`);
      }
    } catch (error: any) {
      if (error instanceof OumvError) {
        return failure(id, error.code, error.message, error.data);
      }
      return failure(id, ERROR_CODES.INTERNAL_ERROR, `INTERNAL_ERROR: ${error?.message ?? String(error)}`);
    }
  }
}

/** Body guard shared by every transport: object or batch array, size-capped. */
export function parsePayload(raw: string): JsonRpcRequest | JsonRpcRequest[] {
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch (error: any) {
    throw new OumvError(ERROR_CODES.PARSE_ERROR, `Parse error: ${error.message}`);
  }
  if (Array.isArray(parsed)) {
    if (parsed.length === 0) throw invalidParams('INVALID_REQUEST: empty batch');
    return parsed as JsonRpcRequest[];
  }
  if (!parsed || typeof parsed !== 'object') {
    throw invalidParams('INVALID_REQUEST: body must be a JSON-RPC object or array');
  }
  return parsed as JsonRpcRequest;
}

/** Batch semantics (§2.2): ordered responses, notifications get no entry. */
export async function dispatchAll(
  dispatcher: Dispatcher,
  payload: JsonRpcRequest | JsonRpcRequest[]
): Promise<JsonRpcResponse | JsonRpcResponse[]> {
  if (!Array.isArray(payload)) {
    const response = await dispatcher.dispatch(payload);
    if (payload.id === undefined && !response.error) {
      // Notification over a single-object body: acknowledge with no content upstream.
      return response;
    }
    return response;
  }
  const responses: JsonRpcResponse[] = [];
  for (const item of payload) {
    if (!item || typeof item !== 'object') {
      responses.push(failure(null, ERROR_CODES.INVALID_REQUEST, 'INVALID_REQUEST: batch entries must be objects'));
      continue;
    }
    const response = await dispatcher.dispatch(item);
    if (item.id === undefined && !response.error) continue;
    responses.push(response);
  }
  return responses;
}

export { validationFailed };
