"""
State manager and delta diff engine.
"""

import copy
import json
from datetime import datetime, timezone
from typing import Any, Dict, Optional
from tokensafe_python.protocol.types import StateDiffResult


class StateManager:
    """Manages application state snapshots and computes deterministic diffs."""

    def __init__(self):
        self._tracked_state: Dict[str, Any] = {}

    def track(self, key: str, value: Any):
        """Stores or updates a tracked state object."""
        self._tracked_state[key] = copy.deepcopy(value)

    def get_snapshot(self) -> Dict[str, Any]:
        """Returns an immutable deep-copy snapshot of the current state."""
        return copy.deepcopy(self._tracked_state)

    @classmethod
    def compute_diff(cls, before: Optional[Dict[str, Any]], after: Optional[Dict[str, Any]]) -> StateDiffResult:
        """Calculates granular additions, modifications, and deletions between two state snapshots."""
        b = before or {}
        a = after or {}

        added = {}
        modified = {}
        removed = {}

        # Detect additions and modifications
        for k, v in a.items():
            if k not in b:
                added[k] = v
            elif b[k] != v:
                modified[k] = {"before": b[k], "after": v}

        # Detect removals
        for k, v in b.items():
            if k not in a:
                removed[k] = v

        has_changes = bool(added or modified or removed)
        return StateDiffResult(
            added=added,
            modified=modified,
            removed=removed,
            has_changes=has_changes,
        )

    def to_timestamp(self) -> str:
        """Returns ISO 8601 UTC timestamp."""
        return datetime.now(timezone.utc).isoformat()
