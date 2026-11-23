package collector

import (
	"fmt"
	"math"
	"time"

	"github.com/tokensafe-dev/tokensafe-backend/benchmark"
)

const (
	DefaultInputPricePerM  = 3.00  // $3.00 per 1M input tokens
	DefaultOutputPricePerM = 15.00 // $15.00 per 1M output tokens
)

// MetricsCollector accumulates per-turn agent telemetry and computes metrics.
type MetricsCollector struct {
	stack             benchmark.StackType
	scenario          string
	turns             []benchmark.AgentTurnRecord
	previouslyPassed  map[string]bool
	totalRegressions  int
}

func NewMetricsCollector(stack benchmark.StackType, scenario string) *MetricsCollector {
	if scenario == "" {
		scenario = "order-processing-api"
	}
	return &MetricsCollector{
		stack:            stack,
		scenario:         scenario,
		turns:            make([]benchmark.AgentTurnRecord, 0),
		previouslyPassed: make(map[string]bool),
	}
}

func (c *MetricsCollector) RecordTurn(t benchmark.AgentTurnRecord) benchmark.AgentTurnRecord {
	currentPassed := make(map[string]bool)
	for _, p := range t.PassedAssertions {
		currentPassed[p] = true
	}

	regressions := make([]string, 0)
	for prior := range c.previouslyPassed {
		if !currentPassed[prior] {
			regressions = append(regressions, prior)
		}
	}
	c.totalRegressions += len(regressions)

	for p := range currentPassed {
		c.previouslyPassed[p] = true
	}

	prevTurnHadError := false
	if len(c.turns) > 0 {
		prev := c.turns[len(c.turns)-1]
		prevTurnHadError = prev.HasSyntaxError || prev.HasTypeError || prev.HasRuntimeError
	}
	currTurnHasError := t.HasSyntaxError || t.HasTypeError || t.HasRuntimeError
	recovered := prevTurnHadError && !currTurnHasError

	record := t
	record.Turn = len(c.turns) + 1
	record.Stack = c.stack
	record.TotalTokens = t.InputTokens + t.OutputTokens
	record.Regressions = regressions
	record.RecoveredFromPreviousError = recovered

	c.turns = append(c.turns, record)
	return record
}

func (c *MetricsCollector) Finalize(totalAvailableAssertions int) benchmark.BenchmarkRunResult {
	if totalAvailableAssertions <= 0 {
		totalAvailableAssertions = 12
	}

	totalTurns := len(c.turns)
	inputTokens := 0
	outputTokens := 0
	wallClockMs := 0.0
	errorCount := 0
	recoveryTurns := 0

	for _, t := range c.turns {
		inputTokens += t.InputTokens
		outputTokens += t.OutputTokens
		wallClockMs += t.DurationMs
		if t.HasSyntaxError || t.HasTypeError || t.HasRuntimeError {
			errorCount++
		}
		if t.RecoveredFromPreviousError {
			recoveryTurns++
		}
	}

	totalTokens := inputTokens + outputTokens
	wallClockSeconds := math.Round((wallClockMs/1000.0)*100) / 100

	selfRecoveryRate := 100
	if errorCount > 0 {
		selfRecoveryRate = int(math.Round((float64(recoveryTurns) / float64(errorCount)) * 100))
	}

	contextSlope := 0
	if len(c.turns) > 1 {
		contextSlope = int(math.Round(float64(c.turns[len(c.turns)-1].InputTokens-c.turns[0].InputTokens) / float64(len(c.turns)-1)))
	}

	passedCount := 0
	dbDirty := 0
	if len(c.turns) > 0 {
		last := c.turns[len(c.turns)-1]
		passedCount = len(last.PassedAssertions)
		dbDirty = last.DBDirtyRecords
	}
	allPassed := passedCount == totalAvailableAssertions

	// FYpT: (Passed / Total Tokens) * 1000
	fypt := 0.0
	if totalTokens > 0 {
		fypt = math.Round((float64(passedCount)/float64(totalTokens))*1000.0*10000) / 10000
	}

	inputCost := (float64(inputTokens) / 1_000_000.0) * DefaultInputPricePerM
	outputCost := (float64(outputTokens) / 1_000_000.0) * DefaultOutputPricePerM
	costUSD := math.Round((inputCost+outputCost)*10000) / 10000

	costYield := 0.0
	if costUSD > 0 {
		costYield = math.Round((float64(passedCount)/costUSD)*100) / 100
	}

	return benchmark.BenchmarkRunResult{
		Stack:                   c.stack,
		Scenario:                c.scenario,
		TotalTurns:              totalTurns,
		InputTokens:             inputTokens,
		OutputTokens:            outputTokens,
		TotalTokens:             totalTokens,
		ContextSlope:            contextSlope,
		WallClockSeconds:        wallClockSeconds,
		PassedAssertionsCount:   passedCount,
		TotalAssertionsCount:    totalAvailableAssertions,
		ErrorCount:              errorCount,
		RegressionCount:         c.totalRegressions,
		DBDirtyRecords:          dbDirty,
		SelfRecoveryRate:        selfRecoveryRate,
		FunctionalYieldPerToken: fypt,
		CostUSD:                 costUSD,
		CostYield:               costYield,
		Turns:                   c.turns,
		Passed:                  allPassed,
	}
}

