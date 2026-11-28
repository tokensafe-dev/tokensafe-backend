# tokensafe-backend-node

OUMV adapter for Node.js and Bun backends — **Express**, **Fastify**, **Next.js App
Router** and **Elysia** — sharing one framework-neutral runtime, so every framework
answers the same wire shapes (spec §4) and certifies identically.

```bash
npm install tokensafe-backend-node
```

The Elysia plugin lives on the `./elysia` subpath and declares `elysia` as an
optional peer dependency, so a plain Node install never pulls Bun packages:

```bash
bun add tokensafe-backend-node
```

## Express

```ts
import express from 'express';
import { OumvRuntime, mountTokensafe, tracingMiddleware } from 'tokensafe-backend-node';

const app = express();
const runtime = new OumvRuntime({ workspaceDir: process.cwd() });
app.use(tracingMiddleware(runtime));        // L4 traces for every request
app.get('/api/orders', async () => ({ orders: [] }));
const oumv = mountTokensafe(app, { runtime, publicUrl: 'http://127.0.0.1:3000/__oumv/rpc' });
const server = app.listen(3000);
const ws = oumv.attachWebSocket(server);    // optional, spec §2.4
```

## Fastify

```ts
import Fastify from 'fastify';
import { OumvRuntime, registerTokensafe } from 'tokensafe-backend-node';

const app = Fastify();
const oumv = await registerTokensafe(app, { runtime: new OumvRuntime({ workspaceDir: process.cwd() }) });
app.get('/api/orders', async () => ({ orders: [] }));   // captured by the onRoute hook
await app.listen({ port: 3000 });
const ws = oumv.attachWebSocket();
```

## Elysia (Bun)

```ts
import { Elysia } from 'elysia';
import { oumv } from 'tokensafe-backend-node/elysia';

const app = new Elysia()
  .get('/api/orders', () => ({ orders: [] }))
  .use(oumv({ authToken: process.env.OUMV_TOKEN, buildCommand: 'bun run build' }));
app.listen(3000);
```

The plugin mounts `POST /__oumv/rpc`, `GET /__oumv/events` (SSE) and the WebSocket
upgrade on the same path, reads the composed app's route table in `onStart` (so L1
is the routes the host actually serves), writes the discovery descriptor with the
bound port, and dispatches `oumv/verify` through `app.handle`. `readOnly: true`
registers no mutating method (`-32601`, §8.5).

## What it implements

| Pillar | Behaviour |
| :--- | :--- |
| observe | L0 pulse, L1 route surface, L2 route contract, L3 source + sha256 OCC token, L4 trace ring |
| understand | impact from the registered route table, causality from real traces (`observed: false` + note when there are none) |
| mutate | `getSource`/`applyEdit` with mandatory `expectedVersion`, guarded in the normative order `-32003 → -32009 → -32002 → -32011 → -32001`; backups under `.tokensafe/backups/` |
| verify | loopback HTTP dispatch against the live handler with `timings`/`durationMs`/`scope`, and the result feeds the L4 ring |
| transports | HTTP POST + batch, SSE (`GET …/events`, `Last-Event-ID`), WebSocket upgrade on `/__oumv/rpc`, stdio NDJSON |
| discovery | `~/.tokensafe/runtimes/<hash16>.json` registry entry (0600, outside the repo), profile-keyed |

## stdio (headless)

Same runtime, no listener:

```bash
tokensafe-oumv-stdio --workspace /path/to/app
tokensafe-oumv-stdio --app ./src/app.js --workspace /path/to/app   # Express or Fastify
```

One JSON-RPC object per line, responses on stdout, diagnostics on stderr. With
`--app` the module's default export is mounted (routes + in-process verify work);
without it, the file-based pillar (`observe L3`, `getSource`, `applyEdit`) still does.

## Certification

```bash
# Node adapters (Express, Fastify, Next.js) — vitest
CERT_OUT_DIR=../certification OUMV_REPO=../../agent-oumv pnpm test
# Elysia/Bun — the fetch binding, the plugin and the HTTP/SSE/WS certificates
CERT_OUT_DIR=../certification OUMV_REPO=../../agent-oumv bun test test-bun
```

The fixture suite (owner: `tokensafe-dev/oumv`) runs over real HTTP against every
adapter, over stdio against the shared runtime, and over SSE/WebSocket for the
transport certificates. Committed reports live in `../certification/`; the Bun
adapter also certifies the HTTP, SSE and WS bindings (`bun-elysia.json` plus
`transports/bun-elysia-{sse,ws}.json`).

Notes: paths are workspace-jailed and credential files are refused; a Node/Bun
backend adapter is development-time tooling and must not be exposed publicly
without the session token.
