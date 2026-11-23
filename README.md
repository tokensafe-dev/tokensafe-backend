# TokenSafe Backend (`tokensafe-backend`)
### Go Reference Implementation of the OUMV Protocol (UARP)
**The Universal Agent-Runtime Protocol: Observe · Understand · Mutate · Verify**

[![Go Report Card](https://goreportcard.com/badge/github.com/tokensafe-dev/tokensafe-backend)](https://goreportcard.com/report/github.com/tokensafe-dev/tokensafe-backend)
[![Protocol Specification](https://img.shields.io/badge/OUMV-v1.0--draft-orange)](https://github.com/tokensafe-dev/oumv/blob/main/spec/SPEC.md)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

> `tokensafe-backend` transforms any Go backend application into an agent-ready, token-efficient live runtime. It implements the **OUMV (Observe, Understand, Mutate, Verify)** standard, allowing autonomous AI agents to inspect, reason about, safely modify, and verify backend services in sub-milliseconds without context waste.

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

import "github.com/tokensafe-dev/tokensafe-backend"

type CreateOrderReq struct {
    Item   string  `json:"item"`
    Amount float64 `json:"amount"`
}

type OrderResp struct {
    ID     string `json:"id"`
    Status string `json:"status"`
}

func main() {
    app := tokensafe.New()

    tokensafe.Post(app, "/api/v1/orders", func(c *tokensafe.Context, req CreateOrderReq) (*OrderResp, error) {
        return &OrderResp{ID: "ord-1", Status: "created"}, nil
    })

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

TokenSafe Backend (Go) achieves an **Autonomous Efficiency Ratio (AER) of 24.6x** over standard Vanilla Gin development:
- **-96% Tokens**: 1,380 vs 33,960 tokens
- **-83% Turns**: 1 turn to green vs 6 turns
- **Zero DB Pollution**: In-memory transactional dry-run simulation with automatic rollback (0 dirty records vs 18)
- **-94% Cost**: $0.0092 vs $0.1536 run cost
- **In-Memory Compiler Guard**: Non-compiling edits blocked in-memory before disk corruption

See full report: [BENCHMARK_REPORT.md](benchmark/BENCHMARK_REPORT.md).

Run benchmark locally:
```bash
go run ./benchmark/cmd/main.go
```

---

## License

MIT © 2026 TokenSafe & Contributors
