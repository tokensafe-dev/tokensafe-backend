# Backend Agent Benchmark — three arms

> Scenario: `order-processing-api (in-memory sandbox)`  
> Generated: `2026-11-26T11:47:44Z`

## Methodology

- **Oracle**: the 12-assertion behavioral suite really executes every arm's HTTP handler (`httptest`).
- **Measured arm (`oumv`)**: JSON-RPC requests/responses are executed and their **bytes are counted**; token figures are `bytes / 4` and are labelled estimates.
- **Simulated arms (`baseline`, `tokensafe`)**: token/turn numbers come from fixture traces in `benchmark/traces` — a model of the workflow, not a measured run.
- **Turns** are counted per executed round trip (measured arm) or per fixture turn (simulated arms).

## Results

| Arm | Source | Turns | Payload bytes | Tokens | Oracle | Wall clock |
| :--- | :--- | ---: | ---: | ---: | ---: | ---: |
| `baseline` | SIMULATED | 6 | — | 33960 (trace) | 12/12 | 44700.0 ms |
| `oumv` | MEASURED | 5 | 2960 | 740 | 12/12 | 0.3 ms |
| `tokensafe` | SIMULATED | 1 | — | 1380 (trace) | 12/12 | 2650.0 ms |

## Per-turn detail (measured arm)

| # | Action | Request B | Response B | ms |
| ---: | :--- | ---: | ---: | ---: |
| 1 | handshake | 87 | 409 | 0.14 |
| 2 | surface | 72 | 747 | 0.03 |
| 3 | contract | 101 | 434 | 0.05 |
| 4 | impact | 108 | 437 | 0.05 |
| 5 | verify | 114 | 451 | 0.05 |

> Only the `oumv` row is a measurement. Rows marked SIMULATED must not be quoted as benchmark results.
