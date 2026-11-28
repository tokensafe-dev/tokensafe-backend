"""
TokenSafe Python - Universal Agent-Runtime Protocol (UARP / OUMV)
Deterministic runtime causality, state inspection, and safe mutation sandbox for Python.
"""

from tokensafe_python.core.runtime import OUMVRuntime
from tokensafe_python.core.contracts import oumv_operation
from tokensafe_python.adapters.fastapi import mount_tokensafe, TokenSafeFastAPI
from tokensafe_python.adapters.starlette import mount_tokensafe_starlette, TokenSafeStarlette

__version__ = "0.2.0"
__all__ = [
    "OUMVRuntime",
    "mount_tokensafe",
    "TokenSafeFastAPI",
    "mount_tokensafe_starlette",
    "TokenSafeStarlette",
    "oumv_operation",
]
