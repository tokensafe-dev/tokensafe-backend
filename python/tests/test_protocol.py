"""
Tests for UARP protocol and JSON-RPC router.
"""

import pytest
from tokensafe_python.protocol.rpc import JSONRPCHandler


@pytest.mark.asyncio
async def test_rpc_handler_success():
    handler = JSONRPCHandler()
    handler.register("ping", lambda msg="": f"pong: {msg}")

    payload = {
        "jsonrpc": "2.0",
        "id": 1,
        "method": "ping",
        "params": {"msg": "hello"}
    }
    resp = await handler.handle_payload(payload)

    assert resp["id"] == 1
    assert resp["result"] == "pong: hello"
    assert resp.get("error") is None


@pytest.mark.asyncio
async def test_rpc_handler_method_not_found():
    handler = JSONRPCHandler()
    payload = {
        "jsonrpc": "2.0",
        "id": 2,
        "method": "unknown_method"
    }
    resp = await handler.handle_payload(payload)

    assert resp["id"] == 2
    assert resp.get("result") is None
    assert resp["error"]["code"] == -32601
    assert "Method not found" in resp["error"]["message"]


@pytest.mark.asyncio
async def test_rpc_handler_batch():
    handler = JSONRPCHandler()
    handler.register("add", lambda a, b: a + b)

    batch = [
        {"jsonrpc": "2.0", "id": 10, "method": "add", "params": {"a": 2, "b": 3}},
        {"jsonrpc": "2.0", "id": 11, "method": "add", "params": {"a": 10, "b": 20}},
    ]
    resps = await handler.handle_payload(batch)

    assert len(resps) == 2
    assert resps[0]["result"] == 5
    assert resps[1]["result"] == 30
