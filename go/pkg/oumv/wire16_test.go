package oumv

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/protocol"
)

// §8 result versioning (wire 1.6.0): observe/understand carry a workspace
// digest, honour ifVersion with a short unchanged answer, and refuse stale
// tokens with -32010 STALE_RESOURCE_VERSION.
func TestWorkspaceDigestFreshnessHandshake(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "app.go"), []byte("package app\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rt := NewRuntime(Config{
		Name:         "etag-test",
		WorkspaceDir: dir,
		Runtime: &RuntimeConfig{
			Enabled:      true,
			Environment:  EnvDevelopment,
			WorkspaceDir: dir,
			Capabilities: CapabilitiesConfig{Observe: true, Understand: true},
		},
	})

	res, oerr := rt.Observe(context.Background(), &protocol.ObserveParams{Level: protocol.LevelL0Pulse})
	if oerr != nil {
		t.Fatalf("observe: %v", oerr)
	}
	digest := res.WorkspaceVersion
	if !strings.HasPrefix(digest, "sha256:") || len(digest) != 71 {
		t.Fatalf("observe must carry a workspaceVersion token, got %q", digest)
	}

	short, oerr := rt.Observe(context.Background(), &protocol.ObserveParams{Level: protocol.LevelL1Surface, IfVersion: digest})
	if oerr != nil {
		t.Fatalf("observe ifVersion match: %v", oerr)
	}
	if !short.Unchanged || short.WorkspaceVersion != digest {
		t.Fatalf("matching ifVersion must answer the short unchanged envelope, got %+v", short)
	}

	// A touched file invalidates the caller's token with -32010.
	if err := os.WriteFile(filepath.Join(dir, "touched.go"), []byte("package app\n\nvar Touched = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fresh, oerr := rt.Observe(context.Background(), &protocol.ObserveParams{Level: protocol.LevelL0Pulse})
	if oerr != nil {
		t.Fatalf("observe after edit: %v", oerr)
	}
	if fresh.WorkspaceVersion == digest {
		t.Fatal("the digest must change when a workspace file is touched")
	}
	_, staleErr := rt.Observe(context.Background(), &protocol.ObserveParams{Level: protocol.LevelL0Pulse, IfVersion: digest})
	if staleErr == nil || staleErr.Code != protocol.ErrStaleResourceVersion {
		t.Fatalf("stale token must answer -32010, got %v", staleErr)
	}
	staleData, _ := staleErr.Data.(map[string]string)
	if staleData == nil || staleData["reason"] != "stale_version" || staleData["current"] != fresh.WorkspaceVersion {
		t.Fatalf("stale error data mismatch: %v", staleErr.Data)
	}

	und, oerr := rt.Understand(context.Background(), &protocol.UnderstandParams{Query: protocol.QueryCausality, Target: "GET /x"})
	if oerr != nil {
		t.Fatalf("understand: %v", oerr)
	}
	if und.WorkspaceVersion != fresh.WorkspaceVersion {
		t.Fatalf("understand must carry the same workspaceVersion, got %q", und.WorkspaceVersion)
	}
	uShort, oerr := rt.Understand(context.Background(), &protocol.UnderstandParams{Query: protocol.QueryCausality, IfVersion: fresh.WorkspaceVersion})
	if oerr != nil {
		t.Fatalf("understand ifVersion match: %v", oerr)
	}
	if !uShort.Unchanged || uShort.WorkspaceVersion != fresh.WorkspaceVersion {
		t.Fatalf("matching ifVersion must answer the short understand envelope, got %+v", uShort)
	}
}

// §14.2 workspace policy: `permissions.query: false` turns oumv/query into -32007
// and is checked before the missing-engine gate, so a policy denial is never
// misreported as "no engine wired".
func TestQueryPermissionsPolicy(t *testing.T) {
	disabled := Config{
		Name:         "query-policy",
		WorkspaceDir: t.TempDir(),
		Runtime: &RuntimeConfig{
			Enabled:      true,
			Environment:  EnvDevelopment,
			Capabilities: CapabilitiesConfig{Observe: true, Understand: true},
			Permissions:  &PermissionsConfig{QueryDisabled: true},
		},
	}
	rt := NewRuntime(disabled)
	_, oerr := rt.Query(context.Background(), &protocol.QueryParams{SQL: "SELECT 1"})
	if oerr == nil || oerr.Code != protocol.ErrPermissionDenied {
		t.Fatalf("policy denial must answer -32007, got %v", oerr)
	}
	if data, _ := oerr.Data.(map[string]interface{}); data == nil || data["reason"] != "query_disabled" {
		t.Fatalf("policy denial must name reason=query_disabled, got %v", oerr.Data)
	}

	// No policy + no engine: unchanged -32006 (never a silent empty result).
	rt = NewRuntime(Config{
		Name:         "query-no-engine",
		WorkspaceDir: t.TempDir(),
		Runtime: &RuntimeConfig{
			Enabled:      true,
			Environment:  EnvDevelopment,
			Capabilities: CapabilitiesConfig{Observe: true, Understand: true},
		},
	})
	_, oerr = rt.Query(context.Background(), &protocol.QueryParams{SQL: "SELECT 1"})
	if oerr == nil || oerr.Code != protocol.ErrUnsupportedCapability {
		t.Fatalf("missing engine must answer -32006, got %v", oerr)
	}
}

// The env knob mirrors CapabilitiesConfig: TOKENSAFE_OUMV_PERMISSIONS_QUERY=false
// disables query without touching code.
func TestPermissionsQueryEnvKnob(t *testing.T) {
	t.Setenv("TOKENSAFE_OUMV_PERMISSIONS_QUERY", "false")
	cfg, err := ResolveRuntimeConfig(nil)
	if err != nil {
		t.Fatalf("resolve config: %v", err)
	}
	if cfg.Permissions == nil || !cfg.Permissions.QueryDisabled {
		t.Fatalf("TOKENSAFE_OUMV_PERMISSIONS_QUERY=false must disable query, got %+v", cfg.Permissions)
	}

	t.Setenv("TOKENSAFE_OUMV_PERMISSIONS_QUERY", "true")
	cfg, err = ResolveRuntimeConfig(nil)
	if err != nil {
		t.Fatalf("resolve config: %v", err)
	}
	if cfg.Permissions != nil && cfg.Permissions.QueryDisabled {
		t.Fatalf("TOKENSAFE_OUMV_PERMISSIONS_QUERY=true must keep query enabled, got %+v", cfg.Permissions)
	}
}
