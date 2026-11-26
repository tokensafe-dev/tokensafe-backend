"""
Level 3 semantic operation contracts (spec §10.4): @oumv_operation decorator,
registry wiring into mounted runtimes, and L2 contract exposure.
"""

import httpx
import pytest
from fastapi import FastAPI

from tokensafe_python.adapters.fastapi import mount_tokensafe
from tokensafe_python.core.contracts import get_operations, oumv_operation, reset_operations


def _declare_cancel_operation():
    """Declares the sample contract inside the fixture window.

    Module-level decorators would run before the autouse reset and be wiped,
    so declarations happen per-test (mirrors real apps declaring at import time
    before mount_tokensafe).
    """

    @oumv_operation(
        "orders.cancel",
        description="Cancel an order",
        input_schema={"order_id": "int"},
        constraints=["Order must not be shipped", "Order must belong to the current customer"],
        verification=["Order status must become cancelled"],
    )
    def cancel_order(order_id: int) -> dict:
        return {"cancelled": order_id}

    return cancel_order


@pytest.fixture(autouse=True)
def clean_registry():
    reset_operations()
    _declare_cancel_operation()
    yield
    reset_operations()


@pytest.mark.asyncio
async def test_oumv_operation_registers_contract():
    ops = get_operations()
    assert "orders.cancel" in ops
    contract = ops["orders.cancel"]
    assert contract["description"] == "Cancel an order"
    assert contract["handler"] == "cancel_order"
    assert contract["constraints"][0] == "Order must not be shipped"
    # Contracts are explicitly declared — contract provenance, confirmed
    assert contract["evidence"]["provenance"] == "contract"
    assert contract["evidence"]["confidence"] == "confirmed"
    # The handler still works as a plain function
    assert ops["orders.cancel"]["handler"] == "cancel_order"
    assert _declare_cancel_operation()(7) == {"cancelled": 7}


@pytest.mark.asyncio
async def test_l2_exposes_declared_operation_contract():
    app = FastAPI()
    mount_tokensafe(app, auto_discover=False)

    transport = httpx.ASGITransport(app=app)
    async with httpx.AsyncClient(transport=transport, base_url="http://test") as client:
        resp = await client.post(
            "/__oumv/rpc",
            json={"jsonrpc": "2.0", "id": 1, "method": "oumv/observe", "params": {"level": "L2", "target": "orders.cancel"}},
        )
    result = resp.json()["result"]
    assert result["data"]["operation"]["id"] == "orders.cancel"
    assert result["data"]["operation"]["inputSchema"] == {"order_id": "int"}
    assert "Order status must become cancelled" in result["data"]["operation"]["verification"]

    # L2 without target lists all declared operations
    async with httpx.AsyncClient(transport=transport, base_url="http://test") as client:
        resp = await client.post(
            "/__oumv/rpc",
            json={"jsonrpc": "2.0", "id": 2, "method": "oumv/observe", "params": {"level": "L2"}},
        )
    operations = resp.json()["result"]["data"]["operations"]
    assert any(op["id"] == "orders.cancel" for op in operations)


@pytest.mark.asyncio
async def test_operation_contract_appears_in_causality_graph():
    app = FastAPI()
    mount_tokensafe(app, auto_discover=False)

    transport = httpx.ASGITransport(app=app)
    async with httpx.AsyncClient(transport=transport, base_url="http://test") as client:
        resp = await client.post(
            "/__oumv/rpc",
            json={"jsonrpc": "2.0", "id": 1, "method": "causality.query", "params": {"model": "orders.cancel"}},
        )
    result = resp.json()["result"]
    assert any(node["id"] == "operation:orders.cancel" for node in result["nodes"])
