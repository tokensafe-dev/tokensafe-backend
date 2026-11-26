package three_arm_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tokensafe-dev/tokensafe-backend/go/benchmark/three_arm"
)

// TestThreeArmReport runs all three arms and asserts the honesty contract:
// the OUMV arm is measured, the other two are labelled simulated, and the
// oracle passed for every arm.
func TestThreeArmReport(t *testing.T) {
	// The OUMV transport fails closed outside development by design; the measured
	// arm therefore runs with APP_ENV=development, exactly like a local dev server.
	t.Setenv("APP_ENV", "development")

	report, err := three_arm.Run("order-processing-api (in-memory sandbox)")
	if err != nil {
		t.Fatalf("three-arm run failed: %v", err)
	}
	if len(report.Arms) != 3 {
		t.Fatalf("expected 3 arms, got %d", len(report.Arms))
	}

	byID := map[three_arm.ArmID]three_arm.ArmResult{}
	for _, arm := range report.Arms {
		byID[arm.Arm] = arm
		if arm.OracleTotal == 0 || arm.OraclePassed != arm.OracleTotal {
			t.Errorf("arm %s: oracle %d/%d", arm.Arm, arm.OraclePassed, arm.OracleTotal)
		}
	}

	omv, ok := byID[three_arm.ArmOUMV]
	if !ok {
		t.Fatal("oumv arm missing")
	}
	if !omv.Measured {
		t.Error("oumv arm must be measured")
	}
	if omv.PayloadBytes <= 0 {
		t.Error("measured arm recorded no payload bytes")
	}
	if omv.TotalTurns != 5 {
		t.Errorf("expected 5 measured round trips, got %d", omv.TotalTurns)
	}
	for _, id := range []three_arm.ArmID{three_arm.ArmBaseline, three_arm.ArmTokenSafe} {
		arm := byID[id]
		if arm.Measured {
			t.Errorf("%s must be labelled simulated", id)
		}
		if arm.PayloadBytes != 0 {
			t.Errorf("%s must not report measured bytes, got %d", id, arm.PayloadBytes)
		}
		if arm.TokenEstimate == 0 {
			t.Errorf("%s should carry fixture token totals", id)
		}
	}

	markdown := three_arm.RenderMarkdown(report)
	for _, want := range []string{"MEASURED", "SIMULATED", "## Methodology", "bytes / 4", "oracle"} {
		if !strings.Contains(markdown, want) && !strings.Contains(markdown, strings.Title(want)) {
			t.Errorf("report missing %q", want)
		}
	}
	if strings.Count(markdown, "| `oumv` |") != 1 {
		t.Error("report must list the oumv arm exactly once")
	}

	if out := os.Getenv("CERT_OUT_DIR"); out != "" {
		path := filepath.Join(out, "benchmark-three-arm.md")
		if err := os.MkdirAll(out, 0o755); err != nil {
			t.Fatalf("create output dir: %v", err)
		}
		if err := os.WriteFile(path, []byte(markdown), 0o644); err != nil {
			t.Fatalf("write report: %v", err)
		}
	}
}
