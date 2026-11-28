"""End-to-end tests for the Starlette UARP adapter.

The wire transport is shared with FastAPI (``adapters/_http_binding.py``), so these
tests focus on what is Starlette-specific: duck-typed route introspection, the
CORS preflight, the lifespan wrapper (no deprecated ``on_event``), state-based DB
discovery and the §8.5 read-only shape.
"""

import json
from contextlib import asynccontextmanager

from sqlalchemy import create_engine
from starlette.applications import Starlette
from starlette.responses import JSONResponse
from starlette.routing import Route, WebSocketRoute
from starlette.testclient import TestClient

from tokensafe_python.adapters.fastapi import mount_tokensafe
from tokensafe_python.adapters.starlette import TokenSafeStarlette, mount_tokensafe_starlette


def test_mount_tokensafe_detects_the_starlette_host():
    """One entry point: the host is detected, not declared."""
    from fastapi import FastAPI

    from tokensafe_python.adapters.fastapi import TokenSafeFastAPI

    assert isinstance(mount_tokensafe(_app(), auto_discover=False), TokenSafeStarlette)
    assert isinstance(mount_tokensafe(FastAPI(), auto_discover=False), TokenSafeFastAPI)
    # The explicit entry stays available for callers that pin the host.
    assert isinstance(mount_tokensafe_starlette(_app(), auto_discover=False), TokenSafeStarlette)


async def home(request):
    return JSONResponse({"ok": True})


async def create_item(request):
    return JSONResponse({"created": True}, status_code=201)


def _app() -> Starlette:
    return Starlette(
        routes=[
            Route("/", home, methods=["GET"]),
            Route("/items", create_item, methods=["POST"]),
        ]
    )


def _rpc(client: TestClient, method: str, params: dict | None = None, request_id: int = 1) -> dict:
    response = client.post(
        "/__oumv/rpc",
        json={"jsonrpc": "2.0", "id": request_id, "method": method, "params": params or {}},
    )
    assert response.status_code == 200, response.text
    return response.json()


def test_health_and_introspected_routes():
    app = _app()
    mount_tokensafe_starlette(app, auto_discover=False)
    client = TestClient(app)

    health = client.get("/__oumv/health").json()
    assert health["status"] == "ok"
    assert health["framework"] == "starlette"
    assert health["registered_routes"] == 2

    state = _rpc(client, "state.read", {"scope": "all"})["result"]
    assert state["framework"] == "starlette"
    assert {route["path"] for route in state["routes"]} >= {"/", "/items"}


def test_options_answers_cors_preflight():
    app = _app()
    mount_tokensafe_starlette(app, auto_discover=False)

    response = TestClient(app).options(
        "/__oumv/rpc", headers={"Origin": "https://app.example"}
    )

    assert response.status_code == 204
    assert "POST" in response.headers["access-control-allow-methods"]
    assert "X-OUMV-Session-Token" in response.headers["access-control-allow-headers"]
    assert response.headers["access-control-allow-origin"] == "https://app.example"


def test_websocket_route_is_not_registered_as_http():
    async def socket(websocket):
        await websocket.accept()

    app = Starlette(routes=[WebSocketRoute("/events", socket)])
    mount_tokensafe_starlette(app, auto_discover=False)

    endpoints = _rpc(TestClient(app), "oumv/observe", {"level": "L1"})["result"]["data"]["endpoints"]
    assert all(endpoint["path"] != "/events" for endpoint in endpoints)


def test_websocket_dispatches_jsonrpc():
    app = _app()
    mount_tokensafe_starlette(app, auto_discover=False)

    with TestClient(app).websocket_connect("/__oumv/rpc") as ws:
        ws.send_json({"jsonrpc": "2.0", "id": 1, "method": "oumv/handshake", "params": {}})
        result = ws.receive_json()

    assert result["result"]["profile"] == "backend"
    assert result["result"]["runtimeInfo"]["framework"] == "starlette"
    assert result["result"]["sessionId"].startswith("sess-")


def test_lifespan_wrapper_preserves_the_existing_lifespan(tmp_path, monkeypatch):
    monkeypatch.setenv("TOKENSAFE_HOME", str(tmp_path / "home"))
    events = []

    @asynccontextmanager
    async def lifespan(app):
        events.append("existing-start")
        yield {"shared": 42}
        events.append("existing-stop")

    app = Starlette(routes=[Route("/", home)], lifespan=lifespan)
    mount_tokensafe_starlette(app, project_root=str(tmp_path / "workspace"))

    with TestClient(app) as client:
        assert events == ["existing-start"]
        assert client.get("/").json() == {"ok": True}
        assert client.get("/__oumv/health").json()["framework"] == "starlette"
        # §9.2: the descriptor is written under TOKENSAFE_HOME on startup.
        descriptors = list((tmp_path / "home" / "runtimes").glob("*.json"))
        assert descriptors, "the discovery descriptor must be written on startup"
        registry = json.loads(descriptors[0].read_text(encoding="utf-8"))
        assert registry["backend"]["url"] == "http://127.0.0.1:8000/__oumv/rpc"

    assert events == ["existing-start", "existing-stop"]


def test_db_engine_is_discovered_from_app_state(tmp_path):
    engine = create_engine(f"sqlite:///{tmp_path / 'cert.sqlite'}")
    app = _app()
    app.state.db_engine = engine

    adapter = mount_tokensafe_starlette(app, auto_discover=False, project_root=str(tmp_path))

    assert adapter.runtime.db_sandbox.has_engine()
    caps = _rpc(TestClient(app), "oumv/handshake", {"protocolVersion": "1.6.0"})["result"]["capabilities"]
    assert caps["verify"]["sandboxRollback"] is True


def test_read_only_removes_writing_methods():
    app = _app()
    mount_tokensafe_starlette(app, auto_discover=False, read_only=True)
    client = TestClient(app)

    body = _rpc(client, "oumv/applyEdit", {"path": "app.py", "replacement": "x = 1"})
    assert body["error"]["code"] == -32601

    caps = _rpc(client, "oumv/handshake", {"protocolVersion": "1.6.0"})["result"]["capabilities"]
    assert caps["readOnly"] is True
    assert "mutate" not in caps
    assert "verify" not in caps
    # Nothing to replay when no write method exists.
    assert caps["sessions"]["idempotency"] is False
    assert _rpc(client, "oumv/observe", {"level": "L0"})["result"]["level"] == "L0"


def test_auth_token_is_required_when_configured(monkeypatch):
    monkeypatch.setenv("TOKENSAFE_OUMV_AUTH_TOKEN", "starlette-secret")
    app = _app()
    mount_tokensafe_starlette(app, auto_discover=False)
    client = TestClient(app)

    missing = client.post("/__oumv/rpc", json={"jsonrpc": "2.0", "id": 1, "method": "oumv/observe", "params": {"level": "L0"}})
    assert missing.status_code == 401
    assert missing.json()["error"]["code"] == -32007

    authorized = client.post(
        "/__oumv/rpc",
        headers={"X-OUMV-Session-Token": "starlette-secret"},
        json={"jsonrpc": "2.0", "id": 2, "method": "oumv/observe", "params": {"level": "L0"}},
    )
    assert authorized.status_code == 200
    assert authorized.json()["result"]["level"] == "L0"


def test_mount_returns_the_adapter():
    adapter = mount_tokensafe_starlette(_app(), read_only=True)

    assert isinstance(adapter, TokenSafeStarlette)
    assert adapter.runtime.framework == "starlette"
    assert adapter.runtime.read_only is True
    assert "oumv/applyEdit" not in adapter.runtime.rpc._handlers
