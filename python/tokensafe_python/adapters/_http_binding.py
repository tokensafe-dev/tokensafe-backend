"""Shared HTTP/SSE/WebSocket binding for the OUMV Python adapters.

The FastAPI and Starlette adapters differ in one place only: how their routes are
declared and introspected (``APIRoute`` objects versus duck-typed Starlette
``Route`` objects). Everything the wire protocol actually sees — JSON-RPC over
POST, the §2.3 SSE stream with ``Last-Event-ID`` replay, the §2.4 WebSocket
session loop, §12 authentication, CORS preflight and the health probe — is one
implementation living here, so ``python-fastapi`` and ``python-starlette`` can
never drift into two different transports.

Private to the package: adapters import from it, applications do not.
"""

import asyncio
import contextlib
import json as _json
import os
import time
from typing import Any, Dict, Optional, Tuple

from starlette.responses import JSONResponse, Response, StreamingResponse
from starlette.websockets import WebSocketDisconnect

# Keep in step with python/pyproject.toml [project].version. The discovery
# descriptor advertises it to the workspace registry.
PACKAGE_VERSION = "0.12.0"


def discover_db_engine(app, *, include_dependency_overrides: bool = False) -> Optional[Any]:
    """Finds a SQLAlchemy engine the app already owns (§4/§12 DB sandbox).

    .NET discovers `AddDbContext` from the service collection; the Python
    frameworks have no container, so the honest equivalent is the state an
    application actually exposes: ``app.state.engine`` / ``db_engine`` (an
    Engine, AsyncEngine or a sessionmaker bound to one). FastAPI additionally
    exposes dependency overrides that close over an engine, which
    ``include_dependency_overrides=True`` inspects; Starlette has no overrides,
    so the Starlette adapter leaves that off. Anything not recognised stays
    None, and the runtime keeps advertising no sandbox capability rather than
    guessing.
    """
    try:
        from sqlalchemy.engine import Engine
    except Exception:
        return None
    try:
        # The asyncio module needs greenlet; a sync-only install must still work.
        from sqlalchemy.ext.asyncio import AsyncEngine
        engine_types: tuple = (Engine, AsyncEngine)
    except Exception:
        AsyncEngine = None  # type: ignore[assignment]
        engine_types = (Engine,)

    def coerce(candidate):
        if isinstance(candidate, engine_types):
            return candidate
        bind = getattr(candidate, "kw", None)
        if isinstance(bind, dict):
            bound = bind.get("bind")
            if isinstance(bound, engine_types):
                return bound
        return None

    state = getattr(app, "state", None)
    if state is not None:
        for name in ("db_engine", "engine", "db", "database"):
            engine = coerce(getattr(state, name, None))
            if engine is not None:
                return engine

    if include_dependency_overrides:
        for override in getattr(app, "dependency_overrides", {}).values():
            for cell in getattr(override, "__closure__", None) or ():
                try:
                    engine = coerce(cell.cell_contents)
                except Exception:
                    continue
                if engine is not None:
                    return engine
    return None


def register_declared_operations(runtime) -> None:
    """Wires Level 3 @oumv_operation contracts (core/contracts.py) into the runtime."""
    try:
        from tokensafe_python.core.contracts import get_operations
        for contract in get_operations().values():
            runtime.register_operation(contract)
    except Exception:
        pass


def make_asgi_action_executor(app):
    """Builds the in-memory ASGI executor for mutation.simulate / mutation.apply."""

    async def execute_asgi(
        path: str,
        method: str,
        body: Optional[Any] = None,
        headers: Optional[Dict[str, str]] = None,
        query_params: Optional[Dict[str, Any]] = None,
        dry_run: bool = True,
    ) -> Tuple[Any, int]:
        import httpx

        transport = httpx.ASGITransport(app=app)
        req_headers = dict(headers or {})
        if dry_run:
            req_headers["X-TokenSafe-Dry-Run"] = "true"

        async with httpx.AsyncClient(transport=transport, base_url="http://testserver") as client:
            req_kwargs: Dict[str, Any] = {
                "headers": req_headers,
                "params": query_params,
            }
            if body is not None:
                if isinstance(body, (dict, list)):
                    req_kwargs["json"] = body
                else:
                    req_kwargs["content"] = str(body)

            resp = await client.request(method.upper(), path, **req_kwargs)

            try:
                res_body = resp.json()
            except Exception:
                res_body = resp.text

            return res_body, resp.status_code

    return execute_asgi


