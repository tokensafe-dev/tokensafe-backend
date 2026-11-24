package oumv_test

import (
	"os"
	"testing"

	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/oumv"
)

func resetEnv(t *testing.T) {
	t.Helper()
	keys := []string{
		"APP_ENV", "ENVIRONMENT", "GO_ENV",
		"TOKENSAFE_OUMV_ENABLED", "TOKENSAFE_OUMV_TRANSPORT_ENABLED",
		"TOKENSAFE_OUMV_HOST", "TOKENSAFE_OUMV_PORT", "TOKENSAFE_OUMV_PATH",
		"TOKENSAFE_OUMV_AUTH_TOKEN", "TOKENSAFE_OUMV_ALLOWED_ORIGINS",
		"TOKENSAFE_OUMV_OBSERVE", "TOKENSAFE_OUMV_UNDERSTAND",
		"TOKENSAFE_OUMV_MUTATE", "TOKENSAFE_OUMV_VERIFY",
	}
	for _, k := range keys {
		_ = os.Unsetenv(k)
	}
}

func TestEnvironmentDetection(t *testing.T) {
	tests := []struct {
		name     string
		envKey   string
		envVal   string
		expected oumv.Environment
	}{
		{"development lower", "APP_ENV", "development", oumv.EnvDevelopment},
		{"dev short", "ENVIRONMENT", "dev", oumv.EnvDevelopment},
		{"local alias", "GO_ENV", "local", oumv.EnvDevelopment},
		{"test", "APP_ENV", "test", oumv.EnvTest},
		{"staging", "APP_ENV", "staging", oumv.EnvStaging},
		{"production", "APP_ENV", "production", oumv.EnvProduction},
		{"prod short", "APP_ENV", "prod", oumv.EnvProduction},
		{"missing environment defaults to production", "", "", oumv.EnvProduction},
		{"unknown environment defaults to production", "APP_ENV", "custom-cluster-9", oumv.EnvProduction},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resetEnv(t)
			if tc.envKey != "" {
				_ = os.Setenv(tc.envKey, tc.envVal)
			}
			detected := oumv.DetectEnvironment()
			if detected != tc.expected {
				t.Fatalf("expected environment %q, got %q", tc.expected, detected)
			}
		})
	}
}

func TestDefaultLifecycleByEnvironment(t *testing.T) {
	tests := []struct {
		name            string
		appEnv          string
		expectedEnabled bool
	}{
		{"development is enabled by default", "development", true},
		{"test is disabled by default", "test", false},
		{"staging is disabled by default", "staging", false},
		{"production is disabled by default", "production", false},
		{"unset env is disabled by default", "", false},
		{"unknown env is disabled by default", "random_k8s_env", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resetEnv(t)
			if tc.appEnv != "" {
				_ = os.Setenv("APP_ENV", tc.appEnv)
			}
			cfg, err := oumv.ResolveRuntimeConfig(nil)
			if err != nil {
				t.Fatalf("unexpected resolution error: %v", err)
			}
			if cfg.Enabled != tc.expectedEnabled {
				t.Fatalf("expected Enabled=%v for %q, got %v", tc.expectedEnabled, tc.appEnv, cfg.Enabled)
			}
		})
	}
}

func TestExplicitOverrideToggles(t *testing.T) {
	resetEnv(t)
	// In production, explicitly enabling OUMV on loopback
	_ = os.Setenv("APP_ENV", "production")
	_ = os.Setenv("TOKENSAFE_OUMV_ENABLED", "true")
	_ = os.Setenv("TOKENSAFE_OUMV_HOST", "127.0.0.1")

	cfg, err := oumv.ResolveRuntimeConfig(nil)
	if err != nil {
		t.Fatalf("failed to resolve: %v", err)
	}
	if !cfg.Enabled {
		t.Fatal("expected OUMV to be enabled via explicit TOKENSAFE_OUMV_ENABLED=true")
	}

	// In production, Mutate must be disabled even if OUMV is enabled, unless explicitly set
	if cfg.Capabilities.Mutate {
		t.Fatal("expected Mutate to remain disabled in production by default")
	}

	// In development, explicitly disabling OUMV
	resetEnv(t)
	_ = os.Setenv("APP_ENV", "development")
	_ = os.Setenv("TOKENSAFE_OUMV_ENABLED", "false")

	cfgDev, err := oumv.ResolveRuntimeConfig(nil)
	if err != nil {
		t.Fatalf("failed to resolve: %v", err)
	}
	if cfgDev.Enabled {
		t.Fatal("expected OUMV to be disabled when TOKENSAFE_OUMV_ENABLED=false in development")
	}
}

func TestSecurityValidationFailClosed(t *testing.T) {
	// 1. Non-loopback host without authentication must fail validation
	resetEnv(t)
	_ = os.Setenv("APP_ENV", "development")
	_ = os.Setenv("TOKENSAFE_OUMV_HOST", "0.0.0.0")

	_, err := oumv.ResolveRuntimeConfig(nil)
	if err == nil {
		t.Fatal("expected non-loopback 0.0.0.0 without auth to fail validation")
	}

	// 2. Non-loopback host WITH authentication must pass validation
	resetEnv(t)
	_ = os.Setenv("APP_ENV", "development")
	_ = os.Setenv("TOKENSAFE_OUMV_HOST", "0.0.0.0")
	_ = os.Setenv("TOKENSAFE_OUMV_AUTH_TOKEN", "secret-agent-token-1234")

	cfg, err := oumv.ResolveRuntimeConfig(nil)
	if err != nil {
		t.Fatalf("expected non-loopback with auth to pass, got: %v", err)
	}
	if !cfg.Enabled || cfg.Security.AuthToken != "secret-agent-token-1234" {
		t.Fatal("expected config to be valid and enabled")
	}

	// 3. Authenticated mode with wildcard CORS must fail validation
	resetEnv(t)
	_ = os.Setenv("APP_ENV", "development")
	_ = os.Setenv("TOKENSAFE_OUMV_AUTH_TOKEN", "secret-token")
	_ = os.Setenv("TOKENSAFE_OUMV_ALLOWED_ORIGINS", "*")

	_, errWildcard := oumv.ResolveRuntimeConfig(nil)
	if errWildcard == nil {
		t.Fatal("expected wildcard CORS with auth to fail validation")
	}

	// 4. Production mutate without auth token must fail validation
	resetEnv(t)
	_ = os.Setenv("APP_ENV", "production")
	_ = os.Setenv("TOKENSAFE_OUMV_ENABLED", "true")
	_ = os.Setenv("TOKENSAFE_OUMV_MUTATE", "true")

	_, errProdMutate := oumv.ResolveRuntimeConfig(nil)
	if errProdMutate == nil {
		t.Fatal("expected production mutate without auth to fail validation")
	}

	// 5. Production mutate with auth token must pass validation
	resetEnv(t)
	_ = os.Setenv("APP_ENV", "production")
	_ = os.Setenv("TOKENSAFE_OUMV_ENABLED", "true")
	_ = os.Setenv("TOKENSAFE_OUMV_MUTATE", "true")
	_ = os.Setenv("TOKENSAFE_OUMV_AUTH_TOKEN", "prod-mutation-key-5678")

	cfgProd, errProd := oumv.ResolveRuntimeConfig(nil)
	if errProd != nil {
		t.Fatalf("expected production mutate with auth token to pass, got: %v", errProd)
	}
	if !cfgProd.Capabilities.Mutate {
		t.Fatal("expected mutate capability to be enabled")
	}
}
