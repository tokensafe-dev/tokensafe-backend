"""
Database setup for TokenSafe FastAPI Sandbox.
Shares exact schema with Vanilla to ensure 100% structural parity.
"""

from benchmark.sandboxes.vanilla_fastapi.database import (
    Base,
    ProductDB,
    OrderDB,
    OrderItemDB,
    create_sqlite_engine,
    seed_database,
    count_dirty_records,
)

__all__ = [
    "Base",
    "ProductDB",
    "OrderDB",
    "OrderItemDB",
    "create_sqlite_engine",
    "seed_database",
    "count_dirty_records",
]
