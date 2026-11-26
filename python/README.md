# tokensafe-python

FastAPI adapter and runtime for the Universal Agent-Runtime Protocol (UARP / OUMV).

### Installation

```bash
pip install "tokensafe-backend-python @ git+https://github.com/tokensafe-dev/tokensafe-backend.git#subdirectory=python"
# or from a local clone: pip install ./python
```

### Usage with FastAPI

```python
from fastapi import FastAPI
from tokensafe_python.adapters.fastapi import mount_tokensafe

app = FastAPI()

# Mount TokenSafe UARP runtime
mount_tokensafe(app)
```

### stdio (headless)

No listener, same runtime — one JSON-RPC object per line on stdin, responses on
stdout, diagnostics on stderr:

```bash
python -m tokensafe_python.stdio --workspace /path/to/app
python -m tokensafe_python.stdio --app myapp.main:app --workspace /path/to/app
```

With `--app` the FastAPI app is mounted in-process, so L1/L2, `verify` and L4 work
exactly like behind HTTP; without it, the file-based pillar (`observe L3`,
`getSource`, `applyEdit`) still answers.

### Features

- `/__oumv/health`: Runtime health check and route discovery.
- `/__oumv/rpc`: JSON-RPC 2.0 protocol endpoint.
- **observe** L0–L4, **understand** impact/causality, **applyEdit** with mandatory
  `expectedVersion` and the normative guard order, **verify** in-process with
  `timings`/`scope`.
- Transports: HTTP POST + batch, SSE, WebSocket, stdio NDJSON.
- Discovery: `~/.tokensafe/runtimes/<hash16>.json` (0600, outside the repo),
  profile-keyed for frontend + backend in one workspace.
- Transactional simulation with automatic rollback.

### Certification

```bash
cd python
CERT_OUT_DIR=../certification .venv/bin/python -m pytest -q tests/test_conformance.py tests/test_stdio.py
```

The language-neutral fixture suite (fixtures v5, 20 cases) certifies the adapter over
HTTP and over stdio: [`certification/python-fastapi.json`](../certification/python-fastapi.json)
and [`certification/transports/python-stdio.json`](../certification/transports/python-stdio.json),
both **Level 3, 20/20**.

### Benchmarks

No simulated AER numbers are published here. The measured agent matrix covers Go and
React today ([Go report](../go/benchmark/OUMV_AGENT_MATRIX.md),
[React report](https://github.com/tokensafe-dev/tokensafe-ui/blob/main/benchmarks/REACT_AGENT_MATRIX.md));
Python is not yet an arm in the agent matrix.

