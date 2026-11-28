"""Live Starlette application for python/certify-starlette.sh.

Mounts the Starlette adapter with a real SQLite engine (discovered from
``app.state.db_engine`` so the §12 sandbox claim is proven through the same
lookup a real application uses) and the routes the conformance markers point at.
Workspace comes from TOKENSAFE_CERT_WORKSPACE; the script starts this module under
uvicorn and points oumv-certify at it. TOKENSAFE_CERT_READ_ONLY=1 produces the
§8.5 read-only deployment shape for the ``-readonly`` certificate.
"""

import os

from sqlalchemy import create_engine, text
from starlette.applications import Starlette
from starlette.responses import JSONResponse
from starlette.routing import Route

from tokensafe_python.adapters.fastapi import mount_tokensafe

WORKSPACE = os.environ.get("TOKENSAFE_CERT_WORKSPACE") or os.getcwd()
DB_PATH = os.path.join(WORKSPACE, "cert.sqlite")
READ_ONLY = os.environ.get("TOKENSAFE_CERT_READ_ONLY") == "1"
# §14.2 workspace policy: the runtime keeps observeQuery.readOnly advertised and
# answers oumv/query with -32007 query_disabled (fixtures v16 policyCases).
QUERY_DISABLED = os.environ.get("TOKENSAFE_CERT_QUERY_DISABLED") == "1"

engine = create_engine(f"sqlite:///{DB_PATH}")
with engine.begin() as connection:
    connection.execute(text("CREATE TABLE IF NOT EXISTS cert_probe (id INTEGER PRIMARY KEY, v INTEGER)"))
    connection.execute(text("INSERT OR IGNORE INTO cert_probe (id, v) VALUES (1, 0)"))


async def orders(request):
    return JSONResponse({"orders": []})


async def sandbox_probe(request):
    if request.headers.get("x-tokensafe-dry-run") != "true":
        return JSONResponse({"error": "dry-run header required"}, status_code=400)
    with engine.connect() as connection:
        transaction = connection.begin()
        connection.execute(text("UPDATE cert_probe SET v = 1 WHERE id = 1"))
        transaction.rollback()
    return JSONResponse({"ok": True})


app = Starlette(
    routes=[
        Route("/api/v1/orders", orders, methods=["GET"]),
        Route("/__oumv_sandbox_probe", sandbox_probe, methods=["POST"]),
    ]
)
# The adapter's DB discovery reads the state a real app already exposes; wiring it
# here (instead of passing db_engine=) certifies that lookup, not just the engine.
app.state.db_engine = engine

mounted = mount_tokensafe(
    app,
    auto_discover=False,
    project_root=WORKSPACE,
    read_only=READ_ONLY,
    permissions={"query": False} if QUERY_DISABLED else None,
)
