import crypto from 'node:crypto';
import { ERROR_CODES, OumvError } from '../errors.js';
import type { OumvRuntime } from '../runtime.js';
import type {
  BrowserBridge,
  BrowserSnapshot,
  BrowserTrace,
  BrowserVerifyOutcome,
  BrowserVerifyRequest,
} from './types.js';

/**
 * BridgeBroker (spec §7.4).
 *
 * A dev-only, same-origin channel between a live page and the runtime. The page
 * registers for a short-lived session token, pushes snapshots and traces over
 * POST, and receives commands over an SSE stream. Commands are addressed to ONE
 * session, so two open tabs never steal each other's verify.
 *
 * Trust model: every message is untrusted. The token is checked on every call,
 * idle sessions expire, stored traces are capped, and the only thing a page can
 * make the runtime do is dispatch a synthetic event into itself.
 */

export interface BridgeSessionInfo {
  sessionId: string;
  url: string;
  title: string;
  route?: string;
  connected: boolean;
  lastSeen: number;
}

interface PendingCommand {
  resolve: (outcome: BrowserVerifyOutcome) => void;
  reject: (error: Error) => void;
  timer: NodeJS.Timeout;
}

type StreamSink = (event: string, data: string) => void;

interface BridgeSession {
  sessionId: string;
  token: string;
  url: string;
  title: string;
  createdAt: number;
  lastSeen: number;
  snapshot?: BrowserSnapshot;
  pending: Map<string, PendingCommand>;
  streams: Set<StreamSink>;
}

export interface BridgeBrokerOptions {
  /** Sessions idle for longer than this are dropped (default 5 min). */
  ttlMs?: number;
  /** A page is "connected" only if it spoke within this window (default 60 s). */
  connectedWindowMs?: number;
  /** How long a dispatched command may take before -32008 (default 10 s). */
  commandTimeoutMs?: number;
  /** Browser traces forwarded to the runtime per session (default 64). */
  maxTraces?: number;
  now?: () => number;
}

export interface BridgeRegistration {
  sessionId: string;
  token: string;
  heartbeatMs: number;
}

export class BridgeBroker implements BrowserBridge {
  private readonly sessions = new Map<string, BridgeSession>();
  private readonly ttlMs: number;
  private readonly connectedWindowMs: number;
  private readonly commandTimeoutMs: number;
  private readonly maxTraces: number;
  private readonly now: () => number;
  private runtime?: OumvRuntime;
  private commandSeq = 0;

  constructor(options: BridgeBrokerOptions & { runtime?: OumvRuntime } = {}) {
    this.ttlMs = options.ttlMs ?? 5 * 60_000;
    this.connectedWindowMs = options.connectedWindowMs ?? 60_000;
    this.commandTimeoutMs = options.commandTimeoutMs ?? 10_000;
    this.maxTraces = options.maxTraces ?? 64;
    this.now = options.now ?? Date.now;
    this.runtime = options.runtime;
  }

  /** A route module compiled later can hand the broker its runtime for tracing. */
  attachRuntime(runtime: OumvRuntime): void {
    if (!this.runtime) this.runtime = runtime;
  }

  /** Drops expired sessions before any read, so a dead tab is never "connected". */
  private sweep(): void {
    const cutoff = this.now() - this.ttlMs;
    for (const [id, session] of this.sessions) {
      if (session.lastSeen >= cutoff) continue;
      for (const pending of session.pending.values()) {
        clearTimeout(pending.timer);
        pending.reject(new OumvError(ERROR_CODES.RUNTIME_UNAVAILABLE, 'RUNTIME_UNAVAILABLE: the browser session expired before answering'));
      }
      session.pending.clear();
      this.sessions.delete(id);
    }
  }

  private isLive(session: BridgeSession): boolean {
    return this.now() - session.lastSeen <= this.connectedWindowMs;
  }

  register(info: { url?: string; title?: string } = {}): BridgeRegistration {
    this.sweep();
    const sessionId = 'bs_' + crypto.randomBytes(9).toString('base64url');
    const token = crypto.randomBytes(24).toString('base64url');
    const session: BridgeSession = {
      sessionId,
      token,
      url: typeof info.url === 'string' ? info.url.slice(0, 2048) : '',
      title: typeof info.title === 'string' ? info.title.slice(0, 256) : '',
      createdAt: this.now(),
      lastSeen: this.now(),
      pending: new Map(),
      streams: new Set(),
    };
    this.sessions.set(sessionId, session);
    return { sessionId, token, heartbeatMs: 15_000 };
  }

