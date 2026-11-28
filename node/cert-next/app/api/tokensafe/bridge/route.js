import { createNextBridgeHandlers } from 'tokensafe-backend-node';

// `createNextBridgeHandlers()` resolves the process-global broker the RPC route
// created, so both Route Handlers share one session registry.
const bridge = createNextBridgeHandlers();

export const GET = bridge.GET;
export const POST = bridge.POST;
