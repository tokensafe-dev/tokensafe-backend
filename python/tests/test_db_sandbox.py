"""
Tests for DBSandbox transactional rollback and query recording.
"""

import pytest
from sqlalchemy import create_engine, text

from tokensafe_python.core.db_sandbox import DBSandbox
from tokensafe_python.core.runtime import OUMVRuntime
from tokensafe_python.protocol.errors import OumvError


def test_db_sandbox_recording():
    sandbox = DBSandbox()
    assert sandbox._is_recording is False

    sandbox.start_recording()
    sandbox._query_log.append({"sql": "SELECT 1", "parameters": {}})
    assert sandbox._is_recording is True

    queries = sandbox.stop_recording()
    assert len(queries) == 1
    assert queries[0]["sql"] == "SELECT 1"
    assert sandbox._is_recording is False


def test_db_sandbox_sync_context_mock():
    sandbox = DBSandbox()

    class MockSession:
        def __init__(self):
            self.rolled_back = False

        def rollback(self):
            self.rolled_back = True

    session = MockSession()

    with sandbox.simulate_sync(session=session):
        sandbox._query_log.append({"sql": "INSERT INTO users VALUES ('alice')", "parameters": {}})

    assert session.rolled_back is True


@pytest.mark.parametrize("commit", [False, True])
def test_db_sandbox_rollback_evidence(commit):
    """rollback_observed() must reflect actual ROLLBACK events on the engine."""
    engine = create_engine("sqlite:///:memory:")
    sandbox = DBSandbox(engine=engine)
    assert sandbox.has_engine() is True

    sandbox.start_recording()
    with engine.begin() as conn:
        conn.execute(text("CREATE TABLE t (id INTEGER)"))
    with engine.connect() as conn:
        trans = conn.begin()
        conn.execute(text("INSERT INTO t VALUES (1)"))
        if commit:
            trans.commit()
        else:
            trans.rollback()
    queries = sandbox.stop_recording()

    assert len(queries) >= 1
    if commit:
        assert sandbox.rollback_observed() is False
    else:
        assert sandbox.rollback_observed() is True


def test_db_sandbox_rollback_window_resets_between_recordings():
    engine = create_engine("sqlite:///:memory:")
    sandbox = DBSandbox(engine=engine)

    # First window: a real rollback happens
    sandbox.start_recording()
    with engine.connect() as conn:
        trans = conn.begin()
        trans.rollback()
    sandbox.stop_recording()
    assert sandbox.rollback_observed() is True

    # Second window: no rollback happens — counter must reset
    sandbox.start_recording()
    sandbox.stop_recording()
    assert sandbox.rollback_observed() is False


def test_db_sandbox_without_engine_reports_not_observed():
    sandbox = DBSandbox()
    assert sandbox.has_engine() is False
    sandbox.start_recording()
    sandbox.stop_recording()
    assert sandbox.rollback_observed() is False


def _engine_with_row():
    engine = create_engine("sqlite://")
    with engine.begin() as conn:
        conn.execute(text("CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)"))
        conn.execute(text("INSERT INTO items (id, name) VALUES (1, 'a')"))
    return engine


def _writing_executor(engine, fail_from_call=None):
    calls = []

    async def execute(path, method, body=None, headers=None, query_params=None, dry_run=True):
        calls.append(path)
        assert dry_run is True, "the sandbox must dispatch with the dry-run header"
        with engine.connect() as conn:
            tx = conn.begin()
            conn.execute(text("UPDATE items SET name = 'b' WHERE id = 1"))
            tx.rollback()
        if fail_from_call is not None and len(calls) >= fail_from_call:
            return {"error": "boom"}, 500
        return {"ok": True}, 200

    return execute, calls


async def test_verify_sandbox_reports_verified_rollback():
    engine = _engine_with_row()
    runtime = OUMVRuntime(db_engine=engine)
    executor, _ = _writing_executor(engine)
    runtime.set_action_executor(executor)

    result = await runtime.handle_oumv_verify(target="POST /api/items", sandbox={"mode": "rollback"})

    assert result["status"] == "passed"
    assert result["rolledBack"] is True
    assert result["rollbackStatus"] == "verified"
    assert result["dbDiff"]["rollback"] is True
    assert result["dbDiff"]["queries"] == 1
    assert result["dbDiff"]["driver"] == "sqlite"
    assert "persistent database state" not in result["scope"]["notExecuted"]

    with engine.connect() as conn:
        assert conn.execute(text("SELECT name FROM items WHERE id = 1")).scalar_one() == "a"


async def test_verify_sandbox_without_engine_is_not_applicable():
    runtime = OUMVRuntime()

    async def execute(path, method, body=None, headers=None, query_params=None, dry_run=True):
        return {"ok": True}, 200

    runtime.set_action_executor(execute)
    result = await runtime.handle_oumv_verify(target="POST /api/items", sandbox={"mode": "rollback"})

    assert result["rolledBack"] is False
    assert result["rollbackStatus"] == "not_applicable"
    assert "dbDiff" not in result
    assert "persistent database state" in result["scope"]["notExecuted"]


async def test_verify_sandbox_multistep_shares_one_window_and_reports_failure():
    engine = _engine_with_row()
    runtime = OUMVRuntime(db_engine=engine)
    executor, calls = _writing_executor(engine, fail_from_call=2)
    runtime.set_action_executor(executor)

    result = await runtime.handle_oumv_verify(
        steps=[{"target": "POST /a"}, {"target": "POST /b"}], sandbox={"mode": "rollback"}
    )

    assert result["status"] == "failed"
    assert [step["status"] for step in result["steps"]] == ["passed", "failed"]
    assert result["dbDiff"]["queries"] == 2
    assert calls == ["/a", "/b"]


async def test_verify_sandbox_rejects_unknown_mode():
    runtime = OUMVRuntime()
    with pytest.raises(OumvError) as excinfo:
        await runtime.handle_oumv_verify(target="GET /", sandbox={"mode": "commit"})
    assert excinfo.value.code == -32006

