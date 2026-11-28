"""SSE and WebSocket transports for the FastAPI adapter (spec §2.3/§2.4)."""

import hashlib
import re

import pytest
import starlette
from fastapi import FastAPI
from fastapi.testclient import TestClient

from tokensafe_python.adapters.fastapi import mount_tokensafe

# Starlette's TestClient only returns response headers for a streaming endpoint from
# 1.10 on; 1.7 blocks inside `client.stream()` until the app finishes, which an
# infinite SSE stream never does. Python 3.10 cannot install fastapi 0.165 (it
# requires >= 3.11), so it resolves 0.162 -> starlette 1.7 and hangs. Gate on the
# client's capability, not on the interpreter, so coverage returns by itself once
# fastapi supports 3.10 again.
_STARLETTE = tuple(int(part) for part in re.findall(r"\d+", starlette.__version__)[:2])
_SSE_STREAMING_CLIENT = _STARLETTE >= (1, 10)

sse_needs_streaming_client = pytest.mark.skipif(
    not _SSE_STREAMING_CLIENT,
    reason=(
        f"starlette {starlette.__version__} TestClient blocks on an infinite SSE stream "
        "(Python 3.10 resolves fastapi 0.162); the transport itself is covered on 3.11+"
    ),
)


def _occ(content: str) -> str:
    return "sha256:" + hashlib.sha256(content.encode("utf-8")).hexdigest()


def _read_state_event(client: TestClient, headers: dict | None = None) -> str:
    # A bounded read: an SSE frame that never arrives must fail the test, not
    # stall the runner (that is how the 3.10 leg used to hang for 50 minutes).
    with client.stream("GET", "/__oumv/events", headers=headers or {}, timeout=10.0) as response:
        text = ""
        for chunk in response.iter_text():
            text += chunk
            if "event: state" in text and "src/probe.py" in text:
                break
    return text


def _state_block(text: str) -> str:
    return next(
        (block for block in text.split("\n\n") if "event: state" in block and "src/probe.py" in block),
        "",
    )


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


@sse_needs_streaming_client
def test_sse_state_event_replays_from_last_event_id(tmp_path):
    app = FastAPI()
    adapter = mount_tokensafe(app, auto_discover=False, project_root=str(tmp_path))
    adapter.runtime.record_state("src/probe.py", "sha256:" + "a" * 64, "patch")

    with TestClient(app) as client:
        first = _state_block(_read_state_event(client))
        assert first, "SSE must stream the published state event"
        event_id = next(line.split(": ", 1)[1] for line in first.splitlines() if line.startswith("id: "))
        assert event_id.isdigit()

        replayed = _state_block(_read_state_event(client, {"Last-Event-ID": "0"}))
        assert replayed == first, "Last-Event-ID: 0 must replay the same frame"


def test_websocket_rejected_handshake_keeps_the_live_session(tmp_path):
    content = "value = 1\n"
    (tmp_path / "target.py").write_text(content, encoding="utf-8")

    app = FastAPI()
    mount_tokensafe(app, auto_discover=False, project_root=str(tmp_path))

    with TestClient(app).websocket_connect("/__oumv/rpc") as ws:
        ws.send_json({"jsonrpc": "2.0", "id": 1, "method": "oumv/handshake", "params": {}})
        session_id = ws.receive_json()["result"]["sessionId"]

        # A negative handshake case must not unbind the connection.
        ws.send_json(
            {"jsonrpc": "2.0", "id": 2, "method": "oumv/handshake", "params": {"protocolVersion": "9.9.9"}}
        )
        rejected = ws.receive_json()
        assert rejected["error"]["code"] == -32006

        ws.send_json(
            {
                "jsonrpc": "2.0",
                "id": 3,
                "method": "oumv/applyEdit",
                "params": {
                    "sessionId": session_id,
                    "path": "target.py",
                    "mode": "patch",
                    "expectedVersion": _occ(content),
                    "hunks": [{"target": "value = 1", "replacement": "value = 2"}],
                },
            }
        )
        frames = []
        for _ in range(4):
            frame = ws.receive_json()
            frames.append(frame)
            if frame.get("method") == "notifications/oumv.state":
                break

    notification = next(
        frame for frame in frames if frame.get("method") == "notifications/oumv.state"
    )
    assert notification["params"]["sessionId"] == session_id
    content = "value = 1\n"
    (tmp_path / "target.py").write_text(content, encoding="utf-8")

    app = FastAPI()
    mount_tokensafe(app, auto_discover=False, project_root=str(tmp_path))

    with TestClient(app).websocket_connect("/__oumv/rpc") as ws:
        ws.send_json({"jsonrpc": "2.0", "id": 1, "method": "oumv/handshake", "params": {}})
        session_id = ws.receive_json()["result"]["sessionId"]
        ws.send_json(
            {
                "jsonrpc": "2.0",
                "id": 2,
                "method": "oumv/applyEdit",
                "params": {
                    "sessionId": session_id,
                    "path": "target.py",
                    "mode": "patch",
                    "expectedVersion": _occ(content),
                    "hunks": [{"target": "value = 1", "replacement": "value = 2"}],
                },
            }
        )

        frames = []
        for _ in range(4):
            frame = ws.receive_json()
            frames.append(frame)
            if frame.get("method") == "notifications/oumv.state":
                break

    notification = next(
        frame for frame in frames if frame.get("method") == "notifications/oumv.state"
    )
    assert notification["params"]["sessionId"] == session_id
    assert notification["params"]["path"] == "target.py"
    assert notification["params"]["operation"] == "patch"
    assert notification["params"]["version"].startswith("sha256:")
