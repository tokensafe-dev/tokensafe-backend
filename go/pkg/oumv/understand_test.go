package oumv

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/protocol"
)

func newTestRuntime(t *testing.T, handler http.Handler) *Runtime {
	t.Helper()
	tmpDir := t.TempDir()
	return NewRuntime(Config{
		Name:         "test-service",
		Version:      "test",
		WorkspaceDir: tmpDir,
		Handler:      handler,
		Runtime: &RuntimeConfig{
			Enabled:      true,
			Environment:  EnvDevelopment,
			WorkspaceDir: tmpDir,
			Capabilities: CapabilitiesConfig{
				Observe:    true,
				Understand: true,
				Mutate:     true,
				Verify:     true,
			},
		},
	})
}

func TestUnderstandCausalityNoTraceIsHonest(t *testing.T) {
	rt := newTestRuntime(t, http.NotFoundHandler())

	res, err := rt.Understand(context.Background(), &protocol.UnderstandParams{
		Query:  protocol.QueryCausality,
		Target: "GET /api/ghost",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	c := res.Causality
	if c == nil {
		t.Fatal("causality result missing")
	}
	// Must NOT fabricate a canned 200/OK answer
	if c.Observed {
		t.Fatal("causality must not claim observed without a trace")
	}
	if c.StatusCode != 0 {
		t.Fatalf("expected zero statusCode for unobserved target, got %d", c.StatusCode)
	}
	if c.Note == "" {
		t.Fatal("expected actionable note for unobserved target")
	}
	if c.Evidence == nil || c.Evidence.Confidence != "unknown" {
		t.Fatalf("expected unknown confidence evidence, got %+v", c.Evidence)
	}
}

func TestUnderstandCausalityFromRealTrace(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	rt := newTestRuntime(t, handler)

	// Real request execution through the runtime handler (like Verify does)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/orders", nil)
	rt.handler.ServeHTTP(rec, req)
	rt.RecordTrace("GET", "/api/orders", "/api/orders", "ordersHandler", rec.Code, 1.25, "")

	res, err := rt.Understand(context.Background(), &protocol.UnderstandParams{
		Query:  protocol.QueryCausality,
		Target: "GET /api/orders",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	c := res.Causality
	if !c.Observed {
		t.Fatal("causality must be observed from the recorded trace")
	}
	if c.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected real status 500, got %d", c.StatusCode)
	}
	if c.LatencyMs <= 0 {
		t.Fatalf("expected positive latency, got %f", c.LatencyMs)
	}
	if c.TraceCount != 1 {
		t.Fatalf("expected 1 matching trace, got %d", c.TraceCount)
	}
	if c.Evidence == nil || c.Evidence.Provenance != "runtime" || c.Evidence.Confidence != "confirmed" {
		t.Fatalf("expected runtime/confirmed evidence, got %+v", c.Evidence)
	}
	// Trace chain must include the handler that actually executed
	found := false
	for _, step := range c.TraceChain {
		if step == "ordersHandler" {
			found = true
		}
	}
	if !found {
		t.Fatalf("trace chain %v must contain the executing handler", c.TraceChain)
	}
}

func TestUnderstandCausalityLatestTraceWins(t *testing.T) {
	rt := newTestRuntime(t, http.NotFoundHandler())
	rt.RecordTrace("GET", "/api/orders", "/api/orders", "h1", 200, 1.0, "")
	rt.RecordTrace("GET", "/api/orders", "/api/orders", "h2", 503, 2.0, "upstream down")

	res, _ := rt.Understand(context.Background(), &protocol.UnderstandParams{
		Query:  protocol.QueryCausality,
		Target: "/api/orders",
	})
	c := res.Causality
	if c.StatusCode != 503 || c.TraceCount != 2 {
		t.Fatalf("expected latest trace (503) with 2 matches, got status=%d count=%d", c.StatusCode, c.TraceCount)
	}
	if c.LastError != "upstream down" {
		t.Fatalf("expected recorded error, got %q", c.LastError)
	}
}

func TestVerifyFeedsCausalityTraces(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})
	rt := newTestRuntime(t, handler)

	_, err := rt.Verify(context.Background(), &protocol.VerifyParams{Target: "POST /api/items"})
	if err != nil {
		t.Fatalf("verify failed: %v", err)
	}

	res, _ := rt.Understand(context.Background(), &protocol.UnderstandParams{
		Query:  protocol.QueryCausality,
		Target: "POST /api/items",
	})
	if !res.Causality.Observed {
		t.Fatal("verify dispatch must leave a trace for causality")
	}
	if res.Causality.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 from verify trace, got %d", res.Causality.StatusCode)
	}
}

func TestUnderstandImpactHonestRisk(t *testing.T) {
	rt := newTestRuntime(t, http.NotFoundHandler())
	rt.RegisterRoute("POST", "/api/orders", "createOrder", "LoggingMiddleware")

	res, err := rt.Understand(context.Background(), &protocol.UnderstandParams{
		Query:  protocol.QueryImpact,
		Target: "/api/orders",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	imp := res.Impact
	if imp == nil {
		t.Fatal("impact result missing")
	}
	// Fabricated "LOW" risk without evidence is forbidden
	if imp.BreakingRisk != "UNKNOWN" {
		t.Fatalf("expected UNKNOWN breaking risk, got %q", imp.BreakingRisk)
	}
	if !contains(imp.AffectedSymbols, "createOrder") || !contains(imp.AffectedSymbols, "LoggingMiddleware") {
		t.Fatalf("expected handler and middleware in affected symbols, got %v", imp.AffectedSymbols)
	}
	if imp.Evidence == nil || imp.Evidence.Provenance != "static" {
		t.Fatalf("expected static provenance, got %+v", imp.Evidence)
	}
}

func TestTraceRingBufferBounded(t *testing.T) {
	rt := newTestRuntime(t, http.NotFoundHandler())
	for i := 0; i < maxRequestTraces+50; i++ {
		rt.RecordTrace("GET", "/api/x", "/api/x", "h", 200, 1.0, "")
	}
	rt.mu.RLock()
	size := len(rt.traces)
	rt.mu.RUnlock()
	if size != maxRequestTraces {
		t.Fatalf("trace buffer must stay bounded at %d, got %d", maxRequestTraces, size)
	}
}

func TestRecordTraceIncrementsErrorCounter(t *testing.T) {
	rt := newTestRuntime(t, http.NotFoundHandler())
	rt.RecordTrace("GET", "/a", "/a", "h", 200, 1, "")
	rt.RecordTrace("GET", "/b", "/b", "h", 500, 1, "boom")
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	if atomicLoadErrCount(rt) != 1 {
		t.Fatal("5xx trace must increment the error counter")
	}
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

func atomicLoadErrCount(rt *Runtime) int64 {
	return atomic.LoadInt64(&rt.errCount1m)
}
