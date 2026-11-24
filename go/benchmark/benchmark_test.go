package benchmark_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/tokensafe-dev/tokensafe-backend/go/benchmark/oracle"
	"github.com/tokensafe-dev/tokensafe-backend/go/benchmark/runner"
	"github.com/tokensafe-dev/tokensafe-backend/go/benchmark/sandboxes/tokensafe_go"
	"github.com/tokensafe-dev/tokensafe-backend/go/benchmark/sandboxes/vanilla_gin"
)

func TestGroundTruthOracleVanilla(t *testing.T) {
	vanillaApp := vanilla_gin.NewVanillaApp(nil)
	orc := oracle.NewOrderApiOracle()
	report := orc.Evaluate(vanillaApp, false)

	if !report.AllPassed {
		for _, a := range report.Assertions {
			if !a.Passed {
				t.Errorf("Assertion %s failed: %s", a.ID, a.Error)
			}
		}
		t.Fatalf("Vanilla app failed %d/%d assertions", report.PassedCount, report.TotalCount)
	}
}

func TestGroundTruthOracleTokenSafe(t *testing.T) {
	tokenSafeApp := tokensafe_go.NewTokenSafeApp(nil)
	orc := oracle.NewOrderApiOracle()
	report := orc.Evaluate(tokenSafeApp, true)

	if !report.AllPassed {
		for _, a := range report.Assertions {
			if !a.Passed {
				t.Errorf("Assertion %s failed: %s", a.ID, a.Error)
			}
		}
		t.Fatalf("TokenSafe app failed %d/%d assertions", report.PassedCount, report.TotalCount)
	}
}

func TestFullBenchmarkExecution(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "aub-benchmark-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	summary, oracleEval, err := runner.RunBenchmark(tmpDir)
	if err != nil {
		t.Fatalf("RunBenchmark failed: %v", err)
	}

	if !oracleEval.AllPassed || oracleEval.PassedCount != 12 {
		t.Fatalf("oracle evaluation failed: passed %d/12", oracleEval.PassedCount)
	}

	if summary.AutonomousEfficiencyRatio < 15.0 {
		t.Errorf("expected AER >= 15.0x, got %.1fx", summary.AutonomousEfficiencyRatio)
	}
	if summary.TokenSavingsPct < 90 {
		t.Errorf("expected token savings >= 90%%, got %d%%", summary.TokenSavingsPct)
	}
	if summary.DBPollutionReductionPct != 100 {
		t.Errorf("expected 100%% db pollution reduction, got %d%%", summary.DBPollutionReductionPct)
	}

	// Verify generated artifacts
	reportPath := filepath.Join(tmpDir, "BENCHMARK_REPORT.md")
	if _, err := os.Stat(reportPath); err != nil {
		t.Errorf("expected BENCHMARK_REPORT.md to exist: %v", err)
	}
	jsonPath := filepath.Join(tmpDir, "aub-results.json")
	if _, err := os.Stat(jsonPath); err != nil {
		t.Errorf("expected aub-results.json to exist: %v", err)
	}
}
