package protocol_test

import (
	"encoding/json"
	"testing"

	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/protocol"
)

func TestEnvelopeSerialization(t *testing.T) {
	req := protocol.OumvRequest{
		JSONRPC: "2.0",
		ID:      "req-1",
		Method:  protocol.MethodObserve,
		Params:  json.RawMessage(`{"level":"L0"}`),
	}

	if err := req.Validate(); err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("failed to marshal request: %v", err)
	}

	var parsed protocol.OumvRequest
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal request: %v", err)
	}

	if parsed.JSONRPC != "2.0" || parsed.Method != protocol.MethodObserve {
		t.Errorf("mismatch in deserialized request: %+v", parsed)
	}
}

func TestEnvelopeValidation(t *testing.T) {
	invalidVersion := protocol.OumvRequest{
		JSONRPC: "1.0",
		ID:      1,
		Method:  "test",
	}
	if err := invalidVersion.Validate(); err == nil {
		t.Errorf("expected error for invalid jsonrpc version")
	}

	emptyMethod := protocol.OumvRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "",
	}
	if err := emptyMethod.Validate(); err == nil {
		t.Errorf("expected error for empty method")
	}
}

func TestOumvErrors(t *testing.T) {
	conflictErr := protocol.NewConflictError("", map[string]string{"file": "main.go"})
	if conflictErr.Code != protocol.ErrConflictDetected {
		t.Errorf("expected %d, got %d", protocol.ErrConflictDetected, conflictErr.Code)
	}

	syntaxErr := protocol.NewSyntaxError("syntax error at line 4", nil)
	if syntaxErr.Code != protocol.ErrSyntaxError {
		t.Errorf("expected %d, got %d", protocol.ErrSyntaxError, syntaxErr.Code)
	}

	targetNotFound := protocol.NewTargetNotFoundError("GET /missing")
	if targetNotFound.Code != protocol.ErrTargetNotFound {
		t.Errorf("expected %d, got %d", protocol.ErrTargetNotFound, targetNotFound.Code)
	}
}

func TestDefaultCapabilities(t *testing.T) {
	caps := protocol.DefaultBackendCapabilities()
	if len(caps.Observe.Levels) != 5 {
		t.Errorf("expected 5 observe levels, got %d", len(caps.Observe.Levels))
	}
	if !caps.Mutate.OCC || !caps.Mutate.SyntaxGuard {
		t.Errorf("expected mutate safeguards to be enabled: %+v", caps.Mutate)
	}
	if !caps.Verify.MultiMetric {
		t.Errorf("expected verify multi-metric timings to be enabled: %+v", caps.Verify)
	}
	// Spec §10: never advertise a capability the runtime does not implement.
	if caps.Mutate.TypeGuard || caps.Verify.DBSandbox || caps.Verify.PayloadDiff || caps.Understand.TypeGraph {
		t.Errorf("capabilities over-claim unimplemented features: %+v", caps)
	}
	if len(caps.Observe.Formats) != 1 || caps.Observe.Formats[0] != "json" {
		t.Errorf("observe must advertise json only, got %v", caps.Observe.Formats)
	}
}
