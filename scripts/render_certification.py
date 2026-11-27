#!/usr/bin/env python3
"""Renders the certification block in README.md from the committed reports.

`certification/*.json` is the source of truth: the adapter count, the fixture
version, the case count and every verdict shown in the README are derived from
it, so the prose cannot drift when fixtures or adapters change.

The block lives between these markers in README.md:

    <!-- certification:start -->
    <!-- certification:end -->

Usage:
  scripts/render_certification.py --write   regenerate the block
  scripts/render_certification.py --check   exit 1 when the block is stale
  scripts/render_certification.py --print   print the block

Exit code 0 means the README matches the reports. CI runs `--check`.
"""

from __future__ import annotations

import json
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
REPORTS = ROOT / "certification"
README = ROOT / "README.md"
START = "<!-- certification:start -->"
END = "<!-- certification:end -->"

FRAMEWORK = {
    "gin": "Gin",
    "chi": "chi",
    "net/http": "net/http",
    "fastapi": "FastAPI",
    "express": "Express",
    "fastify": "Fastify",
    "spring": "Spring Boot",
    "laravel": "Laravel / Symfony",
}
TRANSPORT = {
    "gin": "HTTP `POST /__oumv/rpc`",
    "chi": "HTTP `POST /__oumv/rpc`",
    "net/http": "HTTP `POST /__oumv/rpc`",
    "fastapi": "ASGI `POST /__oumv/rpc`",
    "express": "Node `POST /__oumv/rpc`",
    "fastify": "Node `POST /__oumv/rpc`",
    "spring": "JDK `POST /__oumv/rpc` + stdio",
    "laravel": "PHP `POST /__oumv/rpc` + stdio",
}


def load_reports() -> list[tuple[str, dict]]:
    reports = [
        (path.name, json.loads(path.read_text(encoding="utf-8")))
        for path in sorted(REPORTS.glob("*.json"))
    ]
    if not reports:
        raise SystemExit(f"no committed reports in {REPORTS}")
    return reports


def render(reports: list[tuple[str, dict]]) -> str:
    versions = {report.get("fixtureVersion") for _, report in reports}
    totals = {report.get("total") for _, report in reports}

    problems: list[str] = []
    if len(versions) != 1:
        problems.append(f"mixed fixtureVersion values: {sorted(str(v) for v in versions)}")
    if len(totals) != 1:
        problems.append(f"mixed case counts: {sorted(str(t) for t in totals)}")
    # A Level 2 claim is legitimate when the gap is a named case (a sidecar that
    # does not run the framework cannot record live traces). A Level 2 report with
    # an unnamed failure is not: it must list the failing case ids.
    for name, report in reports:
        failing = [c["id"] for c in report.get("cases", []) if c.get("status") == "failed"]
        if report.get("failed") and not failing:
            problems.append(f"{name}: {report['failed']} failed case(s) without a case id")
        if report.get("attainedLevel", 0) < 2:
            problems.append(f"{name}: attained level {report.get('attainedLevel')} is below 2")
    if problems:
        raise SystemExit("cannot render a truthful block:\n  - " + "\n  - ".join(problems))

    version, total = versions.pop(), totals.pop()
    level3 = [(n, r) for n, r in reports if r.get("attainedLevel", 0) >= 3 and not r.get("failed")]
    source_level = [(n, r) for n, r in reports if (n, r) not in level3]

    if source_level:
        lead = (
            f"**{len(reports)} adapters certified against {total} language-neutral cases** "
            f"(`fixtureVersion` {version}): {len(level3)} at Level 3 and {len(source_level)} "
            "source-level adapter(s) at Level 2, each with its single L3 gap named below."
        )
    else:
        lead = (
            f"**{len(reports)} adapters certified at Level 3** — {total} language-neutral cases "
            f"(`fixtureVersion` {version}) over the transport an agent actually uses."
        )
    lines = [
        lead + " Generated from `certification/*.json` by `scripts/render_certification.py`; "
        "CI fails when this block no longer matches the reports.",
        "",
    ]

    def table(rows: list[tuple[str, dict]]) -> list[str]:
        out = [
            "| Adapter | Framework | Transport exercised | Level | Cases | Report |",
            "| :--- | :--- | :--- | :--- | :--- | :--- |",
        ]
        for name, report in rows:
            framework = report.get("framework", "?")
            out.append(
                f"| `{report.get('adapterName', Path(name).stem)}` "
                f"| {FRAMEWORK.get(framework, framework)} "
                f"| {TRANSPORT.get(framework, '—')} "
                f"| **{report['attainedLevel']}** "
                f"| {report['passed']}/{report['total']} "
                f"| [certification/{name}](certification/{name}) |"
            )
        return out

    lines += table(level3)
    if source_level:
        lines += [
            "",
            "Source-level adapters scan the framework's route declarations statically, apply OCC-checked "
            "edits and run a real verify command, but they do not boot the application — so they record no "
            "live request traces, `observe-l4-returns-recorded-traces` fails, and Level 3 is not claimed:",
            "",
        ]
        lines += table(source_level)
        gaps = []
        for name, report in source_level:
            for case in report.get("cases", []):
                if case.get("status") == "failed":
                    gaps.append(f"`{report['adapterName']}` → `{case['id']}`: {case.get('error', '')}")
        if gaps:
            lines += ["", "Named gaps:", ""] + [f"- {gap}" for gap in gaps]
    return "\n".join(lines) + "\n"


def replace_block(readme: str, block: str) -> str:
    if START not in readme or END not in readme:
        raise SystemExit(f"{README} is missing the {START} / {END} markers")
    head, rest = readme.split(START, 1)
    _, tail = rest.split(END, 1)
    return f"{head}{START}\n{block}{END}{tail}"


def main(argv: list[str]) -> int:
    block = render(load_reports())
    current = README.read_text(encoding="utf-8")

    if "--print" in argv:
        print(block, end="")
        return 0

    expected = replace_block(current, block)

    if "--write" in argv:
        if expected != current:
            README.write_text(expected, encoding="utf-8")
            print("certification block regenerated")
        else:
            print("certification block already current")
        return 0

    if "--check" in argv:
        if expected == current:
            print("certification block matches certification/*.json")
            return 0
        print("certification block is stale — run scripts/render_certification.py --write")
        return 1

    print(__doc__)
    return 2


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
