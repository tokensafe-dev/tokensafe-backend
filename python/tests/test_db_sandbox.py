"""
Tests for DBSandbox transactional rollback and query recording.
"""

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
