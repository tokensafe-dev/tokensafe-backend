// Package three_arm implements an honest three-arm agent benchmark:
//
//	baseline   — plain repository inspection (grep/read/curl), no TokenSafe
//	tokensafe  — the same agent using TokenSafe tools (scoped_search, smart_read_file, ...)
//	oumv       — the same agent using the OUMV protocol (observe/understand/getSource/verify)
//
// The oracle is the real 12-assertion behavioral test suite. Token/turn numbers are
// only reported as MEASURED for the OUMV arm, where the harness actually executes
// JSON-RPC calls and counts the bytes crossing the wire. The other two arms replay
// fixture traces, so their numbers are labelled SIMULATED — a model, not a result.
package three_arm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"time"

	"github.com/tokensafe-dev/tokensafe-backend/go/benchmark"
	"github.com/tokensafe-dev/tokensafe-backend/go/benchmark/oracle"
	"github.com/tokensafe-dev/tokensafe-backend/go/benchmark/sandboxes/tokensafe_go"
	"github.com/tokensafe-dev/tokensafe-backend/go/benchmark/sandboxes/vanilla_gin"
	"github.com/tokensafe-dev/tokensafe-backend/go/benchmark/traces"
)

// ArmID identifies one comparison arm.
type ArmID string

const (
	ArmBaseline  ArmID = "baseline"
	ArmTokenSafe ArmID = "tokensafe"
	ArmOUMV      ArmID = "oumv"
)

// Turn is one agent interaction cycle with the observed payload sizes.
type Turn struct {
	Index      int           `json:"index"`
	Action     string        `json:"action"`
	Request    int           `json:"request_bytes"`
	Response   int           `json:"response_bytes"`
	Calls      []string      `json:"calls,omitempty"`
	Duration   time.Duration `json:"-"`
	DurationMs float64       `json:"duration_ms"`
}

// ArmResult is the per-arm outcome.
type ArmResult struct {
	Arm           ArmID   `json:"arm"`
	Measured      bool    `json:"measured"`
	Description   string  `json:"description"`
	Turns         []Turn  `json:"turns"`
	TotalTurns    int     `json:"total_turns"`
	PayloadBytes  int     `json:"payload_bytes"`
	TokenEstimate int     `json:"token_estimate"`
	OraclePassed  int     `json:"oracle_passed"`
	OracleTotal   int     `json:"oracle_total"`
	WallClockMs   float64 `json:"wall_clock_ms"`
}

// Report is the three-arm comparison artifact.
type Report struct {
	Scenario    string      `json:"scenario"`
	GeneratedAt string      `json:"generated_at"`
	Arms        []ArmResult `json:"arms"`
}

// bytesPerToken is the usual rough ratio for English/code text. Token counts for
// the measured arm are ESTIMATES derived from bytes — stated as such in the report.
const bytesPerToken = 4

func measuredArm(id ArmID, description string, turns []Turn, oraclePassed, oracleTotal int, wall float64) ArmResult {
	payload := 0
	var dur float64
	for _, t := range turns {
		payload += t.Request + t.Response
		dur += t.DurationMs
	}
	return ArmResult{
		Arm:           id,
		Measured:      true,
		Description:   description,
		Turns:         turns,
		TotalTurns:    len(turns),
		PayloadBytes:  payload,
		TokenEstimate: payload / bytesPerToken,
		OraclePassed:  oraclePassed,
		OracleTotal:   oracleTotal,
		WallClockMs:   wall,
	}
}

func simulatedArm(id ArmID, description string, recorded []benchmark.AgentTurnRecord, oraclePassed, oracleTotal int) ArmResult {
	turns := make([]Turn, 0, len(recorded))
	payload := 0
	var dur float64
	for i, rec := range recorded {
		// Fixture traces carry token counts, not wire bytes; keep the byte metric
		// empty for simulated arms instead of inventing one.
		turns = append(turns, Turn{Index: i + 1, Action: fmt.Sprintf("turn %d", i+1)})
		dur += rec.DurationMs
	}
	return ArmResult{
		Arm:          id,
		Measured:     false,
		Description:  description,
		Turns:        turns,
		TotalTurns:   len(recorded),
		PayloadBytes: payload,
		// Token totals come from the fixture traces (simulated), clearly flagged.
		TokenEstimate: simulatedTokens(recorded),
		OraclePassed:  oraclePassed,
		OracleTotal:   oracleTotal,
		WallClockMs:   dur,
	}
}

