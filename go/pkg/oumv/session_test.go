package oumv_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/oumv"
	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/protocol"
	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/transport"
)

func handshakeSession(t *testing.T, rt *oumv.Runtime) *protocol.OumvHandshakeResult {
	t.Helper()
	res, oumvErr := rt.Handshake(context.Background(), &protocol.OumvHandshakeParams{})
	if oumvErr != nil {
		t.Fatalf("handshake failed: %v", oumvErr)
	}
	return res
}

func TestHandshakeIssuesSession(t *testing.T) {
	rt := oumv.NewRuntime(oumv.Config{
		Name:    "sessions",
		Runtime: oumv.DefaultDevelopmentRuntimeConfig(),
	})
	res := handshakeSession(t, rt)

	if res.SessionID == "" {
		t.Fatal("handshake must issue a sessionId (spec §3)")
	}
	if res.TTLSeconds <= 0 {
		t.Fatalf("expected a positive ttlSeconds, got %d", res.TTLSeconds)
	}
	if res.Profile != "backend" {
		t.Fatalf("expected profile=backend, got %q", res.Profile)
	}
	if !rt.ValidateSession(res.SessionID) {
		t.Fatal("issued session must validate")
	}
}

func TestUnknownSessionIsRejectedWithReason(t *testing.T) {
	rt := oumv.NewRuntime(oumv.Config{Name: "sessions", Runtime: oumv.DefaultDevelopmentRuntimeConfig()})
	dispatcher := transport.NewDispatcher(rt)

	params, _ := json.Marshal(map[string]interface{}{"level": "L1", "sessionId": "sess-deadbeef"})
	resp := dispatcher.Dispatch(context.Background(), &protocol.OumvRequest{
		JSONRPC: "2.0", ID: "1", Method: "oumv/observe", Params: params,
	})
	if resp.Error == nil {
		t.Fatal("unknown session must be rejected")
	}
	if resp.Error.Code != protocol.ErrValidationFailed {
		t.Fatalf("expected -32011, got %d", resp.Error.Code)
	}
	data, _ := resp.Error.Data.(map[string]string)
	if data["reason"] != "session_expired" {
		t.Fatalf("expected data.reason=session_expired, got %v", resp.Error.Data)
	}
}

func TestStatelessCallsWithoutSessionAreAllowed(t *testing.T) {
	rt := oumv.NewRuntime(oumv.Config{Name: "sessions", Runtime: oumv.DefaultDevelopmentRuntimeConfig()})
	dispatcher := transport.NewDispatcher(rt)

	params, _ := json.Marshal(map[string]interface{}{"level": "L1"})
	resp := dispatcher.Dispatch(context.Background(), &protocol.OumvRequest{
		JSONRPC: "2.0", ID: "1", Method: "oumv/observe", Params: params,
	})
	if resp.Error != nil {
		t.Fatalf("stateless observe must be allowed, got %v", resp.Error)
	}
}

func TestDisconnectEndsSession(t *testing.T) {
	rt := oumv.NewRuntime(oumv.Config{Name: "sessions", Runtime: oumv.DefaultDevelopmentRuntimeConfig()})
	dispatcher := transport.NewDispatcher(rt)
	session := handshakeSession(t, rt).SessionID

	params, _ := json.Marshal(map[string]interface{}{"sessionId": session})
	resp := dispatcher.Dispatch(context.Background(), &protocol.OumvRequest{
		JSONRPC: "2.0", ID: "2", Method: "oumv/disconnect", Params: params,
	})
	if resp.Error != nil {
		t.Fatalf("disconnect failed: %v", resp.Error)
	}
	if rt.ValidateSession(session) {
		t.Fatal("session must be invalid after disconnect")
	}
}

func TestSessionExpiry(t *testing.T) {
	registry := oumv.NewSessionRegistry(10 * time.Millisecond)
	id := registry.Create()
	if !registry.Validate(id) {
		t.Fatal("fresh session must validate")
	}
	time.Sleep(15 * time.Millisecond)
	if registry.Validate(id) {
		t.Fatal("expired session must not validate")
	}
}

