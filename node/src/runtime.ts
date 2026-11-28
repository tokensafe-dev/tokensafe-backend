import crypto from 'node:crypto';
import fs from 'node:fs';
import http from 'node:http';
import path from 'node:path';
import { parse as parseJs } from '@babel/parser';
import {
  ERROR_CODES,
  OumvError,
  conflict,
  invalidParams,
  permissionDenied,
  resourceNotFound,
  syntaxError,
  targetNotFound,
  validationFailed,
  unsupported,
} from './errors.js';
import { resolveWorkspacePath, splitSourceTarget } from './paths.js';
import { SessionRegistry } from './sessions.js';
import type { BrowserBridge, BrowserSnapshot } from './browser/types.js';

export const PROTOCOL_VERSION = '1.6.0';
export const OBSERVE_LEVELS = ['L0', 'L1', 'L2', 'L3', 'L4'] as const;
export type ObserveLevel = (typeof OBSERVE_LEVELS)[number];

export interface RouteInfo {
  method: string;
  path: string;
  handlerName?: string;
  middlewares?: string[];
}

export interface TraceEntry {
  method: string;
  path: string;
  route: string;
  handler: string;
  statusCode: number;
  durationMs: number;
  timestamp: string;
  error?: string;
}

export interface RuntimeCapabilities {
  /** §8.5 read-only deployment: the writing methods are absent and cannot be bypassed. */
  readOnly?: boolean;
  observe: { levels: string[]; formats: string[] };
  understand: { causality: boolean; signalsGraph: boolean; impactAnalysis: boolean };
  mutate: { occ: boolean; syntaxGuard: boolean; multiFile?: boolean; preview?: boolean; ephemeralDispatch?: boolean };
  verify: { multiMetric: boolean; surfaceDiff: boolean; sandboxRollback?: boolean; build?: boolean };
  /** §3 session lifecycle (wire 1.5.0/1.6.0). */
  sessions?: { ttlSeconds?: boolean; cancel?: boolean; idempotency?: boolean };
  /** Bindings this runtime actually mounted (spec §2.3/§2.4). */
  transports?: string[];
  /** Wire 1.6.0 optional extensions (§14) + push diagnostics (§2.6). */
  index?: { symbols?: boolean };
  observeQuery?: { readOnly?: boolean };
  events?: { diagnostic?: boolean };
}

/**
 * Application-provided database sandbox hook (spec §4). The runtime has no ORM of
 * its own: the adopter supplies `run`, which executes the verify request inside a
 * transaction it always rolls back and reports whether that rollback happened.
 */
export interface VerifySandboxHook {
  db?: {
    run<T>(fn: () => Promise<T> | T): Promise<{ result: T; rolledBack: boolean; queries?: number }>;
    /**
     * §14.2 read-only data access (wire 1.6.0). The adopter supplies the query;
     * the runtime additionally enforces the SELECT/EXPLAIN/PRAGMA/WITH prefix,
     * but the connection itself should be read-only too.
     */
    query?(sql: string, params?: unknown[], limit?: number): Promise<{ columns: string[]; rows: unknown[][] }>;
  };
}

export interface OumvRuntimeOptions {
  name?: string;
  version?: string;
  workspaceDir?: string;
  authToken?: string;
  sessionTtlSeconds?: number;
  /** Application handler used by `oumv/verify` (Express app, Fastify server, plain fn). */
  handler?: http.RequestListener;
  capabilities?: Partial<{
    observe: boolean;
    understand: boolean;
    mutate: boolean;
    verify: boolean;
  }>;
  transportPath?: string;
  maxBodyBytes?: number;
  /** Optional database sandbox; advertises verify.sandboxRollback when present. */
  sandbox?: VerifySandboxHook;
  /** §14.3 default build command (wire 1.6.0). */
  buildCommand?: string;
  /** §14.2 workspace policy: `query: false` turns oumv/query into -32007 (reason query_disabled). */
  permissions?: { query?: boolean };
}

export interface VerifyScope {
  checked: string[];
  notExecuted: string[];
  notSupported: string[];
}

const MAX_TRACES = 128;

function sha256(content: string): string {
  return 'sha256:' + crypto.createHash('sha256').update(content, 'utf-8').digest('hex');
}

function intersect(implemented: string[], requested?: unknown): string[] {
  if (!Array.isArray(requested) || requested.length === 0) return implemented;
  const wanted = new Set(requested.map(String));
  return implemented.filter((item) => wanted.has(item));
}

/**
 * Vendor-neutral OUMV runtime for Node backends. Adapters (Express, Fastify) only
 * mount transports and teach it the route table; every pillar lives here so all
 * Node frameworks answer identically.
 */
export interface SourcePayload extends Record<string, unknown> {
  path: string;
  symbol?: string;
  source?: string;
  unchanged?: boolean;
  version: string;
  startLine?: number;
  endLine?: number;
}

/** §4.1 limit/cursor: an absent limit returns the whole collection. */
function paginate<T>(items: T[], params: Record<string, any>): { page: T[]; hasMore?: boolean; nextCursor?: string } {
  const rawLimit = Number(params?.limit);
  const limit = Number.isFinite(rawLimit) && rawLimit > 0 ? Math.floor(rawLimit) : 0;
  let start = Number.parseInt(String(params?.cursor ?? ''), 10);
  if (!Number.isFinite(start) || start < 0) start = 0;
  if (start > items.length) start = items.length;
  if (limit && start + limit < items.length) {
    return { page: items.slice(start, start + limit), hasMore: true, nextCursor: String(start + limit) };
  }
  return { page: items.slice(start), ...(limit ? { hasMore: false } : {}) };
}

/** Replaces a 1-based inclusive line range, preserving the rest byte for byte. */
function replaceLines(source: string, startLine: number, endLine: number, replacement: string): string {
  const lines = source.split('\n');
  const before = lines.slice(0, startLine - 1);
  const after = lines.slice(endLine);
  const middle = replacement.split('\n');
  if (replacement.endsWith('\n')) middle.pop();
  let patched = [...before, ...middle, ...after].join('\n');
  if (source.endsWith('\n') && !patched.endsWith('\n')) patched += '\n';
  return patched;
}

/** One server-pushed event on SSE/WS (spec §2.3/§2.4); ids are monotonic. */
export interface StreamEvent {
  id: number;
  type: 'trace' | 'state' | 'diagnostic';
  timestamp: string;
  data: Record<string, unknown>;
}

/** Bounded replay log behind `Last-Event-ID`; a mutation never blocks on it. */
const MAX_STREAM_EVENTS = 256;

export class OumvRuntime {
  readonly name: string;
  readonly version: string;
  readonly workspaceDir: string;
  readonly transportPath: string;
  readonly maxBodyBytes: number;
  readonly sessions: SessionRegistry;
  authToken: string;

  private readonly routes = new Map<string, RouteInfo>();
  private readonly traces: TraceEntry[] = [];
  private readonly events: StreamEvent[] = [];
  private readonly eventSubscribers = new Set<(event: StreamEvent) => void>();
  private eventSeq = 0;
  private readonly startTime = Date.now();
  private readonly enabled: { observe: boolean; understand: boolean; mutate: boolean; verify: boolean };
  /** §14.2 workspace policy knobs (wire 1.6.0). */
  private readonly permissions: { query?: boolean };
  private transports: string[] = ['http'];
  private readonly sandbox?: VerifySandboxHook;
  /** §14.3 default build command; `params.command` overrides per call. */
  buildCommand?: string;
  /** §3 idempotency replay map (wire 1.6.0). */
  private readonly idempotency: Map<string, Record<string, unknown>>;
  private handler?: http.RequestListener;
  private dispatchServer?: http.Server;
  private dispatchPort = 0;
  /** §7.4 browser bridge: gives a server runtime the live page (Next instrumentation client). */
  private browserBridge?: BrowserBridge;

  constructor(options: OumvRuntimeOptions = {}) {
    this.name = options.name || 'tokensafe-node';
    this.version = options.version || '0.13.0';
    // Canonical root: `resolveWorkspacePath` realpaths the target, so a symlinked
    // root (macOS /var → /private/var) would otherwise leak `../../..` into every
    // reported `path`/`backupPath`.
    const requestedRoot = path.resolve(options.workspaceDir || process.cwd());
    try {
      this.workspaceDir = fs.realpathSync(requestedRoot);
    } catch {
      this.workspaceDir = requestedRoot;
    }
    this.transportPath = options.transportPath || '/__oumv/rpc';
    this.maxBodyBytes = options.maxBodyBytes ?? 8 * 1024 * 1024;
    this.authToken = options.authToken || process.env.TOKENSAFE_OUMV_AUTH_TOKEN || '';
    this.sandbox = options.sandbox;
    // §14.3 configured build command; a params.command override is honoured per call.
    this.buildCommand = options.buildCommand;
    // §3 idempotency (wire 1.6.0): session-scoped key -> first final result.
    this.idempotency = new Map<string, Record<string, unknown>>();
    this.sessions = new SessionRegistry(options.sessionTtlSeconds ?? 3600);
    this.handler = options.handler;
    // Spec §8.5: the universal read-only switch. Go, the FastAPI autoload and this
    // runtime all read the same variable, so one deployment setting means the same
    // thing on every stack: no writing method is registered.
    const envReadOnly = /^(1|true|yes|on)$/i.test(process.env.TOKENSAFE_OUMV_READ_ONLY ?? '');
    this.enabled = {
      observe: options.capabilities?.observe ?? true,
      understand: options.capabilities?.understand ?? true,
      mutate: options.capabilities?.mutate ?? !envReadOnly,
      verify: options.capabilities?.verify ?? !envReadOnly,
    };
    this.permissions = options.permissions ?? {};
  }

