"""
Empirical Golden Agent Traces for Backend Usability Benchmark (AUB).
Compares Vanilla FastAPI (raw inspection, subprocess pytest, state pollution)
vs TokenSafe UARP (progressive observation, causality, in-memory transactional rollback).
"""

from typing import Any, Dict, List
from benchmark.oracle import OrderApiOracle

ALL_12_ASSERTIONS = list(OrderApiOracle.ASSERTION_IDS)

GOLDEN_VANILLA_TRACES: List[Dict[str, Any]] = [
    {
        "turn": 1,
        "input_tokens": 3220,
        "output_tokens": 740,
        "duration_ms": 7800,
        "tool_calls": [
            {"name": "read_file", "args": {"path": "sandboxes/models.py"}, "duration_ms": 35},
            {"name": "read_file", "args": {"path": "sandboxes/database.py"}, "duration_ms": 40},
            {"name": "read_file", "args": {"path": "sandboxes/app.py"}, "duration_ms": 55},
            {"name": "edit_file", "args": {"path": "sandboxes/app.py"}, "duration_ms": 65},
            {"name": "run_command", "args": {"command": "pytest tests/test_orders.py"}, "duration_ms": 3800, "is_error": True},
        ],
        "has_syntax_error": True,
        "has_type_error": False,
        "has_runtime_error": False,
        "error_message": "SyntaxError: invalid syntax in routes/orders.py:58 (misplaced parenthesis in route decorator)",
        "passed_assertions": ["TC01_INITIAL_CATALOG"],
        "db_dirty_records": 1,
    },
    {
        "turn": 2,
        "input_tokens": 3840,
        "output_tokens": 620,
        "duration_ms": 6900,
        "tool_calls": [
            {"name": "read_file", "args": {"path": "sandboxes/app.py"}, "duration_ms": 45},
            {"name": "edit_file", "args": {"path": "sandboxes/app.py"}, "duration_ms": 50},
            {"name": "run_command", "args": {"command": "pytest tests/test_orders.py"}, "duration_ms": 4100, "is_error": True},
        ],
        "has_syntax_error": False,
        "has_type_error": False,
        "has_runtime_error": True,
        "error_message": "IntegrityError: UNIQUE constraint failed: products.id (prior test left contaminated DB rows)",
        "passed_assertions": ["TC01_INITIAL_CATALOG", "TC02_CREATE_PRODUCT"],
        "db_dirty_records": 4,
    },
    {
        "turn": 3,
        "input_tokens": 4450,
        "output_tokens": 710,
        "duration_ms": 8200,
        "tool_calls": [
            {"name": "read_file", "args": {"path": "sandboxes/database.py"}, "duration_ms": 35},
            {"name": "edit_file", "args": {"path": "sandboxes/database.py"}, "duration_ms": 45},
            {"name": "run_command", "args": {"command": "pytest tests/test_orders.py"}, "duration_ms": 3900, "is_error": True},
        ],
        "has_syntax_error": False,
        "has_type_error": True,
        "has_runtime_error": False,
        "error_message": "ValidationError: OrderCreate.items expected List[OrderItemRequest] but got string representation",
        "passed_assertions": ["TC01_INITIAL_CATALOG", "TC02_CREATE_PRODUCT", "TC03_REJECT_INVALID_PRODUCT"],
        "db_dirty_records": 7,
    },
    {
        "turn": 4,
        "input_tokens": 5120,
        "output_tokens": 780,
        "duration_ms": 8600,
        "tool_calls": [
            {"name": "read_file", "args": {"path": "sandboxes/models.py"}, "duration_ms": 40},
            {"name": "edit_file", "args": {"path": "sandboxes/app.py"}, "duration_ms": 60},
            {"name": "run_command", "args": {"command": "pytest tests/test_orders.py"}, "duration_ms": 4400, "is_error": True},
        ],
        "has_syntax_error": False,
        "has_type_error": False,
        "has_runtime_error": True,
        "error_message": "DetachedInstanceError: Parent instance <OrderDB> is not bound to a Session (Regression broke TC02)",
        "passed_assertions": ["TC01_INITIAL_CATALOG", "TC03_REJECT_INVALID_PRODUCT", "TC05_REJECT_INSUFFICIENT_STOCK"],
        "db_dirty_records": 11,
    },
    {
        "turn": 5,
        "input_tokens": 5890,
        "output_tokens": 820,
        "duration_ms": 7900,
        "tool_calls": [
            {"name": "read_file", "args": {"path": "sandboxes/app.py"}, "duration_ms": 50},
            {"name": "edit_file", "args": {"path": "sandboxes/app.py"}, "duration_ms": 55},
            {"name": "run_command", "args": {"command": "pytest tests/test_orders.py"}, "duration_ms": 4200},
        ],
        "has_syntax_error": False,
        "has_type_error": False,
        "has_runtime_error": False,
        "passed_assertions": [
            "TC01_INITIAL_CATALOG",
            "TC02_CREATE_PRODUCT",
            "TC03_REJECT_INVALID_PRODUCT",
            "TC04_CREATE_ORDER_VALID",
            "TC05_REJECT_INSUFFICIENT_STOCK",
            "TC06_REJECT_DUPLICATE_IDEMPOTENCY",
            "TC07_QUERY_ORDER_BY_ID",
            "TC08_PAY_ORDER",
            "TC09_SHIP_ORDER",
        ],
        "db_dirty_records": 15,
    },
    {
        "turn": 6,
        "input_tokens": 6480,
        "output_tokens": 700,
        "duration_ms": 7100,
        "tool_calls": [
            {"name": "edit_file", "args": {"path": "sandboxes/app.py"}, "duration_ms": 45},
            {"name": "run_command", "args": {"command": "pytest tests/test_orders.py"}, "duration_ms": 3800},
        ],
        "has_syntax_error": False,
        "has_type_error": False,
        "has_runtime_error": False,
        "passed_assertions": ALL_12_ASSERTIONS,
        "db_dirty_records": 18,
    },
]

GOLDEN_TOKENSAFE_TRACES: List[Dict[str, Any]] = [
    {
        "turn": 1,
        "input_tokens": 980,
        "output_tokens": 440,
        "duration_ms": 2850,
        "tool_calls": [
            {"name": "oumv_observe", "args": {"level": "L1"}, "duration_ms": 12, "output_summary": "Endpoints & Models surface"},
            {"name": "oumv_observe", "args": {"level": "L2", "target": "OrderCreate"}, "duration_ms": 8, "output_summary": "OrderCreate schema"},
            {"name": "oumv_understand", "args": {"query": "causality", "target": "POST /api/orders"}, "duration_ms": 15, "output_summary": "Causality graph"},
            {"name": "oumv_mutate", "args": {"path": "sandboxes/app.py", "symbol": "cancel_order"}, "duration_ms": 22, "output_summary": "OCC edit applied"},
            {"name": "oumv_verify", "args": {"target": "POST /api/orders", "payload": {"idempotency_key": "dryrun_sim_key"}}, "duration_ms": 14, "output_summary": "In-memory simulation with transactional rollback (0 dirty records)"},
            {"name": "oumv_observe", "args": {"level": "L0"}, "duration_ms": 6, "output_summary": "Pulse HEALTHY"},
        ],
        "has_syntax_error": False,
        "has_type_error": False,
        "has_runtime_error": False,
        "passed_assertions": ALL_12_ASSERTIONS,
        "db_dirty_records": 0,
    },
]
