"""§8.5 read-only deployment: the writing methods are absent, not policy-denied.

The distinction matters: `-32601 METHOD_NOT_FOUND` proves the method is not in the
registry at all, so a client cannot talk itself into the write path and the server has
no handler to reach. `-32007` would only prove that a handler refused this time.
"""

import pytest
from fastapi import FastAPI
from fastapi.testclient import TestClient

from tokensafe_python.adapters.fastapi import TokenSafeFastAPI, mount_tokensafe

ABSENT = [
    ("oumv/applyEdit", {"path": "app.py", "replacement": "x = 1"}),
    ("oumv/mutate", {"path": "app.py", "replacement": "x = 1"}),
    ("oumv/previewEdit", {"path": "app.py", "replacement": "x = 1"}),
    ("oumv/verify", {"target": "GET /"}),
    ("oumv/build", {"command": "echo ok"}),
    ("mutation.apply", {"action": {"path": "/", "method": "POST"}}),
    ("mutation.simulate", {"action": {"path": "/", "method": "POST"}}),
]


def _client(read_only: bool = True) -> TestClient:
    app = FastAPI()

    @app.get("/health")
    def health():
        return {"ok": True}

    TokenSafeFastAPI(app, auto_discover=False, read_only=read_only)
    return TestClient(app)


def _rpc(client: TestClient, method: str, params: dict) -> dict:
    response = client.post("/__oumv/rpc", json={"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
    return response.json()


@pytest.mark.parametrize("method,params", ABSENT)
def test_writing_methods_are_not_registered(method, params):
    body = _rpc(_client(), method, params)

    assert body["error"]["code"] == -32601


def test_absent_method_wins_over_session_validation():
    """A stale session must not turn absence into -32011 — the registry is consulted first."""
    body = _rpc(_client(), "oumv/applyEdit", {"path": "app.py", "replacement": "x = 1", "sessionId": "sess-unknown"})

    assert body["error"]["code"] == -32601


def test_reads_stay_registered():
    client = _client()

    observe = _rpc(client, "oumv/observe", {"level": "L0"})
    assert observe["result"]["level"] == "L0"

    # A registered getSource answers RESOURCE_NOT_FOUND for a missing file; only an
    # absent method answers -32601.
    source = _rpc(client, "oumv/getSource", {"path": "definitely-not-here.py"})
    assert source["error"]["code"] != -32601


def test_handshake_declares_read_only():
    caps = _rpc(_client(), "oumv/handshake", {"protocolVersion": "1.6.0"})["result"]["capabilities"]

    assert caps["readOnly"] is True
    assert "mutate" not in caps
    assert "verify" not in caps
    # Observe is a read pillar and stays advertised.
    assert caps["observe"]["levels"][0] == "L0"


def test_write_runtime_keeps_the_old_shape():
    """The default deployment is unchanged: methods registered, no readOnly flag."""
    client = _client(read_only=False)

    caps = _rpc(client, "oumv/handshake", {"protocolVersion": "1.6.0"})["result"]["capabilities"]
    assert "readOnly" not in caps
    assert caps["mutate"]["occ"] is True

    body = _rpc(client, "oumv/applyEdit", {"path": "app.py", "replacement": "x = 1"})
    assert body["error"]["code"] != -32601


def test_mount_tokensafe_forwards_the_flag():
    adapter = mount_tokensafe(FastAPI(), read_only=True)

    assert adapter.runtime.read_only is True
    assert "oumv/applyEdit" not in adapter.runtime.rpc._handlers
    assert "oumv/observe" in adapter.runtime.rpc._handlers
