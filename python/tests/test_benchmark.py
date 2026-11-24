"""
Unit test for Backend Agent Usability Benchmark (AUB).
Ensures ground truth oracle and trace replay pass on CI.
"""

import pytest
from pathlib import Path
from benchmark.runner import run_benchmark


@pytest.mark.asyncio
async def test_aub_benchmark_execution(tmp_path: Path):
    summary, oracle_report = await run_benchmark(tmp_path)

    assert oracle_report.all_passed is True
    assert oracle_report.passed_count == 12

    assert summary.autonomous_efficiency_ratio >= 15.0
    assert summary.token_savings_pct >= 90
    assert summary.db_pollution_reduction_pct == 100
    assert (tmp_path / "BENCHMARK_REPORT.md").exists()
    assert (tmp_path / "aub-results.json").exists()
