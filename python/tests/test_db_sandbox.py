"""
Tests for DBSandbox transactional rollback and query recording.
"""

import pytest
from sqlalchemy import create_engine, text

from tokensafe_python.core.db_sandbox import DBSandbox


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

