# tokensafe-backend-node

OUMV adapter for Node.js backends — **Express** and **Fastify** — sharing one
framework-neutral runtime, so every Node framework answers the same wire shapes
(spec §4) and certifies identically.

```bash
npm install tokensafe-backend-node
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

## What it implements

| Pillar | Behaviour |
| :--- | :--- |
| observe | L0 pulse, L1 route surface, L2 route contract, L3 source + sha256 OCC token, L4 trace ring |
| understand | impact from the registered route table, causality from real traces (`observed: false` + note when there are none) |
| mutate | `getSource`/`applyEdit` with mandatory `expectedVersion`, guarded in the normative order `-32003 → -32009 → -32002 → -32011 → -32001`; backups under `.tokensafe/backups/` |
| verify | loopback HTTP dispatch against the live handler with `timings`/`durationMs`/`scope`, and the result feeds the L4 ring |
| transports | HTTP POST + batch, SSE (`GET …/events`, `Last-Event-ID`), WebSocket upgrade on `/__oumv/rpc` |
| discovery | `~/.tokensafe/runtimes/<hash16>.json` registry entry (0600, outside the repo), profile-keyed |

## Certification

```bash
CERT_OUT_DIR=../certification OUMV_REPO=../../agent-oumv pnpm test
```

The fixture suite (owner: `tokensafe-dev/oumv`) runs over real HTTP against both
adapters. Committed reports: `../certification/node-express.json` and
`node-fastify.json` — Level 3, 18/18, fixtureVersion 4.

Notes: paths are workspace-jailed and credential files are refused; a Node backend
adapter is development-time tooling and must not be exposed publicly without the
session token.
