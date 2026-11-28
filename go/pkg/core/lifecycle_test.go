package core_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/core"
	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/oumv"
	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/protocol"
)

func cleanEnv(t *testing.T) {
	t.Helper()
	keys := []string{
		"APP_ENV", "ENVIRONMENT", "GO_ENV",
		"TOKENSAFE_OUMV_ENABLED", "TOKENSAFE_OUMV_TRANSPORT_ENABLED",
		"TOKENSAFE_OUMV_HOST", "TOKENSAFE_OUMV_AUTH_TOKEN",
		"TOKENSAFE_OUMV_OBSERVE", "TOKENSAFE_OUMV_MUTATE", "TOKENSAFE_OUMV_VERIFY",
	}
	for _, k := range keys {
		_ = os.Unsetenv(k)
	}
}

func TestDisabledOUMVReturns404AndKeepsMainAppFunctional(t *testing.T) {
	cleanEnv(t)
	// Force production environment where OUMV is disabled by default
	_ = os.Setenv("APP_ENV", "production")

	app := core.New(core.AppConfig{
		Name:    "prod-test-service",
		Version: "1.0.0",
	})

	// Register a main application route
	app.Handle("GET", "/api/v1/ping", func(c *core.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"pong": "true"})
	}, nil, nil)

	// 1. Verify main application route responds normally
	reqPing := httptest.NewRequest(http.MethodGet, "/api/v1/ping", nil)
	recPing := httptest.NewRecorder()
	app.ServeHTTP(recPing, reqPing)

	if recPing.Code != http.StatusOK {
		t.Fatalf("expected main app /api/v1/ping to return 200, got %d", recPing.Code)
	}

	// 2. Verify /__oumv/rpc returns 404 (not mounted, no information leaked)
	rpcBody, _ := json.Marshal(protocol.NewRequest(1, protocol.MethodHandshake, nil))
	reqRPC := httptest.NewRequest(http.MethodPost, "/__oumv/rpc", bytes.NewReader(rpcBody))
	recRPC := httptest.NewRecorder()
	app.ServeHTTP(recRPC, reqRPC)

	if recRPC.Code != http.StatusNotFound {
		t.Fatalf("expected /__oumv/rpc to return 404 when disabled in production, got %d", recRPC.Code)
	}
}

func TestAuthenticationEnforcement(t *testing.T) {
	cleanEnv(t)
	_ = os.Setenv("APP_ENV", "development")
	_ = os.Setenv("TOKENSAFE_OUMV_AUTH_TOKEN", "super-secret-token")

	app := core.New(core.AppConfig{
		Name: "auth-test-service",
	})

	rpcBody, _ := json.Marshal(protocol.NewRequest(1, protocol.MethodHandshake, nil))

	// 1. Missing token -> 401 Unauthorized
	reqNoAuth := httptest.NewRequest(http.MethodPost, "/__oumv/rpc", bytes.NewReader(rpcBody))
	recNoAuth := httptest.NewRecorder()
	app.ServeHTTP(recNoAuth, reqNoAuth)

	if recNoAuth.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without auth token, got %d", recNoAuth.Code)
	}

	// 2. Invalid token -> 401 Unauthorized
	reqBadAuth := httptest.NewRequest(http.MethodPost, "/__oumv/rpc", bytes.NewReader(rpcBody))
	reqBadAuth.Header.Set("Authorization", "Bearer wrong-token")
	recBadAuth := httptest.NewRecorder()
	app.ServeHTTP(recBadAuth, reqBadAuth)

	if recBadAuth.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 with wrong token, got %d", recBadAuth.Code)
	}

	// 3. Valid Authorization Bearer header -> 200 OK
	reqGoodAuth := httptest.NewRequest(http.MethodPost, "/__oumv/rpc", bytes.NewReader(rpcBody))
	reqGoodAuth.Header.Set("Authorization", "Bearer super-secret-token")
	recGoodAuth := httptest.NewRecorder()
	app.ServeHTTP(recGoodAuth, reqGoodAuth)

	if recGoodAuth.Code != http.StatusOK {
		t.Fatalf("expected 200 with valid Bearer token, got %d: %s", recGoodAuth.Code, recGoodAuth.Body.String())
	}

	// 4. Valid X-OUMV-Session-Token header -> 200 OK
	reqSessionToken := httptest.NewRequest(http.MethodPost, "/__oumv/rpc", bytes.NewReader(rpcBody))
	reqSessionToken.Header.Set("X-OUMV-Session-Token", "super-secret-token")
	recSessionToken := httptest.NewRecorder()
	app.ServeHTTP(recSessionToken, reqSessionToken)

	if recSessionToken.Code != http.StatusOK {
		t.Fatalf("expected 200 with valid session token header, got %d", recSessionToken.Code)
	}
}

