"""§8 result versioning (wire 1.6.0): the observe/understand ifVersion handshake."""

import asyncio
import os

import pytest

from tokensafe_python.core.runtime import OUMVRuntime


@pytest.mark.asyncio
async def test_observe_and_understand_carry_a_workspace_digest(tmp_path):
    (tmp_path / "app.py").write_text("def hello():\n    return 1\n", encoding="utf-8")
    runtime = OUMVRuntime(project_root=str(tmp_path))
    hs = await runtime.handle_handshake(protocolVersion="1.6.0")
    sid = hs["sessionId"]

    obs = await runtime.handle_observe(sessionId=sid, level="L0")
    digest = obs["workspaceVersion"]
    assert digest.startswith("sha256:") and len(digest) == 71

    # Match: the cached copy is still fresh, so the answer is the short envelope.
    short = await runtime.handle_observe(sessionId=sid, level="L1", ifVersion=digest)
    assert short["unchanged"] is True
    assert short["workspaceVersion"] == digest
    assert "data" not in short

    # Any touched file (an edit) invalidates the caller's token.
    (tmp_path / "app.py").write_text("def hello():\n    return 2\n", encoding="utf-8")
    fresh = await runtime.handle_observe(sessionId=sid, level="L0")
    assert fresh["workspaceVersion"] != digest

    from tokensafe_python.protocol.errors import OumvError, STALE_RESOURCE_VERSION

    with pytest.raises(OumvError) as excinfo:
        await runtime.handle_observe(sessionId=sid, level="L0", ifVersion=digest)
    assert excinfo.value.code == STALE_RESOURCE_VERSION
    assert excinfo.value.data["reason"] == "stale_version"
    assert excinfo.value.data["current"] == fresh["workspaceVersion"]

    und = await runtime.handle_understand(sessionId=sid, query="causality", target="x")
    assert und["workspaceVersion"] == fresh["workspaceVersion"]
    u2 = await runtime.handle_understand(sessionId=sid, query="causality", ifVersion=fresh["workspaceVersion"])
    assert u2["unchanged"] is True
