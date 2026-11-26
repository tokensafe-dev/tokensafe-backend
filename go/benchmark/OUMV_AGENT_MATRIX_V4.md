# OUMV agent matrix (Go, measured 2026-11-27)

> Generated from the run artifact by `render_agent_matrix.py` — do not edit by hand.
> Every token and cost number comes from the provider's `usage` payload and a real
> 12-assertion oracle re-run after every agent step. No replay, no estimates.

## Method

| | |
| :--- | :--- |
| Model | `deepseek-chat`, temperature 0 |
| Runs | 5 per task per arm (45 sessions, $0.1383 spent) |
| Harness commit | `689d8210` |
| Oracle | `go test ./benchmark -run TestGroundTruthOracleVanilla` (12 assertions), after every step |
| Sandbox | throwaway copy of `tokensafe-backend/go` with one deliberate regression per task |
| Arms | `baseline` file/command tools; `tokensafe` the product's MCP server; `oumv` the shipped Go adapter |

`tokensafe` runs the product's own loop: `scoped_search` → `smart_read_file` (symbol or
endpoint target) → `apply_edits` with inline `verify="auto"`, so the edit and its test
verdict come back in one call. `oumv` runs `observe L3` → `getSource` → `applyEdit` →
`verify`.

## Results

| Arm | Runs | Solved | Mean steps (min–max) | Mean input (cache-hit share) | Mean output | Cost/run |
| :--- | ---: | ---: | ---: | ---: | ---: | ---: |
| `baseline` | 15 | 15/15 | 4.5 (3–5) | 19,538 (89%) | 1,599 | $0.0035 |
| `tokensafe` | 15 | 15/15 | 4.7 (4–8) | 17,616 (87%) | 750 | $0.0025 |
| `oumv` | 15 | 15/15 | 4.3 (3–7) | 12,323 (84%) | 1,735 | $0.0032 |

**tokensafe vs baseline:** -9.8% input, -28.7% cost, +4.5% steps.
**oumv vs baseline:** -36.9% input, -10.8% cost, -4.5% steps.

### Per task (means)

| Task | baseline input | tokensafe input | oumv input | baseline steps | tokensafe steps | oumv steps |
| :--- | ---: | ---: | ---: | ---: | ---: | ---: |
| `product-create-status` | 12,743 | 14,456 | 10,647 | 3.6 | 4.2 | 4.0 |
| `order-conflict-status` | 20,358 | 15,407 | 7,118 | 5.0 | 4.0 | 3.0 |
| `product-validation-status` | 25,513 | 22,984 | 19,204 | 4.8 | 5.8 | 5.8 |

### Why the two arms differ on output tokens

`oumv` emits 1,735 output tokens per run against `tokensafe`'s 750 (2.3x). The protocol's mutation unit is the resource: `applyEdit` sends the new file (or one whole declaration with `mode=symbol`) and checks the OCC version against it, so a one-line fix still costs a full file on the wire. `apply_edits` sends the hunk plus its target snippet. Both pay it back on input — `oumv` reads the handler once from `observe L3` and has the file it needs — and output is the more expensive side at $1.10/Mtok against $0.07–0.27 for input.

A `mode=patch` (exact-snippet replace under OCC) would close that gap; it is not in the wire today and is the top protocol item on the remaining list.

## Comparison runs

**`1fbaa62b` vs `689d8210`** (identical code where an arm is
unchanged — that difference is the noise floor at this n):

- `baseline`: input 20,631 → 19,538 (-5.3%, steps 4.7 → 4.5, solved 15/15 → 15/15)
- `tokensafe`: input 17,818 → 17,616 (-1.1%, steps 4.7 → 4.7, solved 15/15 → 15/15)
- `oumv`: input 19,536 → 12,323 (-36.9%, steps 5.6 → 4.3, solved 15/15 → 15/15)

## Limits

- One model (`deepseek-chat`, temperature 0), one vendor, 3 tasks, 5 runs per cell: the min–max step range in the table is the honest spread.
- The sandbox is small (a 155-line Go app), so discovery is cheaper than in a large repo
  and the tool-path saving here is a floor, not a ceiling.
- Every advertised tool schema is re-sent in every request; the three arms do not declare
  the same number of tools, and that difference is part of what is measured.
- `oumv` mutates the whole resource and needs the full file in context to do it, and
  `verify` starts a fresh app per call by design; the stateful task is confirmed with the
  oracle in all three arms.

## Artifacts

- `go/benchmark/runs/matrix-v4-final.json`
- `go/benchmark/runs/matrix-v4-oumv-hint.json`
