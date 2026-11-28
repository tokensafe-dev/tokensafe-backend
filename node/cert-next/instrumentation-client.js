import { installNextBridge } from 'tokensafe-backend-node/next-bridge';

// Next >= 15.3 loads this module in the browser before the app code. The helper
// injects the bridge module the Route Handler serves; it is a no-op in production.
installNextBridge();
