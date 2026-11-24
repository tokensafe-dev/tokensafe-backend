package transport_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/oumv"
	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/protocol"
	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/transport"
)

func setupTestDispatcher() *transport.Dispatcher {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	rt := oumv.NewRuntime(oumv.Config{
		Name:    "test-backend",
		Version: "1.0.0",
		Handler: mux,
		Runtime: &oumv.RuntimeConfig{
			Enabled:      true,
			Environment:  oumv.EnvDevelopment,
			Capabilities: oumv.CapabilitiesConfig{
				Observe:    true,
				Understand: true,
				Mutate:     true,
				Verify:     true,
			},
		},
	})
	rt.RegisterRoute("GET", "/health", "healthHandler")

	return transport.NewDispatcher(rt)
}

func TestDispatcherHandshake(t *testing.T) {
	d := setupTestDispatcher()

	req := protocol.OumvRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  protocol.MethodHandshake,
	}
	reqBytes, _ := json.Marshal(req)

	resp := d.Handle(context.Background(), reqBytes)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}

	resultMap, ok := resp.Result.(*protocol.OumvHandshakeResult)
	if !ok {
		t.Fatalf("expected *OumvHandshakeResult, got %T", resp.Result)
	}

	if resultMap.RuntimeInfo.Name != "test-backend" {
		t.Errorf("expected runtime name 'test-backend', got %s", resultMap.RuntimeInfo.Name)
	}
}

func TestDispatcherObserveL0(t *testing.T) {
	d := setupTestDispatcher()

	req := protocol.OumvRequest{
		JSONRPC: "2.0",
		ID:      "obs-1",
		Method:  protocol.MethodObserve,
		Params:  json.RawMessage(`{"level":"L0"}`),
	}
	reqBytes, _ := json.Marshal(req)

	resp := d.Handle(context.Background(), reqBytes)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}

	obsResult, ok := resp.Result.(*protocol.ObserveResult)
	if !ok {
		t.Fatalf("expected *ObserveResult, got %T", resp.Result)
	}

	if obsResult.Level != protocol.LevelL0Pulse {
		t.Errorf("expected level L0, got %s", obsResult.Level)
	}
}

func TestHTTPHandler(t *testing.T) {
	d := setupTestDispatcher()
	h := transport.NewHTTPHandler(d)

	body := []byte(`{"jsonrpc":"2.0","id":42,"method":"oumv/handshake"}`)
	req := httptest.NewRequest(http.MethodPost, "/__oumv/rpc", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}

	var resp protocol.OumvResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.Error != nil {
		t.Fatalf("unexpected error in response: %v", resp.Error)
	}
}

func TestStdioServer(t *testing.T) {
	d := setupTestDispatcher()

	in := bytes.NewBufferString("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"oumv/observe\",\"params\":{\"level\":\"L0\"}}\n")
	out := &bytes.Buffer{}

	server := transport.NewStdioServer(d, in, out)
	if err := server.Serve(context.Background()); err != nil {
		t.Fatalf("unexpected error serving stdio: %v", err)
	}

	var resp protocol.OumvResponse
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse output: %v", err)
	}

	if resp.Error != nil {
		t.Fatalf("stdio request failed: %v", resp.Error)
	}
}
