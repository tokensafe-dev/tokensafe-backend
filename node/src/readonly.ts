import { OumvRuntime, type OumvRuntimeOptions } from './runtime.js';

/**
 * §8.5 (revision v1.11) read-only deployment: a runtime whose mutating methods are
 * not registered at all. `oumv/applyEdit`, `oumv/previewEdit`, `oumv/verify` and
 * `oumv/build` answer `-32601 METHOD_NOT_FOUND` because there is no handler to reach,
 * and the handshake advertises `capabilities.readOnly: true` so a client never
 * mistakes the absence for an older adapter that merely lacks the method.
 *
 * Everything that only reads stays: observe, understand, getSource, index, query.
 */
export function createReadOnlyRuntime(options: OumvRuntimeOptions = {}): OumvRuntime {
  return new OumvRuntime({
    ...options,
    capabilities: { ...options.capabilities, mutate: false, verify: false },
  });
}
