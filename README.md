# TokenSafe Backend (`tokensafe-backend`)
### Unified Backend Implementations of the OUMV Protocol (UARP)
**The Universal Agent-Runtime Protocol: Observe · Understand · Mutate · Verify**

[![Go Report Card](https://goreportcard.com/badge/github.com/tokensafe-dev/tokensafe-backend/go)](https://goreportcard.com/report/github.com/tokensafe-dev/tokensafe-backend/go)
[![Protocol Specification](https://img.shields.io/badge/OUMV-v1.4--canonical-orange)](https://github.com/tokensafe-dev/oumv/blob/main/spec/SPEC.md)
[![OUMV conformance](https://img.shields.io/badge/OUMV-certified_L3_%C2%B7_6_adapters-10B981)](#certified-adapters)
[![License: Apache-2.0](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)

> `tokensafe-backend` is the unified backend repository for the **OUMV (Observe, Understand, Mutate, Verify)** protocol. It houses backend runtime instrumentation and adapters across major server-side technologies:
> - **Go (`go/`)**: Native Go runtime & adapters for Gin, Chi, and `net/http` (`github.com/tokensafe-dev/tokensafe-backend/go`); `oumv.ListenAndServe` writes the §9.2 discovery descriptor with the real bound port.
> - **Python (`python/`)**: Python runtime & FastAPI adapter, published as `tokensafe-backend-python`; HTTP + SSE + WebSocket + stdio.
> - **Node.js (`node/`)**: `tokensafe-backend-node` — Express and Fastify adapters over HTTP, SSE, WebSocket and stdio, certified Level 3 (20/20).

---

## Highlights

- **✅ Certified OUMV adapters (spec §10)**: Gin, Chi, `net/http`, FastAPI, Express and Fastify each pass the language-neutral conformance suite over their real transport — Level 3, reports committed under [`certification/`](certification/) and reproduced in CI. Exact counts are generated below.
- **🛡️ In-Memory Compiler Pre-Flight Guard**: AST-guided symbol edits validated in memory via `go/parser` and `go/types` in <5ms—blocking non-compiling edits before touching the disk.
- **🔄 Zero Database Pollution**: In-memory transactional dry-runs (`net/http/httptest` + `pkg/db`) execute with guaranteed `ROLLBACK`, eliminating state contamination across test cycles.
- **🔍 Progressive Context Disclosure (L0–L4)**: Replaces 3,000+ token file crawls with compact pulse (`L0`, ~20 tokens), endpoint surfaces (`L1`, ~110 tokens), and reflected schemas (`L2`, ~60 tokens).
- **🧩 Dual-Layer Architecture**: Use the native type-safe framework (`tokensafe.New()`) for greenfield services, or mount compatibility adapters (`Gin`, `Chi`, `net/http`) in 2 lines of code.

---

## Certification (spec §10)

<!-- certification:start -->
**6 adapters certified at Level 3** — 33 language-neutral cases (`fixtureVersion` 7) over the transport an agent actually uses. Generated from `certification/*.json` by `scripts/render_certification.py`; CI fails when this block no longer matches the reports.

| Adapter | Framework | Transport exercised | Level | Cases | Report |
| :--- | :--- | :--- | :--- | :--- | :--- |
| `go-chi` | chi | HTTP `POST /__oumv/rpc` | **3** | 33/33 | [certification/go-chi.json](certification/go-chi.json) |
| `go-gin` | Gin | HTTP `POST /__oumv/rpc` | **3** | 33/33 | [certification/go-gin.json](certification/go-gin.json) |
| `go-stdhttp` | net/http | HTTP `POST /__oumv/rpc` | **3** | 33/33 | [certification/go-stdhttp.json](certification/go-stdhttp.json) |
| `node-express` | Express | Node `POST /__oumv/rpc` | **3** | 33/33 | [certification/node-express.json](certification/node-express.json) |
| `node-fastify` | Fastify | Node `POST /__oumv/rpc` | **3** | 33/33 | [certification/node-fastify.json](certification/node-fastify.json) |
| `python-fastapi` | FastAPI | ASGI `POST /__oumv/rpc` | **3** | 33/33 | [certification/python-fastapi.json](certification/python-fastapi.json) |
<!-- certification:end -->

Levels are cumulative and a skipped case never counts as a pass — a missing capability caps the claim instead of inflating it. The same fixtures also run over **stdio** (§2.5) against the Node and Python runtimes: [`certification/transports/node-stdio.json`](certification/transports/node-stdio.json) and [`python-stdio.json`](certification/transports/python-stdio.json), both Level 3, 20/20. Regenerate locally:

```bash
# Go adapters (writes certification/*.json)
cd go && CERT_OUT_DIR=../certification go test ./pkg/adapters -run TestAdapterCertification

# Python/FastAPI — HTTP and stdio
cd python && CERT_OUT_DIR=../certification .venv/bin/python -m pytest -q tests/test_conformance.py tests/test_stdio.py

# Node adapters — HTTP and stdio
cd node && CERT_OUT_DIR=../certification OUMV_REPO=../../agent-oumv pnpm test
```

---


## The Four Pillars for Backend

1. **Observe (L0–L4)**: Progressive context disclosure. From service pulse (`L0`, ~30 tokens) and route surface (`L1`, ~150 tokens) to DTO schemas (`L2`), AST-sliced source code with OCC tokens (`L3`), and in-flight traces (`L4`).
2. **Understand**: Live causality analysis (*"Why did this request fail with 403?"*) and static type-impact analysis using Go AST (`go/types`).
3. **Mutate**: Optimistic Concurrency Control (OCC) with in-memory compiler pre-flight guards (`go/parser`, `go/types`). Changes that don't compile are rejected before touching the filesystem.
4. **Verify**: In-memory action verification (`net/http/httptest`) and transactional DB dry-runs with guaranteed `ROLLBACK`, returning structured diffs and 4-metric latency breakdowns in <5ms.

---

## Architecture: Native Core + Compatibility Adapters

`tokensafe-backend` provides a dual-layer architecture:

1. **Native Agent-First Core Framework**: For new services, providing type-safe generic routing, automatic DTO reflection, transactional DB sandboxes, and zero-configuration OUMV live RPC.
2. **Compatibility Adapters (`pkg/adapters/*`)**: For existing Go services built on Gin, Chi, or standard `net/http`, mounting OUMV introspection in 2 lines of code.

---

## Quickstart (Native Framework)

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

## Installation

### Go Runtime (`go/`)
```bash
go get github.com/tokensafe-dev/tokensafe-backend/go@v0.4.0
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

---

## Benchmarks — what is measured and what is not

| Run | Result | Evidence |
| :--- | :--- | :--- |
| Conformance | 6 adapters at Level 3, 25/25 each | `certification/*.json`, regenerated in CI |
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
file/search/command tools, `tokensafe` the product's own MCP server (`scoped_search` →
`smart_read_file` → `apply_edits` with inline `verify="auto"`), `oumv` the shipped Go adapter
(`observe L3` → `getSource` → `applyEdit` → `verify`).

| Arm | Solved | Mean steps | Mean input | Mean output | Cost/run |
| :--- | ---: | ---: | ---: | ---: | ---: |
| `baseline` | 15/15 | 4.5 | 19,538 | 1,599 | $0.0035 |
| `tokensafe` | 15/15 | 4.7 | 17,616 | 750 | $0.0025 |
| `oumv` | 15/15 | 4.3 | **12,323** | 1,735 | $0.0032 |
| **tokensafe vs baseline** | — | +4.5% | **−9.8%** | **−53.1%** | **−28.7%** |
| **oumv vs baseline** | — | −4.5% | **−36.9%** | +8.5% | −10.8% |

The two arms trade opposite sides of the bill: TokenSafe sends a hunk per edit and reads more
(`apply_edits` + `smart_read_file` anchors), OUMV reads the handler once from `observe L3` but
resends the whole file on `applyEdit` — which costs ~2.3× the output tokens, and output is the
expensive side ($1.10/Mtok vs $0.07–0.27 for input). A `mode=patch` on the wire would close it.

Method, per-task table, comparison runs and limits:
[OUMV_AGENT_MATRIX_V4.md](go/benchmark/OUMV_AGENT_MATRIX_V4.md). Generated from the run
artifacts by `go/benchmark/render_agent_matrix.py`; the earlier losing row is corrected in
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