func TestCapabilityEnforcement(t *testing.T) {
	cleanEnv(t)
	// Explicitly configure OUMV with Observe=false and Mutate=false
	app := core.New(core.AppConfig{
		Name: "caps-test-service",
		OUMV: &oumv.RuntimeConfig{
			Enabled:     true,
			Environment: oumv.EnvDevelopment,
			Transport: oumv.TransportConfig{
				Enabled: true,
				Host:    "127.0.0.1",
				Path:    "/__oumv/rpc",
			},
			Capabilities: oumv.CapabilitiesConfig{
				Observe:    false, // Disabled
				Understand: true,
				Mutate:     false, // Disabled
				Verify:     false, // Disabled
			},
		},
	})

	// §8.5 (revision v1.11): a capability that is off contributes no method at all.
	// Observe/Understand/Mutate/Verify are all disabled except Understand, so every
	// pillar method answers -32601 METHOD_NOT_FOUND, never -32007.
	call := func(id int, method string, params interface{}) *protocol.OumvResponse {
		body, _ := json.Marshal(protocol.NewRequest(id, method, params))
		req := httptest.NewRequest(http.MethodPost, "/__oumv/rpc", bytes.NewReader(body))
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)
		var resp protocol.OumvResponse
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
		return &resp
	}

	for _, tc := range []struct {
		name   string
		method string
		params interface{}
	}{
		{"observe disabled", protocol.MethodObserve, protocol.ObserveParams{Level: "L0"}},
		{"getSource disabled with both read pillars off", protocol.MethodGetSource, protocol.GetSourceParams{Path: "main.go"}},
		{"applyEdit disabled", protocol.MethodApplyEdit, protocol.ApplyEditParams{Path: "main.go"}},
		{"previewEdit disabled", protocol.MethodPreviewEdit, protocol.PreviewEditParams{Path: "main.go"}},
		{"verify disabled", protocol.MethodVerify, protocol.VerifyParams{Target: "GET /"}},
		{"build disabled with verify", protocol.MethodBuild, protocol.BuildParams{Command: "echo ok"}},
	} {
		resp := call(1, tc.method, tc.params)
		if resp.Error == nil || resp.Error.Code != protocol.ErrMethodNotFound {
			t.Fatalf("%s: expected METHOD_NOT_FOUND (-32601), got %+v", tc.name, resp.Error)
		}
	}
}

