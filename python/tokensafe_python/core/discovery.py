"""
Auto-discovery file registration for TokenSafe MCP Gateway.
"""

import json
import os
from datetime import datetime, timezone
from typing import Optional


class DiscoveryManager:
    """Writes runtime discovery beacons (.tokensafe/runtime.json) for zero-config discovery."""

    def __init__(self, project_root: Optional[str] = None):
        self._project_root = project_root or os.getcwd()
        self._registered_files = []

    def register_runtime(
        self,
        framework: str = "fastapi",
        host: str = "127.0.0.1",
        port: int = 8000,
        endpoint_path: str = "/__oumv/rpc",
        metadata: Optional[dict] = None,
        session_token: Optional[str] = None,
    ):
        """Creates .tokensafe/runtime.json and .oumv/runtime.json descriptors."""
        payload = {
            "type": "backend",
            "framework": framework,
            "host": host,
            "port": port,
            "url": f"http://{host}:{port}{endpoint_path}",
            "rpc_endpoint": f"http://{host}:{port}{endpoint_path}",
            "protocol_version": "1.0.0",
            "session_token": session_token,
            "pid": os.getpid(),
            "started_at": datetime.now(timezone.utc).isoformat(),
            "status": "running",
            "metadata": metadata or {},
        }

        # Write to both .tokensafe/ and .oumv/ directories
        for dirname in (".tokensafe", ".oumv"):
            dirpath = os.path.join(self._project_root, dirname)
            try:
                os.makedirs(dirpath, exist_ok=True)
                filepath = os.path.join(dirpath, "runtime.json")
                with open(filepath, "w", encoding="utf-8") as f:
                    json.dump(payload, f, indent=2)
                try:
                    os.chmod(filepath, 0o600)
                except Exception:
                    pass
                self._registered_files.append(filepath)
            except Exception:
                pass

    def unregister_runtime(self):
        """Removes written discovery descriptors on server shutdown."""
        for filepath in self._registered_files:
            try:
                if os.path.exists(filepath):
                    os.remove(filepath)
            except Exception:
                pass
        self._registered_files = []
