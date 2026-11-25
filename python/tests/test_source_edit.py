"""
Tests for real OCC source editing through the FastAPI-mounted OUMV runtime:
replaces the previous fabricated sha256 stub with genuine file edits,
version checks, syntax pre-flight, backups, and error codes.
"""

import hashlib
import os
import shutil
import tempfile

import httpx
import pytest
from fastapi import FastAPI

from tokensafe_python.adapters.fastapi import mount_tokensafe


@pytest.fixture
def occ_app(tmp_path):
    (tmp_path / "service.py").write_text("def compute():\n    return 1\n", encoding="utf-8")
    (tmp_path / "notes.txt").write_text("plain text\n", encoding="utf-8")
    app = FastAPI()
    mount_tokensafe(app, auto_discover=False, project_root=str(tmp_path))
    return app, tmp_path


def _sha256_of(path) -> str:
    with open(path, "rb") as f:
        return "sha256:" + hashlib.sha256(f.read()).hexdigest()


async def _rpc(app, method: str, params: dict) -> dict:
    transport = httpx.ASGITransport(app=app)
    async with httpx.AsyncClient(transport=transport, base_url="http://test") as client:
        resp = await client.post(
            "/__oumv/rpc",
            json={"jsonrpc": "2.0", "id": 1, "method": method, "params": params},
        )
        return resp.json()


@pytest.mark.asyncio
async def test_version_probe_without_replacement(occ_app):
    app, root = occ_app
    body = await _rpc(app, "oumv/applyEdit", {"path": "service.py"})
    result = body["result"]
    assert result["status"] == "CURRENT"
    # Real sha256 of the on-disk content, not a fabricated token
    assert result["version"] == _sha256_of(root / "service.py")


@pytest.mark.asyncio
async def test_real_edit_returns_true_occ_version(occ_app):
    app, root = occ_app
    body = await _rpc(app, "oumv/applyEdit", {
        "path": "service.py",
        "replacement": "def compute():\n    return 2\n",
    })
    result = body["result"]
    assert result["status"] == "APPLIED"
    assert result["mode"] == "RUNTIME_DIRECT_EDIT"
    # The returned version must match the actually-written content
    assert result["newVersion"] == _sha256_of(root / "service.py")
    with open(root / "service.py", encoding="utf-8") as f:
        assert f.read() == "def compute():\n    return 2\n"
    # Syntax pre-flight ran for .py
    assert result["syntaxCheck"]["status"] == "passed"
    assert result["syntaxCheck"]["tool"] == "python-ast"
    # Backup of the previous content exists
    assert result["backupPath"]
    with open(result["backupPath"], encoding="utf-8") as f:
        assert f.read() == "def compute():\n    return 1\n"


@pytest.mark.asyncio
async def test_occ_conflict_returns_32001(occ_app):
    app, root = occ_app
    body = await _rpc(app, "oumv/applyEdit", {
        "path": "service.py",
        "replacement": "def compute():\n    return 3\n",
        "expectedVersion": "sha256:deadbeef",
    })
    err = body["error"]
    assert err["code"] == -32001
    assert "CONFLICT_DETECTED" in err["message"]
    # File untouched
    with open(root / "service.py", encoding="utf-8") as f:
        assert f.read() == "def compute():\n    return 1\n"


@pytest.mark.asyncio
async def test_syntax_guard_returns_32002(occ_app):
    app, root = occ_app
    body = await _rpc(app, "oumv/applyEdit", {
        "path": "service.py",
        "replacement": "def broken(:\n    return 0\n",
    })
    err = body["error"]
    assert err["code"] == -32002
    assert "SYNTAX_ERROR" in err["message"]
    assert err["data"]["syntaxCheck"]["status"] == "failed"
    # File untouched
    with open(root / "service.py", encoding="utf-8") as f:
        assert f.read() == "def compute():\n    return 1\n"


@pytest.mark.asyncio
async def test_path_traversal_returns_32003(occ_app):
    app, root = occ_app
    body = await _rpc(app, "oumv/applyEdit", {
        "path": "../outside.py",
        "replacement": "x = 1\n",
    })
    err = body["error"]
    assert err["code"] == -32003
    assert "SECURITY_VIOLATION" in err["message"]


@pytest.mark.asyncio
async def test_missing_file_returns_32009(occ_app):
    app, root = occ_app
    body = await _rpc(app, "oumv/applyEdit", {"path": "ghost.py", "replacement": "x = 1\n"})
    err = body["error"]
    assert err["code"] == -32009
    assert "RESOURCE_NOT_FOUND" in err["message"]


@pytest.mark.asyncio
async def test_uncheckable_extension_reports_skipped(occ_app):
    app, root = occ_app
    body = await _rpc(app, "oumv/applyEdit", {
        "path": "notes.txt",
        "replacement": "updated text\n",
    })
    result = body["result"]
    assert result["status"] == "APPLIED"
    # Honest reporting: no checker for .txt — never imply validation
    assert result["syntaxCheck"]["status"] == "skipped"
