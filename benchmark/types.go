package benchmark

// StackType identifies the tested backend implementation stack.
type StackType string

const (
	StackVanilla   StackType = "vanilla"
	StackTokenSafe StackType = "tokensafe"
)

// AgentToolCallRecord records a single tool invocation within an agent turn.
type AgentToolCallRecord struct {
	Name          string                 `json:"name"`
	Args          map[string]interface{} `json:"args,omitempty"`
	DurationMs    float64                `json:"duration_ms"`
	OutputSummary string                 `json:"output_summary,omitempty"`
	IsError       bool                   `json:"is_error,omitempty"`
}

// AgentTurnRecord records an iterative interaction cycle by the AI agent.
type AgentTurnRecord struct {
	Turn                       int                   `json:"turn"`
	Stack                      StackType             `json:"stack"`
	InputTokens                int                   `json:"input_tokens"`
	OutputTokens               int                   `json:"output_tokens"`
	TotalTokens                int                   `json:"total_tokens"`
	ToolCalls                  []AgentToolCallRecord `json:"tool_calls"`
	DurationMs                 float64               `json:"duration_ms"`
	HasSyntaxError             bool                  `json:"has_syntax_error"`
	HasTypeError               bool                  `json:"has_type_error"`
	HasRuntimeError            bool                  `json:"has_runtime_error"`
	ErrorMessage               string                `json:"error_message,omitempty"`
	PassedAssertions           []string              `json:"passed_assertions"`
	Regressions                []string              `json:"regressions"`
	RecoveredFromPreviousError bool                  `json:"recovered_from_previous_error"`
	DBDirtyRecords             int                   `json:"db_dirty_records"`
}

// OracleAssertionResult captures the outcome of an acceptance test assertion.
type OracleAssertionResult struct {
	ID         string  `json:"id"`
	Title      string  `json:"title"`
	Passed     bool    `json:"passed"`
	DurationMs float64 `json:"duration_ms"`
	Error      string  `json:"error,omitempty"`
}

// OracleEvaluationReport summarizes the ground truth oracle evaluation.
type OracleEvaluationReport struct {
	PassedCount int                     `json:"passed_count"`
	TotalCount  int                     `json:"total_count"`
	Assertions  []OracleAssertionResult `json:"assertions"`
	AllPassed   bool                    `json:"all_passed"`
	DurationMs  float64                 `json:"duration_ms"`
}

// BenchmarkRunResult contains aggregated telemetry for a single stack replay.
type BenchmarkRunResult struct {
	Stack                   StackType         `json:"stack"`
	Scenario                string            `json:"scenario"`
	TotalTurns              int               `json:"total_turns"`
	InputTokens             int               `json:"input_tokens"`
	OutputTokens            int               `json:"output_tokens"`
	TotalTokens             int               `json:"total_tokens"`
	ContextSlope            int               `json:"context_slope"` // avg input token growth per turn
	WallClockSeconds        float64           `json:"wall_clock_seconds"`
	PassedAssertionsCount   int               `json:"passed_assertions_count"`
	TotalAssertionsCount    int               `json:"total_assertions_count"`
	ErrorCount              int               `json:"error_count"`
	RegressionCount         int               `json:"regression_count"`
	DBDirtyRecords          int               `json:"db_dirty_records"`
	SelfRecoveryRate        int               `json:"self_recovery_rate"` // 0-100%
	FunctionalYieldPerToken float64           `json:"functional_yield_per_token"` // FYpT: (passed / totalTokens) * 1000
	CostUSD                 float64           `json:"cost_usd"`
	CostYield               float64           `json:"cost_yield"` // passed / cost_usd
	Turns                   []AgentTurnRecord `json:"turns"`
	Passed                  bool              `json:"passed"`
}

// ComparativeSummary contains side-by-side comparative analysis of Vanilla vs TokenSafe.
type ComparativeSummary struct {
	Scenario                  string             `json:"scenario"`
	Timestamp                 string             `json:"timestamp"`
	Vanilla                   BenchmarkRunResult `json:"vanilla"`
	TokenSafe                 BenchmarkRunResult `json:"tokensafe"`
	AutonomousEfficiencyRatio float64            `json:"autonomous_efficiency_ratio"` // AER: FYpT_tokensafe / FYpT_vanilla
	TokenSavingsPct           int                `json:"token_savings_pct"`
	TurnReductionPct          int                `json:"turn_reduction_pct"`
	LatencyReductionPct       int                `json:"latency_reduction_pct"`
	RegressionReductionPct    int                `json:"regression_reduction_pct"`
	DBPollutionReductionPct   int                `json:"db_pollution_reduction_pct"`
	SummaryText               string             `json:"summary_text"`
}
