"""
Framework adapters for the TokenSafe UARP runtime.

FastAPI and Starlette share one HTTP/SSE/WebSocket transport
(``_http_binding``); they differ only in route introspection and lifecycle.
"""

from tokensafe_python.adapters.fastapi import mount_tokensafe, TokenSafeFastAPI
from tokensafe_python.adapters.starlette import mount_tokensafe_starlette, TokenSafeStarlette

__all__ = [
    "mount_tokensafe",
    "TokenSafeFastAPI",
    "mount_tokensafe_starlette",
    "TokenSafeStarlette",
]
