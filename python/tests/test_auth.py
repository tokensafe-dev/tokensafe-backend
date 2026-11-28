"""Spec §12 transport auth: the refusal is real on every binding, not prose.

The HTTP refusal is also certified by the transport certificate; these tests pin
the SSE and WebSocket doors and the fail-closed rule (a configured token is never
optional), which a certificate cannot reach without a browser client.
"""

import pytest
from fastapi import FastAPI
from fastapi.testclient import TestClient
from starlette.websockets import WebSocketDisconnect

from tokensafe_python.adapters.fastapi import mount_tokensafe

TOKEN = "oumv-test-token"


@pytest.fixture
def client(tmp_path, monkeypatch):
    app = FastAPI()
    mount_tokensafe(app, auto_discover=False, project_root=str(tmp_path))
    with TestClient(app) as test_client:
        yield test_client


def _rpc(client: TestClient, headers=None):
    return client.post(
        "/__oumv/rpc",
        json={"jsonrpc": "2.0", "id": 1, "method": "oumv/observe", "params": {"level": "L0"}},
        headers=headers or {},
    )


def test_loopback_without_a_configured_token_is_open(client):
    # Development allowance: no token configured → the transport answers.
    assert _rpc(client).status_code == 200


def test_configured_token_is_required_and_fail_closed(client, monkeypatch):
    monkeypatch.setenv("TOKENSAFE_OUMV_AUTH_TOKEN", TOKEN)

    missing = _rpc(client)
    assert missing.status_code == 401
    assert missing.json()["error"]["code"] == -32007

    invalid = _rpc(client, {"X-OUMV-Session-Token": "not-the-token"})
    assert invalid.status_code == 401
    assert invalid.json()["error"]["code"] == -32007

    wrong_scheme = _rpc(client, {"Authorization": "Basic b3Vtdi1ub3QtdGhlLXNjaGVtZQ=="})
    assert wrong_scheme.status_code == 401

    accepted = _rpc(client, {"X-OUMV-Session-Token": TOKEN})
    assert accepted.status_code == 200
    assert accepted.json()["result"]["level"] == "L0"


def test_sse_stream_refuses_an_unauthenticated_connection(client, monkeypatch):
    monkeypatch.setenv("TOKENSAFE_OUMV_AUTH_TOKEN", TOKEN)

    refused = client.get("/__oumv/events")
    assert refused.status_code == 401
    assert refused.json()["error"]["code"] == -32007


def test_websocket_refuses_an_unauthenticated_upgrade(client, monkeypatch):
    monkeypatch.setenv("TOKENSAFE_OUMV_AUTH_TOKEN", TOKEN)

    with pytest.raises(WebSocketDisconnect):
        with client.websocket_connect("/__oumv/rpc"):
            pass

    with client.websocket_connect("/__oumv/rpc", headers={"X-OUMV-Session-Token": TOKEN}) as socket:
        socket.send_json({"jsonrpc": "2.0", "id": 1, "method": "oumv/handshake", "params": {}})
        reply = socket.receive_json()
        assert reply["result"]["protocolVersion"] == "1.6.0"
