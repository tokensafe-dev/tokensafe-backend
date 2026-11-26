package transport_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/oumv"
	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/protocol"
	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/transport"
)

func newBatchHandler(t *testing.T) http.Handler {
	t.Helper()
	rt := oumv.NewRuntime(oumv.Config{Name: "batch", Runtime: oumv.DefaultDevelopmentRuntimeConfig()})
	return transport.NewHTTPHandler(transport.NewDispatcher(rt))
}

func post(t *testing.T, handler http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/__oumv/rpc", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestBatchReturnsOrderedArray(t *testing.T) {
	handler := newBatchHandler(t)
	body := `[
	  {"jsonrpc":"2.0","id":1,"method":"oumv/handshake","params":{}},
	  {"jsonrpc":"2.0","id":2,"method":"oumv/observe","params":{"level":"L1"}}
	]`
	rec := post(t, handler, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var responses []protocol.OumvResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &responses); err != nil {
		t.Fatalf("expected a JSON array, got %s: %v", rec.Body.String(), err)
	}
	if len(responses) != 2 {
		t.Fatalf("expected 2 responses, got %d", len(responses))
	}
	if responses[0].Error != nil || responses[1].Error != nil {
		t.Fatalf("unexpected errors: %+v", responses)
	}
	if responses[0].ID != float64(1) || responses[1].ID != float64(2) {
		t.Fatalf("responses out of order: %+v", responses)
	}
}

func TestBatchNotificationGetsNoEntry(t *testing.T) {
	handler := newBatchHandler(t)
	body := `[
	  {"jsonrpc":"2.0","method":"oumv/observe","params":{"level":"L0"}},
	  {"jsonrpc":"2.0","id":7,"method":"oumv/observe","params":{"level":"L1"}}
	]`
	rec := post(t, handler, body)
	var responses []protocol.OumvResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &responses); err != nil {
		t.Fatalf("expected a JSON array: %s", rec.Body.String())
	}
	if len(responses) != 1 {
		t.Fatalf("notifications must not produce entries, got %d", len(responses))
	}
	if responses[0].ID != float64(7) {
		t.Fatalf("unexpected response id: %+v", responses[0])
	}
}