func TestWriteDescriptorMergesProfileRegistry(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TOKENSAFE_HOME", home)
	workspace := t.TempDir()

	rt := oumv.NewRuntime(oumv.Config{
		Name:         "go-gin",
		Version:      "0.4.3",
		WorkspaceDir: workspace,
		Runtime:      oumv.DefaultDevelopmentRuntimeConfig(),
	})
	rt.SetTransports("http", "sse", "ws")

	path, err := rt.WriteDescriptor("http://127.0.0.1:8080/__oumv/rpc")
	if err != nil {
		t.Fatalf("write descriptor: %v", err)
	}
	if filepath.Dir(path) != filepath.Join(home, "runtimes") {
		t.Fatalf("descriptor must live under %s/runtimes, got %s", home, path)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("descriptor must be 0600, got %o", info.Mode().Perm())
	}

	var doc map[string]json.RawMessage
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	var backend map[string]interface{}
	if err := json.Unmarshal(doc["backend"], &backend); err != nil {
		t.Fatalf("registry has no backend entry: %s", raw)
	}
	for _, key := range []string{"type", "name", "version", "url", "capabilities"} {
		if _, ok := backend[key]; !ok {
			t.Fatalf("backend entry missing %q: %s", key, raw)
		}
	}
	if backend["type"] != "backend" {
		t.Fatalf("expected type=backend, got %v", backend["type"])
	}
	caps := backend["capabilities"].(map[string]interface{})
	if transports, _ := caps["transports"].([]interface{}); len(transports) != 3 {
		t.Fatalf("expected mounted transports http/sse/ws, got %v", caps["transports"])
	}

	// A frontend entry written by the Vite plugin must survive a backend write and
	// a backend delete: two runtimes share one workspace registry.
	doc["frontend"] = json.RawMessage(`{"type":"frontend","name":"react","version":"19.0.0","url":"http://localhost:5173/__oumv/rpc","capabilities":{"observeLevels":["L0","L1","L2"],"transports":["http","sse","ws"]}}`)
	merged, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, merged, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := rt.WriteDescriptor("http://127.0.0.1:8080/__oumv/rpc"); err != nil {
		t.Fatalf("rewrite descriptor: %v", err)
	}
	raw, _ = os.ReadFile(path)
	if !strings.Contains(string(raw), `"frontend"`) {
		t.Fatalf("rewrite dropped the frontend entry: %s", raw)
	}

	if err := rt.DeleteDescriptor(); err != nil {
		t.Fatalf("delete descriptor: %v", err)
	}
	raw, _ = os.ReadFile(path)
	if strings.Contains(string(raw), `"backend"`) {
		t.Fatalf("delete must remove the backend entry: %s", raw)
	}
	if !strings.Contains(string(raw), `"frontend"`) {
		t.Fatalf("delete must keep the frontend entry: %s", raw)
	}
}
func TestHandshakeNarrowsRequestedCapabilities(t *testing.T) {
	rt := oumv.NewRuntime(oumv.Config{
		Name:         "negotiation",
		Version:      "1.0.0",
		WorkspaceDir: t.TempDir(),
		Runtime:      oumv.DefaultDevelopmentRuntimeConfig(),
	})

	full, err := rt.Handshake(context.Background(), &protocol.OumvHandshakeParams{ProtocolVersion: "1.1.0"})
	if err != nil {
		t.Fatalf("handshake without request: %v", err)
	}
	if len(full.Capabilities.Observe.Levels) != 5 {
		t.Fatalf("expected all levels without a request, got %v", full.Capabilities.Observe.Levels)
	}

	narrowed, err := rt.Handshake(context.Background(), &protocol.OumvHandshakeParams{
		ProtocolVersion: "1.1.0",
		RequestedCapabilities: &protocol.OumvCapabilities{
			Observe: protocol.ObserveCapability{Levels: []string{"L0", "L1"}, Formats: []string{"json", "yaml"}},
		},
	})
	if err != nil {
		t.Fatalf("handshake with request: %v", err)
	}
	got := narrowed.Capabilities.Observe.Levels
	if len(got) != 2 || got[0] != "L0" || got[1] != "L1" {
		t.Fatalf("expected [L0 L1], got %v", got)
	}
	formats := narrowed.Capabilities.Observe.Formats
	if len(formats) != 1 || formats[0] != "json" {
		t.Fatalf("expected [json] (yaml is not implemented), got %v", formats)
	}

	// Asking only for YAML yields an empty set — never silently widened.
	empty, err := rt.Handshake(context.Background(), &protocol.OumvHandshakeParams{
		ProtocolVersion: "1.1.0",
		RequestedCapabilities: &protocol.OumvCapabilities{
			Observe: protocol.ObserveCapability{Formats: []string{"yaml"}},
		},
	})
	if err != nil {
		t.Fatalf("handshake with yaml request: %v", err)
	}
	if len(empty.Capabilities.Observe.Formats) != 0 {
		t.Fatalf("expected no formats, got %v", empty.Capabilities.Observe.Formats)
	}
	if len(empty.Capabilities.Observe.Levels) != 5 {
		t.Fatalf("levels must stay untouched, got %v", empty.Capabilities.Observe.Levels)
	}
}

