# OUMV agent matrix v5 — Go, measured (200-file target)

> Generated from the run artifact by `render_agent_matrix.py` — do not edit by hand.
> Every token and cost number comes from the provider's `usage` payload and a real
> 12-assertion oracle re-run after every agent step. No replay, no estimates.

## Method

| | |
| :--- | :--- |
| Model | `deepseek-chat`, temperature 0 |
| Runs | 5 per task per arm (45 sessions, $0.1052 spent) |
| Harness commit | `00dfaac5` |
| Oracle | `go test ./benchmark -run TestGroundTruthOracleVanilla` (12 assertions), after every step |
| Sandbox | throwaway copy of `tokensafe-backend/go` with one deliberate regression per task, plus 200 generated service files |
| Arms | `baseline` file/command tools; `tokensafe` the product's MCP server; `oumv` the shipped Go adapter |

`tokensafe` runs the product's own loop: `smart_read_file(endpoint="POST /api/…")` in one
call (route → handler body + path + line range + version, plus its cross-file callees), then
`apply_edits` with the hunk and inline `verify="auto"`, so the edit and its test verdict come
back in one call. `oumv` runs `observe L3` on the endpoint target (handler source + path +
`startLine`/`endLine` + sha256 OCC version) → `applyEdit mode=patch` with the hunk → the oracle.

## Results

| Arm | Runs | Solved | Mean steps (min–max) | Mean input (cache-hit share) | Mean output | Cost/run |
| :--- | ---: | ---: | ---: | ---: | ---: | ---: |
| `baseline` | 15 | 15/15 | 5.6 (5–8) | 26,412 (86%) | 1,670 | $0.0044 |
| `tokensafe` | 15 | 15/15 | 3.1 (3–4) | 8,177 (90%) | 414 | $0.0012 |
| `oumv` | 15 | 15/15 | 3.7 (2–7) | 9,553 (91%) | 521 | $0.0014 |

**tokensafe vs baseline:** -69.0% input, -73.3% cost, -44.0% steps.
**oumv vs baseline:** -63.8% input, -68.2% cost, -33.3% steps.

### Per task (means)

| Task | baseline input | tokensafe input | oumv input | baseline steps | tokensafe steps | oumv steps |
| :--- | ---: | ---: | ---: | ---: | ---: | ---: |
| `product-create-status` | 22,691 | 6,971 | 3,744 | 5.2 | 3.0 | 2.0 |
| `order-conflict-status` | 26,931 | 9,328 | 4,464 | 5.6 | 3.2 | 2.2 |
| `product-validation-status` | 29,615 | 8,231 | 20,451 | 6.0 | 3.2 | 7.0 |

### Output tokens: the mutation unit

`oumv` emits 521 output tokens per run against `tokensafe`'s 414 (1.26x), so `tokensafe` writes less per fix. Both arms send a hunk rather than a file: `applyEdit mode=patch` under OCC on the protocol side, `apply_edits` with an exact target snippet on the product side. What is left is how much the model must emit to place the edit — the hunk plus its target, and on a rejected edit the retry.

Input is the other half: `oumv` gets the handler, its path and its OCC version from one `observe L3`, and `tokensafe` gets the same from one `smart_read_file(endpoint=…)`. Both arms now resolve an endpoint in a single call, which is the change wave 3 measured.

## Comparison runs

**Sandbox size 0 → 200 generated files** — same code, same arms, same tasks; only the tree the tools have to search:

- `baseline`: input 22,356 → 26,412 (+18.1%), steps 4.8 → 5.6, cost $0.0038 → $0.0044, solved 15/15 → 15/15
- `tokensafe`: input 8,823 → 8,177 (-7.3%), steps 3.3 → 3.1, cost $0.0013 → $0.0012, solved 15/15 → 15/15
- `oumv`: input 9,756 → 9,553 (-2.1%), steps 3.9 → 3.7, cost $0.0015 → $0.0014, solved 15/15 → 15/15

## Limits

- One model (`deepseek-chat`, temperature 0), one vendor, 3 tasks, 5 runs per cell: the min–max step range in the table is the honest spread.
- The target is the module plus 200 generated service files: wider than the 69-file sandbox of matrix v4, still shallower than a real monorepo, so the discovery saving here is a floor, not a ceiling.
- Every advertised tool schema is re-sent in every request; the three arms do not declare
  the same number of tools, and that difference is part of what is measured.
- `oumv` needs the handler source in context to choose its hunk (`observe L3` hands it over),
  and `verify` starts a fresh app per call by design; the stateful task is confirmed with the
  oracle in all three arms.

## Artifacts

- `runs/matrix-v5-big.json`
- `runs/matrix-v5-small.json`
