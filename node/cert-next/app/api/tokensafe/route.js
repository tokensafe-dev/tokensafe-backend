import { createNextRouteHandlers } from 'tokensafe-backend-node';

// The RPC surface. `bridge: true` binds this runtime to the page broker and
// advertises mutate.ephemeralDispatch / verify.surfaceDiff while a page is up.
const handlers = createNextRouteHandlers({
  workspaceDir: process.cwd(),
  bridge: true,
  verifyFetch: async (request) => {
    const url = new URL(request.url);
    if (request.method === 'GET' && url.pathname === '/api/orders') {
      return Response.json({ orders: [] });
    }
    if (request.method === 'POST' && url.pathname === '/api/orders') {
      return Response.json({ id: 1 }, { status: 201 });
    }
    return new Response(JSON.stringify({ error: 'not found' }), { status: 404 });
  },
});

export const POST = handlers.POST;
export const GET = handlers.GET;
