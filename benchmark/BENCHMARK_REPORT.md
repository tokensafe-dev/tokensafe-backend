# Backend Agent Usability Benchmark (AUB) Report (Go)

> **Scenario**: `order-processing-api`  
> **Timestamp**: `2026-11-23T10:09:56Z`  
> **Evaluation Mode**: Behavioral Test Oracle (12 Functional Acceptance Assertions)

---

## Executive Summary

**TokenSafe Backend (Go) achieved an Autonomous Efficiency Ratio (AER) of 24.6x over Vanilla Gin.**

$$\\text{AER} = \\frac{\\text{FYpT}_{\\text{TokenSafe}}}{\\text{FYpT}_{\\text{Vanilla}}} = \\frac{8.6957}{0.3534} = \\mathbf{24.6x}$$

- **Token Economy**: **96%** reduction in total token consumption (1,380 vs 33,960).
- **Autonomous Velocity**: **83%** fewer agent turns to reach complete 12/12 passing status (1 vs 6 turns).
- **Database Safety**: **100%** reduction in state pollution (0 vs 18 dirty records).
- **Code Stability**: **100%** fewer regressions during iterative feature development (0 vs 1 regressions).

---

## Comparative Scorecard

| Metric | Baseline: Vanilla Gin | Target: TokenSafe Backend (Go) | Delta / Advantage |
| :--- | :---: | :---: | :---: |
| **Autonomous Efficiency Ratio (AER)** | $1.0\\times$ | **$24.6\\times$** | **+2361% productivity** |
| **Functional Yield per 1k Tokens (FYpT)** | `0.3534` | **`8.6957`** | **+2361% yield** |
| **Total Tokens** | `33,960` | **`1,380`** | **-96%** |
| • Input Tokens | `29,650` | `960` | -96% |
| • Output Tokens | `4,310` | `420` | -90% |
| **Agent Iterations (Turns)** | `6` | **`1`** | **-83%** |
| **Wall Clock Execution** | `44.70s` | **`2.65s`** | **-94%** |
| **Database State Pollution (Dirty Records)** | `18` | **`0`** | **-100% (Zero-Pollution)** |
| **Diagnostics Errors Encountered** | `4` | **`0`** | **4 fewer errors** |
| **Regressions Encountered** | `1` | **`0`** | **1 fewer regressions** |
| **Self-Recovery Rate (SRR)** | `25%` | **`100%`** | **Superior** |
| **Estimated Run Cost (USD)** | `$0.1536` | **`$0.0092`** | **-94% cost** |
| **Cost Yield (Assertions / $)** | `78.13` | **`1304.35`** | **16.7x yield/$** |

---

## Functional Verification Breakdown (12 Assertions)

```
[x] TC01_INITIAL_CATALOG: Initial catalog inspection & inventory check (0.03ms)
[x] TC02_CREATE_PRODUCT: Create new product with validation (0.03ms)
[x] TC03_REJECT_INVALID_PRODUCT: Reject invalid product (negative price / validation failure) (0.01ms)
[x] TC04_CREATE_ORDER_VALID: Create order with stock calculation & decrement (0.03ms)
[x] TC05_REJECT_INSUFFICIENT_STOCK: Insufficient stock rejection (0.01ms)
[x] TC06_REJECT_DUPLICATE_IDEMPOTENCY: Idempotent duplicate order key rejection (0.01ms)
[x] TC07_QUERY_ORDER_BY_ID: Query order by ID & verify item structure (0.01ms)
[x] TC08_PAY_ORDER: Pay order transition (PENDING -> PAID) (0.02ms)
[x] TC09_SHIP_ORDER: Ship order transition (PAID -> SHIPPED) (0.01ms)
[x] TC10_CANCEL_ORDER_AND_RESTOCK: Cancel order and verify stock replenishment (0.04ms)
[x] TC11_REJECT_CANCEL_SHIPPED_ORDER: Reject cancellation of shipped order (0.01ms)
[x] TC12_DRY_RUN_SANDBOX_ROLLBACK: Dry-run sandbox verification & rollback safety (0.02ms)
```

Both sandboxes verified at **100% functional parity (12/12 assertions passed)**.

---

## Architectural Analysis: Why TokenSafe Backend (Go) Wins

### 1. In-Memory Compiler Pre-flight Guard vs Disk-Corrupting Builds
In traditional Go development, an agent modifying handlers frequently introduces syntax errors, mismatched types, or unused imports. Running `go test` or `go build` forces a full compiler pass (2–4 seconds) and leaves broken code on disk.
With TokenSafe Backend:
- **`pkg/mutate/patcher.go`**: Executes in-memory AST verification using `go/parser` and `go/types` in <5ms.
- **Optimistic Concurrency Control (OCC)**: Validates target version hashes to eliminate concurrent edits and line-drift.
- **Fail-Safe Application**: Non-compiling edits are blocked before touching the filesystem.

### 2. Transactional Dry-Run Sandbox with Guaranteed Rollback
When an agent tests endpoints using standard `go test` or terminal `curl`, test records persist in the database. Subsequent test runs crash due to primary key collisions (`UNIQUE constraint failed`) or missing fixtures.
TokenSafe's `pkg/db/sandbox.go` and `oumv_verify`:
- **Guaranteed Rollback**: Evaluates mutations within an isolated transaction that automatically rolls back upon request completion.
- **Zero Database Pollution**: Leaves 0 dirty records across all test turns.
- **Sub-2ms Verification**: In-memory `httptest.ResponseRecorder` replaces slow subprocess execution.

### 3. Progressive Observation (L0-L4) vs Codebase Crawling
Standard agents burn 3,000+ tokens per turn reading nested structs, route setups, and middleware files.
TokenSafe UARP provides progressive context disclosure:
- **L0 (Pulse)**: Health check & route count in ~20 tokens.
- **L1 (Surface)**: Registered endpoints & DTO models in ~110 tokens.
- **L2 (Contract)**: Exact reflected struct schemas in ~60 tokens.

### 4. Runtime Causality Graph vs Blind Stack Traces
When an endpoint returns a `409 Conflict` or `400 Bad Request`, TokenSafe's `oumv_understand(query="causality")` immediately maps the causal chain:
`Route -> DTO Validation -> Stock Check -> SQL Transaction -> Response`.
The agent fixes the exact defect in 1 turn without trial-and-error logging.
