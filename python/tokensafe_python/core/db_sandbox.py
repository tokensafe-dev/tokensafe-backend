"""
Database transactional sandbox with automatic dry-run rollback and SQL query inspection.
"""

import time
from typing import Any, Dict, List, Optional
from contextlib import asynccontextmanager, contextmanager


class DBSandbox:
    """Manages transactional sandboxes and records executed SQL queries."""

    def __init__(self, engine: Optional[Any] = None):
        self._engine = engine
        self._query_log: List[Dict[str, Any]] = []
        self._is_recording = False
        self._rollback_count = 0
        self._setup_listeners()

    def has_engine(self) -> bool:
        """Returns True when a SQLAlchemy engine is wired into the sandbox."""
        return self._engine is not None

    def _setup_listeners(self):
        """Attaches query execution and rollback listeners to SQLAlchemy engine if available."""
        if not self._engine:
            return

        try:
            from sqlalchemy import event

            # If AsyncEngine, listen on the sync engine
            target = getattr(self._engine, "sync_engine", self._engine)

            @event.listens_for(target, "before_cursor_execute")
            def before_cursor_execute(conn, cursor, statement, parameters, context, executemany):
                if self._is_recording:
                    self._query_log.append({
                        "sql": statement,
                        "parameters": parameters,
                        "timestamp": time.time(),
                        "executemany": executemany,
                    })

            @event.listens_for(target, "rollback")
            def on_rollback(conn):
                # Real ROLLBACK observed on a connection of this engine
                if self._is_recording:
                    self._rollback_count += 1
        except Exception:
            pass

    def start_recording(self):
        self._query_log = []
        self._is_recording = True
        self._rollback_count = 0

    def stop_recording(self) -> List[Dict[str, Any]]:
        self._is_recording = False
        log = list(self._query_log)
        self._query_log = []
        return log

    def rollback_observed(self) -> bool:
        """True only when an actual ROLLBACK fired on the engine during the recording window.

        Evidence-based rollback reporting: a dry-run without an observed engine
        rollback MUST NOT be reported as rolled back (OUMV spec §6.2).
        """
        return self._rollback_count > 0

    @asynccontextmanager
    async def simulate_async(self, session: Optional[Any] = None):
        """Async context manager that forces transaction rollback on completion."""
        self.start_recording()
        # If an explicit SQLAlchemy async session or connection is provided
        nested = None
        if session and hasattr(session, "begin_nested"):
            try:
                nested = await session.begin_nested()
            except Exception:
                nested = None

        try:
            yield self
        finally:
            if nested and hasattr(nested, "rollback"):
                try:
                    await nested.rollback()
                except Exception:
                    pass
            elif session and hasattr(session, "rollback"):
                try:
                    await session.rollback()
                except Exception:
                    pass
            self.stop_recording()

    @contextmanager
    def simulate_sync(self, session: Optional[Any] = None):
        """Sync context manager that forces transaction rollback on completion."""
        self.start_recording()
        nested = None
        if session and hasattr(session, "begin_nested"):
            try:
                nested = session.begin_nested()
            except Exception:
                nested = None

        try:
            yield self
        finally:
            if nested and hasattr(nested, "rollback"):
                try:
                    nested.rollback()
                except Exception:
                    pass
            elif session and hasattr(session, "rollback"):
                try:
                    session.rollback()
                except Exception:
                    pass
            self.stop_recording()
