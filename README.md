# TokenSafe Backend (`tokensafe-backend`)
### Go Reference Implementation of the OUMV Protocol (UARP)
**The Universal Agent-Runtime Protocol: Observe · Understand · Mutate · Verify**

[![Go Report Card](https://goreportcard.com/badge/github.com/tokensafe-dev/tokensafe-backend)](https://goreportcard.com/report/github.com/tokensafe-dev/tokensafe-backend)
[![Protocol Specification](https://img.shields.io/badge/OUMV-v1.0--draft-orange)](https://github.com/tokensafe-dev/oumv/blob/main/spec/SPEC.md)
[![Agent Usability Benchmark](https://img.shields.io/badge/AUB_Benchmark-AER_24.6x-10B981)](benchmark/BENCHMARK_REPORT.md)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

> `tokensafe-backend` transforms any Go backend application into an agent-ready, token-efficient live runtime. It implements the **OUMV (Observe, Understand, Mutate, Verify)** standard, allowing autonomous AI agents to inspect, reason about, safely modify, and verify backend services in sub-milliseconds without context waste.

---

## Highlights

- **⚡ 24.6x Autonomous Efficiency Ratio (AER)**: Cuts token consumption by **96%** (1,380 vs 33,960 tokens) and agent iterations by **83%** (1 turn to green vs 6 turns).
- **🛡️ In-Memory Compiler Pre-Flight Guard**: AST-guided symbol edits validated in memory via `go/parser` and `go/types` in <5ms—blocking non-compiling edits before touching the disk.
- **🔄 Zero Database Pollution**: In-memory transactional dry-runs (`net/http/httptest` + `pkg/db`) execute with guaranteed `ROLLBACK`, eliminating state contamination across test cycles.
- **🔍 Progressive Context Disclosure (L0–L4)**: Replaces 3,000+ token file crawls with compact pulse (`L0`, ~20 tokens), endpoint surfaces (`L1`, ~110 tokens), and reflected schemas (`L2`, ~60 tokens).
- **🧩 Dual-Layer Architecture**: Use the native type-safe framework (`tokensafe.New()`) for greenfield services, or mount compatibility adapters (`Gin`, `Chi`, `net/http`) in 2 lines of code.

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

	"github.com/tokensafe-dev/tokensafe-backend"
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
    ginAdapter "github.com/tokensafe-dev/tokensafe-backend/pkg/adapters/gin"
    "github.com/tokensafe-dev/tokensafe-backend/pkg/oumv"
)

func main() {
    r := gin.Default()
    // ... your existing routes ...

    rt := oumv.NewRuntime(oumv.Config{Name: "my-service", Handler: r})
    ginAdapter.Mount(r, rt) // Auto-discovers routes and mounts /__oumv/rpc

    r.Run(":8080")
}
```

---

## Installation

```bash
go get github.com/tokensafe-dev/tokensafe-backend
```

---

## Agent Usability Benchmark (AUB - Go)

In our standardized **Agent Usability Benchmark (AUB)** evaluating an e-commerce order processing API against a 12-assertion Behavioral Test Oracle:

$$\text{AER} = \frac{\text{FYpT}_{\text{TokenSafe}}}{\text{FYpT}_{\text{Vanilla}}} = \frac{8.6957}{0.3534} = \mathbf{24.6x}$$

### Comparative Scorecard

| Metric | Baseline: Vanilla Gin | Target: TokenSafe Backend (Go) | Delta / Advantage |
| :--- | :---: | :---: | :---: |
| **Autonomous Efficiency Ratio (AER)** | $1.0\times$ | **$24.6\times$** | **+2361% productivity** |
| **Functional Yield per 1k Tokens (FYpT)** | `0.3534` | **`8.6957`** | **+2361% yield** |
| **Total Tokens** | `33,960` | **`1,380`** | **-96%** |
| • Input Tokens | `29,650` | `960` | -96% |
| • Output Tokens | `4,310` | `420` | -90% |
| **Agent Iterations (Turns)** | `6` | **`1`** | **-83% (1 turn to green)** |
| **Wall Clock Execution** | `44.70s` | **`2.65s`** | **-94%** |
| **Database State Pollution (Dirty Records)** | `18` | **`0`** | **-100% (Zero-Pollution)** |
| **Diagnostics Errors Encountered** | `4` | **`0`** | **4 fewer errors** |
| **Regressions Encountered** | `1` | **`0`** | **1 fewer regressions** |
| **Self-Recovery Rate (SRR)** | `25%` | **`100%`** | **Superior** |
| **Estimated Run Cost (USD)** | `$0.1536` | **`$0.0092`** | **-94% cost** |
| **Cost Yield (Assertions / $)** | `78.13` | **`1304.35`** | **16.7x yield/$** |

See full report: [BENCHMARK_REPORT.md](benchmark/BENCHMARK_REPORT.md).

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
import chiAdapter "github.com/tokensafe-dev/tokensafe-backend/pkg/adapters/chi"

r := chi.NewRouter()
// ... register routes ...
chiAdapter.Mount(r, rt)
```

For standard `net/http`:
```go
import stdAdapter "github.com/tokensafe-dev/tokensafe-backend/pkg/adapters/stdhttp"

mux := http.NewServeMux()
// ... register routes ...
stdAdapter.Mount(mux, rt)
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

MIT © 2026 TokenSafe & Contributors
