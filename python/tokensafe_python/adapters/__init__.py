"""
FastAPI adapter for TokenSafe UARP runtime.
"""

from tokensafe_python.adapters.fastapi import mount_tokensafe, TokenSafeFastAPI

__all__ = ["mount_tokensafe", "TokenSafeFastAPI"]
