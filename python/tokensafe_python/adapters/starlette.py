"""
Starlette adapter for TokenSafe UARP (Universal Agent-Runtime Protocol).

Starlette is the ASGI toolkit FastAPI is built on: routes are
``starlette.routing.Route`` / ``WebSocketRoute`` objects (no ``APIRoute``, no
``dependency_overrides``, no ``on_event`` lifecycle). The wire transport is
shared with the FastAPI adapter through ``adapters/_http_binding.py``; this
module adds only the Starlette-specific route introspection and the lifespan
wrapper that replaces the deprecated startup/shutdown events.
"""

from typing import Any, Dict, Optional

from starlette.applications import Starlette
from starlette.responses import JSONResponse
from starlette.routing import Route, WebSocketRoute

from tokensafe_python.core.runtime import OUMVRuntime
from tokensafe_python.adapters._http_binding import (
    HTTPBinding,
    discover_db_engine,
    install_lifespan_hooks,
    make_asgi_action_executor,
    register_declared_operations,
    write_discovery_descriptor,
)


def _handler_name(endpoint: Any) -> str:
    name = getattr(endpoint, "__name__", None)
    if name:
        return str(name)
    # Class-based endpoints (``HTTPEndpoint`` subclasses) have no __name__.
    return type(endpoint).__name__


class TokenSafeStarlette:
    """Instruments a Starlette application with UARP runtime capabilities."""

    def __init__(
        self,
        app: Starlette,
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
            framework="starlette",
            # Explicit wiring wins; otherwise adopt the engine the app's state
            # already holds. Starlette has no dependency_overrides to inspect.
            db_engine=db_engine if db_engine is not None else discover_db_engine(app),
            project_root=project_root,
            permissions=permissions,
            read_only=read_only,
        )
        self._binding = HTTPBinding(self.runtime, rpc_path=rpc_path)
        # SSE heartbeat cadence; tests shrink it to milliseconds.
        self.sse_heartbeat_seconds = self._binding.sse_heartbeat_seconds

        self._introspect_app()
        register_declared_operations(self.runtime)
        self.runtime.set_action_executor(make_asgi_action_executor(self.app))
        self._mount_rpc_endpoints()

        if auto_discover:
            self._register_lifecycle_hooks()

    def _introspect_app(self):
        """Registers every duck-typed Starlette route that exposes path + endpoint.

        Starlette has no request-body models to inspect, so only the routing
        surface is registered: ``Route`` objects carry ``methods``;
        ``WebSocketRoute``/``Mount`` do not, and are left out of the HTTP surface.
        """
        for route in getattr(self.app, "routes", []) or []:
            path = getattr(route, "path", None)
            endpoint = getattr(route, "endpoint", None)
            if not path or endpoint is None:
                continue
            # Skip the OUMV endpoints themselves (also skipped when re-mounting).
            if path == self.rpc_path or path.startswith("/__oumv"):
                continue

            methods = getattr(route, "methods", None)
            if not methods:
                continue  # WebSocketRoute / Mount: no HTTP method surface

            handler_name = _handler_name(endpoint)
            doc = (getattr(endpoint, "__doc__", "") or "").strip()
            for method in methods:
                if method in ("HEAD", "OPTIONS"):
                    continue
                self.runtime.register_route(
                    path=path,
                    method=method,
                    handler_name=handler_name,
                    summary=doc,
                    metadata={
                        "tags": [],
                        "name": getattr(route, "name", None),
                    },
                )

    def _mount_rpc_endpoints(self):
        """Mounts POST/OPTIONS rpc, SSE, WebSocket and health onto the app router."""
        routes = self.app.router.routes
        routes.append(
            Route(self.rpc_path, self._binding.handle_http, methods=["POST", "OPTIONS"], name="oumv_rpc")
        )
        routes.append(
            Route("/__oumv/events", self._transport_sse, methods=["GET"], name="oumv_events")
        )
        routes.append(
            WebSocketRoute(self.rpc_path, self._binding.serve_websocket, name="oumv_ws")
        )
        routes.append(
            Route("/__oumv/health", self._health, methods=["GET"], name="oumv_health")
        )

    async def _transport_sse(self, request):
        return await self._binding.sse_response(request)

    async def _health(self, request):
        # Starlette routes return a Response directly (no implicit JSON encoding).
        return JSONResponse(self._binding.health("starlette"))

    def _register_lifecycle_hooks(self):
        """Registers the runtime on startup/shutdown without the deprecated ``on_event``.

        The application's own lifespan (if any) is wrapped, never replaced, so a
        Starlette app that already opens a connection pool on startup keeps doing
        exactly that.
        """

        async def on_startup():
            # Spec §9.2: the discovery descriptor lives outside the repository.
            try:
                write_discovery_descriptor(
                    self.runtime, host=self.host, port=self.port, rpc_path=self.rpc_path
                )
            except Exception:
                pass
            self.runtime.discovery.register_runtime(
                framework="starlette",
                host=self.host,
                port=self.port,
                endpoint_path=self.rpc_path,
                metadata={
                    "title": getattr(self.app, "title", "Starlette App"),
                    "version": getattr(self.app, "version", "0.1.0"),
                    "routes_count": len(self.runtime._routes),
                },
            )

        async def on_shutdown():
            self.runtime.discovery.unregister_runtime()

        install_lifespan_hooks(self.app, on_startup, on_shutdown)


def mount_tokensafe_starlette(
    app: Starlette,
    *,
    rpc_path: str = "/__oumv/rpc",
    db_engine: Optional[Any] = None,
    auto_discover: bool = True,
    host: str = "127.0.0.1",
    port: int = 8000,
    project_root: Optional[str] = None,
    permissions: Optional[Dict[str, Any]] = None,
    read_only: bool = False,
) -> TokenSafeStarlette:
    """Explicit Starlette entry point (kept for callers that pin the host).

    ``mount_tokensafe`` now detects the host and returns this adapter for a plain
    Starlette application, so new code should call it instead: one entry point,
    one behaviour to document. ``read_only=True`` is the spec §8.5 deployment
    shape (writing methods absent, ``-32601``).
    """
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