def expected_auth_token() -> str:
    return os.environ.get("TOKENSAFE_OUMV_AUTH_TOKEN", "").strip()


def credential_from_headers(headers) -> str:
    provided = (headers.get("x-oumv-session-token") or "").strip()
    auth = headers.get("authorization", "")
    if auth.lower().startswith("bearer "):
        provided = auth[7:].strip() or provided
    return provided


def is_authorized(headers) -> bool:
    """Spec §12: a configured token is always required — never fail open."""
    expected = expected_auth_token()
    if not expected:
        return True
    return credential_from_headers(headers) == expected


def auth_refusal() -> JSONResponse:
    return JSONResponse(
        content={"jsonrpc": "2.0", "id": None,
                 "error": {"code": -32007, "message": "PERMISSION_DENIED: invalid OUMV auth token"}},
        status_code=401,
    )


def cors_headers(origin: str) -> Dict[str, str]:
    return {
        "Access-Control-Allow-Origin": origin or "*",
        "Access-Control-Allow-Methods": "POST, OPTIONS",
        "Access-Control-Allow-Headers": "Content-Type, Authorization, X-OUMV-Session-Token",
    }


class HTTPBinding:
    """The JSON-RPC / SSE / WebSocket transport shared by the Python adapters."""

    def __init__(
        self,
        runtime,
        *,
        rpc_path: str = "/__oumv/rpc",
        sse_heartbeat_seconds: Optional[float] = None,
    ):
        self.runtime = runtime
        self.rpc_path = rpc_path
        if sse_heartbeat_seconds is None:
            sse_heartbeat_seconds = float(os.environ.get("TOKENSAFE_OUMV_SSE_HEARTBEAT", "15"))
        self.sse_heartbeat_seconds = float(sse_heartbeat_seconds)

    async def rpc_response(self, request) -> JSONResponse:
        """POST /rpc: authenticate, parse, dispatch, answer (spec §2.2)."""
        if not is_authorized(request.headers):
            return auth_refusal()
        try:
            body = await request.json()
        except Exception as e:
            return JSONResponse(
                content={
                    "jsonrpc": "2.0",
                    "id": None,
                    "error": {"code": -32700, "message": f"Parse error: {str(e)}"}
                },
                status_code=400,
            )
        res = await self.runtime.rpc.handle_payload(body)
        return JSONResponse(content=res)

    async def options_response(self, request) -> Response:
        """CORS preflight on the RPC path; 204 with the allowed method/header set."""
        origin = (request.headers.get("origin") or "").strip()
        return Response(status_code=204, headers=cors_headers(origin))

    async def handle_http(self, request):
        """One endpoint for the RPC path: OPTIONS is preflight, everything else POST."""
        if request.method.upper() == "OPTIONS":
            return await self.options_response(request)
        return await self.rpc_response(request)

    async def sse_response(self, request) -> StreamingResponse:
        """SSE event stream (spec §2.3): trace/state events plus heartbeats.

        Every event carries a monotonic `id`; `Last-Event-ID` replays what the
        client missed while it was disconnected.
        """
        # §12: an authenticated binding protects SSE too, not just the RPC POST.
        if not is_authorized(request.headers):
            return auth_refusal()
        last_id = 0
        raw_last = (request.headers.get("last-event-id") or "").strip()
        if raw_last.isdigit():
            last_id = int(raw_last)

        async def event_stream():
            nonlocal last_id
            yield ": connected\n\n"
            heartbeat_every = max(self.sse_heartbeat_seconds, 0.001)
            next_heartbeat = time.monotonic() + heartbeat_every
            while True:
                if await request.is_disconnected():
                    return
                for event in self.runtime.events_since(last_id):
                    payload = _json.dumps(event["data"])
                    yield f"id: {event['id']}\nevent: {event['type']}\ndata: {payload}\n\n"
                    last_id = event["id"]
                now = time.monotonic()
                if now >= next_heartbeat:
                    yield f"event: heartbeat\ndata: {_json.dumps({'ts': self.runtime.state.to_timestamp()})}\n\n"
                    next_heartbeat = now + heartbeat_every
                await asyncio.sleep(0.2)

        return StreamingResponse(event_stream(), media_type="text/event-stream")

    async def serve_websocket(self, websocket) -> None:
        """JSON-RPC over WebSocket (spec §2.4) plus server notifications.

        After `oumv/handshake` the connection is bound to that sessionId and
        receives `notifications/oumv.trace` / `notifications/oumv.state`;
        closing the socket ends the session.
        """
        # §12: refuse the upgrade itself; a rejected WebSocket close before
        # accept answers HTTP 403, which is the spec's non-dispatch refusal.
        if not is_authorized(websocket.headers):
            await websocket.close(code=1008)
            return
        await websocket.accept()
        queue, unsubscribe = self.runtime.subscribe_events()
        state = {"session": ""}
        send_lock = asyncio.Lock()

        async def forward_events():
            while True:
                event = await queue.get()
                session_id = state["session"]
                if not session_id:
                    continue  # notifications start after handshake
                method = {
                    "state": "notifications/oumv.state",
                    "diagnostic": "notifications/oumv.diagnostic",
                }.get(event["type"], "notifications/oumv.trace")
                params = dict(event["data"])
                params["sessionId"] = session_id
                async with send_lock:
                    await websocket.send_json(
                        {"jsonrpc": "2.0", "method": method, "params": params}
                    )

        forwarder = asyncio.create_task(forward_events())
        try:
            while True:
                payload = await websocket.receive_json()
                result = await self.runtime.rpc.handle_payload(payload)
                if isinstance(payload, dict) and payload.get("id") is not None:
                    async with send_lock:
                        await websocket.send_json(result)
                    if payload.get("method") == "oumv/handshake" and isinstance(result, dict):
                        # A rejected handshake must not unbind a live session:
                        # only a successful result carrying a sessionId rebinds.
                        handshake_result = result.get("result")
                        if isinstance(handshake_result, dict):
                            session_id = str(handshake_result.get("sessionId") or "")
                            if session_id:
                                state["session"] = session_id
        except WebSocketDisconnect:
            return
        finally:
            forwarder.cancel()
            unsubscribe()
            self.runtime.sessions.delete(state["session"])

    def health(self, framework: str) -> Dict[str, Any]:
        return {
            "status": "ok",
            "runtime": "tokensafe-python",
            "framework": framework,
            "protocol": "UARP/1.1",
            "profile": "backend",
            "rpc_endpoint": self.rpc_path,
            "registered_routes": len(self.runtime._routes),
            "registered_models": len(self.runtime._schemas),
        }


def write_discovery_descriptor(runtime, *, host: str, port: int, rpc_path: str) -> None:
    """Spec §9.2: the discovery descriptor lives outside the repository."""
    from tokensafe_python.core.session import write_descriptor

    write_descriptor(
        project_root=runtime._project_root_abs(),
        url=f"http://{host}:{port}{rpc_path}",
        name="tokensafe-python",
        version=PACKAGE_VERSION,
        session_token="",
        transports=["http", "sse", "ws"],
    )


def install_lifespan_hooks(app, on_startup, on_shutdown) -> None:
    """Wraps the app's existing lifespan instead of the deprecated on_event hooks.

    ``app.router.lifespan_context`` is either the framework's default lifespan
    (``_DefaultLifespan``) or the application's own ``@asynccontextmanager``
    lifespan. Wrapping it preserves whatever the application already does on
    startup/shutdown while adding the OUMV registration around it.
    """
    router = app.router
    original = router.lifespan_context

    @contextlib.asynccontextmanager
    async def lifespan(app_):
        async with original(app_) as state:
            await on_startup()
            try:
                yield state
            finally:
                await on_shutdown()

    router.lifespan_context = lifespan
