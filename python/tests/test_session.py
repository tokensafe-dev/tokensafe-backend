"""Handshake sessions and the §9.2 descriptor (Python runtime)."""

import json
import os
import stat
import tempfile

import httpx
import pytest
from fastapi import FastAPI

from tokensafe_python.adapters.fastapi import mount_tokensafe
from tokensafe_python.core import session as session_mod


async def _rpc(app, method, params=None):
    transport = httpx.ASGITransport(app=app)
    async with httpx.AsyncClient(transport=transport, base_url="http://test") as client:
        resp = await client.post(
            "/__oumv/rpc",
            json={"jsonrpc": "2.0", "id": 1, "method": method, "params": params or {}},
        )
    assert resp.status_code == 200
    return resp.json()


@pytest.mark.asyncio
async def test_handshake_issues_a_session():
    app = FastAPI()
    mount_tokensafe(app, auto_discover=False)

    body = await _rpc(app, "oumv/handshake")
    result = body["result"]
    assert result["profile"] == "backend"
    assert result["sessionId"].startswith("sess-")
    assert result["ttlSeconds"] > 0


@pytest.mark.asyncio
async def test_unknown_session_is_rejected_with_reason():
    app = FastAPI()
    mount_tokensafe(app, auto_discover=False)

    body = await _rpc(app, "oumv/observe", {"level": "L1", "sessionId": "sess-deadbeef"})
    error = body["error"]
    assert error["code"] == -32011
    assert error["data"]["reason"] == "session_expired"


@pytest.mark.asyncio
async def test_stateless_calls_are_allowed():
    app = FastAPI()
    mount_tokensafe(app, auto_discover=False)

    body = await _rpc(app, "oumv/observe", {"level": "L1"})
    assert not body.get("error")
    assert body["result"]["level"] == "L1"


@pytest.mark.asyncio
async def test_disconnect_invalidates_the_session():
    app = FastAPI()
    mounted = mount_tokensafe(app, auto_discover=False)

    session_id = (await _rpc(app, "oumv/handshake"))["result"]["sessionId"]
    closed = await _rpc(app, "oumv/disconnect", {"sessionId": session_id})
    assert closed["result"]["closed"] is True

    after = await _rpc(app, "oumv/observe", {"level": "L1", "sessionId": session_id})
    assert after["error"]["code"] == -32011
    assert mounted.runtime.sessions.count() == 0


def test_descriptor_registers_backend_without_dropping_frontend():
    home = tempfile.mkdtemp(prefix="tokensafe_home_")
    workspace = tempfile.mkdtemp(prefix="tokensafe_ws_")
    previous = os.environ.get("TOKENSAFE_HOME")
    os.environ["TOKENSAFE_HOME"] = home
    try:
        path = session_mod.write_descriptor(
            project_root=workspace,
            url="http://127.0.0.1:8000/__oumv/rpc",
            name="tokensafe-python",
            version="0.3.0",
            transports=["http", "sse", "ws"],
        )
        assert path.startswith(os.path.join(home, "runtimes"))
        assert workspace not in path.replace(os.path.realpath(workspace), "")

        mode = stat.S_IMODE(os.stat(path).st_mode)
        assert mode == 0o600

        registry = json.load(open(path, encoding="utf-8"))
        backend = registry["backend"]
        for key in ("type", "name", "version", "url", "capabilities"):
            assert key in backend
        assert backend["type"] == "backend"
        assert backend["capabilities"]["observeLevels"]
        assert backend["capabilities"]["transports"] == ["http", "sse", "ws"]

        # A frontend entry written by the Vite plugin shares the file (spec §9.2).
        registry["frontend"] = {
            "type": "frontend",
            "name": "react",
            "version": "19.0.0",
            "url": "http://localhost:5173/__oumv/rpc",
            "capabilities": {"observeLevels": ["L0", "L1", "L2"], "transports": ["http", "sse", "ws"]},
        }
        json.dump(registry, open(path, "w", encoding="utf-8"))

        session_mod.write_descriptor(
            project_root=workspace,
            url="http://127.0.0.1:8000/__oumv/rpc",
            name="tokensafe-python",
            version="0.3.0",
        )
        assert "frontend" in json.load(open(path, encoding="utf-8"))

        session_mod.delete_descriptor(workspace)
        remaining = json.load(open(path, encoding="utf-8"))
        assert "backend" not in remaining
        assert "frontend" in remaining
    finally:
        if previous is None:
            os.environ.pop("TOKENSAFE_HOME", None)
        else:
            os.environ["TOKENSAFE_HOME"] = previous


@pytest.mark.asyncio
async def test_auth_token_is_enforced_when_configured(monkeypatch):
    monkeypatch.setenv("TOKENSAFE_OUMV_AUTH_TOKEN", "secret-token")
    app = FastAPI()
    mount_tokensafe(app, auto_discover=False)

    transport = httpx.ASGITransport(app=app)
    async with httpx.AsyncClient(transport=transport, base_url="http://test") as client:
        denied = await client.post(
            "/__oumv/rpc",
            json={"jsonrpc": "2.0", "id": 1, "method": "oumv/handshake", "params": {}},
        )
        allowed = await client.post(
            "/__oumv/rpc",
            headers={"X-OUMV-Session-Token": "secret-token"},
            json={"jsonrpc": "2.0", "id": 2, "method": "oumv/handshake", "params": {}},
        )

    assert denied.status_code == 401
    assert allowed.status_code == 200
    assert allowed.json()["result"]["sessionId"]


@pytest.mark.asyncio
async def test_handshake_narrows_requested_capabilities():
    app = FastAPI()
    mount_tokensafe(app, auto_discover=False)

    full = (await _rpc(app, "oumv/handshake"))["result"]
    assert len(full["capabilities"]["observe"]["levels"]) == 5

    narrowed = (
        await _rpc(
            app,
            "oumv/handshake",
            {
                "protocolVersion": "1.1.0",
                "requestedCapabilities": {"observe": {"levels": ["L0", "L1"], "formats": ["json", "yaml"]}},
            },
        )
    )["result"]
    assert narrowed["capabilities"]["observe"]["levels"] == ["L0", "L1"]
    # yaml is not implemented, so it is filtered out rather than echoed.
    assert narrowed["capabilities"]["observe"]["formats"] == ["json"]

    yaml_only = (
        await _rpc(
            app,
            "oumv/handshake",
            {"protocolVersion": "1.1.0", "requestedCapabilities": {"observe": {"formats": ["yaml"]}}},
        )
    )["result"]
    assert yaml_only["capabilities"]["observe"]["formats"] == []
    assert len(yaml_only["capabilities"]["observe"]["levels"]) == 5
