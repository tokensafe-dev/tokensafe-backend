package oumv_test

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/tokensafe-dev/tokensafe-backend/pkg/oumv"
	"github.com/tokensafe-dev/tokensafe-backend/pkg/protocol"
)

func TestRuntimePillars(t *testing.T) {
	tmpDir := t.TempDir()
	sampleGoFile := filepath.Join(tmpDir, "sample.go")
	initialContent := "package main\n\nfunc Hello() string {\n\treturn \"hello\"\n}\n"
	if err := os.WriteFile(sampleGoFile, []byte(initialContent), 0644); err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/ping", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ping":"pong"}`))
	})

	rt := oumv.NewRuntime(oumv.Config{
		Name:         "test-rt",
		Version:      "0.2.0",
		WorkspaceDir: tmpDir,
		Handler:      mux,
	})
	rt.RegisterRoute("GET", "/api/v1/ping", "PingHandler")

	ctx := context.Background()

	// 1. Handshake
	hsResult, oumvErr := rt.Handshake(ctx, &protocol.OumvHandshakeParams{})
	if oumvErr != nil {
		t.Fatalf("handshake failed: %v", oumvErr)
	}
	if hsResult.RuntimeInfo.Name != "test-rt" {
		t.Errorf("expected runtime 'test-rt', got %s", hsResult.RuntimeInfo.Name)
	}

	// 2. Observe L0 & L1
	obsL0, oumvErr := rt.Observe(ctx, &protocol.ObserveParams{Level: protocol.LevelL0Pulse})
	if oumvErr != nil {
		t.Fatalf("observe L0 failed: %v", oumvErr)
	}
	if obsL0.Level != protocol.LevelL0Pulse {
		t.Errorf("expected L0 level, got %s", obsL0.Level)
	}

	obsL1, oumvErr := rt.Observe(ctx, &protocol.ObserveParams{Level: protocol.LevelL1Surface})
	if oumvErr != nil {
		t.Fatalf("observe L1 failed: %v", oumvErr)
	}
	surfaceData, ok := obsL1.Data.(protocol.L1SurfaceData)
	if !ok || len(surfaceData.Endpoints) != 1 {
		t.Errorf("expected 1 endpoint in L1, got %+v", obsL1.Data)
	}

	// 3. GetSource (Pillar 3A)
	srcResult, oumvErr := rt.GetSource(ctx, &protocol.GetSourceParams{
		Path:   "sample.go",
		Symbol: "Hello",
	})
	if oumvErr != nil {
		t.Fatalf("getSource failed: %v", oumvErr)
	}
	if srcResult.Version == "" {
		t.Fatalf("expected non-empty version token")
	}

	// 4. ApplyEdit with OCC conflict (Pillar 3B)
	_, oumvErr = rt.ApplyEdit(ctx, &protocol.ApplyEditParams{
		Path:            "sample.go",
		Replacement:     "package main\n",
		ExpectedVersion: "sha256:invalidhash",
	})
	if oumvErr == nil || oumvErr.Code != protocol.ErrConflictDetected {
		t.Errorf("expected ErrConflictDetected, got %v", oumvErr)
	}

	// 5. ApplyEdit with Syntax Error (Pre-Flight Guard)
	_, oumvErr = rt.ApplyEdit(ctx, &protocol.ApplyEditParams{
		Path:            "sample.go",
		Replacement:     "package main\n\nfunc Broken( {\n", // Invalid Go syntax
		ExpectedVersion: srcResult.Version,
	})
	if oumvErr == nil || oumvErr.Code != protocol.ErrSyntaxError {
		t.Errorf("expected ErrSyntaxError from pre-flight guard, got %v", oumvErr)
	}

	// 6. ApplyEdit valid replacement
	validReplacement := "package main\n\nfunc Hello() string {\n\treturn \"world\"\n}\n"
	editResult, oumvErr := rt.ApplyEdit(ctx, &protocol.ApplyEditParams{
		Path:            "sample.go",
		Replacement:     validReplacement,
		ExpectedVersion: srcResult.Version,
	})
	if oumvErr != nil {
		t.Fatalf("valid applyEdit failed: %v", oumvErr)
	}
	if !editResult.Success {
		t.Errorf("expected edit success")
	}

	// 7. Verify (Pillar 4)
	verifyResult, oumvErr := rt.Verify(ctx, &protocol.VerifyParams{
		Target: "GET /api/v1/ping",
		Action: "invoke",
	})
	if oumvErr != nil {
		t.Fatalf("verify failed: %v", oumvErr)
	}
	if !verifyResult.Success || verifyResult.StatusCode != http.StatusOK {
		t.Errorf("expected verify success with status 200, got %d", verifyResult.StatusCode)
	}
}
