# tokensafe-python

FastAPI adapter and runtime for the Universal Agent-Runtime Protocol (UARP / OUMV).

### Installation

```bash
pip install tokensafe-python
```

### Usage with FastAPI

```python
from fastapi import FastAPI
from tokensafe_python.adapters.fastapi import mount_tokensafe

app = FastAPI()

# Mount TokenSafe UARP runtime
mount_tokensafe(app)
```

### Features

- `/__oumv/health`: Runtime health check and route discovery.
- `/__oumv/rpc`: JSON-RPC 2.0 protocol endpoint.
- Zero-config auto-discovery via `.tokensafe/runtime.json`.
- Transactional simulation with automatic rollback.


### Agent Usability Benchmark (AUB)

TokenSafe UARP Backend achieves an **Autonomous Efficiency Ratio (AER) of 23.5x** over standard Vanilla FastAPI development:
- **-96% Tokens**: 1,420 vs 33,370 tokens
- **-83% Turns**: 1 turn to green vs 6 turns
- **Zero DB Pollution**: In-memory transactional dry-run simulation with automatic rollback (0 dirty records vs 18)
- **-94% Cost**: $0.0095 vs $0.1526 run cost

See full report: [BENCHMARK_REPORT.md](benchmark/BENCHMARK_REPORT.md).

### Testing

```bash
pytest tests/
# Run Agent Usability Benchmark
python -m benchmark.runner
```

### License

MIT