// Compare produces a side-by-side ComparativeSummary from Vanilla and TokenSafe results.
func Compare(vanilla, tokenSafe benchmark.BenchmarkRunResult) benchmark.ComparativeSummary {
	aer := 999.0
	if vanilla.FunctionalYieldPerToken > 0 {
		aer = math.Round((tokenSafe.FunctionalYieldPerToken/vanilla.FunctionalYieldPerToken)*100) / 100
	}

	tokenSavingsPct := 0
	if vanilla.TotalTokens > 0 {
		tokenSavingsPct = int(math.Round(float64(vanilla.TotalTokens-tokenSafe.TotalTokens) / float64(vanilla.TotalTokens) * 100))
	}

	turnReductionPct := 0
	if vanilla.TotalTurns > 0 {
		turnReductionPct = int(math.Round(float64(vanilla.TotalTurns-tokenSafe.TotalTurns) / float64(vanilla.TotalTurns) * 100))
	}

	latencyReductionPct := 0
	if vanilla.WallClockSeconds > 0 {
		latencyReductionPct = int(math.Round(float64(vanilla.WallClockSeconds-tokenSafe.WallClockSeconds) / float64(vanilla.WallClockSeconds) * 100))
	}

	regressionReductionPct := 100
	if vanilla.RegressionCount > 0 {
		regressionReductionPct = int(math.Round(float64(vanilla.RegressionCount-tokenSafe.RegressionCount) / float64(vanilla.RegressionCount) * 100))
	}

	dbPollutionReductionPct := 100
	if vanilla.DBDirtyRecords > 0 {
		dbPollutionReductionPct = int(math.Round(float64(vanilla.DBDirtyRecords-tokenSafe.DBDirtyRecords) / float64(vanilla.DBDirtyRecords) * 100))
	}

	summaryText := fmt.Sprintf(
		"TokenSafe Backend (Go) achieved an Autonomous Efficiency Ratio (AER) of %.1fx "+
			"with %d%% fewer tokens, %d%% fewer iterations, %d%% zero DB pollution, "+
			"and %d%% fewer regressions compared to Vanilla Gin.",
		aer, tokenSavingsPct, turnReductionPct, dbPollutionReductionPct, regressionReductionPct,
	)

	return benchmark.ComparativeSummary{
		Scenario:                  vanilla.Scenario,
		Timestamp:                 time.Now().UTC().Format(time.RFC3339),
		Vanilla:                   vanilla,
		TokenSafe:                 tokenSafe,
		AutonomousEfficiencyRatio: aer,
		TokenSavingsPct:           tokenSavingsPct,
		TurnReductionPct:          turnReductionPct,
		LatencyReductionPct:       latencyReductionPct,
		RegressionReductionPct:    regressionReductionPct,
		DBPollutionReductionPct:   dbPollutionReductionPct,
		SummaryText:               summaryText,
	}
}
