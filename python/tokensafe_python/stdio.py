"""stdio binding for the OUMV runtime (spec §2.5).

Newline-delimited JSON-RPC: one object per line on stdin, one response per line on
stdout, diagnostics on stderr. Notifications get no reply. This is the transport a
CLI or MCP bridge uses when no HTTP server is running.

    python -m tokensafe_python.stdio --workspace /path/to/app
    python -m tokensafe_python.stdio --app myapp.main:app

Without ``--app`` the runtime serves the file-based pillar (observe L3, getSource,
applyEdit). With a FastAPI app it also answers L1/L2, verify and L4 exactly like the
HTTP adapter, because the app is mounted in-process.
"""

from __future__ import annotations

import argparse
import asyncio
import importlib
import json
import sys
from typing import Any, Awaitable, Callable, Optional, TextIO

from tokensafe_python.adapters.fastapi import mount_tokensafe
from tokensafe_python.core.runtime import OUMVRuntime


def _load_app(spec: str) -> Any:
    module_name, _, attribute = spec.partition(":")
    module = importlib.import_module(module_name)
    return getattr(module, attribute or "app")


def _parse_error(message: str) -> dict:
    return {"jsonrpc": "2.0", "id": None, "error": {"code": -32700, "message": f"Parse error: {message}"}}


async def serve(
    runtime: OUMVRuntime,
    reader: Optional[TextIO] = None,
    writer: Optional[TextIO] = None,
    readline: Optional[Callable[[], Awaitable[str]]] = None,
) -> None:
    reader = reader or sys.stdin
    writer = writer or sys.stdout
    loop = asyncio.get_running_loop()
    read = readline or (lambda: loop.run_in_executor(None, reader.readline))

    def emit(payload: Any) -> None:
        if payload is not None:
            writer.write(json.dumps(payload) + "\n")
            writer.flush()

    while True:
        line = await read()
        if not line:
            break
        stripped = line.strip()
        if not stripped:
            continue
        try:
            payload = json.loads(stripped)
        except json.JSONDecodeError as exc:
            emit(_parse_error(str(exc)))
            continue

        if isinstance(payload, list):
            responses = []
            for item in payload:
                response = await runtime.rpc.handle_payload(item)
                if isinstance(item, dict) and "id" in item:
                    responses.append(response)
            if responses:
                emit(responses)
            continue

        is_notification = isinstance(payload, dict) and "id" not in payload
        response = await runtime.rpc.handle_payload(payload)
        if not is_notification:
            emit(response)


def main(argv: Optional[list[str]] = None) -> int:
    parser = argparse.ArgumentParser(prog="tokensafe-oumv-stdio")
    parser.add_argument("--app", help="import path of a FastAPI app, e.g. 'myapp.main:app'")
    parser.add_argument("--workspace", default=None, help="workspace root for the path jail")
    parser.add_argument(
        "--read-only",
        action="store_true",
        help="spec §8.5: do not register the writing methods (applyEdit/previewEdit/verify/build)",
    )
    args = parser.parse_args(argv)

    if args.app:
        runtime = mount_tokensafe(
            _load_app(args.app), project_root=args.workspace, read_only=args.read_only
        ).runtime
    else:
        runtime = OUMVRuntime(project_root=args.workspace, read_only=args.read_only)

    try:
        asyncio.run(serve(runtime))
    except KeyboardInterrupt:
        return 0
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
