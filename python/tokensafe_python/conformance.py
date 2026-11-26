"""Language-neutral OUMV conformance runner (spec §10).

Executes the canonical cases from ``agent-oumv/fixtures/conformance-cases.json``
through any JSON-RPC transport and derives the conformance level a runtime
actually attains. Reports are the certification artifact: a level claim without
a reproducible passing run is not a certification.
"""

from __future__ import annotations

import hashlib
import json
import time
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, Awaitable, Callable, Dict, List, Optional

SPEC_VERSION = "1.1.0"


@dataclass
class Markers:
    """Concrete values substituted for ``${MARKER}`` placeholders."""

    fixture_file: str
    missing_file: str
    new_file: str
    edit_target: str
    edit_version: str
    edit_content: str = "value = 42\n"
    broken_content: str = "def broken(:\n"
    l2_target: str = ""
    l3_target: str = ""
    verify_target: str = ""
    symbol_file: str = ""
    symbol_name: str = ""
    symbol_replacement: str = ""
    symbol_version: str = ""
    symbol_file_content: str = ""
    delete_file: str = ""
    delete_version: str = ""
    rename_source: str = ""
    rename_target: str = ""
    rename_version: str = ""

    def substitute(self, params: Dict[str, Any]) -> Dict[str, Any]:
        replacements = {
            "${FIXTURE_FILE}": self.fixture_file,
    "${NEW_FILE}": self.new_file,
            "${MISSING_FILE}": self.missing_file,
            "${EDIT_TARGET_FILE}": self.edit_target,
            "${EDIT_TARGET_VERSION}": self.edit_version,
            "${EDIT_CONTENT}": self.edit_content,
            "${BROKEN_CONTENT}": self.broken_content,
            "${L2_TARGET}": self.l2_target,
            "${L3_TARGET}": self.l3_target,
            "${VERIFY_TARGET}": self.verify_target,
            "${SYMBOL_FILE}": self.symbol_file,
            "${SYMBOL_NAME}": self.symbol_name,
            "${SYMBOL_REPLACEMENT}": self.symbol_replacement,
            "${SYMBOL_VERSION}": self.symbol_version,
            "${SYMBOL_FILE_CONTENT}": self.symbol_file_content,
            "${DELETE_FILE}": self.delete_file,
            "${DELETE_VERSION}": self.delete_version,
            "${RENAME_SOURCE}": self.rename_source,
            "${RENAME_TARGET}": self.rename_target,
            "${RENAME_VERSION}": self.rename_version,
        }
        def walk(value: Any) -> Any:
            if isinstance(value, str):
                for marker, replacement in replacements.items():
                    value = value.replace(marker, replacement)
                return value
            if isinstance(value, list):
                return [walk(item) for item in value]
            if isinstance(value, dict):
                return {key: walk(item) for key, item in value.items()}
            return value

        return {key: walk(value) for key, value in params.items()}

    def substitute_text(self, text: str) -> str:
        """Applies the marker table to one string (expectations too)."""
        return self.substitute({"value": text})["value"]


def version_of(content: bytes) -> str:
    """Returns the sha256 OCC token for content."""
    return "sha256:" + hashlib.sha256(content).hexdigest()


def load_cases(path: str | Path) -> Dict[str, Any]:
    """Loads and minimally validates a fixture file."""
    spec = json.loads(Path(path).read_text(encoding="utf-8"))
    cases = spec.get("cases") or []
    if not cases:
        raise ValueError(f"fixture {path} contains no cases")
    return spec


def _json_path(data: Any, path: str) -> Any:
    current = data
    for part in path.split("."):
        if isinstance(current, list):
            try:
                index = int(part)
            except ValueError:
                raise KeyError(path)
            if index < 0 or index >= len(current):
                raise KeyError(path)
            current = current[index]
            continue
        if not isinstance(current, dict) or part not in current:
            raise KeyError(path)
        current = current[part]
    return current


