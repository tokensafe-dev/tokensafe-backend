/**
 * Wire types shared by the browser bridge, the runtime and the Next adapter.
 * Kept dependency-free so `runtime.ts` can import them without pulling the
 * broker (or a Node stream) into a bundle that never uses it.
 */

export interface BrowserSurfaceItem {
  id: string;
  tag?: string;
  role?: string;
  label?: string;
  value?: unknown;
  disabled?: boolean;
  href?: string;
  action?: string;
  component?: string;
}

export interface BrowserComponent {
  id?: string;
  name?: string;
  instances?: number;
  propsKeys?: string[];
  hooksCount?: number;
  stateKeys?: string[];
  source?: { fileName?: string; lineNumber?: number };
}

export interface BrowserTrace {
  type?: string;
  target?: string;
  route?: string;
  status?: number;
  ok?: boolean;
  durationMs?: number;
  message?: string;
  timestamp?: number;
  [key: string]: unknown;
}

/** The snapshot a page publishes: the only source of browser evidence. */
export interface BrowserSnapshot {
  framework?: string;
  route?: string;
  title?: string;
  elementCount?: number;
  mountedComponents?: BrowserComponent[];
  interactiveSurface?: BrowserSurfaceItem[];
  contracts?: unknown[];
  errorsCount?: number;
  errors?: Array<{ message?: string; filename?: string; lineno?: number }>;
  tracesCount?: number;
  timestamp?: number;
}

export interface BrowserVerifyRequest {
  target: string;
  action?: string;
  payload?: unknown;
}

export interface BrowserVerifyOutcome {
  status: 'passed' | 'failed';
  target?: string;
  action?: string;
  diff?: Record<string, unknown>;
  surfaceDiff?: Array<Record<string, unknown>>;
  durationMs?: number;
  latency?: { actionMs?: number; totalMs?: number };
  errorCode?: number;
  error?: string;
}

/** What the runtime needs from a bridge; `BridgeBroker` is the implementation. */
export interface BrowserBridge {
  /** True while at least one page is attached and has published a snapshot. */
  readonly connected: boolean;
  latestSnapshot(): BrowserSnapshot | null;
  /** True when the element id or selector is in the page's interactive surface. */
  hasSurfaceId(id: string): boolean;
  requestVerify(request: BrowserVerifyRequest, options?: { timeoutMs?: number }): Promise<BrowserVerifyOutcome>;
}
