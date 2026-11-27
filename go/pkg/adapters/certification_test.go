package adapters_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	chiRouter "github.com/go-chi/chi/v5"
	chiAdapter "github.com/tokensafe-dev/tokensafe-backend/go/pkg/adapters/chi"
	ginAdapter "github.com/tokensafe-dev/tokensafe-backend/go/pkg/adapters/gin"
	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/adapters/stdhttp"
	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/conformance"
	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/oumv"
	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/protocol"
)

// httpDispatcher speaks the real wire protocol: every conformance case below
// travels over HTTP to the mounted /__oumv/rpc endpoint, exactly as an agent does.
type httpDispatcher struct{ url string }

func (d httpDispatcher) Dispatch(ctx context.Context, req *protocol.OumvRequest) *protocol.OumvResponse {
	body, err := json.Marshal(req)
	if err != nil {
		return &protocol.OumvResponse{Error: &protocol.OumvError{Code: protocol.ErrInternalError, Message: err.Error()}}
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, d.url, bytes.NewReader(body))
	if err != nil {
		return &protocol.OumvResponse{Error: &protocol.OumvError{Code: protocol.ErrInternalError, Message: err.Error()}}
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return &protocol.OumvResponse{Error: &protocol.OumvError{
			Code: protocol.ErrRuntimeUnavailable, Message: "runtime unreachable: " + err.Error()}}
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return &protocol.OumvResponse{Error: &protocol.OumvError{Code: protocol.ErrInternalError, Message: err.Error()}}
	}
	var out protocol.OumvResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		snippet := string(raw)
		if len(snippet) > 200 {
			snippet = snippet[:200]
		}
		return &protocol.OumvResponse{Error: &protocol.OumvError{
			Code:    protocol.ErrInternalError,
			Message: fmt.Sprintf("decode JSON-RPC response (HTTP %d, body %q): %v", resp.StatusCode, snippet, err),
		}}
	}
	return &out
}

// workspace prepares the per-adapter file fixtures and returns conformance markers.
func workspace(t *testing.T, tmpDir, l2Target string) conformance.Markers {
	t.Helper()
	fixtureFile := "cert_fixture.go"
	editTarget := "cert_edit_target.go"
	missingFile := "cert_missing_file.go"

	if err := os.WriteFile(filepath.Join(tmpDir, fixtureFile), []byte("package cert\n"), 0644); err != nil {
		t.Fatal(err)
	}
	original := []byte("package cert\n\nfunc Value() int {\n\treturn 41\n}\n")
	if err := os.WriteFile(filepath.Join(tmpDir, editTarget), original, 0644); err != nil {
		t.Fatal(err)
	}

	symbolFile := "cert_symbol_target.go"
	symbolOriginal := []byte("package cert\n\nfunc Symbol() int {\n\treturn 41\n}\n")
	symbolAfter := []byte("package cert\n\nfunc Symbol() int {\n\treturn 42\n}\n")
	if err := os.WriteFile(filepath.Join(tmpDir, symbolFile), symbolOriginal, 0644); err != nil {
		t.Fatal(err)
	}
	deleteFile := "cert_delete_target.go"
	deleteOriginal := []byte("package cert\n\nvar DeleteMe = true\n")
	if err := os.WriteFile(filepath.Join(tmpDir, deleteFile), deleteOriginal, 0644); err != nil {
		t.Fatal(err)
	}
	renameSource := "cert_rename_source.go"
	renameTarget := "cert_rename_target.go"
	renameOriginal := []byte("package cert\n\nvar RenameMe = true\n")
	if err := os.WriteFile(filepath.Join(tmpDir, renameSource), renameOriginal, 0644); err != nil {
		t.Fatal(err)
	}

	return conformance.Markers{
		FixtureFile:        fixtureFile,
		MissingFile:        missingFile,
		EditTarget:         editTarget,
		NewFile:            "created_new_file.go",
		EditVersion:        conformance.Version(original),
		EditContent:        "package cert\n\nfunc Value() int {\n\treturn 42\n}\n",
		EditContentVersion: conformance.Version([]byte("package cert\n\nfunc Value() int {\n\treturn 42\n}\n")),
		PatchReplacement:   "package cert\n\nfunc Value() int {\n\treturn 43\n}\n",
		BrokenContent:      "package cert\n\nfunc Broken( {\n",
		SymbolFile:         symbolFile,
		SymbolName:         "Symbol",
		SymbolReplacement:  "func Symbol() int {\n\treturn 42\n}\n",
		SymbolVersion:      conformance.Version(symbolOriginal),
		SymbolFileContent:  string(symbolAfter),
		DeleteFile:         deleteFile,
		DeleteVersion:      conformance.Version(deleteOriginal),
		RenameSource:       renameSource,
		RenameTarget:       renameTarget,
		RenameVersion:      conformance.Version(renameOriginal),
		L2Target:           l2Target,
		L3Target:           editTarget,
		VerifyTarget:       l2Target,
		SandboxTarget:      "POST /__oumv_sandbox_probe",
	}
}