// TestReadOnlyProfileHasNoWritingMethods pins the shape the IH comment asked for:
// read-only is a property of the registry, not a check inside a handler.
func TestReadOnlyProfileHasNoWritingMethods(t *testing.T) {
	cleanEnv(t)
	app := core.New(core.AppConfig{
		Name: "read-only-service",
		OUMV: &oumv.RuntimeConfig{
			Enabled:     true,
			Environment: oumv.EnvDevelopment,
			Transport: oumv.TransportConfig{
				Enabled: true,
				Host:    "127.0.0.1",
				Path:    "/__oumv/rpc",
			},
			Capabilities: oumv.CapabilitiesConfig{
				Observe:    true,
				Understand: true,
				Mutate:     false,
				Verify:     false,
			},
		},
	})

	call := func(id int, method string, params interface{}) *protocol.OumvResponse {
		body, _ := json.Marshal(protocol.NewRequest(id, method, params))
		req := httptest.NewRequest(http.MethodPost, "/__oumv/rpc", bytes.NewReader(body))
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)
		var resp protocol.OumvResponse
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
		return &resp
	}

	// The handshake says what the registry is: readOnly, no mutate, no verify.
	handshake := call(1, protocol.MethodHandshake, protocol.OumvHandshakeParams{ProtocolVersion: "1.6.0"})
	if handshake.Error != nil {
		t.Fatalf("handshake failed: %+v", handshake.Error)
	}
	raw, _ := json.Marshal(handshake.Result)
	var shape struct {
		Capabilities protocol.OumvCapabilities `json:"capabilities"`
	}
	_ = json.Unmarshal(raw, &shape)
	if !shape.Capabilities.ReadOnly {
		t.Fatalf("read-only deployment must advertise capabilities.readOnly, got %s", raw)
	}
	if shape.Capabilities.Mutate.OCC || shape.Capabilities.Verify.MultiMetric || shape.Capabilities.Verify.Build {
		t.Fatalf("read-only deployment must not advertise mutate/verify, got %s", raw)
	}

	// Reads stay registered: observe answers, and getSource is registered (a missing
	// file is RESOURCE_NOT_FOUND, which only a registered method can answer).
	if resp := call(2, protocol.MethodObserve, protocol.ObserveParams{Level: "L0"}); resp.Error != nil {
		t.Fatalf("observe must stay registered in read-only mode, got %+v", resp.Error)
	}
	src := call(3, protocol.MethodGetSource, protocol.GetSourceParams{Path: "definitely-not-here.go"})
	if src.Error == nil || src.Error.Code == protocol.ErrMethodNotFound {
		t.Fatalf("getSource must stay registered in read-only mode, got %+v", src.Error)
	}

	// Writes are not registered at all: -32601, not -32007.
	for _, tc := range []struct {
		method string
		params interface{}
	}{
		{protocol.MethodApplyEdit, protocol.ApplyEditParams{Path: "main.go"}},
		{protocol.MethodPreviewEdit, protocol.PreviewEditParams{Path: "main.go"}},
		{protocol.MethodVerify, protocol.VerifyParams{Target: "GET /"}},
		{protocol.MethodBuild, protocol.BuildParams{Command: "echo ok"}},
	} {
		if resp := call(4, tc.method, tc.params); resp.Error == nil || resp.Error.Code != protocol.ErrMethodNotFound {
			t.Fatalf("%s must answer -32601 in read-only mode, got %+v", tc.method, resp.Error)
		}
	}
}

func TestSecurityBoundaryAndSensitivePathProtection(t *testing.T) {
	cleanEnv(t)
	// Enable OUMV with Mutate permitted in development
	app := core.New(core.AppConfig{
		Name:         "path-sec-test",
		WorkspaceDir: ".",
		OUMV: &oumv.RuntimeConfig{
			Enabled:      true,
			Environment:  oumv.EnvDevelopment,
			WorkspaceDir: ".",
			Transport: oumv.TransportConfig{
				Enabled: true,
				Host:    "127.0.0.1",
				Path:    "/__oumv/rpc",
			},
			Capabilities: oumv.CapabilitiesConfig{
				Observe: true,
				Mutate:  true,
			},
		},
	})

	// 1. Path traversal attempt (../../etc/passwd)
	traversalBody, _ := json.Marshal(protocol.NewRequest(1, protocol.MethodGetSource, protocol.GetSourceParams{
		Path: "../../etc/passwd",
	}))
	reqTrav := httptest.NewRequest(http.MethodPost, "/__oumv/rpc", bytes.NewReader(traversalBody))
	recTrav := httptest.NewRecorder()
	app.ServeHTTP(recTrav, reqTrav)

	var travResp protocol.OumvResponse
	_ = json.Unmarshal(recTrav.Body.Bytes(), &travResp)
	if travResp.Error == nil || travResp.Error.Code != protocol.ErrSecurityViolation {
		t.Fatalf("expected ErrSecurityViolation (-32003) for path traversal, got: %+v", travResp.Error)
	}

	// 2. Sensitive credential file (.env) access attempt
	envBody, _ := json.Marshal(protocol.NewRequest(2, protocol.MethodGetSource, protocol.GetSourceParams{
		Path: ".env",
	}))
	reqEnv := httptest.NewRequest(http.MethodPost, "/__oumv/rpc", bytes.NewReader(envBody))
	recEnv := httptest.NewRecorder()
	app.ServeHTTP(recEnv, reqEnv)

	var envResp protocol.OumvResponse
	_ = json.Unmarshal(recEnv.Body.Bytes(), &envResp)
	if envResp.Error == nil || envResp.Error.Code != protocol.ErrSecurityViolation {
		t.Fatalf("expected ErrSecurityViolation (-32003) for .env access, got: %+v", envResp.Error)
	}
}
