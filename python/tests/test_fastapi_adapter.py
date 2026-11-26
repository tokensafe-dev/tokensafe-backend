"""
End-to-end tests for FastAPI TokenSafe UARP adapter.
"""

import pytest
from fastapi import FastAPI, HTTPException
from pydantic import BaseModel
import httpx

from tokensafe_python.adapters.fastapi import mount_tokensafe


class ItemCreate(BaseModel):
    name: str
    price: float


class ItemResponse(BaseModel):
    id: int
    name: str
    price: float


@pytest.fixture
def test_app():
    app = FastAPI(title="Store API", version="1.0.0")
    items_db = []

    @app.get("/items", response_model=list[ItemResponse])
    def list_items():
        """Lists all items in store."""
        return items_db

    @app.post("/items", response_model=ItemResponse, status_code=201)
    def create_item(item: ItemCreate):
        """Creates a new store item."""
        new_item = ItemResponse(id=len(items_db) + 1, name=item.name, price=item.price)
        items_db.append(new_item)
        return new_item

    # Mount TokenSafe UARP runtime
    mount_tokensafe(app, auto_discover=False)
    return app


@pytest.mark.asyncio
async def test_fastapi_health(test_app):
    transport = httpx.ASGITransport(app=test_app)
    async with httpx.AsyncClient(transport=transport, base_url="http://test") as client:
        resp = await client.get("/__oumv/health")
        assert resp.status_code == 200
        data = resp.json()
        assert data["status"] == "ok"
        assert data["framework"] == "fastapi"
        assert data["registered_routes"] >= 2
        assert data["registered_models"] >= 2


@pytest.mark.asyncio
async def test_fastapi_state_read(test_app):
    transport = httpx.ASGITransport(app=test_app)
    async with httpx.AsyncClient(transport=transport, base_url="http://test") as client:
        req_payload = {
            "jsonrpc": "2.0",
            "id": 100,
            "method": "state.read",
            "params": {"scope": "all"}
        }
        resp = await client.post("/__oumv/rpc", json=req_payload)
        assert resp.status_code == 200
        body = resp.json()
        assert body["id"] == 100
        result = body["result"]
        assert result["framework"] == "fastapi"

        # Verify routes are captured
        route_paths = [r["path"] for r in result["routes"]]
        assert "/items" in route_paths

        # Verify models are captured
        assert "ItemCreate" in result["schema_models"]
        assert "ItemResponse" in result["schema_models"]


@pytest.mark.asyncio
async def test_fastapi_causality_query(test_app):
    transport = httpx.ASGITransport(app=test_app)
    async with httpx.AsyncClient(transport=transport, base_url="http://test") as client:
        req_payload = {
            "jsonrpc": "2.0",
            "id": 101,
            "method": "causality.query",
            "params": {"endpoint": "/items"}
        }
        resp = await client.post("/__oumv/rpc", json=req_payload)
        assert resp.status_code == 200
        result = resp.json()["result"]
        assert result["total_nodes"] >= 2
        node_labels = [n["label"] for n in result["nodes"]]
        assert any("items" in l for l in node_labels)


@pytest.mark.asyncio
async def test_fastapi_mutation_simulate_and_verify(test_app):
    transport = httpx.ASGITransport(app=test_app)
    async with httpx.AsyncClient(transport=transport, base_url="http://test") as client:
        # 1. Simulate POST /items
        sim_payload = {
            "jsonrpc": "2.0",
            "id": 200,
            "method": "mutation.simulate",
            "params": {
                "action": {
                    "type": "http_request",
                    "path": "/items",
                    "method": "POST",
                    "body": {"name": "Gaming Laptop", "price": 1499.99}
                }
            }
        }
        resp = await client.post("/__oumv/rpc", json=sim_payload)
        assert resp.status_code == 200
        sim_res = resp.json()["result"]
        assert sim_res["success"] is True
        assert sim_res["status_code"] == 201
        assert sim_res["response"]["name"] == "Gaming Laptop"
        # Honest rollback reporting: this app has no DB engine wired, so no
        # transactional rollback exists — must not be fabricated as True.
        assert sim_res["rolled_back"] is False
        assert sim_res["rollback_status"] == "not_applicable"

        # 2. Evaluate verification assertion
        assert_payload = {
            "jsonrpc": "2.0",
            "id": 201,
            "method": "verification.assert",
            "params": {
                "assertions": [
                    {"target": "status_code", "operator": "eq", "expected": 201},
                    {"target": "response.name", "operator": "eq", "expected": "Gaming Laptop"},
                    {"target": "response.price", "operator": "gt", "expected": 1000.0}
                ],
                "context": sim_res
            }
        }
        assert_resp = await client.post("/__oumv/rpc", json=assert_payload)
        assert assert_resp.status_code == 200
        assert_res = assert_resp.json()["result"]
        assert assert_res["passed"] is True
        assert assert_res["passed_count"] == 3
        assert assert_res["failed_count"] == 0


@pytest.mark.asyncio
async def test_fastapi_oumv_standard_protocol(test_app):
    """Tests standard OUMV methods: handshake, observe, understand, verify."""
    transport = httpx.ASGITransport(app=test_app)
    async with httpx.AsyncClient(transport=transport, base_url="http://test") as client:
        # 1. oumv/handshake
        hs_resp = await client.post(
            "/__oumv/rpc",
            json={"jsonrpc": "2.0", "id": 1, "method": "oumv/handshake", "params": {"protocolVersion": "1.0.0"}},
        )
        assert hs_resp.status_code == 200
        hs_data = hs_resp.json()["result"]
        assert hs_data["protocolVersion"] == "1.1.0"
        assert hs_data["runtimeInfo"]["name"] == "tokensafe-python"
        assert "L0" in hs_data["capabilities"]["observe"]["levels"]

        # 2. oumv/observe L0 (Pulse)
        obs_resp = await client.post(
            "/__oumv/rpc",
            json={"jsonrpc": "2.0", "id": 2, "method": "oumv/observe", "params": {"level": "L0"}},
        )
        assert obs_resp.status_code == 200
        obs_data = obs_resp.json()["result"]
        assert obs_data["data"]["status"] == "HEALTHY"
        assert obs_data["data"]["routesCount"] >= 2

        # 3. oumv/observe L1 (Surface)
        obs_l1_resp = await client.post(
            "/__oumv/rpc",
            json={"jsonrpc": "2.0", "id": 3, "method": "oumv/observe", "params": {"level": "L1"}},
        )
        assert obs_l1_resp.status_code == 200
        l1_data = obs_l1_resp.json()["result"]
        assert any(ep["path"] == "/items" for ep in l1_data["data"]["endpoints"])

        # 4. oumv/understand (Causality)
        und_resp = await client.post(
            "/__oumv/rpc",
            json={"jsonrpc": "2.0", "id": 4, "method": "oumv/understand", "params": {"query": "causality", "target": "POST /items"}},
        )
        assert und_resp.status_code == 200
        und_data = und_resp.json()["result"]
        assert "nodes" in und_data

        # 5. oumv/verify
        ver_resp = await client.post(
            "/__oumv/rpc",
            json={
                "jsonrpc": "2.0",
                "id": 5,
                "method": "oumv/verify",
                "params": {
                    "action": {
                        "type": "http_request",
                        "path": "/items",
                        "method": "POST",
                        "body": {"name": "Test Item", "price": 49.99},
                    }
                },
            },
        )
        assert ver_resp.status_code == 200
        ver_data = ver_resp.json()["result"]
        assert ver_data["status"] == "PASS"
