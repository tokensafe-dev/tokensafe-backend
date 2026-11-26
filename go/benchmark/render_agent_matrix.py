#!/usr/bin/env python3
"""Render a measured agent matrix (JSON from `tokensafe bench-oumv`) as Markdown.

The report is generated from the artifact, never written by hand, so every number in it
can be traced to a run: `render_agent_matrix.py runs/x.json OUMV_AGENT_MATRIX_V4.md`.

Usage:
    render_agent_matrix.py <primary.json> <out.md> [comparison.json ...] [--title T]

The primary run gets the full tables; extra runs are summarised as comparisons (an A/B of
the harness or the product between two commits).
"""
from __future__ import annotations

import json
import sys
from collections import defaultdict
from pathlib import Path
from typing import Any, Dict, List

ARM_ORDER = ("baseline", "tokensafe", "oumv")


def load(path: str):
    report = json.loads(Path(path).read_text(encoding="utf-8"))
    rows: Dict[str, List[dict]] = defaultdict(list)
    for result in report["results"]:
        rows[result["arm"]].append(result)
    return report, rows


def stats(rows: List[dict]) -> Dict[str, float]:
    n = len(rows)
    return {
        "n": n,
        "solved": sum(1 for r in rows if r["solved"]),
        "steps": sum(r["usage"]["calls"] for r in rows) / n,
        "steps_min": min(r["usage"]["calls"] for r in rows),
        "steps_max": max(r["usage"]["calls"] for r in rows),
        "inp": sum(r["usage"]["input_tokens"] for r in rows) / n,
        "out": sum(r["usage"]["output_tokens"] for r in rows) / n,
        "usd": sum(r["usage"]["usd"] for r in rows) / n,
        "hit": sum(r["usage"].get("cache_hit_tokens", 0) for r in rows) / n,
        "max_steps": sum(1 for r in rows if r["stop_reason"] == "max_steps"),
    }


def pct(a: float, b: float) -> str:
    return f"{100 * (b - a) / a:+.1f}%" if a else "—"


def arm_table(report: dict, rows: Dict[str, List[dict]]) -> str:
    out = ["| Arm | Runs | Solved | Mean steps (min–max) | Mean input (cache-hit share) | Mean output | Cost/run |",
           "| :--- | ---: | ---: | ---: | ---: | ---: | ---: |"]
    for arm in ARM_ORDER:
        if arm not in rows:
            continue
        s = stats(rows[arm])
        out.append(
            f"| `{arm}` | {s['n']} | {s['solved']}/{s['n']} | {s['steps']:.1f} "
            f"({s['steps_min']}–{s['steps_max']}) | {s['inp']:,.0f} "
            f"({100 * s['hit'] / s['inp']:.0f}%) | {s['out']:,.0f} | ${s['usd']:.4f} |"
        )
    return "\n".join(out)


def per_task(rows: Dict[str, List[dict]]) -> str:
    tasks = list(dict.fromkeys(r["task"] for arm in rows for r in rows[arm]))
    out = ["| Task | baseline input | tokensafe input | oumv input | baseline steps | tokensafe steps | oumv steps |",
           "| :--- | ---: | ---: | ---: | ---: | ---: | ---: |"]
    for task in tasks:
        cells = []
        for key in ("input_tokens", "calls"):
            for arm in ARM_ORDER:
                rs = [r for r in rows.get(arm, []) if r["task"] == task]
                fmt = "{:,.0f}" if key == "input_tokens" else "{:.1f}"
                cells.append(fmt.format(sum(r['usage'][key] for r in rs) / len(rs)) if rs else "—")
        # cells: base_in, ts_in, oumv_in, base_steps, ts_steps, oumv_steps
        out.append(f"| `{task}` | {cells[0]} | {cells[1]} | {cells[2]} | {cells[3]} | {cells[4]} | {cells[5]} |")
    return "\n".join(out)


def deltas(base: Dict[str, float], other: Dict[str, float]) -> str:
    return (f"{pct(base['inp'], other['inp'])} input, {pct(base['usd'], other['usd'])} cost, "
            f"{pct(base['steps'], other['steps'])} steps")