def _check(case: Dict[str, Any], body: Dict[str, Any], markers: Markers) -> str:
    """Evaluates every expectation a case declares; returns "" on success."""
    expected_error = case.get("expectError")
    if expected_error:
        error = body.get("error")
        if not error:
            return f"expected error code {expected_error['code']}, got result"
        if error.get("code") != expected_error["code"]:
            return f"expected error code {expected_error['code']}, got {error.get('code')} ({error.get('message')})"
        return ""

    if body.get("error"):
        err = body["error"]
        return f"expected success, got error {err.get('code')}: {err.get('message')}"

    result = body.get("result")
    for path in case.get("expectResultFields", []):
        try:
            _json_path(result, path)
        except KeyError:
            return f"result missing expected field {path!r}: {result}"
    if case.get("expectLevelEquals"):
        try:
            level = _json_path(result, "level")
        except KeyError:
            return "result missing level field"
        if level != case["expectLevelEquals"]:
            return f"expected level {case['expectLevelEquals']!r}, got {level!r}"
    if case.get("expectSha256Field"):
        try:
            value = _json_path(result, case["expectSha256Field"])
        except KeyError:
            return f"result missing {case['expectSha256Field']!r}"
        if not (isinstance(value, str) and value.startswith("sha256:") and len(value) == 71):
            return f"{case['expectSha256Field']} must be a sha256 OCC token, got {value!r}"
    if case.get("expectArrayField"):
        try:
            value = _json_path(result, case["expectArrayField"])
        except KeyError:
            return f"result missing {case['expectArrayField']!r}"
        if not isinstance(value, list) or not value:
            return f"{case['expectArrayField']} must be a non-empty array"
    if case.get("expectEvidence"):
        try:
            evidence = _json_path(result, "data.evidence")
        except KeyError:
            return "result must carry evidence metadata at data.evidence"
        if evidence.get("provenance") not in ("runtime", "static", "contract", "inferred", "external"):
            return f"evidence provenance {evidence.get('provenance')!r} is not a spec §6.2 value"
    for path, want in (case.get("expectValues") or {}).items():
        if isinstance(want, str):
            want = markers.substitute_text(want)
        try:
            got = _json_path(result, path)
        except KeyError:
            return f"result missing expected path {path!r}"
        if got != want:
            return f"expected {path}={want!r}, got {got!r}"
    expected_content = case.get("expectVersionMatchesContent")
    if expected_content:
        content = markers.substitute_text(expected_content)
        want = version_of(content.encode("utf-8"))
        if result.get("newVersion") != want:
            return f"version token must match written content: expected {want}, got {result.get('newVersion')}"
    return ""


def attained_level(cases: List[Dict[str, Any]]) -> int:
    """Levels are cumulative; a level must be exercised to be claimed."""
    attained = 0
    for level in (1, 2, 3):
        at_level = [c for c in cases if c["level"] == level]
        if not at_level:
            break
        if any(c["status"] != "passed" for c in cases if c["level"] <= level):
            break
        attained = level
    return attained


def summarize_levels(cases: List[Dict[str, Any]]) -> Dict[str, str]:
    levels: Dict[str, str] = {}
    for level in (1, 2, 3):
        at_level = [c for c in cases if c["level"] == level]
        if not at_level:
            levels[str(level)] = "skipped"
        elif any(c["status"] != "passed" for c in at_level):
            levels[str(level)] = "failed"
        else:
            levels[str(level)] = "passed"
    return levels


async def run_conformance(
    post: Callable[[str, Dict[str, Any]], Awaitable[Dict[str, Any]]],
    spec_path: str | Path,
    markers: Markers,
    adapter_name: str = "tokensafe-python",
    framework: str = "fastapi",
) -> Dict[str, Any]:
    """Runs every fixture case through ``post`` and returns a certification report.

    ``post(method, params)`` must return the decoded JSON-RPC response envelope.
    """
    spec = load_cases(spec_path)
    results: List[Dict[str, Any]] = []

    for case in spec["cases"]:
        outcome = {"id": case["id"], "level": case["level"], "status": "passed", "error": ""}

        for precondition in case.get("preconditions", []):
            if precondition != "verify":
                outcome["status"] = "failed"
                outcome["error"] = f"unknown precondition {precondition!r}"
                break
            body = await post(
                "oumv/verify",
                markers.substitute({"target": markers.verify_target, "action": "conformance-probe"}),
            )
            if body.get("error"):
                outcome["status"] = "failed"
                outcome["error"] = f"verify precondition failed: {body['error']}"
                break
        if outcome["status"] == "passed":
            body = await post(case["method"], markers.substitute(case.get("params", {})))
            message = _check(case, body, markers)
            if message:
                outcome["status"] = "failed"
                outcome["error"] = message
        results.append(outcome)

    passed = sum(1 for r in results if r["status"] == "passed")
    return {
        "adapterName": adapter_name,
        "framework": framework,
        "specVersion": spec.get("protocolVersion", SPEC_VERSION),
        "fixtureVersion": spec.get("fixtureVersion", 1),
        "total": len(results),
        "passed": passed,
        "failed": len(results) - passed,
        "attainedLevel": attained_level(results),
        "levels": summarize_levels(results),
        "cases": results,
        "generatedAt": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
    }


def save_report(report: Dict[str, Any], path: str | Path) -> Path:
    """Writes the report as pretty JSON, creating parent directories."""
    target = Path(path)
    target.parent.mkdir(parents=True, exist_ok=True)
    target.write_text(json.dumps(report, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
    return target
