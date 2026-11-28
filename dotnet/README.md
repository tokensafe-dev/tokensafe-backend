# TokenSafe.Oumv.AspNetCore

The OUMV (Universal Agent-Runtime Protocol) adapter for ASP.NET Core. It serves the
protocol from inside the app: JSON-RPC over HTTP, an SSE event stream and a WebSocket
binding, all backed by the app's own endpoints.

Certified **Level 3 (50/50)** on fixtures v13 / wire 1.6.0, plus SSE (51/51) and
WebSocket (48/48) transport certificates that include the pushed `state` events.
The optional wire 1.6.0 surface — `oumv/previewEdit`, `oumv/index`, `oumv/query`,
`oumv/build`, `oumv/cancel` — is implemented and proven by its capability cases,
and the transport auth refusals (401 + `-32007` before dispatch) are part of the
certificate.

## Install

```bash
dotnet add package TokenSafe.Oumv.AspNetCore
```

Targets `net8.0` and `net10.0`.

## Mount (one line, or two with EF Core)

```csharp
var builder = WebApplication.CreateBuilder(args);
builder.Services.AddControllers();
builder.Services.AddTokensafe();          // required only for the EF Core rollback sandbox

var app = builder.Build();
app.UseWebSockets();                      // for the WS binding
app.UseTokensafe();                       // JSON-RPC at /__oumv/rpc, SSE at /__oumv/events
```

`AddTokensafe()` is optional for apps without EF Core: `UseTokensafe()` then builds the
runtime with every default. It is needed when you want
`verify.sandbox: {mode:"rollback"}`, because ASP.NET Core does not expose the service
collection after `Build()` and DbContext discovery needs it.

`tokensafe inject` writes exactly this pair between `// tokensafe:oumv:begin/end`
markers and can revert it byte for byte.

## What is certified

| Pillar | What the adapter does |
| :--- | :--- |
| `oumv/handshake` | 1.6.0, `sessionTtlSeconds` (1..86400, clamped), `ttlSeconds` echo, `mutate.multiFile`/`mutate.preview`, `verify.build`, `index.symbols`, `observeQuery.readOnly` (with a DbContext), `sessions.ttlSeconds`/`sessions.cancel`, `verify.sandboxRollback` when EF Core is present, `transports: [http, sse, ws]` |
| `oumv/observe` | L0 process pulse, L1 endpoint surface from `EndpointDataSource` (minimal APIs and MVC alike) with `limit`/`cursor`, L2 route contract with runtime evidence, L3 source + sha256 OCC token, L4 recorded request traces |
| `oumv/understand` | `impact` over the live route table, `causality` from recorded traces (never fabricated: `observed` is false until an execution is seen) |
| `oumv/getSource` | whole file, `startLine`/`endLine` window, `symbol` via Roslyn, `ifVersion` dedup pointer |
| `oumv/applyEdit` | `replace`/`create`/`patch`/`symbol`/`delete`/`rename`, OCC on every existing file, Roslyn syntax pre-flight, exact-snippet hunks, and the atomic `edits` batch with `data.editIndex`/`data.rolledBack` |
| `oumv/verify` | in-memory dispatch through the real `RequestDelegate` (model binding, filters, DI), multi-step, and the EF Core rollback sandbox |
| `oumv/previewEdit` | applyEdit's guard chain (jail, existence, OCC, hunks, Roslyn pre-flight) as a dry run: `status: PREVIEW`, `applied: false`, one `previews[]` entry per item; nothing is written |
| `oumv/index` | repo-wide symbol search over source files, ranked exact > prefix > suffix > substring, `limit` + `truncated`, `filesScanned` |
| `oumv/query` | `observeQuery.readOnly`: one SELECT/EXPLAIN/PRAGMA/WITH/VALUES/SHOW/DESCRIBE statement over the discovered DbContext's connection inside a rolled-back transaction; a write or stacked statement answers `-32003` + `reason: query_forbidden` |
| `oumv/build` | runs `OumvOptions.BuildCommand` (or `params.command`) in the workspace root, returns `status`/`command`/`exitCode`/`diagnostics`; no command keeps an honest `skipped` with a reason |
| `oumv/cancel` | cooperative `requestId` cancellation (`target: "last"` alias); an unknown or finished id answers `cancelled: false`, never an error |

## EF Core sandbox

`AddTokensafe()` scans the service collection for every `AddDbContext<T>()` registration.
A `verify` with `sandbox: {mode:"rollback"}` then:

1. creates one DI scope,
2. opens a transaction on each discovered `DbContext`,
3. dispatches the request with `X-TokenSafe-Dry-Run: true`,
4. rolls every transaction back and reports what it actually observed.

`rollbackStatus` is `verified` only when transactions were opened and every rollback
call succeeded; `not_applicable` when no DbContext exists (and then the capability is not
advertised at all). `queries` is omitted: the adapter does not wrap the EF command
pipeline, and inventing a count would be a false claim.

## Workspace and safety

The path jail is anchored at `OumvOptions.WorkspaceRoot` (default: the content root).
Edits are OCC-checked, syntax-checked before the first write, and keep a copy of the
previous bytes under `.tokensafe/backups/`. Set `EnableMutations = false` to mount a
read-only adapter.

## Certification

```bash
OUMV_REPO=../agent-oumv ./certify.sh
```

Builds `certapp/` (minimal APIs + one classic controller + EF Core SQLite), runs it on a
throwaway workspace and writes `certification/dotnet-aspnetcore.json`,
`certification/transports/dotnet-aspnetcore-sse.json` and
`certification/transports/dotnet-aspnetcore-ws.json`.
