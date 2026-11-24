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

	// 1. Calling Observe must be rejected with ErrPermissionDenied (-32007)
	obsBody, _ := json.Marshal(protocol.NewRequest(1, protocol.MethodObserve, protocol.ObserveParams{Level: "L0"}))
	reqObs := httptest.NewRequest(http.MethodPost, "/__oumv/rpc", bytes.NewReader(obsBody))
	recObs := httptest.NewRecorder()
	app.ServeHTTP(recObs, reqObs)

	var obsResp protocol.OumvResponse
	_ = json.Unmarshal(recObs.Body.Bytes(), &obsResp)
	if obsResp.Error == nil || obsResp.Error.Code != protocol.ErrPermissionDenied {
		t.Fatalf("expected ErrPermissionDenied (-32007) when Observe is disabled, got: %+v", obsResp.Error)
	}

	// 2. Calling GetSource must be rejected with ErrPermissionDenied (-32007)
	srcBody, _ := json.Marshal(protocol.NewRequest(2, protocol.MethodGetSource, protocol.GetSourceParams{Path: "main.go"}))
	reqSrc := httptest.NewRequest(http.MethodPost, "/__oumv/rpc", bytes.NewReader(srcBody))
	recSrc := httptest.NewRecorder()
	app.ServeHTTP(recSrc, reqSrc)

	var srcResp protocol.OumvResponse
	_ = json.Unmarshal(recSrc.Body.Bytes(), &srcResp)
	if srcResp.Error == nil || srcResp.Error.Code != protocol.ErrPermissionDenied {
		t.Fatalf("expected ErrPermissionDenied (-32007) when Mutate is disabled, got: %+v", srcResp.Error)
	}

	// 3. Calling Verify must be rejected with ErrPermissionDenied (-32007)
	verBody, _ := json.Marshal(protocol.NewRequest(3, protocol.MethodVerify, protocol.VerifyParams{Target: "GET /"}))
	reqVer := httptest.NewRequest(http.MethodPost, "/__oumv/rpc", bytes.NewReader(verBody))
	recVer := httptest.NewRecorder()
	app.ServeHTTP(recVer, reqVer)

	var verResp protocol.OumvResponse
	_ = json.Unmarshal(recVer.Body.Bytes(), &verResp)
	if verResp.Error == nil || verResp.Error.Code != protocol.ErrPermissionDenied {
		t.Fatalf("expected ErrPermissionDenied (-32007) when Verify is disabled, got: %+v", verResp.Error)
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
