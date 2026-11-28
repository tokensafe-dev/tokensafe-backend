"""
FastAPI adapter for TokenSafe UARP (Universal Agent-Runtime Protocol).
Provides zero-config runtime introspection, /__oumv/rpc JSON-RPC endpoint,
and safe dry-run mutation simulation.

The wire transport (RPC, SSE, WebSocket, auth, CORS) is shared with the
Starlette adapter in ``adapters/_http_binding.py``; this module only adds the
FastAPI-specific route/schema introspection and lifecycle hooks.
"""

from typing import Any, Dict, Optional

from fastapi import FastAPI, Request, WebSocket
from fastapi.responses import JSONResponse, StreamingResponse
from fastapi.routing import APIRoute

from tokensafe_python.core.runtime import OUMVRuntime
from tokensafe_python.adapters._http_binding import (
    HTTPBinding,
    auth_refusal,
    credential_from_headers,
    discover_db_engine,
    expected_auth_token,
    is_authorized,
    make_asgi_action_executor,
    register_declared_operations,
)


def _discover_db_engine(app) -> Optional[Any]:
    """Finds a SQLAlchemy engine the FastAPI app already owns (§4/§12 DB sandbox).

    FastAPI has no container, so the honest equivalent is the state an
    application actually exposes (``app.state.engine`` / ``db_engine``) plus the
    dependency overrides that close over an engine. Anything not recognised
    stays None, and the runtime keeps advertising no sandbox capability rather
    than guessing.
    """
    return discover_db_engine(app, include_dependency_overrides=True)


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
        permissions: Optional[Dict[str, Any]] = None,
        read_only: bool = False,
    ):
        self.app = app
        self.rpc_path = rpc_path
        self.host = host
        self.port = port
        self.runtime = OUMVRuntime(
            framework="fastapi",
            # Explicit wiring wins; otherwise adopt the engine the app already holds
            # (the .NET AddDbContext discovery is the template for this).
            db_engine=db_engine if db_engine is not None else _discover_db_engine(app),
            project_root=project_root,
            permissions=permissions,
            read_only=read_only,
        )
        # One transport implementation for both Python adapters.
        self._binding = HTTPBinding(self.runtime, rpc_path=rpc_path)
        # SSE heartbeat cadence; tests shrink it to milliseconds.
        self.sse_heartbeat_seconds = self._binding.sse_heartbeat_seconds

        self._introspect_app()
        self._register_declared_operations()
        self._setup_action_executor()
        self._mount_rpc_endpoints()

        if auto_discover:
            self._register_lifecycle_events()

    def _register_declared_operations(self):
        """Wires Level 3 @oumv_operation contracts (core/contracts.py) into the runtime."""
        register_declared_operations(self.runtime)

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
        self.runtime.set_action_executor(make_asgi_action_executor(self.app))

    def _expected_auth_token(self) -> str:
        return expected_auth_token()

    @staticmethod
    def _credential_from_headers(headers) -> str:
        return credential_from_headers(headers)

    def _authorized(self, headers) -> bool:
        """Spec §12: a configured token is always required — never fail open."""
        return is_authorized(headers)

    @staticmethod
    def _auth_refusal() -> JSONResponse:
        return auth_refusal()

    def _mount_rpc_endpoints(self):
        """Mounts /__oumv/rpc and /__oumv/health onto the FastAPI app."""

        @self.app.post(self.rpc_path, include_in_schema=False)
        async def handle_rpc(request: Request):
            return await self._binding.rpc_response(request)

        @self.app.get("/__oumv/events", include_in_schema=False)
        async def handle_events(request: Request) -> StreamingResponse:
            """SSE event stream (spec §2.3) with Last-Event-ID replay."""
            return await self._binding.sse_response(request)

        @self.app.websocket(self.rpc_path)
        async def handle_ws(websocket: WebSocket):
            """JSON-RPC over WebSocket (spec §2.4) plus server notifications."""
            await self._binding.serve_websocket(websocket)

        @self.app.get("/__oumv/health", include_in_schema=False)
        async def handle_health():
            return self._binding.health("fastapi")

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


def _is_fastapi_app(app: Any) -> bool:
    """True for FastAPI, false for a plain Starlette application.

    ``FastAPI`` subclasses ``Starlette``, so the check runs most-specific first:
    the class itself, the ``dependency_overrides`` registry FastAPI owns, then an
    ``APIRoute`` (Starlette only ever produces ``Route``/``WebSocketRoute``).
    """
    try:
        from fastapi import FastAPI
        from fastapi.routing import APIRoute
    except Exception:  # pragma: no cover - FastAPI is a hard dependency here
        return False
    if isinstance(app, FastAPI):
        return True
    if hasattr(app, "dependency_overrides"):
        return True
    return any(isinstance(route, APIRoute) for route in getattr(app, "routes", []) or [])


def mount_tokensafe(
    app: Any,
    *,
    rpc_path: str = "/__oumv/rpc",
    db_engine: Optional[Any] = None,
    auto_discover: bool = True,
    host: str = "127.0.0.1",
    port: int = 8000,
    project_root: Optional[str] = None,
    permissions: Optional[Dict[str, Any]] = None,
    read_only: bool = False,
) -> Any:
    """One-liner helper to mount the UARP runtime onto an ASGI application.

    The host is detected, not declared: a FastAPI app gets the FastAPI adapter
    (Pydantic schemas, ``dependency_overrides`` DB discovery, startup/shutdown
    events) and a plain Starlette app gets the Starlette adapter (duck-typed
    routes, ``app.state`` DB discovery, lifespan wrapper). Both share the same
    wire transport, so an agent cannot tell them apart on the protocol surface.

    ``read_only=True`` is the spec §8.5 deployment shape: the writing methods
    (applyEdit/previewEdit/verify/build and the mutation.* extensions) are never
    registered, so they answer -32601 instead of a policy refusal.
    """
    if not _is_fastapi_app(app):
        from tokensafe_python.adapters.starlette import TokenSafeStarlette

        return TokenSafeStarlette(
            app=app,
            rpc_path=rpc_path,
            db_engine=db_engine,
            auto_discover=auto_discover,
            host=host,
            port=port,
            project_root=project_root,
            permissions=permissions,
            read_only=read_only,
        )
    return TokenSafeFastAPI(
        app=app,
        rpc_path=rpc_path,
        db_engine=db_engine,
        auto_discover=auto_discover,
        host=host,
        port=port,
        project_root=project_root,
        permissions=permissions,
        read_only=read_only,
    )
