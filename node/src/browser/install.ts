/**
 * `installNextBridge()` — the one line a Next.js app needs in
 * `instrumentation-client.ts` (Next >= 15.3) to attach its client components to
 * the runtime:
 *
 *   import { installNextBridge } from 'tokensafe-backend-node';
 *   installNextBridge();
 *
 * It injects the bridge module the route handler serves (`?client=1`) into the
 * page. It is a no-op on the server, in production, and when called twice.
 */

export interface InstallNextBridgeOptions {
  /** Bridge Route Handler path (default `/api/tokensafe/bridge`). */
  endpoint?: string;
  /** Test hook; defaults to the global document. */
  document?: {
    getElementById(id: string): unknown;
    createElement(tag: string): {
      id: string;
      type: string;
      async: boolean;
      src: string;
      [key: string]: unknown;
    };
    head: { appendChild(node: unknown): void };
  };
}

export const NEXT_BRIDGE_DEFAULT_ENDPOINT = '/api/tokensafe/bridge';
const SCRIPT_ID = 'oumv-bridge-client';

export function installNextBridge(options: InstallNextBridgeOptions = {}): void {
  if (process.env.NODE_ENV === 'production') return;
  const globalDocument = (globalThis as unknown as { document?: InstallNextBridgeOptions['document'] }).document;
  const doc = options.document ?? globalDocument;
  if (!doc) return;
  if (doc.getElementById(SCRIPT_ID)) return;
  const script = doc.createElement('script');
  script.id = SCRIPT_ID;
  script.type = 'module';
  script.async = true;
  script.src = (options.endpoint ?? NEXT_BRIDGE_DEFAULT_ENDPOINT) + '?client=1';
  doc.head.appendChild(script);
}
