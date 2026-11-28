"""§14.2 workspace policy: `permissions.query: false` turns oumv/query into -32007."""

import pytest
from sqlalchemy import create_engine

from tokensafe_python.core.runtime import OUMVRuntime
from tokensafe_python.protocol.errors import OumvError


def _engine():
    return create_engine("sqlite://")


async def test_query_allowed_without_policy():
    runtime = OUMVRuntime(db_engine=_engine())

    result = await runtime.handle_oumv_query(sql="SELECT 1")

    assert result["columns"] == ["1"]
    assert result["rows"] == [[1]]
    assert result["rowCount"] == 1
    assert result["truncated"] is False


async def test_query_disabled_by_policy_answers_permission_denied():
    runtime = OUMVRuntime(db_engine=_engine(), permissions={"query": False})

    with pytest.raises(OumvError) as excinfo:
        await runtime.handle_oumv_query(sql="SELECT 1")

    assert excinfo.value.code == -32007
    assert excinfo.value.data["reason"] == "query_disabled"


async def test_policy_wins_over_the_missing_engine_gate():
    """The policy check runs before the engine check: -32007, never -32006."""
    runtime = OUMVRuntime(permissions={"query": False})

    with pytest.raises(OumvError) as excinfo:
        await runtime.handle_oumv_query(sql="SELECT 1")

    assert excinfo.value.code == -32007
    assert excinfo.value.data["reason"] == "query_disabled"


async def test_query_still_requires_an_engine_without_a_policy():
    runtime = OUMVRuntime()

    with pytest.raises(OumvError) as excinfo:
        await runtime.handle_oumv_query(sql="SELECT 1")

    assert excinfo.value.code == -32006
    assert excinfo.value.data["reason"] == "capability_not_advertised"


async def test_explicit_query_true_is_allowed():
    runtime = OUMVRuntime(db_engine=_engine(), permissions={"query": True})

    result = await runtime.handle_oumv_query(sql="SELECT 1")

    assert result["rowCount"] == 1


async def test_write_statement_still_rejected_with_reads_allowed():
    runtime = OUMVRuntime(db_engine=_engine())

    with pytest.raises(OumvError) as excinfo:
        await runtime.handle_oumv_query(sql="INSERT INTO t VALUES (1)")

    assert excinfo.value.code == -32003
    assert excinfo.value.data["reason"] == "query_forbidden"


def test_adapter_forwards_the_permissions_policy():
    from tokensafe_python.adapters.fastapi import TokenSafeFastAPI
    from fastapi import FastAPI

    adapter = TokenSafeFastAPI(FastAPI(), permissions={"query": False})

    assert adapter.runtime.permissions == {"query": False}


async def test_mutate_policy_is_method_absence_not_a_refusal():
    """§8.4: `permissions.mutate: false` forces the read-only shape.

    A per-handler `-32007` would leave the write path reachable; the spec says the
    mutating methods are not registered at all (§8.5), so the router answers -32601.
    """
    runtime = OUMVRuntime(permissions={"mutate": False})

    assert runtime.read_only is True
    handshake = await runtime.handle_handshake(protocolVersion="1.6.0")
    assert handshake["capabilities"]["readOnly"] is True
    assert "mutate" not in handshake["capabilities"]
    assert "verify" not in handshake["capabilities"]


def test_mutate_true_leaves_the_default_deployment_alone():
    runtime = OUMVRuntime(permissions={"mutate": True})

    assert runtime.read_only is False
