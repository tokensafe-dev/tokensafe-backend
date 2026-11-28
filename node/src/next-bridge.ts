/**
 * `tokensafe-backend-node/next-bridge` — the client-side installer only.
 *
 * `instrumentation-client.ts` imports exactly this subpath so a Next client
 * bundle never pulls the server runtime, the Babel parser or `ws`. The bridge
 * module itself is served by the Bridge Route Handler, not bundled here.
 */
export { installNextBridge, NEXT_BRIDGE_DEFAULT_ENDPOINT } from './browser/install.js';
export type { InstallNextBridgeOptions } from './browser/install.js';
