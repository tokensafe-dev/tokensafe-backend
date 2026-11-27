package adapters_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/adapters/stdhttp"
	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/oumv"
	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/protocol"
)

// wireCase is one captured request/response pair.
type wireCase struct {
	Name     string          `json:"name"`
	Request  json.RawMessage `json:"request"`
	Response json.RawMessage `json:"response"`
}

// wireFile is the committed golden corpus for one method on the backend profile.
type wireFile struct {
	Method      string     `json:"method"`
	Profile     string     `json:"profile"`
	SpecVersion string     `json:"specVersion"`
	Adapter     string     `json:"adapter"`
	FixtureNote string     `json:"note,omitempty"`
	Cases       []wireCase `json:"cases"`
}

// TestWireExamples captures frozen request/response pairs from a real adapter over
// HTTP and writes them to OUMV_EXAMPLES_DIR (never into the repo by itself).
//
//	cd tokensafe-backend/go
//	OUMV_EXAMPLES_DIR=../../agent-oumv/examples/wire/backend go test ./pkg/adapters -run TestWireExamples
//
// The examples are the human-readable counterpart of the schemas: agent-oumv's
// conformance suite validates every captured result against the method schema.
func TestWireExamples(t *testing.T) {
	outDir := os.Getenv("OUMV_EXAMPLES_DIR")
	if outDir == "" {
		t.Skip("OUMV_EXAMPLES_DIR not set; wire examples are captured on demand")
	}

	workspaceDir := t.TempDir()
	markers := workspace(t, workspaceDir, "GET /api/v1/orders")
	rt := newTestRuntime(t, "tokensafe-stdhttp", workspaceDir)

	mux := stdhttp.NewServeMux(rt)
	mux.HandleFunc("GET /api/v1/orders", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"orders":[]}`))
	})
	mux.Mount("/__oumv/rpc")
	server := httptest.NewServer(mux)
	defer server.Close()

	call := func(id, method string, params map[string]interface{}) (json.RawMessage, json.RawMessage, json.RawMessage) {
		t.Helper()
		request := map[string]interface{}{"jsonrpc": "2.0", "id": id, "method": method, "params": params}
		body, err := json.Marshal(request)
		if err != nil {
			t.Fatalf("marshal %s: %v", method, err)
		}
		resp, err := http.Post(server.URL+"/__oumv/rpc", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("post %s: %v", method, err)
		}
		defer func() { _ = resp.Body.Close() }()
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("read %s: %v", method, err)
		}
		reqJSON, _ := json.Marshal(request)
		return reqJSON, raw, nil
	}

	add := func(files map[string]*wireFile, method, name string, params map[string]interface{}) {
		t.Helper()
		id := method + ":" + name
		req, resp, _ := call(id, method, params)
		file, ok := files[method]
		if !ok {
			file = &wireFile{
				Method:      method,
				Profile:     "backend",
				SpecVersion: protocol.CurrentProtocolVersion,
				Adapter:     "backend/stdhttp",
				FixtureNote: "captured from the net/http adapter against a temp workspace; paths are relative to it",
			}
			files[method] = file
		}
		file.Cases = append(file.Cases, wireCase{Name: name, Request: req, Response: resp})
	}

	files := map[string]*wireFile{}

	add(files, "oumv/handshake", "negotiates capabilities", map[string]interface{}{
		"protocolVersion": protocol.CurrentProtocolVersion,
		"clientInfo":      map[string]interface{}{"name": "oumv-examples", "version": "1.0.0"},
	})
	add(files, "oumv/handshake", "narrows requested observe levels", map[string]interface{}{
		"protocolVersion":       protocol.CurrentProtocolVersion,
		"requestedCapabilities": map[string]interface{}{"observe": map[string]interface{}{"levels": []string{"L0", "L1"}}},
	})

	for _, level := range []string{"L0", "L1"} {
		add(files, "oumv/observe", level+" pulse/surface", map[string]interface{}{"level": level})
	}
	add(files, "oumv/observe", "L2 route contract", map[string]interface{}{"level": "L2", "target": markers.L2Target})
	add(files, "oumv/observe", "L3 handler source", map[string]interface{}{"level": "L3", "target": markers.L3Target})

	// verify first: it feeds the L4 trace ring and the causality evidence.
	add(files, "oumv/verify", "executes the live handler", map[string]interface{}{
		"target": "GET /api/v1/orders",
		"action": "conformance-probe",
	})
	add(files, "oumv/observe", "L4 recorded traces", map[string]interface{}{"level": "L4"})
	add(files, "oumv/understand", "impact from route metadata", map[string]interface{}{
		"query": "impact", "target": markers.L2Target,
	})
	add(files, "oumv/understand", "causality from observed traces", map[string]interface{}{
		"query": "causality", "target": "GET /api/v1/orders",
	})
	add(files, "oumv/getSource", "source with OCC token", map[string]interface{}{"path": markers.EditTarget})
	add(files, "oumv/applyEdit", "versioned edit", map[string]interface{}{
		"path":            markers.EditTarget,
		"replacement":     markers.EditContent,
		"expectedVersion": markers.EditVersion,
	})

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatalf("create examples dir: %v", err)
	}
	for method, file := range files {
		name := map[string]string{
			"oumv/handshake":  "handshake.json",
			"oumv/observe":    "observe.json",
			"oumv/understand": "understand.json",
			"oumv/getSource":  "getSource.json",
			"oumv/applyEdit":  "applyEdit.json",
			"oumv/verify":     "verify.json",
		}[method]
		raw, err := json.MarshalIndent(file, "", "  ")
		if err != nil {
			t.Fatalf("marshal %s: %v", method, err)
		}
		if err := os.WriteFile(filepath.Join(outDir, name), append(raw, '\n'), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		t.Logf("%s: %d case(s) → %s", method, len(file.Cases), name)
	}
}

var _ = oumv.DefaultDevelopmentRuntimeConfig
