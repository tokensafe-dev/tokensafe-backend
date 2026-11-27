"""
FastAPI / Starlette adapter for TokenSafe UARP (Universal Agent-Runtime Protocol).
Provides zero-config runtime introspection, /__oumv/rpc JSON-RPC endpoint,
and safe dry-run mutation simulation.
"""

import json
import os
import time
from typing import Any, Dict, Optional, Tuple
import asyncio
import json as _json

from fastapi import FastAPI, Request, WebSocket, WebSocketDisconnect
from fastapi.responses import JSONResponse, StreamingResponse
from fastapi.routing import APIRoute

from tokensafe_python.core.runtime import OUMVRuntime


class TokenSafeFastAPI:
    """Wrapper that instruments a FastAPI application with UARP runtime capabilities."""

    def __init__(
        self,
        app: FastAPI,
        *,
        rpc_path: str = "/__oumv/rpc",
        db_engine: Optional[Any] = None,
        auto_discover: bool = True,
        host: str = "127.0.0.1",
        port: int = 8000,
        project_root: Optional[str] = None,
    ):
        self.app = app
        self.rpc_path = rpc_path
        self.host = host
        self.port = port
        # SSE heartbeat cadence; tests shrink it to milliseconds.
        self.sse_heartbeat_seconds = float(os.environ.get("TOKENSAFE_OUMV_SSE_HEARTBEAT", "15"))
        self.runtime = OUMVRuntime(
            framework="fastapi",
            db_engine=db_engine,
            project_root=project_root,
        )

        self._introspect_app()
        self._register_declared_operations()
        self._setup_action_executor()
        self._mount_rpc_endpoints()

        if auto_discover:
            self._register_lifecycle_events()

    def _register_declared_operations(self):
        """Wires Level 3 @oumv_operation contracts (core/contracts.py) into the runtime."""
        try:
            from tokensafe_python.core.contracts import get_operations
            for contract in get_operations().values():
                self.runtime.register_operation(contract)
        except Exception:
            pass

    def _introspect_app(self):
        """Introspects FastAPI routes, path parameters, and Pydantic schemas."""
        for route in self.app.routes:
            if not isinstance(route, APIRoute):
                continue

            # Ignore internal oumv routes
            if route.path.startswith("/__oumv"):
                continue

            handler_name = getattr(route.endpoint, "__name__", str(route.endpoint))
            doc = getattr(route.endpoint, "__doc__", "") or route.summary or ""

            methods = list(route.methods) if route.methods else ["GET"]
            for method in methods:
                if method in ("HEAD", "OPTIONS"):
                    continue

                route_id = f"route:{method}:{route.path}"
                self.runtime.register_route(
                    path=route.path,
                    method=method,
                    handler_name=handler_name,
                    summary=doc.strip(),
                    metadata={
                        "tags": route.tags,
                        "name": route.name,
                    }
                )

                # Inspect request body models
                body_params = getattr(route.dependant, "body_params", [])
                for bp in body_params:
                    model_cls = getattr(bp, "type_", None) or (
                        getattr(bp, "field_info", None) and getattr(bp.field_info, "annotation", None)
                    )
                    if model_cls and hasattr(model_cls, "model_json_schema"):
                        schema_name = getattr(model_cls, "__name__", str(model_cls))
                        schema_dict = model_cls.model_json_schema()
                        self.runtime.register_schema(schema_name, schema_dict)
                        self.runtime.causality.add_edge(route_id, f"model:{schema_name}", "validates_with")
                    elif model_cls and hasattr(model_cls, "schema"):
                        schema_name = getattr(model_cls, "__name__", str(model_cls))
                        schema_dict = model_cls.schema()
                        self.runtime.register_schema(schema_name, schema_dict)
                        self.runtime.causality.add_edge(route_id, f"model:{schema_name}", "validates_with")

                # Inspect response model
                resp_model = route.response_model
                if resp_model and hasattr(resp_model, "model_json_schema"):
                    schema_name = resp_model.__name__
                    schema_dict = resp_model.model_json_schema()
                    self.runtime.register_schema(schema_name, schema_dict)
                    self.runtime.causality.add_edge(route_id, f"model:{schema_name}", "produces")
                elif resp_model and hasattr(resp_model, "schema"):
                    schema_name = resp_model.__name__
                    schema_dict = resp_model.schema()
                    self.runtime.register_schema(schema_name, schema_dict)
                    self.runtime.causality.add_edge(route_id, f"model:{schema_name}", "produces")

    def _setup_action_executor(self):
        """Sets up ASGI in-memory execution for mutation.simulate and mutation.apply."""
        import httpx

        async def execute_asgi(
            path: str,
            method: str,
            body: Optional[Any] = None,
            headers: Optional[Dict[str, str]] = None,
            query_params: Optional[Dict[str, Any]] = None,
            dry_run: bool = True,
        ) -> Tuple[Any, int]:
            transport = httpx.ASGITransport(app=self.app)
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

        self.runtime.set_action_executor(execute_asgi)

    def _mount_rpc_endpoints(self):
        """Mounts /__oumv/rpc and /__oumv/health onto the FastAPI app."""

        @self.app.post(self.rpc_path, include_in_schema=False)
        async def handle_rpc(request: Request):
            # Spec §2.2: a token, when configured, is required. Loopback development
            # may run without one, so the check is opt-in via env (or explicit token).
            expected = os.environ.get("TOKENSAFE_OUMV_AUTH_TOKEN", "").strip()
            if expected:
                provided = (request.headers.get("x-oumv-session-token") or "").strip()
                auth = request.headers.get("authorization", "")
                if auth.lower().startswith("bearer "):
                    provided = auth[7:].strip() or provided
                if provided != expected:
                    return JSONResponse(
                        content={"jsonrpc": "2.0", "id": None,
                                 "error": {"code": -32007, "message": "PERMISSION_DENIED: invalid OUMV auth token"}},
                        status_code=401,
                    )
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

        @self.app.get("/__oumv/events", include_in_schema=False)
        async def handle_events(request: Request):
            """SSE event stream (spec §2.3): trace/state events plus heartbeats.

            Every event carries a monotonic `id`; `Last-Event-ID` replays what the
            client missed while it was disconnected.
            """
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

        @self.app.websocket(self.rpc_path)
        async def handle_ws(websocket: WebSocket):
            """JSON-RPC over WebSocket (spec §2.4) plus server notifications.

            After `oumv/handshake` the connection is bound to that sessionId and
            receives `notifications/oumv.trace` / `notifications/oumv.state`;
            closing the socket ends the session.
            """
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

        @self.app.get("/__oumv/health", include_in_schema=False)
        async def handle_health():
            return {
                "status": "ok",
                "runtime": "tokensafe-python",
                "framework": "fastapi",
                "protocol": "UARP/1.1",
                "profile": "backend",
                "rpc_endpoint": self.rpc_path,
                "registered_routes": len(self.runtime._routes),
                "registered_models": len(self.runtime._schemas),
            }

    def _register_lifecycle_events(self):
        """Hooks into startup and shutdown events for runtime.json registration."""

        @self.app.on_event("startup")
        async def on_startup():
            # Spec §9.2: the discovery descriptor lives outside the repository.
            try:
                from tokensafe_python.core.session import write_descriptor
                write_descriptor(
                    project_root=self.runtime._project_root_abs(),
                    url=f"http://{self.host}:{self.port}{self.rpc_path}",
                    name="tokensafe-python",
                    version="0.3.0",
                    session_token="",
                    transports=["http", "sse", "ws"],
                )
            except Exception:
                pass
            self.runtime.discovery.register_runtime(
                framework="fastapi",
                host=self.host,
                port=self.port,
                endpoint_path=self.rpc_path,
                metadata={
                    "title": getattr(self.app, "title", "FastAPI App"),
                    "version": getattr(self.app, "version", "0.1.0"),
                    "routes_count": len(self.runtime._routes),
                }
            )

        @self.app.on_event("shutdown")
        async def on_shutdown():
            self.runtime.discovery.unregister_runtime()


def mount_tokensafe(
    app: FastAPI,
    *,
    rpc_path: str = "/__oumv/rpc",
    db_engine: Optional[Any] = None,
    auto_discover: bool = True,
    host: str = "127.0.0.1",
    port: int = 8000,
    project_root: Optional[str] = None,
) -> TokenSafeFastAPI:
    """One-liner helper to mount TokenSafe UARP runtime onto a FastAPI application."""
    return TokenSafeFastAPI(
        app=app,
        rpc_path=rpc_path,
        db_engine=db_engine,
        auto_discover=auto_discover,
        host=host,
        port=port,
        project_root=project_root,
    )
