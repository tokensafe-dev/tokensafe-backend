"""SSE and WebSocket transports for the FastAPI adapter (spec §2.3/§2.4)."""

from fastapi import FastAPI
from fastapi.testclient import TestClient

from tokensafe_python.adapters.fastapi import mount_tokensafe


def test_websocket_dispatches_jsonrpc():
    app = FastAPI()
    mount_tokensafe(app, auto_discover=False)

    with TestClient(app).websocket_connect("/__oumv/rpc") as ws:
        ws.send_json({"jsonrpc": "2.0", "id": 1, "method": "oumv/handshake", "params": {}})
        result = ws.receive_json()

    assert "error" not in result or not result["error"]
    assert result["result"]["profile"] == "backend"
    assert result["result"]["sessionId"].startswith("sess-")


def test_websocket_notification_gets_no_reply():
    app = FastAPI()
    mount_tokensafe(app, auto_discover=False)

    with TestClient(app).websocket_connect("/__oumv/rpc") as ws:
        ws.send_json({"jsonrpc": "2.0", "method": "oumv/observe", "params": {"level": "L0"}})
        ws.send_json({"jsonrpc": "2.0", "id": 2, "method": "oumv/observe", "params": {"level": "L1"}})
        reply = ws.receive_json()

    # The notification must not consume a frame: the first reply belongs to id=2.
    assert reply["id"] == 2
    assert reply["result"]["level"] == "L1"
