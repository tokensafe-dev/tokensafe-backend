"""
Behavioral Test Oracle for Order Processing API.
Executes 12 Functional Acceptance Assertions against any ASGI backend implementation.
"""

import time
from typing import Any, Callable, Dict, List
import httpx

from benchmark.types import OracleAssertionResult, OracleEvaluationReport


class OrderApiOracle:
    """Ground truth behavioral oracle with 12 acceptance test cases."""

    ASSERTION_IDS = [
        "TC01_INITIAL_CATALOG",
        "TC02_CREATE_PRODUCT",
        "TC03_REJECT_INVALID_PRODUCT",
        "TC04_CREATE_ORDER_VALID",
        "TC05_REJECT_INSUFFICIENT_STOCK",
        "TC06_REJECT_DUPLICATE_IDEMPOTENCY",
        "TC07_QUERY_ORDER_BY_ID",
        "TC08_PAY_ORDER",
        "TC09_SHIP_ORDER",
        "TC10_CANCEL_ORDER_AND_RESTOCK",
        "TC11_REJECT_CANCEL_SHIPPED_ORDER",
        "TC12_DRY_RUN_SANDBOX_ROLLBACK",
    ]

    async def evaluate(self, app_factory: Callable[[], Any], is_tokensafe: bool = False) -> OracleEvaluationReport:
        app = app_factory()
        transport = httpx.ASGITransport(app=app)
        start_all = time.perf_counter()
        assertions: List[OracleAssertionResult] = []

        context: Dict[str, Any] = {}

        async with httpx.AsyncClient(transport=transport, base_url="http://test") as client:

            async def run_assertion(test_id: str, title: str, fn):
                start = time.perf_counter()
                try:
                    await fn()
                    assertions.append(
                        OracleAssertionResult(
                            id=test_id,
                            title=title,
                            passed=True,
                            duration_ms=round((time.perf_counter() - start) * 1000, 2),
                        )
                    )
                except Exception as e:
                    assertions.append(
                        OracleAssertionResult(
                            id=test_id,
                            title=title,
                            passed=False,
                            duration_ms=round((time.perf_counter() - start) * 1000, 2),
                            error=str(e),
                        )
                    )

            # TC01: Initial catalog inspection
            async def tc01():
                resp = await client.get("/api/products")
                assert resp.status_code == 200, f"Expected 200, got {resp.status_code}"
                data = resp.json()
                assert len(data) >= 2, f"Expected at least 2 pre-seeded products, got {len(data)}"
                titles = [p["title"] for p in data]
                assert "Mechanical Keyboard" in titles
                assert "Ergonomic Mouse" in titles
            await run_assertion("TC01_INITIAL_CATALOG", "Initial catalog inspection & inventory check", tc01)

            # TC02: Create valid product
            async def tc02():
                payload = {"title": "USB-C Hub", "price": 39.99, "stock": 15, "category": "electronics"}
                resp = await client.post("/api/products", json=payload)
                assert resp.status_code == 201, f"Expected 201, got {resp.status_code}"
                data = resp.json()
                assert data["id"] >= 3
                assert data["title"] == "USB-C Hub"
                assert data["price"] == 39.99
                context["new_prod_id"] = data["id"]
            await run_assertion("TC02_CREATE_PRODUCT", "Create new product with validation", tc02)

            # TC03: Reject invalid product (negative price)
            async def tc03():
                payload = {"title": "Bad Product", "price": -10.0, "stock": 5}
                resp = await client.post("/api/products", json=payload)
                assert resp.status_code == 422, f"Expected 422, got {resp.status_code}"
            await run_assertion("TC03_REJECT_INVALID_PRODUCT", "Reject invalid product (negative price / validation failure)", tc03)

            # TC04: Create valid order with stock decrement
            async def tc04():
                payload = {
                    "idempotency_key": "order_key_001",
                    "customer_id": "cust_100",
                    "items": [
                        {"product_id": 1, "quantity": 2},  # 129.99 * 2 = 259.98
                        {"product_id": 2, "quantity": 1},  # 79.50 * 1 = 79.50
                    ],
                }
                resp = await client.post("/api/orders", json=payload)
                assert resp.status_code == 201, f"Expected 201, got {resp.status_code}: {resp.text}"
                data = resp.json()
                assert data["status"] == "PENDING"
                assert data["total_amount"] == 339.48
                context["order_1_id"] = data["id"]

                # Verify stock decrement
                p1_resp = await client.get("/api/products/1")
                assert p1_resp.json()["stock"] == 8, f"Expected stock 8, got {p1_resp.json()['stock']}"
            await run_assertion("TC04_CREATE_ORDER_VALID", "Create order with stock calculation & decrement", tc04)

            # TC05: Insufficient stock rejection
            async def tc05():
                payload = {
                    "idempotency_key": "order_key_fail",
                    "customer_id": "cust_100",
                    "items": [{"product_id": 1, "quantity": 999}],
                }
                resp = await client.post("/api/orders", json=payload)
                assert resp.status_code == 400, f"Expected 400, got {resp.status_code}"
                assert "Insufficient stock" in resp.json().get("detail", "")
            await run_assertion("TC05_REJECT_INSUFFICIENT_STOCK", "Insufficient stock rejection", tc05)

            # TC06: Idempotent order duplicate rejection
            async def tc06():
                payload = {
                    "idempotency_key": "order_key_001",
                    "customer_id": "cust_100",
                    "items": [{"product_id": 2, "quantity": 1}],
                }
                resp = await client.post("/api/orders", json=payload)
                assert resp.status_code == 409, f"Expected 409 Conflict, got {resp.status_code}"
            await run_assertion("TC06_REJECT_DUPLICATE_IDEMPOTENCY", "Idempotent duplicate order key rejection", tc06)

            # TC07: Query order by ID
            async def tc07():
                order_id = context["order_1_id"]
                resp = await client.get(f"/api/orders/{order_id}")
                assert resp.status_code == 200, f"Expected 200, got {resp.status_code}"
                data = resp.json()
                assert data["id"] == order_id
                assert len(data["items"]) == 2
            await run_assertion("TC07_QUERY_ORDER_BY_ID", "Query order by ID & verify item structure", tc07)

            # TC08: Pay order (PENDING -> PAID)
            async def tc08():
                order_id = context["order_1_id"]
                resp = await client.post(f"/api/orders/{order_id}/pay")
                assert resp.status_code == 200, f"Expected 200, got {resp.status_code}"
                assert resp.json()["status"] == "PAID"
            await run_assertion("TC08_PAY_ORDER", "Pay order transition (PENDING -> PAID)", tc08)

            # TC09: Ship order (PAID -> SHIPPED)
            async def tc09():
                order_id = context["order_1_id"]
                resp = await client.post(f"/api/orders/{order_id}/ship")
                assert resp.status_code == 200, f"Expected 200, got {resp.status_code}"
                assert resp.json()["status"] == "SHIPPED"
            await run_assertion("TC09_SHIP_ORDER", "Ship order transition (PAID -> SHIPPED)", tc09)

            # TC10: Cancel order and verify stock replenishment
            async def tc10():
                # Create a second order
                create_resp = await client.post("/api/orders", json={
                    "idempotency_key": "order_key_cancelable",
                    "customer_id": "cust_200",
                    "items": [{"product_id": 1, "quantity": 3}],
                })
                assert create_resp.status_code == 201
                ord2_id = create_resp.json()["id"]

                # Stock for product 1 before cancel was 8 - 3 = 5
                p1_before = (await client.get("/api/products/1")).json()["stock"]
                assert p1_before == 5

                # Cancel order
                cancel_resp = await client.post(f"/api/orders/{ord2_id}/cancel")
                assert cancel_resp.status_code == 200
                assert cancel_resp.json()["status"] == "CANCELLED"

                # Verify stock replenished: 5 + 3 = 8
                p1_after = (await client.get("/api/products/1")).json()["stock"]
                assert p1_after == 8, f"Expected replenished stock 8, got {p1_after}"
            await run_assertion("TC10_CANCEL_ORDER_AND_RESTOCK", "Cancel order and verify stock replenishment", tc10)

            # TC11: Reject cancellation of already shipped order
            async def tc11():
                order_1_id = context["order_1_id"]
                resp = await client.post(f"/api/orders/{order_1_id}/cancel")
                assert resp.status_code == 400, f"Expected 400, got {resp.status_code}"
                assert "already shipped" in resp.json().get("detail", "").lower()
            await run_assertion("TC11_REJECT_CANCEL_SHIPPED_ORDER", "Reject cancellation of shipped order", tc11)

            # TC12: Dry-run simulation rollback & zero DB pollution guarantee
            async def tc12():
                if is_tokensafe:
                    # In TokenSafe: test UARP in-memory simulation / dry-run
                    rpc_payload = {
                        "jsonrpc": "2.0",
                        "id": 12,
                        "method": "oumv/verify",
                        "params": {
                            "target": "POST /api/orders",
                            "payload": {
                                "idempotency_key": "dryrun_sim_key",
                                "customer_id": "cust_sim",
                                "items": [{"product_id": 1, "quantity": 2}],
                            },
                        },
                    }
                    resp = await client.post("/__oumv/rpc", json=rpc_payload)
                    assert resp.status_code == 200, f"RPC failed: {resp.text}"
                    res = resp.json()["result"]
                    assert res["status"] == "PASS"
                    assert res.get("rolledBack") is True

                    # Verify product 1 stock was NOT decremented by dry run
                    p1 = (await client.get("/api/products/1")).json()
                    assert p1["stock"] == 8, f"Dry-run polluted state! Stock became {p1['stock']}"
                else:
                    # In Vanilla: verify that without dry-run, a direct test run consumes stock and mutates DB
                    resp = await client.post("/api/orders", json={
                        "idempotency_key": "vanilla_test_key",
                        "customer_id": "cust_vanilla",
                        "items": [{"product_id": 1, "quantity": 1}],
                    })
                    assert resp.status_code == 201
            await run_assertion("TC12_DRY_RUN_SANDBOX_ROLLBACK", "Dry-run sandbox verification & rollback safety", tc12)

        duration_ms = round((time.perf_counter() - start_all) * 1000, 2)
        passed_count = sum(1 for a in assertions if a.passed)
        all_passed = (passed_count == len(assertions))

        return OracleEvaluationReport(
            passed_count=passed_count,
            total_count=len(assertions),
            assertions=assertions,
            all_passed=all_passed,
            duration_ms=duration_ms,
        )
