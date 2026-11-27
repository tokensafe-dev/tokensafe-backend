"""SSE and WebSocket transports for the FastAPI adapter (spec §2.3/§2.4)."""

import hashlib

from fastapi import FastAPI
from fastapi.testclient import TestClient

from tokensafe_python.adapters.fastapi import mount_tokensafe


def _occ(content: str) -> str:
    return "sha256:" + hashlib.sha256(content.encode("utf-8")).hexdigest()


def _read_state_event(client: TestClient, headers: dict | None = None) -> str:
    with client.stream("GET", "/__oumv/events", headers=headers or {}) as response:
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


def test_websocket_pushes_state_notification(tmp_path):
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
