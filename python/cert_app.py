"""Live FastAPI application for python/certify-transports.sh.

Mounts the FastAPI adapter with a real SQLite engine (so verify.sandboxRollback is
advertised and verified) and the routes the conformance markers point at.
Workspace comes from TOKENSAFE_CERT_WORKSPACE; the script starts this module under
uvicorn and points oumv-certify at it.
"""

import os

from fastapi import FastAPI, Request
from fastapi.responses import JSONResponse
from sqlalchemy import create_engine, text

from tokensafe_python.adapters.fastapi import mount_tokensafe

# §14.2 workspace policy: the runtime keeps observeQuery.readOnly advertised and
# answers oumv/query with -32007 query_disabled (fixtures policyCases).
QUERY_DISABLED = os.environ.get("TOKENSAFE_CERT_QUERY_DISABLED") == "1"

WORKSPACE = os.environ.get("TOKENSAFE_CERT_WORKSPACE") or os.getcwd()
DB_PATH = os.path.join(WORKSPACE, "cert.sqlite")

engine = create_engine(f"sqlite:///{DB_PATH}")
with engine.begin() as connection:
    connection.execute(text("CREATE TABLE IF NOT EXISTS cert_probe (id INTEGER PRIMARY KEY, v INTEGER)"))
    connection.execute(text("INSERT OR IGNORE INTO cert_probe (id, v) VALUES (1, 0)"))

app = FastAPI()
mounted = mount_tokensafe(app, auto_discover=False, project_root=WORKSPACE, db_engine=engine,
                          permissions={"query": False} if QUERY_DISABLED else None)
mounted.runtime.register_route("/api/v1/orders", "GET", "orders")


@app.get("/api/v1/orders")
async def orders():
    return {"orders": []}


@app.post("/__oumv_sandbox_probe")
async def sandbox_probe(request: Request):
    if request.headers.get("x-tokensafe-dry-run") != "true":
        return JSONResponse({"error": "dry-run header required"}, status_code=400)
    with engine.connect() as connection:
        transaction = connection.begin()
        connection.execute(text("UPDATE cert_probe SET v = 1 WHERE id = 1"))
        transaction.rollback()
    return {"ok": True}
