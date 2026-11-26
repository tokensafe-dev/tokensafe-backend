"""Handshake sessions (spec §3).

A session is advisory: callers may omit `sessionId` (stateless mode), but a supplied
id must be live. Expiry is enforced on read, so no background sweeper is needed.
"""

from __future__ import annotations

import hashlib
import os
import secrets
import time
from typing import Dict, Optional

DEFAULT_TTL_SECONDS = 3600


class SessionRegistry:
    def __init__(self, ttl_seconds: int = DEFAULT_TTL_SECONDS):
        self.ttl = max(int(ttl_seconds), 1)
        self._sessions: Dict[str, float] = {}

    def create(self) -> str:
        session_id = "sess-" + secrets.token_hex(8)
        self._sessions[session_id] = time.time() + self.ttl
        return session_id

    def validate(self, session_id: Optional[str]) -> bool:
        if not session_id:
            return True  # stateless callers are allowed
        expires_at = self._sessions.get(session_id)
        if expires_at is None:
            return False
        if time.time() > expires_at:
            self._sessions.pop(session_id, None)
            return False
        return True

    def delete(self, session_id: Optional[str]) -> bool:
        if not session_id:
            return False
        return self._sessions.pop(session_id, None) is not None

    def count(self) -> int:
        return len(self._sessions)


def descriptor_path(project_root: str, home: Optional[str] = None) -> str:
    """~/.tokensafe/runtimes/<sha256(realpath(project_root))[:16]>.json (spec §9.2)."""
    root = os.environ.get("TOKENSAFE_HOME") or home or os.path.join(os.path.expanduser("~"), ".tokensafe")
    digest = hashlib.sha256(os.path.realpath(project_root).encode("utf-8")).hexdigest()[:16]
    return os.path.join(root, "runtimes", f"{digest}.json")


def write_descriptor(
    project_root: str,
    url: str,
    name: str,
    version: str,
    session_token: str = "",
    observe_levels: Optional[list] = None,
) -> str:
    """Writes the discovery descriptor outside the repository, mode 0600."""
    import json
    import stat

    path = descriptor_path(project_root)
    os.makedirs(os.path.dirname(path), exist_ok=True)
    payload = {
        "type": "backend",
        "name": name,
        "version": version,
        "url": url,
        "sessionToken": session_token,
        "workspace": os.path.abspath(project_root),
        "capabilities": {
            "observeLevels": observe_levels or ["L0", "L1", "L2", "L3", "L4"],
            "transports": ["http"],
        },
        "startedAt": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
    }
    with open(path, "w", encoding="utf-8") as handle:
        json.dump(payload, handle, indent=2)
    os.chmod(path, stat.S_IRUSR | stat.S_IWUSR)
    return path


def delete_descriptor(project_root: str) -> None:
    try:
        os.unlink(descriptor_path(project_root))
    except OSError:
        pass