func simulatedTokens(recorded []benchmark.AgentTurnRecord) int {
	total := 0
	for _, rec := range recorded {
		total += rec.TotalTokens
	}
	return total
}

// Run executes all three arms against fresh sandboxes.
func Run(scenario string) (Report, error) {
	report := Report{
		Scenario:    scenario,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
	}

	// --- oracle results are real for every arm -----------------------------
	orc := oracle.NewOrderApiOracle()
	vanillaApp := vanilla_gin.NewVanillaApp(nil)
	vanillaEval := orc.Evaluate(vanillaApp, false)

	tsApp := tokensafe_go.NewTokenSafeApp(nil)
	tsEval := orc.Evaluate(tsApp, true)

	// --- arm 1: baseline (simulated trace) ---------------------------------
	report.Arms = append(report.Arms, simulatedArm(
		ArmBaseline,
		"agent inspects the repo with grep/read/curl only (fixture trace — simulated)",
		traces.GoldenVanillaTraces,
		vanillaEval.PassedCount,
		vanillaEval.TotalCount,
	))

	// --- arm 2: TokenSafe tools (simulated trace) --------------------------
	report.Arms = append(report.Arms, simulatedArm(
		ArmTokenSafe,
		"agent uses TokenSafe tools (scoped_search/smart_read_file/oumv_gateway) (fixture trace — simulated)",
		traces.GoldenTokenSafeTraces,
		tsEval.PassedCount,
		tsEval.TotalCount,
	))

	// --- arm 3: OUMV protocol (measured) -----------------------------------
	omvTurns, omvWall, err := runOUMVArm(tsApp)
	if err != nil {
		return report, fmt.Errorf("oumv arm: %w", err)
	}
	report.Arms = append(report.Arms, measuredArm(
		ArmOUMV,
		"agent uses OUMV observe/understand/getSource/verify over JSON-RPC (measured bytes)",
		omvTurns,
		tsEval.PassedCount,
		tsEval.TotalCount,
		omvWall,
	))

	return report, nil
}