  /** Constant-time token check; an unknown session answers null, never a new one. */
  authorize(sessionId: string, token: string): BridgeSession | null {
    this.sweep();
    const session = this.sessions.get(String(sessionId ?? ''));
    if (!session) return null;
    const expected = Buffer.from(session.token, 'utf-8');
    const provided = Buffer.from(String(token ?? ''), 'utf-8');
    if (expected.length !== provided.length) return null;
    if (!crypto.timingSafeEqual(expected, provided)) return null;
    return session;
  }

  /** Registers an SSE sink; returns the unsubscribe function. */
  openStream(sessionId: string, sink: StreamSink): () => void {
    const session = this.sessions.get(sessionId);
    if (!session) return () => {};
    session.streams.add(sink);
    session.lastSeen = this.now();
    return () => {
      session.streams.delete(sink);
    };
  }

  streamCount(sessionId: string): number {
    return this.sessions.get(sessionId)?.streams.size ?? 0;
  }

  /** Applies one POSTed message from a page (the caller already authorized it). */
  recordMessage(sessionId: string, message: Record<string, unknown>): void {
    const session = this.sessions.get(sessionId);
    if (!session) return;
    session.lastSeen = this.now();
    const kind = String(message.kind ?? '');
    switch (kind) {
      case 'snapshot':
        if (message.snapshot && typeof message.snapshot === 'object') {
          session.snapshot = message.snapshot as BrowserSnapshot;
        }
        break;
      case 'trace':
        if (message.trace && typeof message.trace === 'object') {
          this.recordTrace(session, message.trace as BrowserTrace);
        }
        break;
      case 'observe-res':
      case 'verify-res': {
        const requestId = String(message.requestId ?? '');
        const pending = session.pending.get(requestId);
        if (!pending) return;
        session.pending.delete(requestId);
        clearTimeout(pending.timer);
        if (message.snapshot && typeof message.snapshot === 'object') {
          session.snapshot = message.snapshot as BrowserSnapshot;
        }
        pending.resolve(message as unknown as BrowserVerifyOutcome);
        break;
      }
      case 'heartbeat':
        break;
      default:
        break;
    }
  }

  private recordTrace(session: BridgeSession, trace: BrowserTrace): void {
    if (!this.runtime) return;
    // Browser traces become runtime traces so L4 and causality see them, but the
    // mapping is explicit: method BROWSER, handler = event type (+ target). No
    // HTTP status or duration is invented — absent fields stay absent.
    const handler = 'browser:' + String(trace.type ?? 'event') + (trace.target ? ':' + String(trace.target) : '');
    this.runtime.recordTrace({
      method: 'BROWSER',
      path: String(trace.route ?? session.snapshot?.route ?? '/'),
      route: String(trace.route ?? session.snapshot?.route ?? '/'),
      handler,
      statusCode: typeof trace.status === 'number' ? trace.status : 0,
      durationMs: typeof trace.durationMs === 'number' ? trace.durationMs : 0,
    });
  }

  get connected(): boolean {
    this.sweep();
    for (const session of this.sessions.values()) {
      if (session.snapshot && session.streams.size > 0 && this.isLive(session)) return true;
    }
    return false;
  }

  sessionsInfo(): BridgeSessionInfo[] {
    this.sweep();
    return Array.from(this.sessions.values()).map((session) => ({
      sessionId: session.sessionId,
      url: session.url,
      title: session.title,
      ...(session.snapshot?.route ? { route: session.snapshot.route } : {}),
      connected: Boolean(session.snapshot) && session.streams.size > 0 && this.isLive(session),
      lastSeen: session.lastSeen,
    }));
  }

  latestSnapshot(): BrowserSnapshot | null {
    this.sweep();
    let latest: BridgeSession | undefined;
    for (const session of this.sessions.values()) {
      if (!session.snapshot || !this.isLive(session)) continue;
      if (!latest || session.lastSeen > latest.lastSeen) latest = session;
    }
    return latest?.snapshot ?? null;
  }

