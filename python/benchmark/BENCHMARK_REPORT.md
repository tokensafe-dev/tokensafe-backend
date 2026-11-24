# Backend Agent Usability Benchmark (AUB) Report

> **Scenario**: `order-processing-api`  
> **Timestamp**: `2026-11-23T05:11:50.456023+00:00`  
> **Evaluation Mode**: Behavioral Test Oracle (12 Functional Acceptance Assertions)

---

## Executive Summary

**TokenSafe UARP Backend achieved an Autonomous Efficiency Ratio (AER) of 23.5x over Vanilla FastAPI.**

$$\text{AER} = \frac{\text{FYpT}_{\text{TokenSafe}}}{\text{FYpT}_{\text{Vanilla}}} = \frac{8.4507}{0.3596} = \mathbf{23.5x}$$

- **Token Economy**: **96%** reduction in total token consumption (1,420 vs 33,370).
- **Autonomous Velocity**: **83%** fewer agent turns to reach complete 12/12 passing status (1 vs 6 turns).
- **Database Safety**: **100%** reduction in state pollution (0 vs 18 dirty records).
- **Code Stability**: **100%** fewer regressions during iterative feature development (0 vs 1 regressions).

---

## Comparative Scorecard

| Metric | Baseline: Vanilla FastAPI | Target: TokenSafe UARP | Delta / Advantage |
| :--- | :---: | :---: | :---: |
| **Autonomous Efficiency Ratio (AER)** | $1.0\times$ | **$23.5\times$** | **+2250% productivity** |
| **Functional Yield per 1k Tokens (FYpT)** | `0.3596` | **`8.4507`** | **+2250% yield** |
| **Total Tokens** | `33,370` | **`1,420`** | **-96%** |
| • Input Tokens | `29,000` | `980` | -97% |
| • Output Tokens | `4,370` | `440` | -90% |
| **Agent Iterations (Turns)** | `6` | **`1`** | **-83%** |
| **Wall Clock Execution** | `46.50s` | **`2.85s`** | **-94%** |
| **Database State Pollution (Dirty Records)** | `18` | **`0`** | **-100% (Zero-Pollution)** |
| **Diagnostics Errors Encountered** | `4` | **`0`** | **4 fewer errors** |
| **Regressions Encountered** | `1` | **`0`** | **1 fewer regressions** |
| **Self-Recovery Rate (SRR)** | `25%` | **`100%`** | **Superior** |
| **Estimated Run Cost (USD)** | `$0.1526` | **`$0.0095`** | **-94% cost** |
| **Cost Yield (Assertions / $)** | `78.64` | **`1263.16`** | **16.1x yield/$** |

---

## Functional Verification Breakdown (12 Assertions)

```
[x] TC01_INITIAL_CATALOG: Initial catalog inspection & inventory check (1.71ms)
[x] TC02_CREATE_PRODUCT: Create new product with validation (1.64ms)
[x] TC03_REJECT_INVALID_PRODUCT: Reject invalid product (negative price / validation failure) (0.87ms)
[x] TC04_CREATE_ORDER_VALID: Create order with stock calculation & decrement (6.17ms)
[x] TC05_REJECT_INSUFFICIENT_STOCK: Insufficient stock rejection (1.23ms)
[x] TC06_REJECT_DUPLICATE_IDEMPOTENCY: Idempotent duplicate order key rejection (0.92ms)
[x] TC07_QUERY_ORDER_BY_ID: Query order by ID & verify item structure (1.69ms)
[x] TC08_PAY_ORDER: Pay order transition (PENDING -> PAID) (2.2ms)
[x] TC09_SHIP_ORDER: Ship order transition (PAID -> SHIPPED) (1.71ms)
[x] TC10_CANCEL_ORDER_AND_RESTOCK: Cancel order and verify stock replenishment (6.66ms)
[x] TC11_REJECT_CANCEL_SHIPPED_ORDER: Reject cancellation of shipped order (0.79ms)
[x] TC12_DRY_RUN_SANDBOX_ROLLBACK: Dry-run sandbox verification & rollback safety (3.68ms)
```

Both sandboxes verified at **100% functional parity (12/12 assertions passed)**.

---

## Architectural Analysis: Why TokenSafe UARP Backend Wins

### 1. Progressive Observation (L0-L4) vs Blind File Crawling
In traditional backend development, an agent must repeatedly read full route files (`app.py`), database models (`database.py`), and DTO definitions (`models.py`) to grasp request/response contracts—costing 3,000+ tokens per inspection.
With TokenSafe UARP:
- **L0 (Pulse)**: Verifies runtime health and route counts in ~20 tokens.
- **L1 (Surface)**: Delivers all registered endpoints and DTO schema names in ~120 tokens.
- **L2 (Contract)**: Retrieves exact JSON Schemas for specific request/response targets in ~60 tokens.

### 2. In-Memory Transactional Sandbox with Automatic Dry-Run Rollback
When an agent tests endpoints using `curl` or subprocess `pytest`, every mutation writes persistent rows into SQLite/Postgres. In subsequent turns, foreign key constraints or unique idempotency keys collide (`IntegrityError: UNIQUE constraint failed`), causing cascading false failures.
TokenSafe UARP's `oumv_verify` and `DBSandbox` execute mutations inside an isolated transactional proxy that automatically calls `rollback()` at the end of simulation:
- **Zero Database Pollution**: Clean state across all test turns.
- **Sub-15ms Latency**: In-memory ASGI dispatch replaces slow 4-second `pytest` subprocess spawning.

### 3. Runtime Causality Graph vs Opaque Stack Traces
When a complex order workflow fails with a `422 Unprocessable Entity` or `400 Bad Request`, standard agents face opaque stack traces that reveal little about internal data flow.
TokenSafe's `oumv_understand(query="causality", target="POST /api/orders")` provides instant visibility into the exact dependency chain:
`Route -> Pydantic Schema Validation -> Inventory Query -> DB Transaction -> Response Model`.
The agent diagnoses requirements immediately without trial-and-error logging.

### 4. OCC Atomic Mutation Safety
Source modifications are applied through AST-guided symbol edits with Optimistic Concurrency Control (OCC), preventing syntax line-drift, missing imports, and broken indentation.
