"""
TokenSafe Python - Universal Agent-Runtime Protocol (UARP / OUMV)
Deterministic runtime causality, state inspection, and safe mutation sandbox for Python.
"""

from tokensafe_python.core.runtime import OUMVRuntime
from tokensafe_python.adapters.fastapi import mount_tokensafe, TokenSafeFastAPI

__version__ = "0.1.0"
__all__ = ["OUMVRuntime", "mount_tokensafe", "TokenSafeFastAPI"]