func newTestRuntime(t *testing.T, name, workspaceDir string) *oumv.Runtime {
	t.Helper()
	return oumv.NewRuntime(oumv.Config{
		Name:         name,
		Version:      "cert",
		WorkspaceDir: workspaceDir,
		Runtime: &oumv.RuntimeConfig{
			Enabled:      true,
			Environment:  oumv.EnvDevelopment,
			WorkspaceDir: workspaceDir,
			Transport:    oumv.TransportConfig{Enabled: true, Host: "127.0.0.1", Path: "/__oumv/rpc"},
			Capabilities: oumv.CapabilitiesConfig{
				Observe:    true,
				Understand: true,
				Mutate:     true,
				Verify:     true,
			},
		},
	})
}

// TestAdapterCertification certifies each framework adapter over HTTP and writes
// a machine-readable report (CERT_OUT_DIR) so the level claim is reproducible.
func TestAdapterCertification(t *testing.T) {
	spec, err := conformance.LoadCases(filepath.Join("..", "oumv", "testdata", "conformance-cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	outDir := os.Getenv("CERT_OUT_DIR")
	gin.SetMode(gin.ReleaseMode)

	cases := []struct {
		id        string
		framework string
		build     func(t *testing.T, rt *oumv.Runtime) http.Handler
	}{
		{
			id:        "go-gin",
			framework: "gin",
			build: func(t *testing.T, rt *oumv.Runtime) http.Handler {
				engine := gin.New()
				engine.GET("/api/v1/orders", func(c *gin.Context) {
					c.JSON(http.StatusOK, gin.H{"orders": []string{}})
				})
				engine.POST("/__oumv_sandbox_probe", gin.WrapF(sandboxProbe))
				ginAdapter.Mount(engine, rt)
				// Routes exist before Mount: tracing must come from the wrapper,
				// not from r.Use (gin freezes handler chains at registration).
				return oumv.TraceHandler(rt, engine)
			},
		},
		{
			id:        "go-chi",
			framework: "chi",
			build: func(t *testing.T, rt *oumv.Runtime) http.Handler {
				router := chiRouter.NewRouter()
				router.Get("/api/v1/orders", func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(`{"orders":[]}`))
				})
				router.Post("/__oumv_sandbox_probe", sandboxProbe)
				if err := chiAdapter.Mount(router, rt); err != nil {
					t.Fatalf("mount chi adapter: %v", err)
				}
				return oumv.TraceHandler(rt, router)
			},
		},
		{
			id:        "go-stdhttp",
			framework: "net/http",
			build: func(t *testing.T, rt *oumv.Runtime) http.Handler {
				mux := stdhttp.NewServeMux(rt)
				mux.HandleFunc("GET /api/v1/orders", func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(`{"orders":[]}`))
				})
				mux.HandleFunc("POST /__oumv_sandbox_probe", sandboxProbe)
				mux.Mount("/__oumv/rpc")
				return mux
			},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.id, func(t *testing.T) {
			workspaceDir := t.TempDir()
			rt := newTestRuntime(t, "tokensafe-"+tc.framework, workspaceDir)
			rt.SetDBSandbox(newCertSandbox(t))
			handler := tc.build(t, rt)
			server := httptest.NewServer(handler)
			defer server.Close()

			markers := workspace(t, workspaceDir, "GET /api/v1/orders")
			report := conformance.Run(
				httpDispatcher{url: server.URL + "/__oumv/rpc"},
				spec,
				markers,
			)
			report.AdapterName = tc.id
			report.Framework = tc.framework

			t.Logf("%s: conformance level %d — %d/%d cases over HTTP", tc.id, report.AttainedLevel, report.Passed, report.Total)
			if report.Failed > 0 {
				for _, c := range report.FailedCaseIDs(3) {
					t.Logf("  blocking: %s", c)
				}
			}
			if report.AttainedLevel != 3 {
				t.Errorf("%s attains level %d, expected 3; blocking cases: %v", tc.id, report.AttainedLevel, report.FailedCaseIDs(3))
				for _, c := range report.Cases {
					if c.Status != "passed" {
						t.Logf("  %s (L%d): %s", c.ID, c.Level, c.Error)
					}
				}
			}

			if outDir != "" {
				if err := os.MkdirAll(outDir, 0755); err != nil {
					t.Fatalf("create certification dir: %v", err)
				}
				if err := report.Save(filepath.Join(outDir, tc.id+".json")); err != nil {
					t.Fatalf("save report: %v", err)
				}
			}
		})
	}
}
