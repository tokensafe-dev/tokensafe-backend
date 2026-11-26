package oumv_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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

func TestWriteDescriptorIs0600OutsideRepo(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TOKENSAFE_HOME", home)
	workspace := t.TempDir()

	rt := oumv.NewRuntime(oumv.Config{
		Name:         "go-gin",
		Version:      "0.4.3",
		WorkspaceDir: workspace,
		Runtime:      oumv.DefaultDevelopmentRuntimeConfig(),
	})
	path, err := rt.WriteDescriptor("http://127.0.0.1:8080/__oumv/rpc")
	if err != nil {
		t.Fatalf("write descriptor: %v", err)
	}
	if filepath.Dir(filepath.Dir(path)) != filepath.Join(home, "runtimes") && filepath.Dir(path) != filepath.Join(home, "runtimes") {
		t.Fatalf("descriptor must live under %s/runtimes, got %s", home, path)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("descriptor must be 0600, got %o", info.Mode().Perm())
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]interface{}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"type", "name", "version", "url", "capabilities"} {
		if _, ok := doc[key]; !ok {
			t.Fatalf("descriptor missing %q: %s", key, raw)
		}
	}
	if doc["type"] != "backend" {
		t.Fatalf("expected type=backend, got %v", doc["type"])
	}
}