func TestApplyEditCreateMode(t *testing.T) {
	workspace := t.TempDir()
	rt := oumv.NewRuntime(oumv.Config{Name: "create", Version: "1", WorkspaceDir: workspace, Runtime: oumv.DefaultDevelopmentRuntimeConfig()})
	ctx := context.Background()

	created, applyErr := rt.ApplyEdit(ctx, &protocol.ApplyEditParams{
		Path:        "pkg/new_file.go",
		Replacement: "package pkg\n\nfunc NewValue() int {\n\treturn 1\n}\n",
		Mode:        "create",
	})
	if applyErr != nil {
		t.Fatalf("create: %v", applyErr)
	}
	if !created.Created || created.Status != "APPLIED" || created.NewVersion == "" {
		t.Fatalf("unexpected create result: %+v", created)
	}
	if _, err := os.Stat(filepath.Join(workspace, "pkg", "new_file.go")); err != nil {
		t.Fatalf("file was not created: %v", err)
	}

	// A second create must never overwrite.
	_, conflictErr := rt.ApplyEdit(ctx, &protocol.ApplyEditParams{
		Path:        "pkg/new_file.go",
		Replacement: "package pkg\n",
		Mode:        "create",
	})
	if conflictErr == nil || conflictErr.Code != protocol.ErrConflictDetected {
		t.Fatalf("expected CONFLICT_DETECTED, got %+v", conflictErr)
	}
	if data, ok := conflictErr.Data.(map[string]string); ok {
		if data["reason"] != "file_exists" {
			t.Fatalf("expected reason=file_exists, got %v", data)
		}
	}

	// expectedVersion is forbidden for create.
	_, validationErr := rt.ApplyEdit(ctx, &protocol.ApplyEditParams{
		Path:            "pkg/other.go",
		Replacement:     "package pkg\n",
		Mode:            "create",
		ExpectedVersion: "sha256:" + strings.Repeat("0", 64),
	})
	if validationErr == nil || validationErr.Code != protocol.ErrValidationFailed {
		t.Fatalf("expected VALIDATION_FAILED, got %+v", validationErr)
	}

	// Syntax still pre-flights a brand-new file.
	_, syntaxErr := rt.ApplyEdit(ctx, &protocol.ApplyEditParams{
		Path:        "pkg/broken.go",
		Replacement: "package pkg\n\nfunc Broken( {\n",
		Mode:        "create",
	})
	if syntaxErr == nil || syntaxErr.Code != protocol.ErrSyntaxError {
		t.Fatalf("expected SYNTAX_ERROR, got %+v", syntaxErr)
	}
}

// TestApplyEditGuardOrderSyntaxBeforeVersion pins §4.3: the syntax pre-flight is
// guard 3 and the OCC match is guard 5, so a request that violates both must
// answer SYNTAX_ERROR — in the single-edit path and in every batch item.
func TestApplyEditGuardOrderSyntaxBeforeVersion(t *testing.T) {
	workspace := t.TempDir()
	original := "package main\n\nfunc main() {}\n"
	if err := os.WriteFile(filepath.Join(workspace, "main.go"), []byte(original), 0o644); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	rt := oumv.NewRuntime(oumv.Config{Name: "guards", Version: "1", WorkspaceDir: workspace, Runtime: oumv.DefaultDevelopmentRuntimeConfig()})
	ctx := context.Background()
	stale := "sha256:" + strings.Repeat("0", 64)
	broken := "package main\n\nfunc Broken( {\n"

	_, singleErr := rt.ApplyEdit(ctx, &protocol.ApplyEditParams{
		Path: "main.go", Mode: "replace", Replacement: broken, ExpectedVersion: stale,
	})
	if singleErr == nil || singleErr.Code != protocol.ErrSyntaxError {
		t.Fatalf("single: expected SYNTAX_ERROR before the version guard, got %+v", singleErr)
	}

	_, batchErr := rt.ApplyEdit(ctx, &protocol.ApplyEditParams{
		Edits: []protocol.ApplyEditBatchItem{
			{Path: "main.go", Mode: "replace", Replacement: broken, ExpectedVersion: stale},
		},
	})
	if batchErr == nil || batchErr.Code != protocol.ErrSyntaxError {
		t.Fatalf("batch: expected SYNTAX_ERROR before the version guard, got %+v", batchErr)
	}

	if after, _ := os.ReadFile(filepath.Join(workspace, "main.go")); string(after) != original {
		t.Fatalf("guards must not write: file changed to %q", string(after))
	}
}
