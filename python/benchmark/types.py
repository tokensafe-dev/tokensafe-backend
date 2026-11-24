"""
Telemetry and metrics types for Backend Agent Usability Benchmark (AUB).
"""

from typing import Any, Dict, List, Literal, Optional
from pydantic import BaseModel, Field

StackType = Literal["vanilla", "tokensafe"]


class AgentToolCallRecord(BaseModel):
    name: str
    args: Optional[Dict[str, Any]] = None
    duration_ms: float = 0.0
    output_summary: Optional[str] = None
    is_error: bool = False


class AgentTurnRecord(BaseModel):
    turn: int
    stack: StackType
    input_tokens: int
    output_tokens: int
    total_tokens: int
    tool_calls: List[AgentToolCallRecord] = Field(default_factory=list)
    duration_ms: float = 0.0
    has_syntax_error: bool = False
    has_type_error: bool = False
    has_runtime_error: bool = False
    error_message: Optional[str] = None
    passed_assertions: List[str] = Field(default_factory=list)
    regressions: List[str] = Field(default_factory=list)
    recovered_from_previous_error: bool = False
    db_dirty_records: int = 0


class OracleAssertionResult(BaseModel):
    id: str
    title: str
    passed: bool
    duration_ms: float
    error: Optional[str] = None


class OracleEvaluationReport(BaseModel):
    passed_count: int
    total_count: int
    assertions: List[OracleAssertionResult]
    all_passed: bool
    duration_ms: float


class BenchmarkRunResult(BaseModel):
    stack: StackType
    scenario: str
    total_turns: int
    input_tokens: int
    output_tokens: int
    total_tokens: int
    context_slope: int  # avg token increase per turn
    wall_clock_seconds: float
    passed_assertions_count: int
    total_assertions_count: int
    error_count: int
    regression_count: int
    db_dirty_records: int
    self_recovery_rate: int  # 0 - 100%
    functional_yield_per_token: float  # FYpT: (passed / total_tokens) * 1000
    cost_usd: float
    cost_yield: float  # passed / cost_usd
    turns: List[AgentTurnRecord]
    passed: bool


class ComparativeSummary(BaseModel):
    scenario: str
    timestamp: str
    vanilla: BenchmarkRunResult
    tokensafe: BenchmarkRunResult
    autonomous_efficiency_ratio: float  # AER: FYpT_tokensafe / FYpT_vanilla
    token_savings_pct: int
    turn_reduction_pct: int
    latency_reduction_pct: int
    regression_reduction_pct: int
    db_pollution_reduction_pct: int
    summary_text: str
