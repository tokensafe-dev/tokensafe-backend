# TokenSafe Backend (`tokensafe-backend`)
### Unified Backend Implementations of the OUMV Protocol (UARP)
**The Universal Agent-Runtime Protocol: Observe · Understand · Mutate · Verify**

[![Go Report Card](https://goreportcard.com/badge/github.com/tokensafe-dev/tokensafe-backend/go)](https://goreportcard.com/report/github.com/tokensafe-dev/tokensafe-backend/go)
[![Protocol Specification](https://img.shields.io/badge/OUMV-v1.10--canonical-orange)](https://github.com/tokensafe-dev/oumv/blob/main/spec/SPEC.md)
[![OUMV conformance](https://img.shields.io/badge/OUMV-certified_L3_%C2%B7_7_adapters-10B981)](#certification-spec-10)
[![License: Apache-2.0](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)

> `tokensafe-backend` is the unified backend repository for the **OUMV (Observe, Understand, Mutate, Verify)** protocol. It houses backend runtime instrumentation and adapters across major server-side technologies:
> - **Go (`go/`)**: adapters for Gin, Chi and `net/http` (`github.com/tokensafe-dev/tokensafe-backend/go`); one `AutoMount` line, JSON-RPC + SSE + WebSocket + stdio.
> - **Python (`python/`)**: FastAPI adapter published as `tokensafe-backend-python`; HTTP + SSE + WebSocket + stdio, SQLAlchemy rollback sandbox.
> - **Node.js (`node/`)**: `tokensafe-backend-node` — Express and Fastify over HTTP, SSE, WebSocket and stdio.
> - **.NET (`dotnet/`)**: `TokenSafe.Oumv.AspNetCore` — minimal APIs and MVC controllers on `net8.0`/`net10.0`, in-process endpoint dispatch and an EF Core rollback sandbox that discovers `DbContext`s from DI.
> - **Sidecars (`java/`, `php/`)**: single-file Spring and Laravel/Symfony adapters for projects that cannot take a library dependency (Level 2: no live traces).

---

## Highlights

- **✅ Certified OUMV adapters (spec §10)**: Gin, Chi, `net/http`, FastAPI, Express, Fastify and ASP.NET Core each pass the language-neutral conformance suite over their real transport — Level 3, reports committed under [`certification/`](certification/) and reproduced in CI. Spring and PHP sidecars certify Level 2 and name their single missing case. Exact counts are generated below.
- **🛡️ In-Memory Compiler Pre-Flight Guard**: AST-guided symbol edits validated in memory via `go/parser` and `go/types` in <5ms—blocking non-compiling edits before touching the disk.
- **🔄 Zero Database Pollution**: In-memory transactional dry-runs (`net/http/httptest` + `pkg/db`) execute with guaranteed `ROLLBACK`, eliminating state contamination across test cycles.
- **🔍 Progressive Context Disclosure (L0–L4)**: Replaces 3,000+ token file crawls with compact pulse (`L0`, ~20 tokens), endpoint surfaces (`L1`, ~110 tokens), and reflected schemas (`L2`, ~60 tokens).
- **🧩 Adapters first**: every supported stack mounts a thin adapter and keeps its own framework — one line for Go, Node, Python and ASP.NET Core, nothing written into the repo for React/Vue. The native Go core (`tokensafe.New()`) stays as a reference implementation for tests and examples, not as the recommended path.

---

## Certification (spec §10)

<!-- certification:start -->
**10 adapters certified against the same language-neutral fixtures** (`fixtureVersion` 13): 8 at Level 3 and 2 source-level adapter(s) at Level 2 — frozen on the wire 1.5.0 subset they still serve, each with its single L3 gap named below. The smallest suite in the table is 33 cases: a runtime that advertises an optional capability also runs the case gated on it, and a runtime frozen on an older wire skips the newer-wire cases instead of failing them. Generated from `certification/*.json` by `scripts/render_certification.py`; CI fails when this block no longer matches the reports.

| Adapter | Framework | Transport exercised | Level | Cases | Report |
| :--- | :--- | :--- | :--- | :--- | :--- |
| `dotnet-aspnetcore` | aspnetcore | ASP.NET Core `POST /__oumv/rpc` | **3** | 50/50 | [certification/dotnet-aspnetcore.json](certification/dotnet-aspnetcore.json) |
| `go-chi` | chi | HTTP `POST /__oumv/rpc` | **3** | 46/46 | [certification/go-chi.json](certification/go-chi.json) |
| `go-gin` | Gin | HTTP `POST /__oumv/rpc` | **3** | 46/46 | [certification/go-gin.json](certification/go-gin.json) |
| `go-stdhttp` | net/http | HTTP `POST /__oumv/rpc` | **3** | 46/46 | [certification/go-stdhttp.json](certification/go-stdhttp.json) |
| `next-app-router` | nextjs | — | **3** | 48/48 | [certification/next-app-router.json](certification/next-app-router.json) |
| `node-express` | Express | Node `POST /__oumv/rpc` | **3** | 46/46 | [certification/node-express.json](certification/node-express.json) |
| `node-fastify` | Fastify | Node `POST /__oumv/rpc` | **3** | 46/46 | [certification/node-fastify.json](certification/node-fastify.json) |
| `python-fastapi` | FastAPI | ASGI `POST /__oumv/rpc` | **3** | 48/48 | [certification/python-fastapi.json](certification/python-fastapi.json) |

Source-level adapters scan the framework's route declarations statically, apply OCC-checked edits and run a real verify command, but they do not boot the application — so they record no live request traces, `observe-l4-returns-recorded-traces` fails, and Level 3 is not claimed:

| Adapter | Framework | Transport exercised | Level | Cases | Report |
| :--- | :--- | :--- | :--- | :--- | :--- |
| `java-spring` | Spring Boot | JDK `POST /__oumv/rpc` + stdio | **2** | 32/33 | [certification/java-spring.json](certification/java-spring.json) |
| `php-laravel` | Laravel / Symfony | PHP `POST /__oumv/rpc` + stdio | **2** | 32/33 | [certification/php-laravel.json](certification/php-laravel.json) |

Named gaps:

- `java-spring` → `observe-l4-returns-recorded-traces`: expected success, got error -32006: UNSUPPORTED_CAPABILITY: observe L4 requires live request traces; this JDK-only sidecar does not run the Spring application, so it records none
- `php-laravel` → `observe-l4-returns-recorded-traces`: field "data.traces" must be a non-empty array

Transport certificates run the same suite over the optional bindings, plus the fixture v8 push cases (§2.3 `state` with `Last-Event-ID` replay, §2.4 `notifications/oumv.state`):

| Certificate | Adapter | Transport | Cases |
| :--- | :--- | :--- | :--- |
| [transports/dotnet-aspnetcore-sse.json](certification/transports/dotnet-aspnetcore-sse.json) | `dotnet-aspnetcore-sse` | sse | 51/51 |
| [transports/dotnet-aspnetcore-ws.json](certification/transports/dotnet-aspnetcore-ws.json) | `dotnet-aspnetcore-ws` | ws | 48/48 |
| [transports/go-stdhttp-sse.json](certification/transports/go-stdhttp-sse.json) | `go-stdhttp-sse` | sse | 50/50 |
| [transports/go-stdhttp-ws.json](certification/transports/go-stdhttp-ws.json) | `go-stdhttp-ws` | ws | 47/47 |
| [transports/node-express-sse.json](certification/transports/node-express-sse.json) | `node-express-sse` | sse | 50/50 |
| [transports/node-express-ws.json](certification/transports/node-express-ws.json) | `node-express-ws` | ws | 47/47 |
| [transports/node-fastify-sse.json](certification/transports/node-fastify-sse.json) | `node-fastify-sse` | sse | 50/50 |
| [transports/node-fastify-ws.json](certification/transports/node-fastify-ws.json) | `node-fastify-ws` | ws | 47/47 |
| [transports/node-stdio.json](certification/transports/node-stdio.json) | `node-stdio` | stdio | 37/37 |
| [transports/python-fastapi-sse.json](certification/transports/python-fastapi-sse.json) | `python-fastapi-sse` | sse | 52/52 |
| [transports/python-fastapi-ws.json](certification/transports/python-fastapi-ws.json) | `python-fastapi-ws` | ws | 49/49 |
| [transports/python-stdio.json](certification/transports/python-stdio.json) | `python-stdio` | stdio | 45/45 |
<!-- certification:end -->

Levels are cumulative and a skipped case never counts as a pass — a missing capability caps the claim instead of inflating it. The transport certificates above cover **stdio** (§2.5) plus the optional **SSE** (§2.3) and **WebSocket** (§2.4) bindings for Go net/http, Node Express/Fastify, Python FastAPI and ASP.NET Core; those runs include the push cases (SSE `state` with `Last-Event-ID` replay, WS `notifications/oumv.state`) and are Level 3, 38/38. Regenerate locally:

```bash
# Go adapters (writes certification/*.json)
cd go && CERT_OUT_DIR=../certification go test ./pkg/adapters -run TestAdapterCertification

# Python/FastAPI — HTTP and stdio
cd python && CERT_OUT_DIR=../certification .venv/bin/python -m pytest -q tests/test_conformance.py tests/test_stdio.py

# Node adapters — HTTP, stdio, SSE and WS (writes certification/*.json and transports/*.json)
cd node && CERT_OUT_DIR=../certification OUMV_REPO=../../agent-oumv pnpm test

# Spring (JDK-only sidecar) and Laravel/Symfony (PHP sidecar) — Level 2, stdio
cd java && ./certify.sh
cd ../php && ./certify.sh

# ASP.NET Core — HTTP, SSE and WS certificates (builds certapp, net10 SDK)
cd ../dotnet && ./certify.sh
```

---


## The Four Pillars for Backend

1. **Observe (L0–L4)**: Progressive context disclosure. From service pulse (`L0`, ~30 tokens) and route surface (`L1`, ~150 tokens) to DTO schemas (`L2`), AST-sliced source code with OCC tokens (`L3`), and in-flight traces (`L4`).
2. **Understand**: Live causality analysis (*"Why did this request fail with 403?"*) and static type-impact analysis using Go AST (`go/types`).
3. **Mutate**: Optimistic Concurrency Control (OCC) with in-memory compiler pre-flight guards (`go/parser`, `go/types`). Changes that don't compile are rejected before touching the filesystem.
4. **Verify**: In-memory action verification (`net/http/httptest`) and transactional DB dry-runs with guaranteed `ROLLBACK`, returning structured diffs and 4-metric latency breakdowns in <5ms.

---

## Architecture: one runtime, thin adapters

Every adapter mounts the same runtime and the same wire contract; none of them asks a
team to migrate a framework.

1. **Compatibility adapters** are the supported path: `pkg/adapters/*` (Go), `mount_tokensafe` (FastAPI), `mountTokensafe` (Express/Fastify), `UseTokensafe()` (ASP.NET Core), plus the Spring/PHP sidecars. They introspect the framework's own route table and serve the protocol from inside the app.
2. **The native Go core** (`tokensafe.New()`, `app.Simulate`) remains as a reference implementation used by tests and examples; new services should mount an adapter.

---

## Quickstart (native Go core, reference only)

```go
package main

import (
	"net/http"

	"github.com/tokensafe-dev/tokensafe-backend/go"
)

type CreateOrderReq struct {
	Item   string  `json:"item" validate:"required,min=2"`
	Amount float64 `json:"amount" validate:"required,gt=0"`
}

type OrderResp struct {
	ID     string  `json:"id"`
	Item   string  `json:"item"`
	Amount float64 `json:"amount"`
	Status string  `json:"status"`
}

func main() {
	app := tokensafe.New()

	// 1. Type-safe POST with automatic DTO validation & custom status
	tokensafe.Post(app, "/api/v1/orders", func(c *tokensafe.Context, req CreateOrderReq) (*OrderResp, error) {
		c.Status(http.StatusCreated) // Returns 201 Created
		return &OrderResp{
			ID:     "ord-1",
			Item:   req.Item,
			Amount: req.Amount,
			Status: "PENDING",
		}, nil
	})

	// 2. Go 1.22+ Path-Value routing ({id}) with c.Param("id")
	app.Get("/api/v1/orders/{id}", func(c *tokensafe.Context) error {
		id := c.Param("id")
		if id != "ord-1" {
			return tokensafe.NewHTTPError(http.StatusNotFound, "order not found")
		}
		return c.JSON(http.StatusOK, map[string]string{"id": id, "status": "PENDING"})
	})

	// 3. Sub-millisecond in-memory testing without network sockets
	res := app.Simulate(http.MethodGet, "/api/v1/orders/ord-1", nil)
	println("In-memory response status:", res.Code)

	app.Run(":8080")
}
```

---

## Quickstart (Gin Adapter for Existing Backends)

```go
import (
    "github.com/gin-gonic/gin"
    ginAdapter "github.com/tokensafe-dev/tokensafe-backend/go/pkg/adapters/gin"
    "github.com/tokensafe-dev/tokensafe-backend/go/pkg/oumv"
)

func main() {
    r := gin.Default()
    // ... your existing routes ...

    rt := oumv.NewRuntime(oumv.Config{Name: "my-service", Handler: r})
    ginAdapter.Mount(r, rt) // Auto-discovers routes and mounts /__oumv/rpc

    // For runtime causality evidence (oumv/understand), serve through the trace wrapper:
    // http.ListenAndServe(":8080", oumv.TraceHandler(rt, r))
    r.Run(":8080")
}
```

---

## Quickstart: Python / FastAPI (`python/`)

Instrument any FastAPI application with OUMV in 2 lines of code:

```python
from fastapi import FastAPI
from tokensafe_python import mount_tokensafe

app = FastAPI(title="Store API")

# Auto-introspects routes, Pydantic schemas, and mounts /__oumv/rpc with DB rollback sandbox
mount_tokensafe(app)

@app.get("/api/products")
def list_products():
    return [{"id": "prod-1", "name": "Coffee Beans", "price": 14.50}]
```

**Honest evidence guarantees (v0.2.0+):**
- `rolled_back` in dry-run results is `true` **only** when an actual ROLLBACK was observed on the wired SQLAlchemy engine (`rollback_status`: `verified` / `unverified` / `not_applicable`).
- `oumv/applyEdit` performs a real OCC workspace edit (sha256 versions, `expectedVersion` conflict checks, syntax pre-flight for `.py`/`.js`, automatic backups, path-boundary jail) — no fabricated version tokens.

---

## Quickstart: ASP.NET Core (`dotnet/`)

```csharp
var builder = WebApplication.CreateBuilder(args);
builder.Services.AddControllers();
builder.Services.AddTokensafe();   // scans DI for DbContexts → enables verify.sandbox

var app = builder.Build();
app.UseWebSockets();
app.UseTokensafe();                // JSON-RPC /__oumv/rpc, SSE /__oumv/events

app.MapGet("/api/v1/orders", (AppDbContext db) => Results.Json(db.Orders.ToList()));
app.MapControllers();
app.Run();
```

Minimal APIs and controllers are both reached through the app's `EndpointDataSource`, so
`observe L1/L2` and `verify` see one route table. `oumv/verify` runs the endpoint's real
`RequestDelegate` on a synthetic `HttpContext` — model binding, filters and DI included,
no network hop. With `AddTokensafe()` present, a sandboxed verify opens one transaction
per discovered `DbContext`, dispatches with `X-TokenSafe-Dry-Run: true`, rolls back, and
reports `rollbackStatus: "verified"` only when the rollback was observed.

The wire 1.6.0 surface is implemented as well: `oumv/previewEdit` (the same guard chain as
applyEdit, no bytes written), `oumv/index` (ranked repo-wide symbol search), `oumv/query`
(read-only SQL over the discovered `DbContext` connection, writes refused with `-32003`),
`oumv/build` (`OumvOptions.BuildCommand`, honest `skipped` without one) and `oumv/cancel`
(cooperative, `requestId`-scoped).

---

## Installation

### Go Runtime (`go/`)
```bash
go get github.com/tokensafe-dev/tokensafe-backend/go@v0.11.0
```
`go/` is a nested module, so it resolves against `go/vX.Y.Z` tags (the release
workflow creates them alongside every `vX.Y.Z` release tag). A plain `vX.Y.Z`
tag is invisible to the module proxy.

### Python Runtime (`python/`)
```bash
pip install "tokensafe-backend-python @ git+https://github.com/tokensafe-dev/tokensafe-backend.git#subdirectory=python"
# local monorepo copy:
pip install ./python
# or from git:
pip install "tokensafe-backend @ git+https://github.com/tokensafe-dev/tokensafe-backend.git#subdirectory=python"
```

### .NET (`dotnet/`)
```bash
dotnet add package TokenSafe.Oumv.AspNetCore
```
Targets `net8.0` and `net10.0`. `tokensafe inject` adds the package and the
`AddTokensafe()`/`UseTokensafe()` pair between markers, validates with `dotnet build`, and
reverts byte for byte on failure.

### Node (`node/`)
```bash
npm install tokensafe-backend-node
```

---

## Benchmarks — what is measured and what is not

| Run | Result | Evidence |
| :--- | :--- | :--- |
| Conformance | 7 adapters at Level 3, 37/37 each | `certification/*.json`, regenerated in CI |
| Three-arm, order-processing sandbox | `oumv` arm **measured** (740 payload tokens, 5 round trips, oracle 12/12) vs `baseline` **simulated** (33,960 trace tokens, 6 turns, 12/12) | [THREE_ARM_REPORT.md](go/benchmark/THREE_ARM_REPORT.md) |
| Agent benchmark (DeepSeek, real agent) | **Go, 3 arms: OUMV 9/9, −45.8% input tokens / −33.1% cost vs baseline; TokenSafe-tools arm 7/9** | [OUMV_AGENT_MATRIX.md](go/benchmark/OUMV_AGENT_MATRIX.md) |

The three-arm run is a methodology demonstration, not a speed-up claim: only the OUMV
arm executes real JSON-RPC against the sandbox, the baseline numbers are fixture traces.
The measured agent matrix above is the only published OUMV productivity result; the
tool-level study in `tokensafe/docs/BENCHMARK_REAL.json` (15 real repositories, −84.8%
tokens, zero-loss gates) measures the TokenSafe tools, not OUMV.

### Measured agent matrix (Go, DeepSeek)

3 regression tasks × 5 runs × 3 arms on `deepseek-chat` at temperature 0, a real 12-assertion
oracle re-run after every step, tokens from the provider's `usage` payload. `baseline` gets
file/search/command tools, `tokensafe` the product's own MCP server
(`smart_read_file(endpoint="POST /api/…")` in one call → `apply_edits` with the hunk and inline
`verify="auto"`), `oumv` the shipped Go adapter (`observe L3` on the endpoint →
`applyEdit mode=patch` → the oracle). The target is the module plus **200 generated service
files** (269 code files), so discovery has to compete with a realistic tree.

| Arm | Solved | Mean steps | Mean input | Mean output | Cost/run |
| :--- | ---: | ---: | ---: | ---: | ---: |
| `baseline` | 15/15 | 5.6 | 26,412 | 1,670 | $0.0044 |
| `tokensafe` | 15/15 | **3.1** | **8,177** | **414** | **$0.0012** |
| `oumv` | 15/15 | 3.7 | 9,553 | 521 | $0.0014 |
| **tokensafe vs baseline** | — | −44.0% | **−69.0%** | **−75.2%** | **−73.3%** |
| **oumv vs baseline** | — | −33.3% | **−63.8%** | **−68.8%** | **−68.2%** |

Tree size is the lever both product arms removed: on the same tasks with a 69-file tree the
file-tool arm costs 22,356 input tokens per run against 8,823 (TokenSafe) and 9,756 (OUMV);
adding 200 files moves only `baseline` (+18%), because the two arms resolve
`POST /api/products` to its handler in a single call instead of searching for it. Both arms now
send a hunk rather than a file, so the remaining gap is step count and how much the model must
emit to place the edit.

Method, per-task table, the small-target comparison, artifacts and limits:
[OUMV_AGENT_MATRIX_V5.md](go/benchmark/OUMV_AGENT_MATRIX_V5.md). Generated from the run
artifacts by `go/benchmark/render_agent_matrix.py`; the previous measured run is
[OUMV_AGENT_MATRIX_V4.md](go/benchmark/OUMV_AGENT_MATRIX_V4.md), and the earlier losing row is
corrected in
[OUMV_AGENT_MATRIX.md](go/benchmark/OUMV_AGENT_MATRIX.md#correction-post-run-diagnosis-2026-11-26).

Protocol-level numbers (deterministic, no model — `tokensafe bench-oumv --mechanics`): one
`observe L3` call resolves an endpoint to its handler body in **721 chars**, where search +
read costs **5,115 chars**; an in-process `verify` takes **1.2 ms** (27 ms including the helper
process) against **~3.1 s** for the test-oracle run; a stale `expectedVersion` is rejected
with `-32001` and a syntactically broken edit with `-32002`.

### Agent Usability Benchmark (AUB - Go, simulated traces)

> ⚠️ **Simulated run — not a measured agent session.** The 12-assertion oracle below is
> real (it executes the handler through `httptest`), but the token/turn numbers come from
> **fixture traces** (`go/benchmark/traces/traces.go`), not from an LLM agent. Treat them as
> a model of the intended workflow, not as a benchmark result.
>
> The measured comparison now lives in
> [`go/benchmark/THREE_ARM_REPORT.md`](go/benchmark/THREE_ARM_REPORT.md): three arms
> (baseline / TokenSafe tools / OUMV protocol), where the OUMV arm's payload bytes are
> counted from real JSON-RPC round trips and the other two are explicitly labelled
> SIMULATED. Regenerate with
> `cd go && CERT_OUT_DIR=/tmp/out go test ./benchmark/three_arm`.

The older AUB scorecard that quoted a **24.6× AER** was derived from those fixture
traces, so it is not published as a result. Its only real ingredients are the oracle
(12/12 for every arm) and the OUMV arm's measured payload (740 tokens, 5 round trips);
everything else on that scorecard was modelled. The detailed trace-based numbers remain
in [go/benchmark/BENCHMARK_REPORT.md](go/benchmark/BENCHMARK_REPORT.md) for methodology
review.
Run benchmark locally:
```bash
# Execute CLI benchmark runner
go run ./benchmark/cmd/main.go

# Run test suite
go test -v ./...
```

---

## Chi & Standard HTTP Adapters

For Chi:
```go
import chiAdapter "github.com/tokensafe-dev/tokensafe-backend/go/pkg/adapters/chi"

r := chi.NewRouter()
// ... register routes ...
chiAdapter.Mount(r, rt)
```

For standard `net/http`:
```go
import stdAdapter "github.com/tokensafe-dev/tokensafe-backend/go/pkg/adapters/stdhttp"

mux := http.NewServeMux()
// ... register routes ...
stdAdapter.Mount(mux, rt)
```

---

## Production Safety & Runtime Lifecycle Hardening

OUMV is an agent-native development interface designed to give AI coding agents deep introspection and verification capabilities (route catalogs, memory metrics, runtime causality, AST symbol extraction, and transactional dry-runs). Because development tools should never unintentionally expose application internals in production, `tokensafe-backend` enforces a strict **production fail-closed lifecycle**.

### Lifecycle by Environment

| Environment (`APP_ENV`) | Default State | Default Network Binding | Allowed Capabilities |
|---|---|---|---|
| `development` / `local` | **Enabled** | `127.0.0.1` (loopback) | Observe, Understand, Mutate, Verify |
| `test` | **Disabled** | None | Explicitly configurable |
| `staging` | **Disabled** | None | Read-only unless explicitly configured |
| `production` | **Disabled** | None | Disabled (endpoint unmounted, returns 404) |
| Missing / Unknown | **Disabled** | None | Treated strictly as production |

### Configuration Reference

```bash
# Local Development (enabled by default when APP_ENV=development)
export APP_ENV=development
export TOKENSAFE_OUMV_ENABLED=true
export TOKENSAFE_OUMV_HOST=127.0.0.1
export TOKENSAFE_OUMV_PATH=/__oumv/rpc

# Production (disabled by default; RPC endpoint returns 404)
export APP_ENV=production
export TOKENSAFE_OUMV_ENABLED=false

# Staging (explicit opt-in with mandatory session token)
export APP_ENV=staging
export TOKENSAFE_OUMV_ENABLED=true
export TOKENSAFE_OUMV_AUTH_TOKEN="your-secure-staging-token"
export TOKENSAFE_OUMV_OBSERVE=true
export TOKENSAFE_OUMV_UNDERSTAND=true
export TOKENSAFE_OUMV_MUTATE=false # Mutate disabled by default in staging/prod
```

### Security Rules Enforced at Startup

1. **Production Fail-Closed**: When `APP_ENV` is unset, missing, or unrecognized, the application defaults to `production` and OUMV is completely disabled. The endpoint `/__oumv/rpc` is not mounted, returning standard 404 responses.
2. **Non-Loopback Protection**: If `TOKENSAFE_OUMV_HOST` is bound to a non-loopback interface (e.g. `0.0.0.0`), startup will immediately fail unless `TOKENSAFE_OUMV_AUTH_TOKEN` is configured.
3. **No Wildcard CORS with Auth**: Wildcard CORS (`*`) is strictly rejected when authentication is required or in production environments.
4. **Path Traversal & Credential Redaction**: File inspection (`getSource`) and mutations (`applyEdit`) validate workspace boundaries, blocking path traversal (`../`) and access to sensitive files (`.env`, `.git`, SSH keys).
5. **Transport auth covers every binding (spec §12)**: a configured token is required on the RPC POST, the SSE stream and the WebSocket upgrade — a missing, invalid or wrong-scheme credential answers HTTP 401 with the `-32007` envelope *before dispatch*, and a non-loopback bind with no token answers 403 (`reason: token_required_non_loopback`). A configured token is never optional: the Node adapter used to accept an empty credential unless `requireAuth` was passed, and now fails closed. The refusal is certified by the fixtures' `authCases` family (`oumv-certify --auth`) and pinned by `python/tests/test_auth.py`, `go/pkg/transport/authz_test.go` and `node/test/auth.test.ts`.

Startup log in production:
```text
🔒 OUMV runtime disabled (environment: production)
🚀 TokenSafe backend application listening on :8080
```

---

## In-Memory Compiler Pre-Flight Guard

The `pkg/mutate` patcher blocks syntax and type errors before files are written to disk:

```go
import "github.com/tokensafe-dev/tokensafe-backend/pkg/mutate"

patcher := mutate.NewPatcher(".")

// Applies surgical AST replacement only if the resulting code compiles
result, err := patcher.ApplySymbolPatch(
	"handlers/orders.go",
	"CancelOrder",
	newFunctionCode,
	"sha256:current_hash",
)
if err != nil {
	// Rejection reasons: OCC version mismatch, syntax error, or unresolvable types
	log.Printf("Edit rejected by compiler guard: %v", err)
}
```

---

## Database Transactional Sandbox

Execute dry-run mutations with guaranteed rollback:

```go
import "github.com/tokensafe-dev/tokensafe-backend/pkg/db"

sandbox := db.NewSandbox(sqlDB)

// Runs inside a transaction and ALWAYS executes tx.Rollback()
report, err := sandbox.ExecuteDryRun(ctx, func(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, "INSERT INTO orders (id, total) VALUES ($1, $2)", "ord_test", 150.0)
	return err
})
// DB state is 100% pristine; report contains duration and query logs
```

---

## In-Memory Test Client (`app.Simulate`)

Test handlers directly in memory without starting a background HTTP server or binding network ports:

```go
res := app.Simulate(http.MethodPost, "/api/v1/orders", CreateOrderReq{
	Item:   "Laptop",
	Amount: 1299.99,
})

if res.Code != http.StatusCreated {
	t.Fatalf("expected 201 Created, got %d: %s", res.Code, res.String())
}

var resp OrderResp
if err := res.JSON(&resp); err != nil {
	t.Fatalf("failed to decode JSON: %v", err)
}
```

---

## Scaffolding with TokenSafe CLI

Scaffold a greenfield Go backend service with TokenSafe out-of-the-box:

```bash
tokensafe create my-service --template go
```

---

## License

| Path | License |
| :--- | :--- |
| Repository and Go module (`go/`) | [Apache-2.0](LICENSE) |
| Python runtime (`python/`) | [Apache-2.0](python/LICENSE) from 0.5.0 |

The Python runtime shipped under MIT up to and including 0.3.0. That grant is irrevocable for
copies already distributed — the last MIT commit stays forkable under MIT forever — but new
versions ship under Apache-2.0, matching the Go module and the rest of the stack. Versions
≤0.3.0 keep their MIT metadata; nothing is back-dated.

"OUMV Certified" and the certification badge are marks and are not licensed by either grant —
see [TRADEMARK.md](https://github.com/tokensafe-dev/oumv/blob/main/TRADEMARK.md).
