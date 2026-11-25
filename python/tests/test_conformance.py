"""
Language-neutral OUMV Core conformance runner (spec §10).

Loads the vendored case file (canonical source: agent-oumv/fixtures/conformance-cases.json)
and executes every case through the JSON-RPC endpoint of a mounted FastAPI runtime:
handshake shape, error-code model (OCC conflict, path traversal, syntax guard,
missing resource), and real OCC version tokens on applied edits.
"""

import hashlib
import json
from pathlib import Path

import httpx
import pytest
from fastapi import FastAPI

from tokensafe_python.adapters.fastapi import mount_tokensafe

FIXTURES = Path(__file__).parent / "conformance-cases.json"
EDIT_CONTENT = "value = 42\n"
_CASES = json.loads(FIXTURES.read_text(encoding="utf-8"))["cases"]
_CASE_IDS = [c["id"] for c in _CASES]


def _substitute(value, fixture_file, missing_file, edit_target):
    if isinstance(value, str):
        return (
            value.replace("${FIXTURE_FILE}", fixture_file)
            .replace("${MISSING_FILE}", missing_file)
            .replace("${EDIT_TARGET_FILE}", edit_target)
            .replace("${EDIT_CONTENT}", EDIT_CONTENT)
        )
    return value


def _path_exists(data, path):
    current = data
    for part in path.split("."):
        if not isinstance(current, dict) or part not in current:
            return False
        current = current[part]
    return True


@pytest.fixture
def conformance_env(tmp_path):
    (tmp_path / "conformance_fixture.py").write_text("value = 0\n", encoding="utf-8")
    (tmp_path / "conformance_edit_target.py").write_text("value = 41\n", encoding="utf-8")

    app = FastAPI()
    mount_tokensafe(app, auto_discover=False, project_root=str(tmp_path))
    return app, _CASES


@pytest.mark.asyncio
@pytest.mark.parametrize("case_index", range(len(_CASES)), ids=_CASE_IDS)
async def test_conformance_cases(conformance_env, case_index):
    app, cases = conformance_env
    case = cases[case_index]

    params = {
        k: _substitute(v, "conformance_fixture.py", "conformance_missing_file.py", "conformance_edit_target.py")
        for k, v in case.get("params", {}).items()
    }

    transport = httpx.ASGITransport(app=app)
    async with httpx.AsyncClient(transport=transport, base_url="http://test") as client:
        resp = await client.post(
            "/__oumv/rpc",
            json={"jsonrpc": "2.0", "id": case["id"], "method": case["method"], "params": params},
        )
    assert resp.status_code == 200
    body = resp.json()

    expect_error = case.get("expectError")
    if expect_error:
        assert body.get("error"), f"expected error {expect_error['code']}, got result: {body.get('result')}"
        assert body["error"]["code"] == expect_error["code"], body["error"]
        return

    assert not body.get("error"), body.get("error")
    result = body["result"]
    for field in case.get("expectResultFields", []):
        assert _path_exists(result, field), f"result missing field {field!r}: {result}"

    expected_content = case.get("expectVersionMatchesContent")
    if expected_content:
        digest = hashlib.sha256(expected_content.replace("${EDIT_CONTENT}", EDIT_CONTENT).encode("utf-8")).hexdigest()
        assert result["newVersion"] == f"sha256:{digest}"
        assert result["status"] == "APPLIED"
        # The edit must have actually reached the file with a backup of the previous content
        assert result["backupPath"]


def test_fixture_file_has_expected_shape():
    data = json.loads(FIXTURES.read_text(encoding="utf-8"))
    assert data["protocolVersion"] == "1.0.0"
    assert len(data["cases"]) >= 6
    for case in data["cases"]:
        assert case["id"]
        assert case["method"].startswith("oumv/")
        assert case.get("expectError") or case.get("expectResultFields")
