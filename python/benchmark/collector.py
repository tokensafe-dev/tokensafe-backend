"""
Metrics collector and statistical comparator for Backend Usability Benchmark.
"""

from datetime import datetime, timezone
from typing import Dict, List, Optional, Set
from benchmark.types import (
    AgentTurnRecord,
    AgentToolCallRecord,
    BenchmarkRunResult,
    ComparativeSummary,
    StackType,
)

DEFAULT_INPUT_PRICE_PER_M = 3.00   # $3.00 per 1M input tokens
DEFAULT_OUTPUT_PRICE_PER_M = 15.00  # $15.00 per 1M output tokens


class MetricsCollector:
    """Collects per-turn agent telemetry and calculates comparative usability metrics."""

    def __init__(self, stack: StackType, scenario: str = "order-processing-api"):
        self.stack = stack
        self.scenario = scenario
        self.turns: List[AgentTurnRecord] = []
        self._previously_passed: Set[str] = set()
        self.total_regressions: int = 0

    def record_turn(self, turn_data: Dict) -> AgentTurnRecord:
        turn_num = len(self.turns) + 1
        input_tokens = turn_data.get("input_tokens", 0)
        output_tokens = turn_data.get("output_tokens", 0)
        total_tokens = input_tokens + output_tokens

        current_passed = set(turn_data.get("passed_assertions", []))
        regressions: List[str] = []
        for prior in self._previously_passed:
            if prior not in current_passed:
                regressions.append(prior)
        self.total_regressions += len(regressions)

        for p in current_passed:
            self._previously_passed.add(p)

        # Check self-recovery
        prev_turn = self.turns[-1] if self.turns else None
        prev_had_error = (
            prev_turn.has_syntax_error or prev_turn.has_type_error or prev_turn.has_runtime_error
            if prev_turn
            else False
        )
        curr_has_error = (
            turn_data.get("has_syntax_error", False)
            or turn_data.get("has_type_error", False)
            or turn_data.get("has_runtime_error", False)
        )
        recovered = bool(prev_had_error and not curr_has_error)

        tool_calls = [
            AgentToolCallRecord(**tc) for tc in turn_data.get("tool_calls", [])
        ]

        record = AgentTurnRecord(
            turn=turn_num,
            stack=self.stack,
            input_tokens=input_tokens,
            output_tokens=output_tokens,
            total_tokens=total_tokens,
            tool_calls=tool_calls,
            duration_ms=turn_data.get("duration_ms", 0.0),
            has_syntax_error=turn_data.get("has_syntax_error", False),
            has_type_error=turn_data.get("has_type_error", False),
            has_runtime_error=turn_data.get("has_runtime_error", False),
            error_message=turn_data.get("error_message"),
            passed_assertions=turn_data.get("passed_assertions", []),
            regressions=regressions,
            recovered_from_previous_error=recovered,
            db_dirty_records=turn_data.get("db_dirty_records", 0),
        )
        self.turns.append(record)
        return record

    def finalize(self, total_available_assertions: int = 12) -> BenchmarkRunResult:
        total_turns = len(self.turns)
        input_tokens = sum(t.input_tokens for t in self.turns)
        output_tokens = sum(t.output_tokens for t in self.turns)
        total_tokens = input_tokens + output_tokens

        wall_clock_seconds = round(sum(t.duration_ms for t in self.turns) / 1000.0, 2)

        error_count = sum(
            1 for t in self.turns if (t.has_syntax_error or t.has_type_error or t.has_runtime_error)
        )
        error_turns = sum(
            1 for t in self.turns if (t.has_syntax_error or t.has_type_error or t.has_runtime_error)
        )
        recovery_turns = sum(1 for t in self.turns if t.recovered_from_previous_error)

        self_recovery_rate = (
            round((recovery_turns / error_turns) * 100) if error_turns > 0 else 100
        )

        # Context Slope: average input token growth per turn
        context_slope = 0
        if len(self.turns) > 1:
            context_slope = round(
                (self.turns[-1].input_tokens - self.turns[0].input_tokens) / (len(self.turns) - 1)
            )

        last_turn = self.turns[-1] if self.turns else None
        passed_count = len(last_turn.passed_assertions) if last_turn else 0
        all_passed = (passed_count == total_available_assertions)
        db_dirty = last_turn.db_dirty_records if last_turn else 0

        # FYpT: (Passed / Total Tokens) * 1,000
        fypt = (
            round((passed_count / total_tokens) * 1000.0, 4)
            if total_tokens > 0
            else 0.0
        )

        input_cost = (input_tokens / 1_000_000.0) * DEFAULT_INPUT_PRICE_PER_M
        output_cost = (output_tokens / 1_000_000.0) * DEFAULT_OUTPUT_PRICE_PER_M
        cost_usd = round(input_cost + output_cost, 4)
        cost_yield = round(passed_count / cost_usd, 2) if cost_usd > 0 else 0.0

        return BenchmarkRunResult(
            stack=self.stack,
            scenario=self.scenario,
            total_turns=total_turns,
            input_tokens=input_tokens,
            output_tokens=output_tokens,
            total_tokens=total_tokens,
            context_slope=context_slope,
            wall_clock_seconds=wall_clock_seconds,
            passed_assertions_count=passed_count,
            total_assertions_count=total_available_assertions,
            error_count=error_count,
            regression_count=self.total_regressions,
            db_dirty_records=db_dirty,
            self_recovery_rate=self_recovery_rate,
            functional_yield_per_token=fypt,
            cost_usd=cost_usd,
            cost_yield=cost_yield,
            turns=self.turns,
            passed=all_passed,
        )

    @staticmethod
    def compare(vanilla: BenchmarkRunResult, tokensafe: BenchmarkRunResult) -> ComparativeSummary:
        aer = (
            round(tokensafe.functional_yield_per_token / vanilla.functional_yield_per_token, 2)
            if vanilla.functional_yield_per_token > 0
            else 999.0
        )

        token_savings_pct = (
            round(((vanilla.total_tokens - tokensafe.total_tokens) / vanilla.total_tokens) * 100)
            if vanilla.total_tokens > 0
            else 0
        )

        turn_reduction_pct = (
            round(((vanilla.total_turns - tokensafe.total_turns) / vanilla.total_turns) * 100)
            if vanilla.total_turns > 0
            else 0
        )

        latency_reduction_pct = (
            round(((vanilla.wall_clock_seconds - tokensafe.wall_clock_seconds) / vanilla.wall_clock_seconds) * 100)
            if vanilla.wall_clock_seconds > 0
            else 0
        )

        regression_reduction_pct = (
            round(((vanilla.regression_count - tokensafe.regression_count) / vanilla.regression_count) * 100)
            if vanilla.regression_count > 0
            else (100 if tokensafe.regression_count == 0 else 0)
        )

        db_pollution_reduction_pct = (
            round(((vanilla.db_dirty_records - tokensafe.db_dirty_records) / vanilla.db_dirty_records) * 100)
            if vanilla.db_dirty_records > 0
            else (100 if tokensafe.db_dirty_records == 0 else 0)
        )

        summary_text = (
            f"TokenSafe UARP achieved an Autonomous Efficiency Ratio (AER) of {aer}x "
            f"with {token_savings_pct}% fewer tokens, {turn_reduction_pct}% fewer iterations, "
            f"{db_pollution_reduction_pct}% zero DB pollution, and {regression_reduction_pct}% fewer regressions "
            f"compared to Vanilla FastAPI."
        )

        return ComparativeSummary(
            scenario=vanilla.scenario,
            timestamp=datetime.now(timezone.utc).isoformat(),
            vanilla=vanilla,
            tokensafe=tokensafe,
            autonomous_efficiency_ratio=aer,
            token_savings_pct=token_savings_pct,
            turn_reduction_pct=turn_reduction_pct,
            latency_reduction_pct=latency_reduction_pct,
            regression_reduction_pct=regression_reduction_pct,
            db_pollution_reduction_pct=db_pollution_reduction_pct,
            summary_text=summary_text,
        )
