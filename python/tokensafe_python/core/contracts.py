"""
Level 3 semantic operation contracts (OUMV spec §10.4).

Declares domain operations (business entities, invariants, allowed transitions)
so agents understand not only what exists, but what the application MEANS.
Contracts are opt-in and incremental: undeclared handlers keep working unchanged.

    from tokensafe_python.contracts import oumv_operation

    @oumv_operation(
        "orders.cancel",
        description="Cancel an order",
        input_schema={"order_id": "int"},
        constraints=["Order must not be shipped", "Order must belong to the current customer"],
        verification=["Order status must become cancelled"],
    )
    def cancel_order(order_id: int):
        ...
"""

from typing import Any, Callable, Dict, List, Optional

# Global registry: operation id -> contract (wired into runtimes by mount_tokensafe)
OPERATIONS: Dict[str, Dict[str, Any]] = {}


def oumv_operation(
    operation_id: str,
    description: str = "",
    input_schema: Optional[Dict[str, Any]] = None,
    constraints: Optional[List[str]] = None,
    verification: Optional[List[str]] = None,
) -> Callable:
    """Decorator attaching a semantic operation contract to a handler (spec §7.4)."""

    def decorator(fn: Callable) -> Callable:
        OPERATIONS[operation_id] = {
            "id": operation_id,
            "description": description,
            "inputSchema": input_schema,
            "constraints": constraints or [],
            "verification": verification or [],
            "handler": getattr(fn, "__name__", str(fn)),
            "evidence": {"provenance": "contract", "sourceAdapter": "tokensafe-python-contracts", "confidence": "confirmed"},
        }
        setattr(fn, "__oumv_operation__", operation_id)
        return fn

    return decorator


def get_operations() -> Dict[str, Dict[str, Any]]:
    """Returns all declared operation contracts (contract provenance)."""
    return dict(OPERATIONS)


def reset_operations() -> None:
    """Clears the registry (test isolation)."""
    OPERATIONS.clear()
