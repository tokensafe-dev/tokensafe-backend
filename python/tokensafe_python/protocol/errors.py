"""OUMV typed protocol errors mapped to JSON-RPC error codes (spec §5)."""

from typing import Optional

INVALID_PARAMS = -32602
CONFLICT_DETECTED = -32001
SYNTAX_ERROR = -32002
SECURITY_VIOLATION = -32003
RESOURCE_NOT_FOUND = -32009
UNSUPPORTED_CAPABILITY = -32006
VALIDATION_FAILED = -32011


class OumvError(Exception):
    """Typed OUMV protocol error; the JSON-RPC layer maps it to its code."""

    def __init__(self, code: int, message: str, data: Optional[dict] = None):
        super().__init__(message)
        self.code = code
        self.message = message
        self.data = data
