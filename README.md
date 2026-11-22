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

## Architecture & Transports

- **In-Process HTTP Middleware**: Mount `/__oumv/rpc` on standard `net/http`, Gin, Chi, or Echo routers.
- **Model Context Protocol (MCP)**: Run standalone `oumv-mcp` over `stdio` to bridge directly with Claude Code, Cursor, Antigravity, and other coding agents.

---

## Installation

```bash
go get github.com/tokensafe-dev/tokensafe-backend
```

---

## License

MIT © 2026 TokenSafe & Contributors
