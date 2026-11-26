# OUMV agent matrix v3 (Go, measured)

> Measured with `tokensafe bench-oumv --target go --tasks all --arms baseline,tokensafe,oumv
> --runs 3 --budget 5`. Every token and cost number comes from the provider's `usage`
> payload and a real 12-assertion oracle re-run after every step. No replay, no estimates.

This file supersedes the 2026-11-26 v2 run, whose `tokensafe` row measured an incomplete
toolkit (see the correction section of [`OUMV_AGENT_MATRIX.md`](OUMV_AGENT_MATRIX.md)).
Two runs are published because the second one isolates a schema change; both artifacts are
in [`runs/`](runs/).

## Method

| | |
| :--- | :--- |
| Model | `deepseek-chat`, temperature 0 |
| Prices (snapshot 2026-11-26) | input $0.27 / Mtok (cache miss), $0.07 (hit), output $1.10 |
| Runs | 3 per task per arm (27 sessions per run) |
| Oracle | `go test ./benchmark -run TestGroundTruthOracleVanilla` (12 assertions), after every step |
| Sandbox | throwaway copy of `tokensafe-backend/go` with one deliberate regression per task |
| Arms | `baseline` file/command tools; tokenSafe = the product's MCP server; `oumv` = the shipped Go adapter |

`tokensafe` runs the product's own loop: `scoped_search` → `smart_read_file` (symbol or
endpoint target) → `apply_edits` with inline `verify="auto"`, i.e. edit and test verdict in
one call. `oumv` runs `observe L3` → `getSource` → `applyEdit` → `verify`.

## Run A — commit `86d38357` (schema before the trim)

| Arm | Runs | Solved | Mean steps | Mean input | Mean output | Cost/run |
| :--- | ---: | ---: | ---: | ---: | ---: | ---: |
| `baseline` | 9 | 9/9 | 4.6 | 19,186 | 1,587 | $0.0034 |
| `tokensafe` | 9 | 9/9 | 4.6 | 18,555 | 631 | $0.0028 |
| `oumv` | 9 | 9/9 | 3.7 | 9,429 | 1,513 | $0.0026 |

**vs baseline:** tokensafe -3.3% input, -17.6% cost,
+0.0% steps · oumv -50.9% input,
-24.7% cost, -19.5% steps.

## Run B — commit `961979ad` (tool schema cut 35%)

| Arm | Runs | Solved | Mean steps | Mean input | Mean output | Cost/run |
| :--- | ---: | ---: | ---: | ---: | ---: | ---: |
| `baseline` | 9 | 9/9 | 4.7 | 21,532 | 1,614 | $0.0037 |
| `tokensafe` | 9 | 9/9 | 4.4 | 17,183 | 652 | $0.0026 |
| `oumv` | 9 | 9/9 | 4.0 | 10,848 | 1,570 | $0.0027 |

**vs baseline:** tokensafe -20.2% input, -30.2% cost,
-4.8% steps · oumv -49.6% input,
-25.6% cost, -14.3% steps.

### Per task (run B)

| Task | baseline input | tokensafe input | oumv input | baseline steps | tokensafe steps | oumv steps |
| :--- | ---: | ---: | ---: | ---: | ---: | ---: |
| `product-create-status` | 13,176 | 7,284 | 6,587 | 3.7 | 3.0 | 3.0 |
| `order-conflict-status` | 20,103 | 29,267 | 6,917 | 5.0 | 5.7 | 3.0 |
| `product-validation-status` | 31,318 | 14,997 | 19,041 | 5.3 | 4.7 | 6.0 |

## What the A/B does and does not show

The schema trim is exact by construction — 7 tools went from 7,919 to 5,170 chars, i.e.
**2,400 → 1,615 tokens re-sent in every request**. This matrix cannot attribute the A/B
difference to it: the arm declared its own 3,165-char stand-in schema instead of the
server's `tools/list`, so the product's schema never reached the model.

The noise floor is visible in the two runs of the *unchanged* arms: baseline input
+12.2% and oumv +15.1% between runs on identical
code. At 3 runs per cell a ~7% effect is not resolvable; the honest reading is
"tokensafe moved from 3.5% below baseline to 20% below, both runs 9/9".

Since run B the arm *does* show the model the shipped schema (`build_arm` in
`tokensafe/src/bench/go_target.py`), which makes the arm's schema cost larger and real —
so run B understates the product's schema tax rather than overstating it. A run on that
configuration is the next measurement.

## Limits

- One model (`deepseek-chat`, temperature 0), one vendor, three tasks, n=3 per cell.
- Sandboxes are small (a 155-line Go app); discovery is cheaper than in a large repo, so
  the tool-path savings here are a floor.
- The `tokensafe` arm pays a fixed MCP schema in every request; the `oumv` arm's five tools
  are declared by the harness and are smaller. Neither arm's schema is a product marketing
  claim — it is a cost both sides pay.
- `oumv/verify` starts a fresh app per call by design, so the stateful task is confirmed
  with the oracle in all three arms.
- Per-task variance is large at n=3: one `tokensafe` run on `order-conflict-status` took 9
  steps / 58.8k input after falling back to raw `read_file` calls, and it alone is most of
  that arm's mean. Per-run records are in the JSON; quote the per-task table, not just the
  arm mean.