def main(argv: List[str]) -> int:
    args = [a for a in argv if not a.startswith("--")]
    title = "OUMV agent matrix"
    for i, a in enumerate(argv):
        if a == "--title" and i + 1 < len(argv):
            title = argv[i + 1]
            args = [x for x in args if x != title]
    if len(args) < 2:
        print(__doc__)
        return 2

    primary_path, out_path, extra_paths = args[0], args[1], args[2:]
    report, rows = load(primary_path)
    s_base = stats(rows["baseline"])
    s_ts = stats(rows["tokensafe"]) if "tokensafe" in rows else None
    s_ou = stats(rows["oumv"]) if "oumv" in rows else None
    prov = report.get("provenance", {})
    commit = (prov.get("harness_commit") or "unknown")[:8]
    dirty = prov.get("dirty")

    md: List[str] = [
        f"# {title}",
        "",
        "> Generated from the run artifact by `render_agent_matrix.py` — do not edit by hand.",
        "> Every token and cost number comes from the provider's `usage` payload and a real",
        "> 12-assertion oracle re-run after every agent step. No replay, no estimates.",
        "",
        "## Method",
        "",
        "| | |",
        "| :--- | :--- |",
        f"| Model | `{report.get('model')}`, temperature 0 |",
        f"| Runs | {report.get('runs')} per task per arm "
        f"({len(report['results'])} sessions, ${report.get('spent_usd', 0):.4f} spent) |",
        f"| Harness commit | `{commit}`{' + uncommitted changes' if dirty else ''} |",
        "| Oracle | `go test ./benchmark -run TestGroundTruthOracleVanilla` (12 assertions), after every step |",
        "| Sandbox | throwaway copy of `tokensafe-backend/go` with one deliberate regression per task |",
        "| Arms | `baseline` file/command tools; `tokensafe` the product's MCP server; `oumv` the shipped Go adapter |",
        "",
        "`tokensafe` runs the product's own loop: `scoped_search` → `smart_read_file` (symbol or",
        "endpoint target) → `apply_edits` with inline `verify=\"auto\"`, so the edit and its test",
        "verdict come back in one call. `oumv` runs `observe L3` → `getSource` → `applyEdit` →",
        "`verify`.",
        "",
        "## Results",
        "",
        arm_table(report, rows),
        "",
    ]
    if s_ts and s_ou:
        md += [
            f"**tokensafe vs baseline:** {deltas(s_base, s_ts)}.",
            f"**oumv vs baseline:** {deltas(s_base, s_ou)}.",
            "",
        ]
    md += ["### Per task (means)", "", per_task(rows), ""]

    if s_ts and s_ou:
        ratio = s_ou["out"] / s_ts["out"] if s_ts["out"] else 0
        md += [
            "### Why the two arms differ on output tokens",
            "",
            f"`oumv` emits {s_ou['out']:,.0f} output tokens per run against `tokensafe`'s "
            f"{s_ts['out']:,.0f} ({ratio:.1f}x). The protocol's mutation unit is the resource: "
            "`applyEdit` sends the new file (or one whole declaration with `mode=symbol`) and "
            "checks the OCC version against it, so a one-line fix still costs a full file on the "
            "wire. `apply_edits` sends the hunk plus its target snippet. Both pay it back on "
            "input — `oumv` reads the handler once from `observe L3` and has the file it needs — "
            "and output is the more expensive side at $1.10/Mtok against $0.07–0.27 for input.",
            "",
            "A `mode=patch` (exact-snippet replace under OCC) would close that gap; it is not in "
            "the wire today and is the top protocol item on the remaining list.",
            "",
        ]

    if extra_paths:
        md += ["## Comparison runs", ""]
        for path in extra_paths:
            other, other_rows = load(path)
            other_commit = (other.get("provenance", {}).get("harness_commit") or "unknown")[:8]
            md.append(f"**`{other_commit}` vs `{commit}`** (identical code where an arm is")
            md.append("unchanged — that difference is the noise floor at this n):")
            md.append("")
            for arm in ARM_ORDER:
                if arm not in rows or arm not in other_rows:
                    continue
                a, b = stats(other_rows[arm]), stats(rows[arm])
                md.append(f"- `{arm}`: input {a['inp']:,.0f} → {b['inp']:,.0f} ({pct(a['inp'], b['inp'])}, "
                          f"steps {a['steps']:.1f} → {b['steps']:.1f}, "
                          f"solved {a['solved']}/{a['n']} → {b['solved']}/{b['n']})")
            md.append("")

    md += [
        "## Limits",
        "",
        f"- One model (`{report.get('model')}`, temperature 0), one vendor, "
        f"{len(rows['baseline']) // report.get('runs', 1)} tasks, {report.get('runs')} runs per cell: "
        "the min–max step range in the table is the honest spread.",
        "- The sandbox is small (a 155-line Go app), so discovery is cheaper than in a large repo",
        "  and the tool-path saving here is a floor, not a ceiling.",
        "- Every advertised tool schema is re-sent in every request; the three arms do not declare",
        "  the same number of tools, and that difference is part of what is measured.",
        "- `oumv` mutates the whole resource and needs the full file in context to do it, and",
        "  `verify` starts a fresh app per call by design; the stateful task is confirmed with the",
        "  oracle in all three arms.",
        "",
        "## Artifacts",
        "",
        f"- `{primary_path}`",
    ]
    for path in extra_paths:
        md.append(f"- `{path}`")
    md.append("")

    Path(out_path).write_text("\n".join(md), encoding="utf-8")
    print(f"wrote {out_path}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
