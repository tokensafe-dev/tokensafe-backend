"""Language-neutral OUMV conformance certification for the FastAPI adapter.

Runs the canonical case file (agent-oumv/fixtures/conformance-cases.json, vendored
here) through the mounted JSON-RPC endpoint and asserts the conformance level the
runtime attains. Set CERT_OUT_DIR to write the machine-readable report.
"""

import json
import os
from pathlib import Path

import httpx
import pytest
from fastapi import FastAPI, Request
from fastapi.responses import JSONResponse
from sqlalchemy import create_engine, text

from tokensafe_python import conformance
from tokensafe_python.adapters.fastapi import mount_tokensafe

FIXTURES = Path(__file__).parent / "conformance-cases.json"
EDIT_TARGET = "conformance_edit_target.py"
EDIT_ORIGINAL = b"value = 41\n"


def _cert_app(tmp_path, *, read_only: bool = False):
    """The certification app: fixture files, a real SQLite engine and the routes."""
    (tmp_path / "conformance_fixture.py").write_text("value = 0\n", encoding="utf-8")
    (tmp_path / EDIT_TARGET).write_bytes(EDIT_ORIGINAL)
    (tmp_path / "conformance_symbol_target.py").write_text("def value():\n    return 41\n", encoding="utf-8")
    (tmp_path / "conformance_delete_target.py").write_text("value = 1\n", encoding="utf-8")
    (tmp_path / "conformance_rename_source.py").write_text("value = 2\n", encoding="utf-8")

    app = FastAPI()
    engine = create_engine(f"sqlite:///{tmp_path / 'cert.sqlite'}")
    with engine.begin() as conn:
        conn.execute(text("CREATE TABLE cert_probe (id INTEGER PRIMARY KEY, v INTEGER)"))
        conn.execute(text("INSERT INTO cert_probe (id, v) VALUES (1, 0)"))
    mounted = mount_tokensafe(
        app, auto_discover=False, project_root=str(tmp_path), db_engine=engine, read_only=read_only
    )

    @app.post("/__oumv_sandbox_probe")
    async def sandbox_probe(request: Request):
        """Writes only under the dry-run header and rolls the transaction back."""
        if request.headers.get("x-tokensafe-dry-run") != "true":
            return JSONResponse({"error": "dry-run header required"}, status_code=400)
        with engine.connect() as conn:
            tx = conn.begin()
            conn.execute(text("UPDATE cert_probe SET v = 1 WHERE id = 1"))
            tx.rollback()
        return {"ok": True}
    # L2 needs a real target: a registered model and route, not a fabricated one.
    mounted.runtime.register_schema("Order", {"type": "object", "properties": {"id": {"type": "string"}}})
    mounted.runtime.register_route("/api/v1/orders", "GET", "list_orders")

    markers = conformance.Markers(
        fixture_file="conformance_fixture.py",
        missing_file="conformance_missing_file.py",
        edit_target=EDIT_TARGET,
        new_file="conformance_created_file.py",
        edit_version=conformance.version_of(EDIT_ORIGINAL),
        l2_target="Order",
        l3_target=EDIT_TARGET,
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
        batch_file_a="conformance_batch_a.py",
        batch_file_b="conformance_batch_b.py",
        batch_content="value = 7\n",
    )
    return app, markers


@pytest.mark.asyncio
async def test_conformance_certification(tmp_path):
    app, markers = _cert_app(tmp_path)

    transport = httpx.ASGITransport(app=app)
    async with httpx.AsyncClient(transport=transport, base_url="http://test") as client:

        async def post(method, params):
            resp = await client.post(
                "/__oumv/rpc",
                json={"jsonrpc": "2.0", "id": method, "method": method, "params": params},
            )
            assert resp.status_code == 200, resp.text
            return resp.json()

        report = await conformance.run_conformance(
            post, FIXTURES, markers, adapter_name="python-fastapi", framework="fastapi"
        )

    out_dir = os.environ.get("CERT_OUT_DIR")
    if out_dir:
        conformance.save_report(report, Path(out_dir) / "python-fastapi.json")

    failures = [c for c in report["cases"] if c["status"] != "passed"]
    assert not failures, failures
    assert report["attainedLevel"] == 3, (report["attainedLevel"], report["levels"])
    assert report["specVersion"] == "1.6.0"


