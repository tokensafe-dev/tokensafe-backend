#!/usr/bin/env python3
"""Compares freshly generated OUMV certification reports with the committed ones.

A conformance level is only a claim until CI reproduces it. This check ignores
wall-clock fields (generatedAt) and asserts the substantive verdicts: attained
level, per-level status, counts, and the status of every case.

Usage: check_certification.py <fresh-dir> <committed-dir>
Exit code 1 with a diff summary when a committed claim no longer holds.
"""

from __future__ import annotations

import json
import sys
from pathlib import Path

FIELDS = ("attainedLevel", "total", "passed", "failed", "levels")


def load_reports(directory: Path) -> dict[str, dict]:
    reports: dict[str, dict] = {}
    for path in sorted(directory.glob("*.json")):
        reports[path.name] = json.loads(path.read_text(encoding="utf-8"))
    # Same adapters over extra transports (stdio, WS) live in a subdirectory: they
    # are transport evidence, not additional adapters, so the README table ignores
    # them while this check still reproduces them.
    transports = directory / "transports"
    if transports.is_dir():
        for path in sorted(transports.glob("*.json")):
            reports[f"transports/{path.name}"] = json.loads(path.read_text(encoding="utf-8"))
    return reports


def case_statuses(report: dict) -> dict[str, str]:
    return {c["id"]: c["status"] for c in report.get("cases", [])}


def main() -> int:
    if len(sys.argv) != 3:
        print(__doc__)
        return 2

    fresh = load_reports(Path(sys.argv[1]))
    committed = load_reports(Path(sys.argv[2]))

    if not committed:
        print(f"no committed certification reports in {sys.argv[2]}")
        return 1

    problems: list[str] = []
    for name, expected in committed.items():
        actual = fresh.get(name)
        if actual is None:
            problems.append(f"{name}: committed report has no fresh counterpart")
            continue
        for field in FIELDS:
            if actual.get(field) != expected.get(field):
                problems.append(f"{name}: {field} {actual.get(field)!r} != committed {expected.get(field)!r}")
        want_cases, got_cases = case_statuses(expected), case_statuses(actual)
        if want_cases != got_cases:
            for case_id, status in want_cases.items():
                if got_cases.get(case_id) != status:
                    problems.append(f"{name}: case {case_id} is {got_cases.get(case_id)!r}, committed {status!r}")

    for name in fresh:
        if name not in committed:
            problems.append(f"{name}: fresh report is not committed (run the generator and commit it)")

    if problems:
        print("Certification drift detected:")
        for problem in problems:
            print(f"  - {problem}")
        return 1

    summary = ", ".join(
        f"{name.replace('.json', '')}=L{report['attainedLevel']}" for name, report in sorted(fresh.items())
    )
    print(f"Certification reproducible: {summary}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
