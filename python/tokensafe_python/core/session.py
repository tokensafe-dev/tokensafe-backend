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
MAX_TTL_SECONDS = 86400


def clamp_ttl(seconds: int) -> int:
    """Clamps a client-requested lifetime to [1, 86400] (spec §3, wire 1.5.0)."""
    return max(1, min(int(seconds), MAX_TTL_SECONDS))


class SessionRegistry:
    def __init__(self, ttl_seconds: int = DEFAULT_TTL_SECONDS):
        self.ttl = max(int(ttl_seconds), 1)
        self._sessions: Dict[str, float] = {}

    def create(self, ttl_seconds: Optional[int] = None) -> str:
        """Issues a session; a requested lifetime is clamped, not trusted."""
        ttl = self.ttl if ttl_seconds is None else clamp_ttl(ttl_seconds)
        session_id = "sess-" + secrets.token_hex(8)
        self._sessions[session_id] = time.time() + ttl
        return session_id

    def granted_ttl(self, requested: Optional[int]) -> int:
        """The lifetime the runtime answers for a request (spec §3, wire 1.5.0)."""
        return self.ttl if requested is None else clamp_ttl(requested)

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


def _read_registry(path: str) -> dict:
    import json

    try:
        with open(path, "r", encoding="utf-8") as handle:
            data = json.load(handle)
        return data if isinstance(data, dict) else {}
    except (OSError, ValueError):
        # A missing or corrupt registry must not block starting a runtime.
        return {}


def _write_registry(path: str, registry: dict) -> None:
    import json
    import stat

    with open(path, "w", encoding="utf-8") as handle:
        json.dump(registry, handle, indent=2)
        handle.write("\n")
    os.chmod(path, stat.S_IRUSR | stat.S_IWUSR)


def write_descriptor(
    project_root: str,
    url: str,
    name: str,
    version: str,
    session_token: str = "",
    observe_levels: Optional[list] = None,
    transports: Optional[list] = None,
) -> str:
    """Registers this runtime in the workspace discovery registry (mode 0600).

    The file is keyed by profile (spec §9.2): a React frontend served by `tokensafe dev`
    and this backend share one file, so a backend write preserves the frontend entry.
    """
    path = descriptor_path(project_root)
    os.makedirs(os.path.dirname(path), exist_ok=True)
    registry = _read_registry(path)
    registry["backend"] = {
        "type": "backend",
        "name": name,
        "version": version,
        "url": url,
        "sessionToken": session_token,
        "workspace": os.path.realpath(project_root),
        "capabilities": {
            "observeLevels": observe_levels or ["L0", "L1", "L2", "L3", "L4"],
            "transports": transports or ["http"],
        },
        "startedAt": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
    }
    registry["workspaceRoot"] = os.path.realpath(project_root)
    registry["pid"] = os.getpid()
    registry["updatedAt"] = int(time.time() * 1000)
    _write_registry(path, registry)
    return path


def delete_descriptor(project_root: str) -> None:
    """Removes only this runtime's entry; other profiles keep the file alive."""
    path = descriptor_path(project_root)
    registry = _read_registry(path)
    if not registry:
        return
    registry.pop("backend", None)
    if "frontend" in registry:
        _write_registry(path, registry)
    else:
        try:
            os.unlink(path)
        except OSError:
            pass