@pytest.mark.asyncio
async def test_read_only_conformance_certification(tmp_path):
    """§8.5 read-only profile: the writing methods are absent, the read surface stays."""
    app, markers = _cert_app(tmp_path, read_only=True)

    transport = httpx.ASGITransport(app=app)
    async with httpx.AsyncClient(transport=transport, base_url="http://test") as client:

        async def post(method, params):
            resp = await client.post(
                "/__oumv/rpc",
                json={"jsonrpc": "2.0", "id": method, "method": method, "params": params},
            )
            assert resp.status_code == 200, resp.text
            return resp.json()

        report = await conformance.run_conformance(
            post, FIXTURES, markers, adapter_name="python-fastapi", framework="fastapi"
        )

    out_dir = os.environ.get("CERT_OUT_DIR")
    if out_dir:
        conformance.save_report(report, Path(out_dir) / "python-fastapi-readonly.json")

    read_only_cases = [c for c in report["cases"] if c["id"].startswith("read-only-")]
    failures = [c for c in report["cases"] if c["status"] != "passed"]
    assert len(read_only_cases) == 6, read_only_cases
    assert not failures, failures
    assert report["attainedLevel"] == 3, (report["attainedLevel"], report["levels"])


def test_fixture_file_has_level_tagged_v6_shape():
    data = json.loads(FIXTURES.read_text(encoding="utf-8"))
    assert data["fixtureVersion"] == 17
    assert data["protocolVersion"] == "1.6.0"
    assert sorted(data["levelDefinitions"]) == ["1", "2", "3"]
    # Push cases belong to transport certificates; the native runners assert the
    # request/response suite and leave them to `oumv-certify --sse/--ws`.
    assert {case["transport"] for case in data.get("pushCases", [])} <= {"sse", "ws"}
    # Capability and session cases are gated on the handshake claim.
    assert all(case.get("requiresCapability") for case in data.get("capabilityCases", []))
    assert all(case.get("requiresCapability") for case in data.get("sessionCases", []))
    # v12: the §4.1 result-versioning cases are gated by minWireVersion, not a capability.
    etag_cases = [case for case in data["cases"] if case.get("minWireVersion")]
    assert {case["minWireVersion"] for case in etag_cases} == {"1.6.0"}
    assert any(case["id"] == "observe-stale-token-rejected" for case in etag_cases)
    assert data["sessionCases"], "fixtures v10 must prove an expired session"

    levels = {case["level"] for case in data["cases"]}
    assert levels == {1, 2, 3}
    ids = {case["id"] for case in data["cases"]}
    for required in ("unknown-session-is-rejected", "get-source-returns-occ-token",
                     "verify-returns-status-timings-and-scope"):
        assert required in ids

    # OCC is mandatory for replace: a case may omit the version only when it either
    # asserts the guard itself, or asks for mode=create (which forbids it).
    edit_cases = [c for c in data["cases"] if c["method"] == "oumv/applyEdit"]
    without_version = sorted(c["id"] for c in edit_cases if "expectedVersion" not in c["params"])
    assert without_version == [
        "apply-edit-without-version-rejected",
        "create-existing-file-rejected",
        "create-new-file",
    ]
    guard = next(c for c in edit_cases if c["id"] == "apply-edit-without-version-rejected")
    assert guard["expectError"]["code"] == -32011
    create_case = next(c for c in edit_cases if c["id"] == "create-new-file")
    assert create_case["params"]["mode"] == "create"
    assert create_case["expectResultFields"][-1] == "created"