  hasSurfaceId(id: string): boolean {
    const snapshot = this.latestSnapshot();
    if (!snapshot) return false;
    return (snapshot.interactiveSurface ?? []).some((item) => item.id === id);
  }

  /** Dispatches a verify into the freshest attached page and awaits its reply. */
  async requestVerify(
    request: BrowserVerifyRequest,
    options: { timeoutMs?: number; sessionId?: string } = {}
  ): Promise<BrowserVerifyOutcome> {
    this.sweep();
    let session: BridgeSession | undefined;
    if (options.sessionId) {
      session = this.sessions.get(options.sessionId);
    } else {
      for (const candidate of this.sessions.values()) {
        if (!candidate.snapshot || candidate.streams.size === 0 || !this.isLive(candidate)) continue;
        if (!session || candidate.lastSeen > session.lastSeen) session = candidate;
      }
    }
    if (!session || session.streams.size === 0) {
      throw new OumvError(
        ERROR_CODES.RUNTIME_UNAVAILABLE,
        'RUNTIME_UNAVAILABLE: no browser page is attached — open the app with the OUMV bridge enabled before verifying a DOM target'
      );
    }

    const requestId = `br_${++this.commandSeq}_${crypto.randomBytes(4).toString('hex')}`;
    const command = JSON.stringify({
      requestId,
      kind: 'verify',
      target: request.target,
      action: request.action ?? 'click',
      ...(request.payload !== undefined ? { payload: request.payload } : {}),
    });
    const timeoutMs = options.timeoutMs ?? this.commandTimeoutMs;

    const outcome = await new Promise<BrowserVerifyOutcome>((resolve, reject) => {
      const timer = setTimeout(() => {
        session?.pending.delete(requestId);
        reject(
          new OumvError(
            ERROR_CODES.RUNTIME_UNAVAILABLE,
            `RUNTIME_UNAVAILABLE: the attached page did not answer '${request.target}' within ${timeoutMs} ms`
          )
        );
      }, timeoutMs);
      session!.pending.set(requestId, { resolve, reject, timer });
      let delivered = 0;
      for (const sink of session!.streams) {
        try {
          sink('command', command);
          delivered += 1;
        } catch {
          session!.streams.delete(sink);
        }
      }
      if (delivered === 0) {
        session!.pending.delete(requestId);
        clearTimeout(timer);
        reject(
          new OumvError(
            ERROR_CODES.RUNTIME_UNAVAILABLE,
            'RUNTIME_UNAVAILABLE: the attached page is not reading its command stream'
          )
        );
      }
    });

    if (outcome.status !== 'passed' && !outcome.errorCode) outcome.errorCode = ERROR_CODES.ACTION_NOT_FOUND;
    return outcome;
  }

  /** Closes every stream (adapter shutdown). Sessions are left to expire. */
  close(): void {
    for (const session of this.sessions.values()) {
      for (const pending of session.pending.values()) {
        clearTimeout(pending.timer);
        pending.reject(new OumvError(ERROR_CODES.RUNTIME_UNAVAILABLE, 'RUNTIME_UNAVAILABLE: the bridge is shutting down'));
      }
      session.pending.clear();
      session.streams.clear();
    }
  }
}

const GLOBAL_BROKERS = Symbol.for('tokensafe.oumv.bridgeBrokers');

type BrokerRegistry = Map<string, BridgeBroker>;

function registry(): BrokerRegistry {
  const holder = globalThis as unknown as Record<symbol, BrokerRegistry | undefined>;
  if (!holder[GLOBAL_BROKERS]) holder[GLOBAL_BROKERS] = new Map();
  return holder[GLOBAL_BROKERS]!;
}

/**
 * One broker per workspace per process. Next compiles each route module into its
 * own bundle, so a module-level singleton would give the RPC route and the bridge
 * route two different brokers — and the runtime would be blind to the page.
 */
export function getOrCreateBridgeBroker(workspaceDir: string, runtime?: OumvRuntime): BridgeBroker {
  const key = String(workspaceDir);
  const existing = registry().get(key);
  if (existing) {
    if (runtime) existing.attachRuntime(runtime);
    return existing;
  }
  const broker = new BridgeBroker(runtime ? { runtime } : {});
  registry().set(key, broker);
  return broker;
}

export function peekBridgeBroker(workspaceDir: string): BridgeBroker | undefined {
  return registry().get(String(workspaceDir));
}
