"""
JSON-RPC 2.0 router and dispatcher for UARP protocol methods.
"""

import inspect
import json
import traceback
from typing import Any, Callable, Dict, Optional, Union
from tokensafe_python.protocol.errors import OumvError
from tokensafe_python.protocol.types import (
    JSONRPCRequest,
    JSONRPCResponse,
    JSONRPCError,
)


class JSONRPCHandler:
    """Dispatches JSON-RPC 2.0 calls to registered method handlers."""

    def __init__(self):
        self._handlers: Dict[str, Callable] = {}

    def register(self, method: str, handler: Callable):
        """Registers a handler for a given JSON-RPC method."""
        self._handlers[method] = handler

    async def handle_payload(self, raw_body: Union[str, bytes, dict, list]) -> Union[Dict[str, Any], list]:
        """Parses and executes a JSON-RPC request (single or batch)."""
        if isinstance(raw_body, (str, bytes)):
            try:
                data = json.loads(raw_body)
            except Exception as e:
                return JSONRPCResponse(
                    id=None,
                    error=JSONRPCError(code=-32700, message=f"Parse error: {str(e)}")
                ).model_dump()
        else:
            data = raw_body

        if isinstance(data, list):
            # Batch request
            results = []
            for item in data:
                resp = await self._handle_single(item)
                results.append(resp.model_dump())
            return results
        elif isinstance(data, dict):
            resp = await self._handle_single(data)
            return resp.model_dump()
        else:
            return JSONRPCResponse(
                id=None,
                error=JSONRPCError(code=-32600, message="Invalid Request: must be an object or array")
            ).model_dump()

    async def _handle_single(self, data: dict) -> JSONRPCResponse:
        try:
            req = JSONRPCRequest.model_validate(data)
        except Exception as e:
            return JSONRPCResponse(
                id=data.get("id") if isinstance(data, dict) else None,
                error=JSONRPCError(code=-32600, message=f"Invalid Request: {str(e)}")
            )

        handler = self._handlers.get(req.method)
        if not handler:
            return JSONRPCResponse(
                id=req.id,
                error=JSONRPCError(code=-32601, message=f"Method not found: '{req.method}'")
            )

        try:
            params = req.params or {}
            if inspect.iscoroutinefunction(handler):
                result = await handler(**params)
            else:
                result = handler(**params)

            # Convert result if it's a Pydantic model
            if hasattr(result, "model_dump"):
                result = result.model_dump()

            return JSONRPCResponse(id=req.id, result=result)
        except TypeError as te:
            return JSONRPCResponse(
                id=req.id,
                error=JSONRPCError(code=-32602, message=f"Invalid params: {str(te)}")
            )
        except OumvError as oe:
            return JSONRPCResponse(
                id=req.id,
                error=JSONRPCError(code=oe.code, message=oe.message, data=oe.data)
            )
        except Exception as ex:
            return JSONRPCResponse(
                id=req.id,
                error=JSONRPCError(
                    code=-32603,
                    message=f"Internal error: {str(ex)}",
                    data={"trace": traceback.format_exc()}
                )
            )
