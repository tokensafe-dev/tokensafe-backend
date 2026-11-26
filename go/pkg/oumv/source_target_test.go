package oumv_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/oumv"
	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/protocol"
)

// L3 accepts the §4 endpoint target forms, not just path#symbol: a route that was
// registered with its source location resolves to the handler body.
func TestObserveL3ResolvesEndpointTarget(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "app.go")
	source := strings.Join([]string{
		"package app",                  // 1
		"",                             // 2
		"import \"net/http\"",          // 3
		"",                             // 4
		"func NewApp() http.Handler {", // 5
		"\tmux := http.NewServeMux()",  // 6
		"\tmux.HandleFunc(\"/api/products\", func(w http.ResponseWriter, r *http.Request) {", // 7
		"\t\tw.WriteHeader(http.StatusOK)",                                                   // 8
		"\t})",                                                                               // 9
		"\treturn mux",                                                                       // 10
		"}",                                                                                  // 11
		"",                                                                                   // 12
	}, "\n")
	if err := os.WriteFile(file, []byte(source), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	rt := oumv.NewRuntime(oumv.Config{
		Name:         "l3-target",
		WorkspaceDir: dir,
		Runtime: &oumv.RuntimeConfig{
			Enabled:      true,
			Environment:  oumv.EnvDevelopment,
			WorkspaceDir: dir,
			Capabilities: oumv.CapabilitiesConfig{Observe: true, Mutate: true, Understand: true, Verify: true},
		},
	})
	rt.RegisterRouteSource("POST", "/api/products", "app.NewApp.func1", file, 7)

	result, oumvErr := rt.Observe(context.Background(), &protocol.ObserveParams{
		Level:  protocol.LevelL3Source,
		Target: "POST /api/products",
	})
	if oumvErr != nil {
		t.Fatalf("observe L3 by endpoint failed: %v", oumvErr)
	}
	data, ok := result.Data.(protocol.L3SourceData)
	if !ok {
		t.Fatalf("unexpected data type %T", result.Data)
	}
	if data.Path != "app.go" {
		t.Errorf("path must stay workspace-relative, got %q", data.Path)
	}
	if !strings.Contains(data.Source, "HandleFunc") || !strings.Contains(data.Source, "StatusOK") {
		t.Errorf("expected the handler closure, got %q", data.Source)
	}
	if !strings.HasPrefix(data.Version, "sha256:") {
		t.Errorf("expected an OCC version, got %q", data.Version)
	}
	if data.StartLine != 7 || data.EndLine != 9 {
		t.Errorf("expected the closure range 7-9, got %d-%d", data.StartLine, data.EndLine)
	}

	// An endpoint without a source location is a parameter error, not a silent
	// full-file fallback.
	rt.RegisterRoute("GET", "/api/unknown", "app.Unknown")
	if _, oumvErr := rt.Observe(context.Background(), &protocol.ObserveParams{
		Level:  protocol.LevelL3Source,
		Target: "GET /api/unknown",
	}); oumvErr == nil {
		t.Fatal("expected an error for a route without a source location")
	}
}