// runOUMVArm drives the real OUMV endpoint of the instrumented sandbox and measures
// exactly what crosses the wire.
func runOUMVArm(app *tokensafe_go.TokenSafeApp) ([]Turn, float64, error) {
	turns := make([]Turn, 0, 6)
	var wall float64

	// One measured round trip: marshal, dispatch in-process, count bytes.
	call := func(action, method string, params map[string]interface{}) ([]byte, error) {
		paramsJSON, err := json.Marshal(params)
		if err != nil {
			return nil, err
		}
		reqBody, err := json.Marshal(map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      len(turns) + 1,
			"method":  method,
			"params":  json.RawMessage(paramsJSON),
		})
		if err != nil {
			return nil, err
		}

		start := time.Now()
		req := httptest.NewRequest(http.MethodPost, "/__oumv/rpc", bytes.NewReader(reqBody))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)
		elapsed := float64(time.Since(start).Microseconds()) / 1000.0
		wall += elapsed

		if rec.Code >= http.StatusBadRequest {
			return nil, fmt.Errorf("%s returned HTTP %d: %s", method, rec.Code, strings.TrimSpace(rec.Body.String()))
		}
		var envelope struct {
			Error  interface{}     `json:"error"`
			Result json.RawMessage `json:"result"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
			return nil, fmt.Errorf("%s: invalid JSON-RPC response: %w", method, err)
		}
		if envelope.Error != nil {
			return nil, fmt.Errorf("%s: JSON-RPC error %v", method, envelope.Error)
		}

		turns = append(turns, Turn{
			Index:      len(turns) + 1,
			Action:     action,
			Request:    len(reqBody),
			Response:   rec.Body.Len(),
			Calls:      []string{method},
			DurationMs: elapsed,
		})
		return envelope.Result, nil
	}

	if _, err := call("handshake", "oumv/handshake", map[string]interface{}{"protocolVersion": "1.1.0"}); err != nil {
		return nil, wall, err
	}

	// L1 gives the agent the surface; the first endpoint becomes the target for the
	// remaining steps, so the harness works against any sandbox shape.
	surface, err := call("surface", "oumv/observe", map[string]interface{}{"level": "L1"})
	if err != nil {
		return nil, wall, err
	}
	var observed struct {
		Data struct {
			Endpoints []struct {
				Method string `json:"method"`
				Path   string `json:"path"`
			} `json:"endpoints"`
		} `json:"data"`
	}
	if err := json.Unmarshal(surface, &observed); err != nil {
		return nil, wall, fmt.Errorf("parse L1 surface: %w", err)
	}
	if len(observed.Data.Endpoints) == 0 {
		return nil, wall, fmt.Errorf("sandbox exposes no endpoints to observe")
	}
	first := observed.Data.Endpoints[0]
	target := fmt.Sprintf("%s %s", strings.ToUpper(first.Method), first.Path)

	if _, err := call("contract", "oumv/observe", map[string]interface{}{"level": "L2", "target": target}); err != nil {
		return nil, wall, err
	}
	if _, err := call("impact", "oumv/understand", map[string]interface{}{"query": "impact", "target": target}); err != nil {
		return nil, wall, err
	}
	if _, err := call("verify", "oumv/verify", map[string]interface{}{"target": target, "action": "benchmark-probe"}); err != nil {
		return nil, wall, err
	}
	return turns, wall, nil
}

// RenderMarkdown produces the report shown to humans, with measured vs simulated
// numbers separated instead of blended into one claim.
func RenderMarkdown(report Report) string {
	var b strings.Builder
	b.WriteString("# Backend Agent Benchmark — three arms\n\n")
	b.WriteString(fmt.Sprintf("> Scenario: `%s`  \n> Generated: `%s`\n\n", report.Scenario, report.GeneratedAt))
	b.WriteString("## Methodology\n\n")
	b.WriteString("- **Oracle**: the 12-assertion behavioral suite really executes every arm's HTTP handler (`httptest`).\n")
	b.WriteString("- **Measured arm (`oumv`)**: JSON-RPC requests/responses are executed and their **bytes are counted**; token figures are `bytes / 4` and are labelled estimates.\n")
	b.WriteString("- **Simulated arms (`baseline`, `tokensafe`)**: token/turn numbers come from fixture traces in `benchmark/traces` — a model of the workflow, not a measured run.\n")
	b.WriteString("- **Turns** are counted per executed round trip (measured arm) or per fixture turn (simulated arms).\n\n")

	b.WriteString("## Results\n\n")
	b.WriteString("| Arm | Source | Turns | Payload bytes | Tokens | Oracle | Wall clock |\n")
	b.WriteString("| :--- | :--- | ---: | ---: | ---: | ---: | ---: |\n")

	arms := append([]ArmResult(nil), report.Arms...)
	sort.SliceStable(arms, func(i, j int) bool { return arms[i].Arm < arms[j].Arm })
	for _, a := range arms {
		source := "SIMULATED"
		bytes := "—"
		if a.Measured {
			source = "MEASURED"
			bytes = fmt.Sprintf("%d", a.PayloadBytes)
		}
		tokens := fmt.Sprintf("%d", a.TokenEstimate)
		if !a.Measured {
			tokens = fmt.Sprintf("%d (trace)", a.TokenEstimate)
		}
		b.WriteString(fmt.Sprintf("| `%s` | %s | %d | %s | %s | %d/%d | %.1f ms |\n",
			a.Arm, source, a.TotalTurns, bytes, tokens, a.OraclePassed, a.OracleTotal, a.WallClockMs))
	}

	b.WriteString("\n## Per-turn detail (measured arm)\n\n")
	for _, a := range report.Arms {
		if !a.Measured {
			continue
		}
		b.WriteString("| # | Action | Request B | Response B | ms |\n| ---: | :--- | ---: | ---: | ---: |\n")
		for _, t := range a.Turns {
			b.WriteString(fmt.Sprintf("| %d | %s | %d | %d | %.2f |\n", t.Index, t.Action, t.Request, t.Response, t.DurationMs))
		}
	}

	b.WriteString("\n> Only the `oumv` row is a measurement. Rows marked SIMULATED must not be quoted as benchmark results.\n")
	return b.String()
}