  /* ---------------------------------------------------------------- registry */

  /**
   * The closed dispatch registry (spec §3.1 rule 5 / §8.5): a capability that is off
   * contributes no method, so the dispatcher answers -32601 before session validation
   * and no handler runs. `readOnly` is exactly "no mutating method is in this list".
   */
  registeredMethods(): string[] {
    const methods = [
      'oumv/handshake',
      'handshake',
      'oumv/disconnect',
      'oumv/cancel',
      'oumv/index',
      'oumv/query',
    ];
    if (this.enabled.observe) methods.push('oumv/observe');
    if (this.enabled.understand) methods.push('oumv/understand');
    // Source read is an observation capability: a read-only agent must be able to
    // inspect code, so getSource survives either pillar being on.
    if (this.enabled.observe || this.enabled.mutate) methods.push('oumv/getSource');
    if (this.enabled.mutate) methods.push('oumv/applyEdit', 'oumv/previewEdit');
    // Build executes a command, so it belongs to the writing surface.
    if (this.enabled.verify) methods.push('oumv/verify', 'oumv/build');
    return methods;
  }

  registerRoute(method: string, routePath: string, handlerName = 'handler', middlewares: string[] = []): void {
    // Paths are case-sensitive (`/api/orders` !== `/API/ORDERS`); only the method is normalised.
    const key = `${method.toUpperCase()} ${routePath}`;
    if (this.routes.has(key)) return;
    this.routes.set(key, {
      method: method.toUpperCase(),
      path: routePath,
      handlerName,
      ...(middlewares.length > 0 ? { middlewares } : {}),
    });
  }

  listRoutes(): RouteInfo[] {
    return Array.from(this.routes.values());
  }

  /** Records an executed request: the only source of L4/causality evidence. */
  recordTrace(entry: Omit<TraceEntry, 'timestamp'> & { timestamp?: string }): void {
    const trace: TraceEntry = { ...entry, timestamp: entry.timestamp ?? new Date().toISOString() };
    this.traces.push(trace);
    if (this.traces.length > MAX_TRACES) this.traces.shift();
    this.publishEvent('trace', trace as unknown as Record<string, unknown>);
  }

  recentTraces(): TraceEntry[] {
    return this.traces.slice();
  }

  /** Publishes a successful mutation to streaming clients (spec §2.3 `state`). */
  recordState(resourcePath: string, version: string, operation: string): void {
    this.publishEvent('state', { path: resourcePath, version, operation });
  }

  /** Events with an id greater than `lastId` (0 = everything still in the log). */
  eventsSince(lastId: number): StreamEvent[] {
    return this.events.filter((event) => event.id > lastId);
  }

  /** Live event feed for the WebSocket binding; returns the unsubscribe function. */
  subscribeEvents(subscriber: (event: StreamEvent) => void): () => void {
    this.eventSubscribers.add(subscriber);
    return () => {
      this.eventSubscribers.delete(subscriber);
    };
  }

  private publishEvent(type: StreamEvent['type'], data: Record<string, unknown>): void {
    const event: StreamEvent = {
      id: ++this.eventSeq,
      type,
      timestamp: new Date().toISOString(),
      data,
    };
    this.events.push(event);
    if (this.events.length > MAX_STREAM_EVENTS) this.events.shift();
    for (const subscriber of this.eventSubscribers) {
      try {
        subscriber(event);
      } catch {
        // A broken subscriber must never fail the mutation that published it.
      }
    }
  }

  setHandler(handler: http.RequestListener): void {
    this.handler = handler;
  }

  /**
   * Attaches a browser bridge (spec §7.4). While one is set, handshake advertises
   * `mutate.ephemeralDispatch` / `verify.surfaceDiff`, observe merges the page's
   * snapshot, and verify dispatches DOM targets into the attached tab.
   */
  setBrowserBridge(bridge: BrowserBridge | undefined): void {
    this.browserBridge = bridge;
  }

  /** True while a page is attached and has published a snapshot. */
  get browserConnected(): boolean {
    return Boolean(this.browserBridge?.connected);
  }

  setTransports(transports: string[]): void {
    this.transports = transports;
  }

  /* --------------------------------------------------------------- handshake */

  handshake(params: Record<string, any> = {}): Record<string, unknown> {
    // §11: serve our own MINOR and every earlier one; another MAJOR is -32006.
    const requestedVersion = String(params.protocolVersion ?? '').trim();
    if (requestedVersion) {
      const [majorRaw, minorRaw] = requestedVersion.split('.');
      const major = Number(majorRaw);
      const minor = Number(minorRaw);
      if (!Number.isInteger(major) || !Number.isInteger(minor) || major !== 1 || minor > 6) {
        throw unsupported(
          `UNSUPPORTED_CAPABILITY: protocol version ${requestedVersion} (this runtime serves ${PROTOCOL_VERSION})`,
        );
      }
    }
    const capabilities: RuntimeCapabilities = {
      observe: { levels: [...OBSERVE_LEVELS], formats: ['json'] },
      understand: { causality: true, signalsGraph: false, impactAnalysis: true },
      mutate: { occ: true, syntaxGuard: true, multiFile: true, preview: true },
      verify: {
        multiMetric: true,
        surfaceDiff: false,
        sandboxRollback: Boolean(this.sandbox?.db),
        build: true,
      },
      index: { symbols: true },
      observeQuery: { readOnly: Boolean(this.sandbox?.db?.query) },
      sessions: { ttlSeconds: true, cancel: true, idempotency: true },
      transports: [...this.transports],
    };
    if (!this.enabled.understand) {
      capabilities.understand = { causality: false, signalsGraph: false, impactAnalysis: false };
    }
    // §7.4: both bridge capabilities exist only while a page can answer. A runtime
    // without a bridge keeps the exact capability set it had before.
    if (this.browserBridge) {
      if (this.enabled.mutate) capabilities.mutate.ephemeralDispatch = true;
      if (this.enabled.verify) capabilities.verify.surfaceDiff = true;
    }
    // §8.5 (revision v1.11): a capability that is off is absent, not a block of
    // falses, and the methods that serve it are not in the registry. When both
    // writing pillars are off the deployment is read-only and says so explicitly.
    const advertised = capabilities as unknown as Record<string, unknown>;
    if (!this.enabled.mutate) delete advertised.mutate;
    if (!this.enabled.verify) delete advertised.verify;
    // §3 idempotency replays applyEdit/previewEdit results: with the mutating
    // methods absent it has nothing to replay, and advertising it would make the
    // capability case call a method that answers -32601.
    if (!this.enabled.mutate && capabilities.sessions) capabilities.sessions.idempotency = false;
    if (!this.enabled.mutate && !this.enabled.verify) advertised.readOnly = true;

    // §3.1: narrow to the client's request; an empty intersection stays empty.
    const requested = params.requestedCapabilities;
    if (requested && typeof requested === 'object') {
      capabilities.observe.levels = intersect(capabilities.observe.levels, requested.observe?.levels);
      capabilities.observe.formats = intersect(capabilities.observe.formats, requested.observe?.formats);
    }
    if (!this.enabled.observe) capabilities.observe = { levels: [], formats: [] };

    // Wire 1.5.0: honour a client-requested lifetime and echo what was granted.
    const requestedTtl = Number(params.sessionTtlSeconds);
    const session =
      Number.isFinite(requestedTtl) && requestedTtl > 0
        ? this.sessions.createWithTtl(requestedTtl)
        : { id: this.sessions.create(), ttlSeconds: this.sessions.ttlSeconds };

    return {
      protocolVersion: PROTOCOL_VERSION,
      profile: 'backend',
      sessionId: session.id,
      ttlSeconds: session.ttlSeconds,
      runtimeInfo: { name: this.name, version: this.version },
      capabilities,
    };
  }

  /* ----------------------------------------------------------------- observe */

