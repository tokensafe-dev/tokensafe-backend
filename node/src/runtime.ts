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

export const PROTOCOL_VERSION = '1.3.0';
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
  observe: { levels: string[]; formats: string[] };
  understand: { causality: boolean; signalsGraph: boolean; impactAnalysis: boolean };
  mutate: { occ: boolean; syntaxGuard: boolean };
  verify: { multiMetric: boolean; surfaceDiff: boolean };
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
  type: 'trace' | 'state';
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
  private transports: string[] = ['http'];
  private handler?: http.RequestListener;
  private dispatchServer?: http.Server;
  private dispatchPort = 0;

  constructor(options: OumvRuntimeOptions = {}) {
    this.name = options.name || 'tokensafe-node';
    this.version = options.version || '0.3.0';
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
    this.authToken = options.authToken || '';
    this.sessions = new SessionRegistry(options.sessionTtlSeconds ?? 3600);
    this.handler = options.handler;
    this.enabled = {
      observe: options.capabilities?.observe ?? true,
      understand: options.capabilities?.understand ?? true,
      mutate: options.capabilities?.mutate ?? true,
      verify: options.capabilities?.verify ?? true,
    };
  }

  /* ---------------------------------------------------------------- registry */

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
      if (!Number.isInteger(major) || !Number.isInteger(minor) || major !== 1 || minor > 3) {
        throw unsupported(
          `UNSUPPORTED_CAPABILITY: protocol version ${requestedVersion} (this runtime serves ${PROTOCOL_VERSION})`,
        );
      }
    }
    const capabilities: RuntimeCapabilities = {
      observe: { levels: [...OBSERVE_LEVELS], formats: ['json'] },
      understand: { causality: true, signalsGraph: false, impactAnalysis: true },
      mutate: { occ: true, syntaxGuard: true },
      verify: { multiMetric: true, surfaceDiff: false },
    };
    if (!this.enabled.understand) {
      capabilities.understand = { causality: false, signalsGraph: false, impactAnalysis: false };
    }
    if (!this.enabled.mutate) capabilities.mutate = { occ: false, syntaxGuard: false };
    if (!this.enabled.verify) capabilities.verify = { multiMetric: false, surfaceDiff: false };

    // §3.1: narrow to the client's request; an empty intersection stays empty.
    const requested = params.requestedCapabilities;
    if (requested && typeof requested === 'object') {
      capabilities.observe.levels = intersect(capabilities.observe.levels, requested.observe?.levels);
      capabilities.observe.formats = intersect(capabilities.observe.formats, requested.observe?.formats);
    }
    if (!this.enabled.observe) capabilities.observe = { levels: [], formats: [] };

    return {
      protocolVersion: PROTOCOL_VERSION,
      profile: 'backend',
      sessionId: this.sessions.create(),
      ttlSeconds: this.sessions.ttlSeconds,
      runtimeInfo: { name: this.name, version: this.version },
      capabilities,
    };
  }

  /* ----------------------------------------------------------------- observe */

  observe(params: Record<string, any> = {}): Record<string, unknown> {
    if (!this.enabled.observe) throw permissionDenied('PERMISSION_DENIED: observe capability is disabled');
    const level = String(params.level || 'L0').toUpperCase() as ObserveLevel;
    const format = String(params.format || 'json').toLowerCase();
    if (!OBSERVE_LEVELS.includes(level)) throw unsupported(`UNSUPPORTED_CAPABILITY: unknown observe level ${level}`);
    if (format !== 'json') throw unsupported(`UNSUPPORTED_CAPABILITY: format ${format} is not implemented`);

    switch (level) {
      case 'L0': {
        const memory = process.memoryUsage().rss / (1024 * 1024);
        return {
          level,
          format: 'json',
          data: {
            status: 'HEALTHY',
            uptimeSeconds: Math.floor((Date.now() - this.startTime) / 1000),
            activeRoutes: this.routes.size,
            errorCount1m: 0,
            memoryAllocMB: Math.round(memory),
          },
          tokenEstimate: 30,
        };
      }

      case 'L1': {
        const paged = paginate(this.listRoutes(), params);
        return {
          level,
          format: 'json',
          data: { endpoints: paged.page, ...(paged.hasMore !== undefined ? { hasMore: paged.hasMore } : {}), ...(paged.nextCursor ? { nextCursor: paged.nextCursor } : {}) },
          tokenEstimate: 30 + paged.page.length * 15,
        };
      }

      case 'L2': {
        const target = String(params.target || '').trim();
        if (!target) throw invalidParams('INVALID_PARAMS: target is required for L2');
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

  understand(params: Record<string, any> = {}): Record<string, unknown> {
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
      let text = current;
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
            { reason: 'target_not_unique', hunk: index, count, lines },
          );
        }
        const next = hunk.replaceAll ? region.split(target).join(replacement) : region.replace(target, replacement);
        text = text.slice(0, startOff) + next + text.slice(endOff);
      });
      const syntax = checkSyntax(resolved, text);
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
      const range = extractSymbolRange(current, symbol);
      if (!range) {
        throw resourceNotFound(`RESOURCE_NOT_FOUND: symbol ${JSON.stringify(symbol)} not found in ${requestedPath}`);
      }
      const patched = replaceLines(current, range.startLine, range.endLine, replacement);
      const syntax = checkSyntax(resolved, patched);
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
    if (!this.enabled.verify) throw permissionDenied('PERMISSION_DENIED: verify capability is disabled');
    const topTarget = String(params.target ?? '').trim();
    const steps = Array.isArray(params.steps) ? params.steps : [];
    if (!topTarget && steps.length === 0) throw invalidParams('INVALID_PARAMS: target ("METHOD /path") is required');
    if (!this.handler) throw resourceUnavailable('RUNTIME_UNAVAILABLE: no application handler is mounted for verification');

    await this.ensureDispatchServer();

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
      const results: Array<{ target: string; statusCode: number; status: string; responseBody: string }> = [];
      let total = 0;
      for (const step of steps) {
        const stepTarget = String(step?.target ?? '').trim();
        if (!stepTarget) throw invalidParams('INVALID_PARAMS: every verify step needs a target');
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
      return {
        status: 'passed',
        target: topTarget || last.target,
        statusCode: last.statusCode,
        steps: results,
        diff: { stepCount: results.length, status: last.statusCode, responseBody: last.responseBody },
        timings: { t_action_ms: total, t_total_ms: total },
        durationMs: total,
        scope: {
          checked: ['ordered multi-step loopback dispatch against one handler'],
          notExecuted: ['external processes', 'persistent database state'],
          notSupported: ['browser/DOM assertions'],
        } satisfies VerifyScope,
      };
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
        notSupported: ['browser/DOM assertions'],
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
