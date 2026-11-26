"""stdio binding certification (spec §2.5)."""

import asyncio
import io
import json
import os
import sys
from pathlib import Path

import pytest

from tokensafe_python import conformance
from tokensafe_python.core.runtime import OUMVRuntime
from tokensafe_python.stdio import serve

FIXTURES = Path(__file__).parent / "conformance-cases.json"
PYTHON_ROOT = Path(__file__).resolve().parents[1]


@pytest.mark.asyncio
async def test_serve_answers_requests_and_skips_notifications(tmp_path):
    runtime = OUMVRuntime(project_root=str(tmp_path))
    lines = iter(
        [
            json.dumps({"jsonrpc": "2.0", "id": 1, "method": "oumv/handshake", "params": {}}) + "\n",
            json.dumps({"jsonrpc": "2.0", "method": "oumv/observe", "params": {"level": "L0"}}) + "\n",
            json.dumps({"jsonrpc": "2.0", "id": 2, "method": "oumv/observe", "params": {"level": "L0"}}) + "\n",
            "{not json}\n",
            "",
        ]
    )

    async def readline() -> str:
        return next(lines, "")

    output = io.StringIO()
    await serve(runtime, writer=output, readline=readline)
    responses = [json.loads(line) for line in output.getvalue().splitlines()]

    assert len(responses) == 3
    assert responses[0]["result"]["profile"] == "backend"
    assert responses[1]["result"]["level"] == "L0"
    assert responses[2]["error"]["code"] == -32700


@pytest.mark.asyncio
async def test_stdio_conformance_certification(tmp_path):
    (tmp_path / "conformance_fixture.py").write_text("value = 0\n", encoding="utf-8")
    (tmp_path / "conformance_edit_target.py").write_bytes(b"value = 41\n")
    (tmp_path / "conformance_symbol_target.py").write_text("def value():\n    return 41\n", encoding="utf-8")
    (tmp_path / "conformance_delete_target.py").write_text("value = 1\n", encoding="utf-8")
    (tmp_path / "conformance_rename_source.py").write_text("value = 2\n", encoding="utf-8")
    (tmp_path / "conformance_app.py").write_text(
        "\n".join(
            [
                "from fastapi import FastAPI",
                "",
                "app = FastAPI()",
                "",
                '@app.get("/api/v1/orders")',
                "def list_orders():",
                '    return {"orders": []}',
                "",
            ]
        ),
        encoding="utf-8",
    )

    env = {
        **os.environ,
        "PYTHONPATH": os.pathsep.join([str(PYTHON_ROOT), str(tmp_path)]),
    }
    proc = await asyncio.create_subprocess_exec(
        sys.executable,
        "-m",
        "tokensafe_python.stdio",
        "--app",
        "conformance_app:app",
        "--workspace",
        str(tmp_path),
        cwd=str(tmp_path),
        env=env,
        stdin=asyncio.subprocess.PIPE,
        stdout=asyncio.subprocess.PIPE,
        stderr=asyncio.subprocess.PIPE,
    )

    async def post(method, params):
        assert proc.stdin is not None
        assert proc.stdout is not None
        request = {"jsonrpc": "2.0", "id": method, "method": method, "params": params}
        proc.stdin.write((json.dumps(request) + "\n").encode())
        await proc.stdin.drain()
        line = await asyncio.wait_for(proc.stdout.readline(), timeout=30)
        assert line, f"stdio runtime produced no response: {(await proc.stderr.read()).decode()[:400]}"
        return json.loads(line)

    markers = conformance.Markers(
        fixture_file="conformance_fixture.py",
        missing_file="conformance_missing_file.py",
        edit_target="conformance_edit_target.py",
        new_file="conformance_created_file.py",
        edit_version=conformance.version_of(b"value = 41\n"),
        l2_target="GET /api/v1/orders",
        l3_target="conformance_edit_target.py",
        verify_target="GET /api/v1/orders",
        symbol_file="conformance_symbol_target.py",
        symbol_name="value",
        symbol_replacement="def value():\n    return 42\n",
        symbol_version=conformance.version_of(b"def value():\n    return 41\n"),
        symbol_file_content="def value():\n    return 42\n",
        delete_file="conformance_delete_target.py",
        delete_version=conformance.version_of(b"value = 1\n"),
        rename_source="conformance_rename_source.py",
        rename_target="conformance_renamed.py",
        rename_version=conformance.version_of(b"value = 2\n"),
    )

    try:
        report = await conformance.run_conformance(
            post, FIXTURES, markers, adapter_name="python-stdio", framework="python"
        )
    finally:
        proc.terminate()
        await proc.wait()

    report["environment"] = {"transport": "stdio", "python": sys.version.split()[0]}
    out_dir = os.environ.get("CERT_OUT_DIR")
    if out_dir:
        conformance.save_report(report, Path(out_dir) / "transports" / "python-stdio.json")

    failures = [case for case in report["cases"] if case["status"] != "passed"]
    assert not failures, failures
    assert report["attainedLevel"] == 3