  /**
   * §8 result versioning (wire 1.6.0): a freshness token over the workspace
   * inventory (relative path, size, mtime). Any touched file changes it, so an
   * agent can learn exactly when foreign edits made a cached answer stale.
   */
  workspaceDigest(): string {
    const entries: string[] = [];
    const SKIP = new Set(['.git', 'node_modules', '.venv', 'venv', '__pycache__', '.tokensafe', '.oumv', 'dist', 'build', '.pytest_cache', 'coverage', '.next']);
    const walk = (dir: string): void => {
      for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
        if (entry.isDirectory()) {
          if (!SKIP.has(entry.name)) walk(path.join(dir, entry.name));
          continue;
        }
        let stat: fs.Stats;
        try {
          stat = fs.statSync(path.join(dir, entry.name));
        } catch {
          continue;
        }
        const rel = path.relative(this.workspaceDir, path.join(dir, entry.name)).split(path.sep).join('/');
        entries.push(`${rel}:${stat.size}:${stat.mtimeMs}`);
      }
    };
    try {
      walk(this.workspaceDir);
    } catch {
      // An unreadable workspace still yields a stable (short) digest.
    }
    entries.sort();
    return sha256(entries.join('\n'));
  }

  /** observe with the §8 ifVersion freshness handshake (wire 1.6.0). */
  observe(params: Record<string, any> = {}): Record<string, unknown> {
    const digest = this.workspaceDigest();
    const token = String(params?.ifVersion ?? '').trim();
    if (token) {
      if (token !== digest) {
        throw new OumvError(ERROR_CODES.STALE_RESOURCE_VERSION, `STALE_RESOURCE_VERSION: the workspace changed since the caller's snapshot (expected ${token}, current is ${digest}) — refetch without ifVersion`, {
          reason: 'stale_version',
          current: digest,
          expected: token,
        });
      }
      return { level: String(params?.level ?? 'L0').toUpperCase(), format: 'json', unchanged: true, workspaceVersion: digest };
    }
    const result = this.observeUncached(params) as Record<string, unknown>;
    result.workspaceVersion = digest;
    return result;
  }

  /** understand with the §8 ifVersion freshness handshake (wire 1.6.0). */
  understand(params: Record<string, any> = {}): Record<string, unknown> {
    const digest = this.workspaceDigest();
    const token = String(params?.ifVersion ?? '').trim();
    if (token) {
      if (token !== digest) {
        throw new OumvError(ERROR_CODES.STALE_RESOURCE_VERSION, `STALE_RESOURCE_VERSION: the workspace changed since the caller's snapshot (expected ${token}, current is ${digest}) — refetch without ifVersion`, {
          reason: 'stale_version',
          current: digest,
          expected: token,
        });
      }
      return { query: String(params?.query ?? 'causality'), unchanged: true, workspaceVersion: digest };
    }
    const result = this.understandUncached(params) as Record<string, unknown>;
    result.workspaceVersion = digest;
    return result;
  }

  private observeUncached(params: Record<string, any> = {}): Record<string, unknown> {
    if (!this.enabled.observe) throw permissionDenied('PERMISSION_DENIED: observe capability is disabled');
    const level = String(params.level || 'L0').toUpperCase() as ObserveLevel;
    const format = String(params.format || 'json').toLowerCase();
    if (!OBSERVE_LEVELS.includes(level)) throw unsupported(`UNSUPPORTED_CAPABILITY: unknown observe level ${level}`);
    if (format !== 'json') throw unsupported(`UNSUPPORTED_CAPABILITY: format ${format} is not implemented`);

    switch (level) {
      case 'L0': {
        const memory = process.memoryUsage().rss / (1024 * 1024);
        const snapshot = this.browserBridge?.latestSnapshot() ?? null;
        return {
          level,
          format: 'json',
          data: {
            status: 'HEALTHY',
            uptimeSeconds: Math.floor((Date.now() - this.startTime) / 1000),
            activeRoutes: this.routes.size,
            errorCount1m: 0,
            memoryAllocMB: Math.round(memory),
            // §7.4: page data rides on L0 only while a page is attached, and it is
            // runtime evidence — never a static guess about what the app renders.
            ...(this.browserBridge
              ? {
                  browserConnected: Boolean(snapshot),
                  ...(snapshot
                    ? {
                        route: snapshot.route ?? '/',
                        mountedComponents: (snapshot.mountedComponents ?? [])
                          .map((component) => component.name)
                          .filter((name): name is string => Boolean(name))
                          .slice(0, 15),
                        mountedCount: (snapshot.mountedComponents ?? []).length,
                        elements: snapshot.elementCount ?? 0,
                        interactiveCount: (snapshot.interactiveSurface ?? []).length,
                        errorsCount: snapshot.errorsCount ?? 0,
                      }
                    : {}),
                }
              : {}),
          },
          tokenEstimate: 30,
        };
      }

      case 'L1': {
        const paged = paginate(this.listRoutes(), params);
        const snapshot = this.browserBridge?.latestSnapshot() ?? null;
        const filter = String(params.target ?? '').trim().toLowerCase();
        const surface = (snapshot?.interactiveSurface ?? []).filter(
          (item) =>
            !filter ||
            item.id.toLowerCase().includes(filter) ||
            (item.label ?? '').toLowerCase().includes(filter) ||
            (item.component ?? '').toLowerCase().includes(filter)
        );
        const itemLimit = Number.isFinite(Number(params.limit)) && Number(params.limit) > 0 ? Math.min(Number(params.limit), 100) : 50;
        return {
          level,
          format: 'json',
          data: {
            endpoints: paged.page,
            ...(paged.hasMore !== undefined ? { hasMore: paged.hasMore } : {}),
            ...(paged.nextCursor ? { nextCursor: paged.nextCursor } : {}),
            ...(this.browserBridge
              ? {
                  browserConnected: Boolean(snapshot),
                  ...(snapshot
                    ? {
                        elements: surface.slice(0, itemLimit).map((item) => ({
                          id: item.id,
                          tag: item.tag,
                          ...(item.role ? { role: item.role } : {}),
                          ...(item.label ? { label: item.label } : {}),
                          ...(item.action ? { action: item.action } : {}),
                          ...(item.component ? { component: item.component } : {}),
                        })),
                        elementCount: surface.length,
                      }
                    : {}),
                }
              : {}),
          },
          tokenEstimate: 30 + paged.page.length * 15 + surface.length * 10,
        };
      }

      case 'L2': {
        const target = String(params.target || '').trim();
        if (!target) throw invalidParams('INVALID_PARAMS: target is required for L2');
        const snapshot = this.browserBridge?.latestSnapshot() ?? null;
        if (snapshot) {
          const key = target.replace(/^frontend\.component:/, '').replace(/^component:/, '');
          const component = (snapshot.mountedComponents ?? []).find(
            (item) => item.id === target || item.id === `frontend.component:${key}` || item.name === key
          );
          if (component) {
            return {
              level,
              format: 'json',
              data: {
                target,
                componentId: component.name ?? key,
                propsKeys: component.propsKeys ?? [],
                hooksCount: component.hooksCount ?? null,
                instances: component.instances ?? 1,
                ...(component.source ? { source: component.source } : {}),
                contracts: (snapshot.contracts ?? []).filter(
                  (contract) =>
                    typeof contract === 'object' &&
                    contract !== null &&
                    String((contract as Record<string, unknown>).componentId ?? '') === (component.name ?? key)
                ),
                evidence: { provenance: 'runtime', sourceAdapter: this.name, confidence: 'confirmed' },
              },
              tokenEstimate: 60,
            };
          }
        }
        const route = this.findRoute(target);
        if (!route) throw resourceNotFound(`RESOURCE_NOT_FOUND: no route matches ${JSON.stringify(target)}`);
        return {
          level,
          format: 'json',
          data: {
            target,
            method: route.method,
            path: route.path,
            handlerName: route.handlerName,
            middlewares: route.middlewares ?? [],
            // A mounted Express/Fastify router exposes handler names, not DTOs:
            // saying so is honest, inventing schemas is not.
            note: 'route contract from live router registration; request/response DTOs are not reflectable — use observe L3 for handler source',
            evidence: { provenance: 'runtime', sourceAdapter: this.name, confidence: 'confirmed' },
          },
          tokenEstimate: 80,
        };
      }

      case 'L3': {
        const target = String(params.target || '').trim();
        if (!target) throw invalidParams('INVALID_PARAMS: target (path or path#symbol) is required for L3');
        const { path: filePath, symbol } = splitSourceTarget(target);
        const source = this.readSource(filePath, symbol);
        return {
          level,
          format: 'json',
          data: source,
          tokenEstimate: Math.ceil((source.source ?? '').length / 4),
        };
      }

      case 'L4': {
        const target = String(params.target || '').trim();
        const traces = target ? this.traces.filter((trace) => matchesTarget(trace, target)) : this.recentTraces();
        const paged = paginate(traces, params);
        return {
          level,
          format: 'json',
          data: {
            ...(target ? { target } : {}),
            ...(this.browserBridge ? { browserConnected: Boolean(this.browserBridge.latestSnapshot()) } : {}),
            traces: paged.page,
            traceCount: traces.length,
            ...(paged.hasMore !== undefined ? { hasMore: paged.hasMore } : {}),
            ...(paged.nextCursor ? { nextCursor: paged.nextCursor } : {}),
            ...(traces.length === 0
              ? { note: 'no request has been executed for this target yet — verify or instrument the app first' }
              : {}),
            evidence: {
              provenance: 'runtime',
              sourceAdapter: this.name,
              confidence: traces.length > 0 ? 'confirmed' : 'unknown',
            },
          },
          tokenEstimate: 40 + traces.length * 12,
        };
      }
    }
  }

  /* -------------------------------------------------------------- understand */

  private understandUncached(params: Record<string, any> = {}): Record<string, unknown> {
    if (!this.enabled.understand) throw permissionDenied('PERMISSION_DENIED: understand capability is disabled');
    const query = String(params.query || 'causality');
    const target = String(params.target || '').trim();

    if (query === 'impact') {
      if (!target) throw invalidParams('INVALID_PARAMS: target is required for impact');
      const route = this.findRoute(target);
      const affected = route?.handlerName ? [route.handlerName] : [];
      return {
        query: 'impact',
        target,
        impact: {
          target,
          affectedSymbols: affected,
          breakingRisk: route ? 'LOW' : 'UNKNOWN',
          summary: route
            ? `impact derived from the registered route table for ${route.method} ${route.path}`
            : 'no matching route; impact cannot be derived from registration data',
          observed: false,
          evidence: {
            provenance: 'static',
            sourceAdapter: this.name,
            confidence: route ? 'probable' : 'unknown',
          },
        },
      };
    }

    if (query === 'causality' || query === 'why_state') {
      const matching = target ? this.traces.filter((trace) => matchesTarget(trace, target)) : this.recentTraces();
      const newest = matching[matching.length - 1];
      if (!newest) {
        return {
          query: 'causality',
          target,
          causality: {
            target,
            observed: false,
            traceCount: 0,
            traceChain: [],
            rootCause: '',
            note: 'no execution trace for this target yet — run oumv/verify or serve a request first',
            evidence: { provenance: 'static', sourceAdapter: this.name, confidence: 'unknown' },
          },
        };
      }
      return {
        query: 'causality',
        target,
        causality: {
          target,
          observed: true,
          traceCount: matching.length,
          traceChain: matching.slice(-5).map((trace) => ({
            method: trace.method,
            path: trace.path,
            statusCode: trace.statusCode,
            durationMs: trace.durationMs,
            timestamp: trace.timestamp,
          })),
          statusCode: newest.statusCode,
          latencyMs: newest.durationMs,
          rootCause: `${newest.method} ${newest.path} -> ${newest.statusCode}`,
          evidence: { provenance: 'runtime', sourceAdapter: this.name, confidence: 'confirmed' },
        },
      };
    }

    throw unsupported(`UNSUPPORTED_CAPABILITY: understand query ${query}`);
  }

  /* ------------------------------------------------------------ source & edits */

  getSource(params: Record<string, any> = {}): Record<string, unknown> {
    const filePath = String(params.path ?? params.filePath ?? '').trim();
    if (!filePath) throw invalidParams('INVALID_PARAMS: path is required');
    return this.readSource(
      filePath,
      params.symbol ? String(params.symbol) : undefined,
      typeof params.startLine === 'number' ? params.startLine : undefined,
      typeof params.endLine === 'number' ? params.endLine : undefined,
      params.ifVersion ? String(params.ifVersion) : undefined,
    );
  }

  private readSource(
    filePath: string,
    symbol?: string,
    startLine?: number,
    endLine?: number,
    ifVersion?: string,
  ): SourcePayload {
    const resolved = resolveWorkspacePath(filePath, this.workspaceDir);
    if (!fs.existsSync(resolved) || !fs.statSync(resolved).isFile()) {
      throw resourceNotFound(`RESOURCE_NOT_FOUND: ${filePath}`);
    }
    const full = fs.readFileSync(resolved, 'utf-8');
    const relative = path.relative(this.workspaceDir, resolved).split(path.sep).join('/');
    const version = sha256(full);

    // Wire 1.3.0: the dedup pointer answers without resending bytes; version is
    // always the whole file's hash, so OCC never depends on the window.
    if (ifVersion && ifVersion === version) {
      return { path: relative, version, unchanged: true, ...(symbol ? { symbol } : {}) };
    }

    if (!symbol) {
      const all = full.split('\n');
      const start = Math.max(1, startLine ?? 1);
      let end = endLine ?? all.length;
      end = Math.min(Math.max(end, start), all.length);
      return {
        path: relative,
        source: all.slice(start - 1, end).join('\n'),
        version,
        startLine: start,
        endLine: end,
      };
    }
    const range = extractSymbolRange(full, symbol);
    if (!range) throw resourceNotFound(`RESOURCE_NOT_FOUND: symbol ${JSON.stringify(symbol)} not found in ${relative}`);
    return {
      path: relative,
      symbol,
      source: range.source,
      version,
      startLine: range.startLine,
      endLine: range.endLine,
    };
  }

  applyEdit(params: Record<string, any> = {}): Record<string, unknown> {
    // §3 idempotency (wire 1.6.0): a retry with the same key replays the first result.
    const idemKey = this.idempotencyKey(params);
    if (idemKey && this.idempotency.has(idemKey)) {
      return this.idempotency.get(idemKey) as Record<string, unknown>;
    }
    const result = this.applyEditUncached(params);
    if (idemKey && result && result.status === 'APPLIED') {
      this.idempotency.set(idemKey, result);
    }
    return result;
  }

  private idempotencyKey(params: Record<string, any>): string | undefined {
    const key = params.idempotencyKey;
    if (!key) return undefined;
    return `${String(params.sessionId ?? 'default')}:${String(key)}`;
  }

  /** §3 best-effort cancellation: nothing here is observably in-flight (§3 contract). */
  cancel(params: Record<string, any> = {}): Record<string, unknown> {
    if (params.target === 'last') return { cancelled: false, finished: true };
    return { cancelled: false, requestId: params.requestId };
  }

  /** §4.4 dry-run: applyEdit's guard order, no bytes written, diff optional. */
  previewEdit(params: Record<string, any> = {}): Record<string, unknown> {
    if (!this.enabled.mutate) throw permissionDenied('PERMISSION_DENIED: mutate capability is disabled');
    const idemKey = this.idempotencyKey(params);
    if (idemKey && this.idempotency.has(idemKey)) {
      return this.idempotency.get(idemKey) as Record<string, unknown>;
    }
    const items: Array<Record<string, any>> = Array.isArray(params.edits) && params.edits.length > 0
      ? params.edits
      : [params];
    if (items.length > 16) {
      throw validationFailed('VALIDATION_FAILED: batch must hold 1..16 items', { reason: 'batch_size' });
    }
    const previews = items.map((item) => this.previewOne(item));
    const result: Record<string, unknown> = { status: 'PREVIEW', applied: false, previews };
    if (idemKey) this.idempotency.set(idemKey, result);
    return result;
  }

  private previewOne(item: Record<string, any>): Record<string, unknown> {
    const rel = String(item?.path ?? item?.filePath ?? '').trim();
    if (!rel) throw invalidParams('INVALID_PARAMS: path is required');
    const resolved = resolveWorkspacePath(rel, this.workspaceDir);
    const mode = String(item?.mode ?? 'replace').toLowerCase();
    if (!['replace', 'create', 'patch', 'symbol', 'delete', 'rename'].includes(mode)) {
      throw unsupported(`UNSUPPORTED_CAPABILITY: unknown preview mode ${JSON.stringify(mode)}`);
    }
    const expectedVersion = String(item?.expectedVersion ?? '').trim();
    const exists = fs.existsSync(resolved) && fs.statSync(resolved).isFile();
    if (mode === 'create') {
      if (expectedVersion) {
        throw validationFailed('VALIDATION_FAILED: expectedVersion must be omitted for mode=create', {
          reason: 'create_forbids_version',
          path: rel,
        });
      }
      if (exists) {
        throw conflict('CONFLICT_DETECTED: file already exists; use mode=replace with its version', {
          reason: 'file_exists',
          path: rel,
        });
      }
    } else {
      if (!exists) throw resourceNotFound(`RESOURCE_NOT_FOUND: ${rel}`);
      const current = fs.readFileSync(resolved, 'utf-8');
      const currentVersion = sha256(current);
      if (!expectedVersion) {
        throw validationFailed('VALIDATION_FAILED: expectedVersion is required to preview an edit of an existing file', {
          reason: 'expected_version_required',
          path: rel,
          current: currentVersion,
        });
      }
      if (expectedVersion !== currentVersion) {
        throw conflict(
          `CONFLICT_DETECTED: version mismatch: expected ${expectedVersion}, current is ${currentVersion}`,
          { reason: 'version_mismatch', path: rel },
        );
      }
    }
    const replacement = String(item?.replacement ?? item?.source ?? '');
    // Guard order (§4.3): the syntax pre-flight runs before any would-be write.
    const syntax = checkSyntax(resolved, replacement);
    const currentText = exists ? fs.readFileSync(resolved, 'utf-8') : '';
    const newVersion = sha256(replacement);
    return {
      path: rel,
      mode,
      unchanged: currentText === replacement,
      ...(exists ? { currentVersion: sha256(currentText) } : {}),
      newVersion,
      ...(syntax ? { syntaxCheck: syntax } : {}),
      wouldApply: true,
    };
  }

  /** §14.1 repo-wide symbol search over the workspace tree (honest L1-grade index). */
  index(params: Record<string, any> = {}): Record<string, unknown> {
    const query = String(params.query ?? '').trim();
    if (!query) throw invalidParams('INVALID_PARAMS: query is required');
    const q = query.toLowerCase();
    const kinds = Array.isArray(params.kinds) ? (params.kinds as string[]).map((k) => String(k).toLowerCase()) : [];
    const scope = String(params.scope ?? '').replace(/^\/+/, '');
    const cap = Math.max(1, Math.min(Number(params.limit) || 25, 200));
    const scored: Array<{ rank: number; file: string; name: string; kind: string; line: number; lineEnd: number }> = [];
    const SKIP = new Set(['.git', 'node_modules', 'dist', 'build', '.tokensafe', '.oumv', 'coverage', '.next']);
    const walk = (dir: string): void => {
      for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
        if (entry.isDirectory()) {
          if (!SKIP.has(entry.name)) walk(path.join(dir, entry.name));
          continue;
        }
        if (!/\.(js|mjs|cjs|ts|tsx|jsx)$/.test(entry.name)) continue;
        const abs = path.join(dir, entry.name);
        const rel = path.relative(this.workspaceDir, abs).split(path.sep).join('/');
        if (scope && !rel.startsWith(scope)) continue;
        let stat: fs.Stats;
        try {
          stat = fs.statSync(abs);
        } catch {
          continue;
        }
        if (stat.size > 512 * 1024) continue;
        let text: string;
        try {
          text = fs.readFileSync(abs, 'utf-8');
        } catch {
          continue;
        }
        const patterns: Array<[RegExp, string]> = [
          [/\b(?:export\s+)?(?:default\s+)?(?:async\s+)?function\s+([A-Za-z_$][\w$]*)/g, 'function'],
          [/\b(?:export\s+)?(?:abstract\s+)?class\s+([A-Za-z_$][\w$]*)/g, 'class'],
          [/\b(?:export\s+)?const\s+([A-Za-z_$][\w$]*)\s*[:=]/g, 'variable'],
        ];
        for (const [regex, kind] of patterns) {
          for (const match of text.matchAll(regex)) {
            const name = match[1];
            const lower = name.toLowerCase();
            let rank = -1;
            if (q === lower) rank = 0;
            else if (lower.startsWith(q)) rank = 1;
            else if (q.length >= 2 && lower.includes(q)) rank = 2;
            if (rank < 0) continue;
            if (kinds.length && !kinds.includes(kind)) continue;
            const line = text.slice(0, match.index ?? 0).split('\n').length;
            scored.push({ rank, file: rel, name, kind, line, lineEnd: line });
          }
        }
      }
    };
    walk(this.workspaceDir);
    scored.sort((a, b) => a.rank - b.rank || a.file.localeCompare(b.file) || a.name.localeCompare(b.name));
    const matches = scored.slice(0, cap).map((entry) => ({
      resourceId: `${entry.kind}:${entry.name}@${entry.file}`,
      name: entry.name,
      kind: entry.kind,
      file: entry.file,
      lines: [entry.line, entry.lineEnd],
      match: ({ 0: 'exact', 1: 'prefix', 2: 'substring' } as Record<number, string>)[entry.rank],
    }));
    return { matches, truncated: scored.length > matches.length, totalMatches: scored.length };
  }

  /** §14.2 read-only data access through the adopter's sandbox query hook. */
  async query(params: Record<string, any> = {}): Promise<Record<string, unknown>> {
    // Policy first: a workspace that disables query answers -32007 even with a hook wired.
    if (this.permissions.query === false) {
      throw new OumvError(ERROR_CODES.PERMISSION_DENIED, 'PERMISSION_DENIED: the workspace policy disables read-only query (permissions.query: false)', {
        reason: 'query_disabled',
      });
    }
    const hook = this.sandbox?.db?.query;
    if (!hook) {
      throw unsupported('UNSUPPORTED_CAPABILITY: no read-only database query hook is configured');
    }
    const statement = String(params.sql ?? '').trim().replace(/;\s*$/, '');
    const first = (statement.split(/[\s(]+/)[0] ?? '').toUpperCase();
    if (!['SELECT', 'EXPLAIN', 'PRAGMA', 'WITH'].includes(first)) {
      throw new OumvError(ERROR_CODES.SECURITY_VIOLATION, 'SECURITY_VIOLATION: only SELECT/EXPLAIN/PRAGMA statements are accepted', {
        reason: 'query_forbidden',
      });
    }
    const limit = Math.max(1, Math.min(Number(params.limit) || 100, 1000));
    const { columns, rows } = await hook(statement, Array.isArray(params.params) ? params.params : [], limit);
    return { columns, rows, rowCount: rows.length, truncated: rows.length >= limit };
  }

  /** §14.3 run the build/test command and return structured diagnostics. */
  async build(params: Record<string, any> = {}): Promise<Record<string, unknown>> {
    const { execFile } = await import('node:child_process');
    const command = String(params.command ?? this.buildCommand ?? '').trim();
    if (!command) {
      return {
        status: 'skipped',
        command: 'none',
        diagnostics: [],
        skipReason: 'no build command is configured for this runtime (set buildCommand or pass command)',
      };
    }
    const [argv0, ...argvRest] = command.split(/\s+/);
    const timeoutMs = Math.max(1000, Math.min(Number(params.timeoutMs) || 120000, 600000));
    const started = Date.now();
    const diagnostics: Array<Record<string, unknown>> = [];
    let failed = false;
    await new Promise<void>((resolve) => {
      execFile(argv0, argvRest, { cwd: this.workspaceDir, timeout: timeoutMs, windowsHide: true }, (err, stdout, stderr) => {
        failed = Boolean(err);
        for (const line of String(stderr || '').split('\n').concat(String(stdout || '').split('\n'))) {
          if (!line.trim() || diagnostics.length >= 50) continue;
          const low = line.toLowerCase();
          diagnostics.push({
            severity: low.includes('error') || low.includes('failed') ? 'error' : low.includes('warn') ? 'warning' : 'info',
            source: 'build',
            message: line.slice(0, 500),
            tool: argv0,
          });
        }
        resolve();
      });
    });
    for (const diag of diagnostics.filter((d) => d.severity === 'error').slice(0, 20)) {
      this.publishEvent('diagnostic', diag);
    }
    return {
      status: failed ? 'failed' : 'passed',
      command,
      diagnostics,
      durationMs: Date.now() - started,
      truncated: diagnostics.length >= 50,
    };
  }

  private applyEditUncached(params: Record<string, any> = {}): Record<string, unknown> {
    // Wire 1.5.0: a request carrying `edits` is a multi-file atomic batch.
    if (Array.isArray(params.edits) && params.edits.length > 0) {
      const result = this.applyEditBatch(params);
      for (const item of result.edits as Array<Record<string, unknown>>) {
        this.recordState(String(item.path ?? ''), String(item.newVersion ?? ''), 'batch');
      }
      return result;
    }
    const result = this.applyEditInternal(params);
    if (result && result.status === 'APPLIED') {
      // Publish after the bytes are on disk: a subscriber must never observe a
      // state event for a mutation that then failed to persist.
      this.recordState(
        String(result.path ?? params.path ?? ''),
        String(result.newVersion ?? ''),
        String(params.mode ?? 'replace').toLowerCase()
      );
    }
    return result;
  }

  /**
   * Multi-file atomic mutation (wire 1.5.0, spec §4.3): every item is jail-,
   * version- and syntax-checked before the first write; a write failure restores
   * the files already written and answers -32603 with data.rolledBack.
   */
  private applyEditBatch(params: Record<string, any>): Record<string, unknown> {
    if (!this.enabled.mutate) throw permissionDenied('PERMISSION_DENIED: mutate capability is disabled');
    const items = params.edits as Array<Record<string, any>>;
    if (items.length > 16) {
      throw validationFailed('VALIDATION_FAILED: edits must hold 1..16 items', {
        reason: 'batch_size',
        count: items.length,
      });
    }

    interface Staged {
      resolved: string;
      rel: string;
      content: string;
      previous?: string;
      created: boolean;
      hunksApplied?: number;
    }

    const staged: Staged[] = [];
    const seen = new Set<string>();
    items.forEach((item, index) => {
      const rel = String(item?.path ?? '').trim();
      const annotate = (err: any): never => {
        throw new OumvError(err.code, err.message, {
          ...(err.data ?? {}),
          editIndex: index,
          ...(rel ? { path: rel } : {}),
        });
      };
      try {
        if (!rel) throw invalidParams('INVALID_PARAMS: path is required');
        const resolved = resolveWorkspacePath(rel, this.workspaceDir);
        if (seen.has(resolved)) {
          throw validationFailed('VALIDATION_FAILED: duplicate path in batch', { reason: 'duplicate_path' });
        }
        seen.add(resolved);
        const mode = String(item?.mode ?? 'replace').toLowerCase();
        if (!['replace', 'create', 'patch'].includes(mode)) {
          throw unsupported(
            `UNSUPPORTED_CAPABILITY: applyEdit batch mode ${JSON.stringify(mode)} (only replace/create/patch are atomic)`
          );
        }
        if (mode === 'create') {
          const replacement = String(item.replacement ?? '');
          if (!replacement) {
            throw validationFailed('VALIDATION_FAILED: replacement is required for mode=create', {
              reason: 'replacement_required',
            });
          }
          if (item.expectedVersion) {
            throw validationFailed('VALIDATION_FAILED: expectedVersion must be omitted for mode=create', {
              reason: 'create_forbids_version',
            });
          }
          if (fs.existsSync(resolved)) {
            throw conflict('CONFLICT_DETECTED: file already exists; use mode=replace with its version', {
              reason: 'file_exists',
            });
          }
          checkSyntax(resolved, replacement);
          staged.push({ resolved, rel, content: replacement, created: true });
          return;
        }
        if (!fs.existsSync(resolved) || !fs.statSync(resolved).isFile()) {
          throw resourceNotFound(`RESOURCE_NOT_FOUND: ${rel}`);
        }
        const current = fs.readFileSync(resolved, 'utf-8');
        const currentVersion = sha256(current);
        // §4.3 guard order: the syntax pre-flight (step 3) runs before the
        // expectedVersion rules (steps 4-5) in every batch item too.
        let content: string;
        let hunksApplied: number | undefined;
        if (mode === 'patch') {
          const hunks: any[] = Array.isArray(item.hunks) ? item.hunks : [];
          if (hunks.length === 0) {
            throw validationFailed('VALIDATION_FAILED: hunks is required for mode=patch', {
              reason: 'hunks_required',
            });
          }
          content = this.spliceHunks(current, hunks);
          hunksApplied = hunks.length;
        } else {
          content = String(item.replacement ?? '');
          if (!content) {
            throw validationFailed('VALIDATION_FAILED: replacement is required for mode=replace', {
              reason: 'replacement_required',
            });
          }
        }
        checkSyntax(resolved, content);
        const expectedVersion = String(item.expectedVersion ?? '').trim();
        if (!expectedVersion) {
          throw validationFailed('VALIDATION_FAILED: expectedVersion is required to edit an existing file', {
            reason: 'expected_version_required',
            current: currentVersion,
          });
        }
        if (expectedVersion !== currentVersion) {
          throw conflict('CONFLICT_DETECTED: version mismatch in batch', {
            reason: 'version_mismatch',
            expected: expectedVersion,
            current: currentVersion,
          });
        }
        staged.push({ resolved, rel, content, previous: current, created: false, hunksApplied });
      } catch (err) {
        annotate(err);
      }
    });

    const written: Array<{ resolved: string; previous?: string; created: boolean }> = [];
    for (let index = 0; index < staged.length; index++) {
      const edit = staged[index];
      try {
        if (edit.created) fs.mkdirSync(path.dirname(edit.resolved), { recursive: true });
        fs.writeFileSync(edit.resolved, edit.content, 'utf-8');
        written.push({ resolved: edit.resolved, previous: edit.previous, created: edit.created });
      } catch (err: any) {
        const failures: string[] = [];
        for (const wrote of [...written].reverse()) {
          try {
            if (wrote.created) fs.rmSync(wrote.resolved, { force: true });
            else if (wrote.previous !== undefined) fs.writeFileSync(wrote.resolved, wrote.previous, 'utf-8');
          } catch (restoreErr: any) {
            failures.push(`${wrote.resolved}: ${restoreErr.message}`);
          }
        }
        throw new OumvError(ERROR_CODES.INTERNAL_ERROR, `failed to persist edit: ${err.message}`, {
          editIndex: index,
          path: edit.rel,
          rolledBack: failures.length === 0 && written.length > 0,
          ...(failures.length ? { rollbackErrors: failures } : {}),
        });
      }
    }

    return {
      status: 'APPLIED',
      edits: staged.map((edit) => ({
        path: edit.rel,
        newVersion: sha256(edit.content),
        ...(edit.created ? { created: true } : {}),
        ...(edit.hunksApplied ? { hunksApplied: edit.hunksApplied } : {}),
      })),
    };
  }

  /** Applies patch hunks in order to the evolving text (shared by patch and batch). */
  private spliceHunks(source: string, hunks: any[]): string {
    let text = source;
    hunks.forEach((hunk: any, index: number) => {
      const target = String(hunk?.target ?? '');
      const replacement = String(hunk?.replacement ?? '');
      if (!target) {
        throw validationFailed(`VALIDATION_FAILED: hunk ${index} has an empty target`, {
          reason: 'target_required',
          hunk: index,
        });
      }
      const [startOff, endOff] = hunkWindow(text, hunk, index);
      const region = text.slice(startOff, endOff);
      const count = region.split(target).length - 1;
      if (count === 0) {
        throw resourceNotFound(`RESOURCE_NOT_FOUND: hunk ${index} target does not occur in the file`);
      }
      if (count > 1 && !hunk.replaceAll) {
        const lines: number[] = [];
        let pos = 0;
        while (lines.length < 5) {
          const found = region.indexOf(target, pos);
          if (found < 0) break;
          lines.push(1 + (region.slice(0, found).match(/\n/g) || []).length);
          pos = found + target.length;
        }
        throw validationFailed(
          `VALIDATION_FAILED: hunk ${index} target occurs ${count} times; narrow it with startLine/endLine or set replaceAll`,
          { reason: 'target_not_unique', hunk: index, count, lines }
        );
      }
      const next = hunk.replaceAll ? region.split(target).join(replacement) : region.replace(target, replacement);
      text = text.slice(0, startOff) + next + text.slice(endOff);
    });
    return text;
  }

  private applyEditInternal(params: Record<string, any> = {}): Record<string, unknown> {
    if (!this.enabled.mutate) throw permissionDenied('PERMISSION_DENIED: mutate capability is disabled');

    // Guard order is normative (spec §4.6): jail -> missing -> syntax -> version -> OCC.
    const requestedPath = String(params.path ?? params.filePath ?? '').trim();
    if (!requestedPath) throw invalidParams('INVALID_PARAMS: path is required');
    const resolved = resolveWorkspacePath(requestedPath, this.workspaceDir);

    // create mode (spec §4.3): new file, no expectedVersion, never an overwrite.
    if (String(params.mode ?? 'replace').toLowerCase() === 'create') {
      const replacement = String(params.replacement ?? params.source ?? '');
      if (!replacement) throw invalidParams('INVALID_PARAMS: replacement is required for mode=create');
      if (params.expectedVersion) {
        throw validationFailed('VALIDATION_FAILED: expectedVersion must be omitted for mode=create', {
          reason: 'create_forbids_version',
        });
      }
      if (fs.existsSync(resolved)) {
        throw conflict('CONFLICT_DETECTED: file already exists; use mode=replace with its version', {
          reason: 'file_exists',
        });
      }
      const syntax = checkSyntax(resolved, replacement);
      fs.mkdirSync(path.dirname(resolved), { recursive: true });
      fs.writeFileSync(resolved, replacement, 'utf-8');
      return {
        status: 'APPLIED',
        path: path.relative(this.workspaceDir, resolved).split(path.sep).join('/'),
        newVersion: sha256(replacement),
        syntaxCheck: syntax,
        created: true,
      };
    }

    const mode = String(params.mode ?? 'replace').toLowerCase();
    if (!['replace', 'create', 'symbol', 'delete', 'rename', 'patch'].includes(mode)) {
      throw unsupported(`UNSUPPORTED_CAPABILITY: unknown applyEdit mode ${JSON.stringify(mode)}`);
    }

    if (mode === 'patch') {
      if (!fs.existsSync(resolved) || !fs.statSync(resolved).isFile()) {
        throw resourceNotFound(`RESOURCE_NOT_FOUND: ${requestedPath}`);
      }
      const hunks: any[] = Array.isArray(params.hunks) ? params.hunks : [];
      if (hunks.length === 0) {
        throw validationFailed('VALIDATION_FAILED: hunks is required for mode=patch', { reason: 'hunks_required' });
      }
      const current = fs.readFileSync(resolved, 'utf-8');
      const currentVersion = sha256(current);
      // §4.3 guard order: splice + syntax-check the result (step 3) before the
      // expectedVersion rules (steps 4-5).
      const text = this.spliceHunks(current, hunks);
      const syntax = checkSyntax(resolved, text);
      const expectedVersion = String(params.expectedVersion ?? '').trim();
      if (!expectedVersion) {
        throw validationFailed(
          'VALIDATION_FAILED: expectedVersion is required to edit an existing file — call oumv/getSource (or observe L3) and pass its version',
          { reason: 'expected_version_required', current: currentVersion },
        );
      }
      if (expectedVersion !== currentVersion) {
        throw conflict('CONFLICT_DETECTED: version mismatch on patch', {
          expected: expectedVersion,
          current: currentVersion,
        });
      }
      let backupPath: string | undefined;
      try {
        const backupDir = path.join(this.workspaceDir, '.tokensafe', 'backups');
        fs.mkdirSync(backupDir, { recursive: true });
        const backupName = `${Date.now()}-${path.basename(resolved)}`;
        backupPath = path.relative(this.workspaceDir, path.join(backupDir, backupName)).split(path.sep).join('/');
        fs.writeFileSync(path.join(this.workspaceDir, backupPath), current, 'utf-8');
      } catch {
        backupPath = undefined;
      }
      fs.writeFileSync(resolved, text, 'utf-8');
      return {
        status: 'APPLIED',
        path: path.relative(this.workspaceDir, resolved).split(path.sep).join('/'),
        newVersion: sha256(text),
        syntaxCheck: syntax,
        hunksApplied: hunks.length,
        ...(backupPath ? { backupPath } : {}),
      };
    }

    if (mode === 'delete') {
      const provided = params.replacement ?? params.source;
      if (provided !== undefined && String(provided) !== '') {
        throw validationFailed('VALIDATION_FAILED: replacement must be omitted for mode=delete', {
          reason: 'replacement_forbidden',
        });
      }
      if (!fs.existsSync(resolved) || !fs.statSync(resolved).isFile()) {
        throw resourceNotFound(`RESOURCE_NOT_FOUND: ${requestedPath}`);
      }
      const expectedVersion = String(params.expectedVersion ?? '').trim();
      if (!expectedVersion) {
        throw validationFailed('VALIDATION_FAILED: expectedVersion is required to delete a file', {
          reason: 'expected_version_required',
          path: requestedPath,
        });
      }
      const currentVersion = sha256(fs.readFileSync(resolved, 'utf-8'));
      if (expectedVersion !== currentVersion) {
        throw conflict('CONFLICT_DETECTED: version mismatch on delete', {
          expected: expectedVersion,
          current: currentVersion,
        });
      }
      fs.unlinkSync(resolved);
      return {
        status: 'APPLIED',
        path: path.relative(this.workspaceDir, resolved).split(path.sep).join('/'),
        syntaxCheck: { status: 'skipped' },
        deleted: true,
      };
    }

    if (mode === 'rename') {
      const renameTo = String(params.renameTo ?? '').trim();
      if (!renameTo) {
        throw validationFailed('VALIDATION_FAILED: renameTo is required for mode=rename', {
          reason: 'rename_to_required',
        });
      }
      const provided = params.replacement ?? params.source;
      if (provided !== undefined && String(provided) !== '') {
        throw validationFailed('VALIDATION_FAILED: replacement must be omitted for mode=rename', {
          reason: 'replacement_forbidden',
        });
      }
      const target = resolveWorkspacePath(renameTo, this.workspaceDir);
      if (!fs.existsSync(resolved) || !fs.statSync(resolved).isFile()) {
        throw resourceNotFound(`RESOURCE_NOT_FOUND: ${requestedPath}`);
      }
      if (fs.existsSync(target)) {
        throw conflict('CONFLICT_DETECTED: rename target already exists', { reason: 'target_exists', renameTo });
      }
      const expectedVersion = String(params.expectedVersion ?? '').trim();
      if (!expectedVersion) {
        throw validationFailed('VALIDATION_FAILED: expectedVersion is required to rename a file', {
          reason: 'expected_version_required',
          path: requestedPath,
        });
      }
      const currentVersion = sha256(fs.readFileSync(resolved, 'utf-8'));
      if (expectedVersion !== currentVersion) {
        throw conflict('CONFLICT_DETECTED: version mismatch on rename', {
          expected: expectedVersion,
          current: currentVersion,
        });
      }
      fs.mkdirSync(path.dirname(target), { recursive: true });
      fs.renameSync(resolved, target);
      return {
        status: 'APPLIED',
        path: path.relative(this.workspaceDir, resolved).split(path.sep).join('/'),
        newPath: renameTo.split(path.sep).join('/'),
        syntaxCheck: { status: 'skipped' },
      };
    }

    if (mode === 'symbol') {
      const symbol = String(params.symbol ?? '').trim();
      if (!symbol) {
        throw validationFailed('VALIDATION_FAILED: symbol is required for mode=symbol', { reason: 'symbol_required' });
      }
      const replacement = String(params.replacement ?? params.source ?? '');
      if (!replacement) throw invalidParams('INVALID_PARAMS: replacement is required for mode=symbol');
      if (!fs.existsSync(resolved) || !fs.statSync(resolved).isFile()) {
        throw resourceNotFound(`RESOURCE_NOT_FOUND: ${requestedPath}`);
      }
      const current = fs.readFileSync(resolved, 'utf-8');
      // §4.3 guard order: resolve the symbol and syntax-check the patched text
      // (steps 2-3) before the expectedVersion rules (steps 4-5).
      const range = extractSymbolRange(current, symbol);
      if (!range) {
        throw resourceNotFound(`RESOURCE_NOT_FOUND: symbol ${JSON.stringify(symbol)} not found in ${requestedPath}`);
      }
      const patched = replaceLines(current, range.startLine, range.endLine, replacement);
      const syntax = checkSyntax(resolved, patched);
      const expectedVersion = String(params.expectedVersion ?? '').trim();
      if (!expectedVersion) {
        throw validationFailed('VALIDATION_FAILED: expectedVersion is required to edit a symbol', {
          reason: 'expected_version_required',
          path: requestedPath,
        });
      }
      const currentVersion = sha256(current);
      if (expectedVersion !== currentVersion) {
        throw conflict(`CONFLICT_DETECTED: expected ${expectedVersion}, current is ${currentVersion}`, {
          expected: expectedVersion,
          current: currentVersion,
        });
      }
      let backupPath: string | undefined;
      try {
        const backupDir = path.join(this.workspaceDir, '.tokensafe', 'backups');
        fs.mkdirSync(backupDir, { recursive: true });
        const backupName = `${Date.now()}-${path.basename(resolved)}`;
        backupPath = path.relative(this.workspaceDir, path.join(backupDir, backupName)).split(path.sep).join('/');
        fs.writeFileSync(path.join(this.workspaceDir, backupPath), current, 'utf-8');
      } catch {
        backupPath = undefined;
      }
      fs.writeFileSync(resolved, patched, 'utf-8');
      return {
        status: 'APPLIED',
        path: path.relative(this.workspaceDir, resolved).split(path.sep).join('/'),
        symbol,
        newVersion: sha256(patched),
        syntaxCheck: syntax,
        ...(backupPath ? { backupPath } : {}),
      };
    }

    if (!fs.existsSync(resolved) || !fs.statSync(resolved).isFile()) {
      throw resourceNotFound(`RESOURCE_NOT_FOUND: ${requestedPath}`);
    }
    const current = fs.readFileSync(resolved, 'utf-8');
    const replacement = String(params.replacement ?? params.source ?? '');
    if (!replacement) throw invalidParams('INVALID_PARAMS: replacement is required');

    // checkSyntax throws -32002 itself (pre-flight guard, spec §4.3).
    const syntax = checkSyntax(resolved, replacement);

    const expectedVersion = String(params.expectedVersion ?? '').trim();
    if (!expectedVersion) {
      throw validationFailed(
        'VALIDATION_FAILED: expectedVersion is required to edit an existing file — call oumv/getSource and pass its version',
        { path: requestedPath, current: sha256(current) }
      );
    }
    const currentVersion = sha256(current);
    if (expectedVersion !== currentVersion) {
      throw conflict(`CONFLICT_DETECTED: expected ${expectedVersion}, current is ${currentVersion}`, {
        expected: expectedVersion,
        current: currentVersion,
      });
    }

    const backupPath = path.join(this.workspaceDir, '.tokensafe', 'backups', `${Date.now()}-${path.basename(resolved)}`);
    let backedUp: string | undefined;
    try {
      fs.mkdirSync(path.dirname(backupPath), { recursive: true });
      fs.writeFileSync(backupPath, current, 'utf-8');
      backedUp = path.relative(this.workspaceDir, backupPath).split(path.sep).join('/');
    } catch {
      backedUp = undefined;
    }

    fs.writeFileSync(resolved, replacement, 'utf-8');
    return {
      status: 'APPLIED',
      path: path.relative(this.workspaceDir, resolved).split(path.sep).join('/'),
      ...(params.symbol ? { symbol: String(params.symbol) } : {}),
      newVersion: sha256(replacement),
      syntaxCheck: syntax,
      ...(backedUp ? { backupPath: backedUp } : {}),
    };
  }

  /* ------------------------------------------------------------------ verify */

  async verify(params: Record<string, any> = {}): Promise<Record<string, unknown>> {
    const sandbox = params?.sandbox as { mode?: string } | undefined;
    if (sandbox && sandbox.mode) {
      if (sandbox.mode !== 'rollback') {
        throw unsupported(`UNSUPPORTED_CAPABILITY: sandbox mode ${JSON.stringify(sandbox.mode)}`);
      }
      const hook = this.sandbox?.db;
      if (!hook) {
        throw resourceUnavailable(
          'RUNTIME_UNAVAILABLE: sandbox=rollback requested but no database sandbox is configured'
        );
      }
      // The adopter's hook owns the transaction; we only report what it observes.
      const outcome = await hook.run(() => this.verifyInternal(params, true));
      const result = outcome.result as Record<string, unknown>;
      result.rolledBack = outcome.rolledBack;
      result.rollbackStatus = outcome.rolledBack ? 'verified' : 'unverified';
      result.dbDiff = {
        rollback: outcome.rolledBack,
        ...(typeof outcome.queries === 'number' ? { queries: outcome.queries } : {}),
        driver: 'app-hook',
      };
      const scope = result.scope as VerifyScope | undefined;
      if (scope && outcome.rolledBack) {
        scope.notExecuted = scope.notExecuted.filter((item) => item !== 'persistent database state');
      }
      return result;
    }
    return this.verifyInternal(params, false);
  }

  private async verifyInternal(params: Record<string, any>, dryRun: boolean): Promise<Record<string, unknown>> {
    if (!this.enabled.verify) throw permissionDenied('PERMISSION_DENIED: verify capability is disabled');
    const topTarget = String(params.target ?? '').trim();
    const steps = Array.isArray(params.steps) ? params.steps : [];
    if (!topTarget && steps.length === 0) throw invalidParams('INVALID_PARAMS: target ("METHOD /path") is required');
    // §7.4: a DOM target is answered by the attached page, not by the loopback
    // dispatcher, so its absence is only fatal when the whole request is loopback.
    const stepTargets = steps.map((step: any) => String(step?.target ?? '').trim());
    const wantsBrowser = this.isBrowserTarget(topTarget) || stepTargets.some((target: string) => this.isBrowserTarget(target));
    if (wantsBrowser && !this.browserBridge) {
      throw resourceUnavailable(
        'RUNTIME_UNAVAILABLE: this target is a DOM/component target but no browser bridge is configured — mount the Next bridge route and load the instrumentation client'
      );
    }
    if (!this.handler && !wantsBrowser) throw resourceUnavailable('RUNTIME_UNAVAILABLE: no application handler is mounted for verification');

    if (this.handler) await this.ensureDispatchServer();

    const dispatch = async (stepTarget: string, payload: unknown, headers?: Record<string, string>) => {
      const { method, url } = parseVerifyTarget(stepTarget);
      const body = payload === undefined ? undefined : JSON.stringify(payload);
      const startedAt = process.hrtime.bigint();
      const response = await new Promise<{ statusCode: number; body: string }>((resolve, reject) => {
        const request = http.request(
          {
            host: '127.0.0.1',
            port: this.dispatchPort,
            method,
            path: url,
            headers: {
              'content-type': 'application/json',
              ...(dryRun ? { 'x-tokensafe-dry-run': 'true' } : {}),
              ...(body ? { 'content-length': Buffer.byteLength(body).toString() } : {}),
              ...(headers && typeof headers === 'object' ? headers : {}),
            },
          },
          (res) => {
            let data = '';
            res.setEncoding('utf-8');
            res.on('data', (chunk) => (data += chunk));
            res.on('end', () => resolve({ statusCode: res.statusCode ?? 0, body: data }));
          }
        );
        request.on('error', reject);
        if (body) request.write(body);
        request.end();
      });
      const durationMs = Number(process.hrtime.bigint() - startedAt) / 1_000_000;
      this.recordTrace({
        method,
        path: url,
        route: url,
        handler: 'loopback',
        statusCode: response.statusCode,
        durationMs,
      });
      return { method, url, response, durationMs };
    };

    if (steps.length > 0) {
      const results: Array<{ target: string; statusCode: number; status: string; responseBody: unknown }> = [];
      let total = 0;
      for (const step of steps) {
        const stepTarget = String(step?.target ?? '').trim();
        if (!stepTarget) throw invalidParams('INVALID_PARAMS: every verify step needs a target');
        if (this.isBrowserTarget(stepTarget)) {
          const outcome = await this.browserBridge!.requestVerify({
            target: stepTarget,
            action: String(step?.action ?? 'click'),
            payload: step?.payload,
          });
          const stepMs = Number(outcome.durationMs ?? 0);
          total += stepMs;
          results.push({
            target: stepTarget,
            statusCode: outcome.status === 'failed' ? 500 : 200,
            status: outcome.status,
            responseBody: outcome.diff ?? {},
          });
          continue;
        }
        if (!this.handler) throw resourceUnavailable('RUNTIME_UNAVAILABLE: no application handler is mounted for verification');
        const dispatched = await dispatch(stepTarget, step?.payload, step?.headers);
        total += dispatched.durationMs;
        results.push({
          target: stepTarget,
          statusCode: dispatched.response.statusCode,
          status: dispatched.response.statusCode < 400 ? 'passed' : 'failed',
          responseBody: dispatched.response.body,
        });
      }
      const last = results[results.length - 1];
      const status = results.every((step) => step.status === 'passed') ? 'passed' : 'failed';
      return {
        status,
        target: topTarget || last.target,
        statusCode: last.statusCode,
        steps: results,
        diff: { stepCount: results.length, status: last.statusCode, responseBody: last.responseBody },
        timings: { t_action_ms: total, t_total_ms: total },
        durationMs: total,
        scope: {
          checked: ['ordered multi-step dispatch against one handler'],
          notExecuted: ['external processes', 'persistent database state'],
          notSupported: this.browserBridge ? [] : ['browser/DOM assertions'],
        } satisfies VerifyScope,
      };
    }

    if (this.isBrowserTarget(topTarget)) {
      return this.browserVerifyResult(topTarget, String(params.action ?? 'click'), params.payload);
    }

    const dispatched = await dispatch(topTarget, params.payload, params.headers);
    const status = dispatched.response.statusCode < 400 ? 'passed' : 'failed';
    return {
      status,
      target: topTarget,
      statusCode: dispatched.response.statusCode,
      diff: { status: dispatched.response.statusCode, responseBody: dispatched.response.body },
      timings: { t_action_ms: dispatched.durationMs, t_total_ms: dispatched.durationMs },
      durationMs: dispatched.durationMs,
      scope: {
        checked: ['loopback HTTP dispatch against the live handler'],
        notExecuted: ['external processes', 'persistent database state'],
        notSupported: this.browserBridge ? [] : ['browser/DOM assertions'],
      } satisfies VerifyScope,
    };
  }

  /**
   * A browser target is a selector, an element id the page reported, or the
   * `component:` reference frontend runtimes use. `METHOD /path` never matches,
   * so a route named like an id cannot hijack the loopback path.
   */
  private isBrowserTarget(target: string): boolean {
    const trimmed = target.trim();
    if (!trimmed) return false;
    if (/^(<|#|\[|component:|frontend\.component:)/.test(trimmed)) return true;
    if (/^[A-Za-z]+\s+\//.test(trimmed)) return false;
    return Boolean(this.browserBridge?.hasSurfaceId(trimmed));
  }

  /** Dispatches one action into the attached page and maps its reply to §4.5. */
  private async browserVerifyResult(
    target: string,
    action: string,
    payload: unknown
  ): Promise<Record<string, unknown>> {
    const bridge = this.browserBridge;
    if (!bridge) {
      throw resourceUnavailable('RUNTIME_UNAVAILABLE: no browser bridge is configured for DOM targets');
    }
    const outcome = await bridge.requestVerify({ target, action, payload });
    if (outcome.status === 'failed') {
      throw new OumvError(
        (outcome.errorCode ?? ERROR_CODES.ACTION_NOT_FOUND) as any,
        outcome.error ?? `verify failed on ${target}`
      );
    }
    const totalMs = Number(outcome.durationMs ?? outcome.latency?.totalMs ?? 0);
    const actionMs = Number(outcome.latency?.actionMs ?? 0);
    return {
      status: 'passed',
      target: outcome.target ?? target,
      action: outcome.action ?? action,
      diff: outcome.diff ?? {},
      surfaceDiff: outcome.surfaceDiff ?? [],
      timings: {
        t_action_ms: actionMs,
        t_dom_ms: Math.max(0, Number((totalMs - actionMs).toFixed(2))),
        t_total_ms: totalMs,
      },
      durationMs: totalMs,
      scope: {
        checked: ['browser bridge dispatch (synthetic DOM events)'],
        notExecuted: ['native OS events', 'real network'],
        notSupported: [],
      } satisfies VerifyScope,
    };
  }

  private async ensureDispatchServer(): Promise<void> {
    if (this.dispatchServer) return;
    const server = http.createServer((req, res) => this.handler!(req, res));
    await new Promise<void>((resolve, reject) => {
      server.once('error', reject);
      server.listen(0, '127.0.0.1', () => resolve());
    });
    const address = server.address();
    this.dispatchPort = typeof address === 'object' && address ? address.port : 0;
    this.dispatchServer = server;
  }

  /** Releases the loopback dispatcher (tests, shutdown hooks). */
  async dispose(): Promise<void> {
    if (!this.dispatchServer) return;
    const server = this.dispatchServer;
    this.dispatchServer = undefined;
    this.dispatchPort = 0;
    await new Promise<void>((resolve) => server.close(() => resolve()));
  }

  /* ---------------------------------------------------------------- helpers */

  private findRoute(target: string): RouteInfo | undefined {
    const trimmed = target.trim();
    const match = trimmed.match(/^([A-Za-z]+)\s+(\S+)$/);
    if (match) {
      const byRoute = this.routes.get(`${match[1].toUpperCase()} ${match[2]}`);
      if (byRoute) return byRoute;
    }
    for (const route of this.routes.values()) {
      if (route.path === trimmed) return route;
    }
    return undefined;
  }
}

/** True when a trace belongs to `"METHOD /path"`, `"/path"` or a bare path segment. */
export function matchesTarget(trace: TraceEntry, target: string): boolean {
  const trimmed = target.trim();
  const upper = trimmed.toUpperCase();
  if (`${trace.method} ${trace.path}`.toUpperCase() === upper) return true;
  if (trace.path === trimmed) return true;
  if (trace.route && trace.route === trimmed) return true;
  return trace.path.includes(trimmed) && trimmed.startsWith('/');
}

/** `"POST /api/orders"` (or a bare path, which means POST) -> method + url. */
export function parseVerifyTarget(target: string): { method: string; url: string } {
  const match = target.trim().match(/^([A-Za-z]+)\s+(\S+)$/);
  if (match) return { method: match[1].toUpperCase(), url: match[2] };
  return { method: 'POST', url: target.trim() };
}

/** Pre-flight syntax guard (spec §4.3, `-32002`). */
/** Byte range a patch hunk searches, from its 1-based inclusive startLine/endLine. */
function hunkWindow(text: string, hunk: any, index: number): [number, number] {
  const startLine = Number(hunk?.startLine ?? 0);
  const endLine = Number(hunk?.endLine ?? 0);
  if (!startLine && !endLine) return [0, text.length];
  const lines = text.split('\n');
  const start = Math.max(1, startLine || 1);
  let end = endLine || lines.length;
  if (start > lines.length) return [text.length, text.length];
  if (end > lines.length) end = lines.length;
  if (end < start) {
    throw validationFailed('VALIDATION_FAILED: endLine precedes startLine', {
      reason: 'inverted_line_range',
      hunk: index,
    });
  }
  let startOff = 0;
  for (let i = 0; i < start - 1; i += 1) startOff += lines[i].length + 1;
  let endOff = startOff;
  for (let i = start - 1; i < end; i += 1) endOff += lines[i].length + 1;
  endOff = Math.min(endOff - 1, text.length);
  return [startOff, Math.max(startOff, endOff)];
}

export function checkSyntax(filePath: string, source: string): { status: 'passed' | 'skipped'; tool: string } {
  if (!/\.(c|m)?(t|j)sx?$/.test(filePath)) return { status: 'skipped', tool: 'none' };
  try {
    parseJs(source, {
      sourceType: 'unambiguous',
      plugins: ['typescript', 'jsx', 'decorators-legacy'],
      errorRecovery: false,
    });
  } catch (error: any) {
    throw syntaxError(`SYNTAX_ERROR: pre-flight parse failed: ${error.message}`);
  }
  return { status: 'passed', tool: '@babel/parser' };
}

/** Best-effort symbol slice: declaration start to its matching brace. */
export function extractSymbolRange(
  source: string,
  symbol: string
): { source: string; startLine: number; endLine: number } | undefined {
  const escaped = symbol.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  const pattern = new RegExp(
    `(?:function\\s+${escaped}\\b|class\\s+${escaped}\\b|(?:const|let|var)\\s+${escaped}\\b|${escaped}\\s*[:=]\\s*(?:async\\s*)?\\(|export\\s+(?:default\\s+)?(?:function|class|const|let)\\s+${escaped}\\b)`
  );
  const match = pattern.exec(source);
  if (!match) return undefined;
  const start = match.index;
  let braceStart = source.indexOf('{', start);
  const arrowEnd = source.indexOf(';', start);
  if (braceStart === -1 || (arrowEnd !== -1 && arrowEnd < braceStart)) {
    const end = arrowEnd === -1 ? source.length : arrowEnd + 1;
    const slice = source.slice(start, end);
    return {
      source: slice,
      startLine: source.slice(0, start).split('\n').length,
      endLine: source.slice(0, end).split('\n').length,
    };
  }
  let depth = 0;
  let end = braceStart;
  for (let i = braceStart; i < source.length; i += 1) {
    if (source[i] === '{') depth += 1;
    else if (source[i] === '}') {
      depth -= 1;
      if (depth === 0) {
        end = i + 1;
        break;
      }
    }
  }
  return {
    source: source.slice(start, end),
    startLine: source.slice(0, start).split('\n').length,
    endLine: source.slice(0, end).split('\n').length,
  };
}

function resourceUnavailable(message: string): OumvError {
  return new OumvError(ERROR_CODES.RUNTIME_UNAVAILABLE, message);
}
