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

export const PROTOCOL_VERSION = '1.1.0';
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
  source: string;
  version: string;
  startLine: number;
  endLine: number;
}

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
  private readonly startTime = Date.now();
  private readonly enabled: { observe: boolean; understand: boolean; mutate: boolean; verify: boolean };
  private transports: string[] = ['http'];
  private handler?: http.RequestListener;
  private dispatchServer?: http.Server;
  private dispatchPort = 0;

  constructor(options: OumvRuntimeOptions = {}) {
    this.name = options.name || 'tokensafe-node';
    this.version = options.version || '0.1.0';
    this.workspaceDir = path.resolve(options.workspaceDir || process.cwd());
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
    this.traces.push({ ...entry, timestamp: entry.timestamp ?? new Date().toISOString() });
    if (this.traces.length > MAX_TRACES) this.traces.shift();
  }

  recentTraces(): TraceEntry[] {
    return this.traces.slice();
  }

  setHandler(handler: http.RequestListener): void {
    this.handler = handler;
  }

  setTransports(transports: string[]): void {
    this.transports = transports;
  }

  /* --------------------------------------------------------------- handshake */

  handshake(params: Record<string, any> = {}): Record<string, unknown> {
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
    if (!OBSERVE_LEVELS.includes(level)) throw invalidParams(`INVALID_PARAMS: unknown observe level ${level}`);
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

      case 'L1':
        return {
          level,
          format: 'json',
          data: { endpoints: this.listRoutes() },
          tokenEstimate: 30 + this.routes.size * 15,
        };

      case 'L2': {
        const target = String(params.target || '').trim();
        if (!target) throw invalidParams('INVALID_PARAMS: target is required for L2');
        const route = this.findRoute(target);
        if (!route) throw targetNotFound(`TARGET_NOT_FOUND: no route matches ${JSON.stringify(target)}`);
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
          tokenEstimate: Math.ceil(source.source.length / 4),
        };
      }

      case 'L4': {
        const target = String(params.target || '').trim();
        const traces = target ? this.traces.filter((trace) => matchesTarget(trace, target)) : this.recentTraces();
        return {
          level,
          format: 'json',
          data: {
            ...(target ? { target } : {}),
            traces,
            traceCount: traces.length,
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
    return this.readSource(filePath, params.symbol ? String(params.symbol) : undefined);
  }

  private readSource(filePath: string, symbol?: string): SourcePayload {
    const resolved = resolveWorkspacePath(filePath, this.workspaceDir);
    if (!fs.existsSync(resolved) || !fs.statSync(resolved).isFile()) {
      throw resourceNotFound(`RESOURCE_NOT_FOUND: ${filePath}`);
    }
    const full = fs.readFileSync(resolved, 'utf-8');
    const relative = path.relative(this.workspaceDir, resolved).split(path.sep).join('/');
    const version = sha256(full);

    if (!symbol) {
      const lines = full.split('\n').length + (full.endsWith('\n') ? 0 : 1);
      return { path: relative, source: full, version, startLine: 1, endLine: lines };
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
    const target = String(params.target ?? '').trim();
    if (!target) throw invalidParams('INVALID_PARAMS: target ("METHOD /path") is required');
    if (!this.handler) throw resourceUnavailable('RUNTIME_UNAVAILABLE: no application handler is mounted for verification');

    const { method, url } = parseVerifyTarget(target);
    await this.ensureDispatchServer();

    const body = params.payload === undefined ? undefined : JSON.stringify(params.payload);
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
            ...(params.headers && typeof params.headers === 'object' ? params.headers : {}),
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
    const status = response.statusCode < 400 ? 'passed' : 'failed';

    this.recordTrace({
      method,
      path: url,
      route: url,
      handler: 'loopback',
      statusCode: response.statusCode,
      durationMs,
    });

    return {
      status,
      target,
      statusCode: response.statusCode,
      diff: { status: response.statusCode, responseBody: response.body },
      timings: { t_action_ms: durationMs, t_total_ms: durationMs },
      durationMs,
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
