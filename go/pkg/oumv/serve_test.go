package oumv_test

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/oumv"
)

// Serve is the Go listen hook: the descriptor must carry the port the kernel
// actually assigned, exist before the first request, and disappear on shutdown.
func TestServeWritesDescriptorWithBoundPort(t *testing.T) {
	t.Setenv("TOKENSAFE_HOME", t.TempDir())
	dir := t.TempDir()
	rt := oumv.NewRuntime(oumv.Config{
		Name:         "serve-test",
		Version:      "0.4.4",
		WorkspaceDir: dir,
		Runtime: &oumv.RuntimeConfig{
			Enabled:      true,
			Environment:  oumv.EnvDevelopment,
			WorkspaceDir: dir,
			Transport:    oumv.TransportConfig{Enabled: true, Host: "127.0.0.1", Path: "/__oumv/rpc"},
			Capabilities: oumv.CapabilitiesConfig{Observe: true, Understand: true, Mutate: true, Verify: true},
		},
	})
	rt.SetTransports("http", "sse")

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	served := make(chan error, 1)
	go func() {
		served <- oumv.Serve(rt, ln, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}))
	}()

	path := oumv.DescriptorPath(dir)
	registry := waitForRegistry(t, path)

	var backend oumv.Descriptor
	if err := json.Unmarshal(registry["backend"], &backend); err != nil {
		t.Fatalf("descriptor is not a Descriptor: %v", err)
	}
	wantURL := fmt.Sprintf("http://127.0.0.1:%d/__oumv/rpc", port)
	if backend.URL != wantURL {
		t.Errorf("descriptor url = %q, want %q", backend.URL, wantURL)
	}
	if backend.Type != "backend" || backend.Workspace != realPath(t, dir) {
		t.Errorf("descriptor profile/workspace = %q/%q, want backend/%q", backend.Type, backend.Workspace, realPath(t, dir))
	}
	if len(backend.Capabilities.Transports) != 2 {
		t.Errorf("transports = %v, want http+sse", backend.Capabilities.Transports)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("descriptor mode = %v (err %v), want 0600", info.Mode().Perm(), err)
	}

	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/", port))
	if err != nil {
		t.Fatalf("request against the served listener: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("status = %d, want 204", resp.StatusCode)
	}

	if err := ln.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	<-served
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("descriptor must be removed on shutdown, stat err = %v", err)
	}
}

func realPath(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("resolve %s: %v", path, err)
	}
	return resolved
}

func waitForRegistry(t *testing.T, path string) map[string]json.RawMessage {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		raw, err := os.ReadFile(path)
		if err == nil {
			registry := map[string]json.RawMessage{}
			if err := json.Unmarshal(raw, &registry); err != nil {
				t.Fatalf("registry is not JSON: %v", err)
			}
			return registry
		}
		if time.Now().After(deadline) {
			t.Fatalf("descriptor was not written before the first request: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
