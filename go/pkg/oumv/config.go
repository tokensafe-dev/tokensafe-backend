package oumv

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
)

// Environment represents the deployment tier.
type Environment string

const (
	EnvDevelopment Environment = "development"
	EnvTest        Environment = "test"
	EnvStaging     Environment = "staging"
	EnvProduction  Environment = "production"
)

// DetectEnvironment inspects APP_ENV, ENVIRONMENT, or GO_ENV.
// If unset, empty, or unknown, it strictly defaults to EnvProduction (fail-closed).
func DetectEnvironment() Environment {
	raw := os.Getenv("APP_ENV")
	if raw == "" {
		raw = os.Getenv("ENVIRONMENT")
	}
	if raw == "" {
		raw = os.Getenv("GO_ENV")
	}

	normalized := strings.ToLower(strings.TrimSpace(raw))
	switch normalized {
	case "development", "dev", "local":
		return EnvDevelopment
	case "test", "testing":
		return EnvTest
	case "staging", "stage":
		return EnvStaging
	case "production", "prod":
		return EnvProduction
	default:
		// Default safe: any unrecognized, missing, or empty value is treated as production
		return EnvProduction
	}
}

// CapabilitiesConfig defines which OUMV functional pillars are enabled.
type CapabilitiesConfig struct {
	Observe    bool `json:"observe"`
	Understand bool `json:"understand"`
	Mutate     bool `json:"mutate"`
	Verify     bool `json:"verify"`
}

// PermissionsConfig holds workspace-policy switches that can disable otherwise
// advertised capabilities (spec §14.2): QueryDisabled turns oumv/query into
// -32007 PERMISSION_DENIED (reason query_disabled) even with an engine wired.
type PermissionsConfig struct {
	QueryDisabled bool `json:"queryDisabled"`
}

// SecurityConfig holds authentication and CORS origin controls.
type SecurityConfig struct {
	AuthenticationRequired bool     `json:"authenticationRequired"`
	AuthToken              string   `json:"authToken,omitempty"`
	AllowedOrigins         []string `json:"allowedOrigins"`
}

// TransportConfig defines network listener and route exposure settings.
type TransportConfig struct {
	Enabled bool   `json:"enabled"`
	Host    string `json:"host"`
	Port    int    `json:"port"`
	Path    string `json:"path"`
}

// RuntimeConfig represents the complete, resolved configuration of the OUMV runtime.
type RuntimeConfig struct {
	Enabled      bool               `json:"enabled"`
	Environment  Environment        `json:"environment"`
	Name         string             `json:"name"`
	Version      string             `json:"version"`
	WorkspaceDir string             `json:"workspaceDir"`
	Transport    TransportConfig    `json:"transport"`
	Security     SecurityConfig     `json:"security"`
	Capabilities CapabilitiesConfig `json:"capabilities"`
	// BuildCommand is the §14.3 default build/test command (shell-split, run in
	// the workspace root). Empty = oumv/build answers `skipped` with a reason.
	BuildCommand string `json:"buildCommand,omitempty"`
	// Permissions is the §14.2 workspace-policy layer (nil = everything allowed).
	Permissions *PermissionsConfig `json:"permissions,omitempty"`
}

// IsLoopbackHost returns true if the host string is a local loopback address.
func IsLoopbackHost(host string) bool {
	h := strings.TrimSpace(host)
	if h == "" {
		return false
	}
	if h == "localhost" || h == "127.0.0.1" || h == "::1" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// ResolveRuntimeConfig merges programmatic options with environment variables.
// Follows strict production fail-closed defaults.
func ResolveRuntimeConfig(base *RuntimeConfig) (RuntimeConfig, error) {
	env := DetectEnvironment()

	cfg := RuntimeConfig{
		Environment:  env,
		Name:         "tokensafe-backend",
		Version:      "0.9.1",
		WorkspaceDir: ".",
		Transport: TransportConfig{
			Enabled: false,
			Host:    "127.0.0.1",
			Port:    0,
			Path:    "/__oumv/rpc",
		},
		Security: SecurityConfig{
			AuthenticationRequired: false,
			AllowedOrigins:         make([]string, 0),
		},
		Capabilities: CapabilitiesConfig{
			Observe:    false,
			Understand: false,
			Mutate:     false,
			Verify:     false,
		},
	}

	// 1. Initial defaults based on detected environment
	switch env {
	case EnvDevelopment:
		cfg.Enabled = true
		cfg.Transport.Enabled = true
		cfg.Capabilities.Observe = true
		cfg.Capabilities.Understand = true
		cfg.Capabilities.Mutate = true
		cfg.Capabilities.Verify = true
	case EnvTest:
		// In Test environment, disabled unless explicitly configured
		cfg.Enabled = false
		cfg.Transport.Enabled = false
	default: // Staging or Production
		cfg.Enabled = false
		cfg.Transport.Enabled = false
	}

	// 2. Apply programmatic base config overrides if provided
	if base != nil {
		if base.Environment != "" {
			cfg.Environment = base.Environment
		}
		if base.Name != "" {
			cfg.Name = base.Name
		}
		if base.Version != "" {
			cfg.Version = base.Version
		}
		if base.WorkspaceDir != "" {
			cfg.WorkspaceDir = base.WorkspaceDir
		}
		if base.Transport.Host != "" {
			cfg.Transport.Host = base.Transport.Host
		}
		if base.Transport.Path != "" {
			cfg.Transport.Path = base.Transport.Path
		}
		if base.Transport.Port != 0 {
			cfg.Transport.Port = base.Transport.Port
		}
		if base.Enabled {
			cfg.Enabled = true
			cfg.Transport.Enabled = base.Transport.Enabled
		}
		if base.Security.AuthToken != "" {
			cfg.Security.AuthToken = base.Security.AuthToken
			cfg.Security.AuthenticationRequired = true
		}
		if base.Security.AuthenticationRequired {
			cfg.Security.AuthenticationRequired = true
		}
		if len(base.Security.AllowedOrigins) > 0 {
			cfg.Security.AllowedOrigins = append([]string{}, base.Security.AllowedOrigins...)
		}
		// If base explicitly provided non-zero capabilities, adopt them
		if base.Capabilities != (CapabilitiesConfig{}) {
			cfg.Capabilities = base.Capabilities
		}
		// §14.2 policy is copied, not aliased, so a caller mutating its own
		// struct cannot flip a live runtime's policy behind its back.
		if base.Permissions != nil {
			permissions := *base.Permissions
			cfg.Permissions = &permissions
		}
	}

	// Override with TOKENSAFE_OUMV_ENABLED if set
	if envVal, exists := os.LookupEnv("TOKENSAFE_OUMV_ENABLED"); exists {
		parsed, err := strconv.ParseBool(strings.TrimSpace(envVal))
		if err == nil {
			cfg.Enabled = parsed
			cfg.Transport.Enabled = parsed
		} else {
			// Unparseable value -> fail safe (disabled)
			cfg.Enabled = false
			cfg.Transport.Enabled = false
		}
	}

	// Override transport enabled if TOKENSAFE_OUMV_TRANSPORT_ENABLED is set
	if envVal, exists := os.LookupEnv("TOKENSAFE_OUMV_TRANSPORT_ENABLED"); exists {
		parsed, err := strconv.ParseBool(strings.TrimSpace(envVal))
		if err == nil {
			cfg.Transport.Enabled = parsed
		}
	}

	// Host & Path
	if envHost := os.Getenv("TOKENSAFE_OUMV_HOST"); envHost != "" {
		cfg.Transport.Host = strings.TrimSpace(envHost)
	}
	if envPort := os.Getenv("TOKENSAFE_OUMV_PORT"); envPort != "" {
		if p, err := strconv.Atoi(strings.TrimSpace(envPort)); err == nil {
			cfg.Transport.Port = p
		}
	}
	if envPath := os.Getenv("TOKENSAFE_OUMV_PATH"); envPath != "" {
		cfg.Transport.Path = strings.TrimSpace(envPath)
	}

	// Security
	if envToken := os.Getenv("TOKENSAFE_OUMV_AUTH_TOKEN"); envToken != "" {
		cfg.Security.AuthToken = strings.TrimSpace(envToken)
		cfg.Security.AuthenticationRequired = true
	}
	if envOrigins := os.Getenv("TOKENSAFE_OUMV_ALLOWED_ORIGINS"); envOrigins != "" {
		for _, o := range strings.Split(envOrigins, ",") {
			trimmed := strings.TrimSpace(o)
			if trimmed != "" {
				cfg.Security.AllowedOrigins = append(cfg.Security.AllowedOrigins, trimmed)
			}
		}
	}

	// Capabilities overrides from env
	if val, exists := os.LookupEnv("TOKENSAFE_OUMV_OBSERVE"); exists {
		if b, err := strconv.ParseBool(strings.TrimSpace(val)); err == nil {
			cfg.Capabilities.Observe = b
		}
	}
	if val, exists := os.LookupEnv("TOKENSAFE_OUMV_UNDERSTAND"); exists {
		if b, err := strconv.ParseBool(strings.TrimSpace(val)); err == nil {
			cfg.Capabilities.Understand = b
		}
	}
	if val, exists := os.LookupEnv("TOKENSAFE_OUMV_MUTATE"); exists {
		if b, err := strconv.ParseBool(strings.TrimSpace(val)); err == nil {
			cfg.Capabilities.Mutate = b
		}
	}
	if val, exists := os.LookupEnv("TOKENSAFE_OUMV_VERIFY"); exists {
		if b, err := strconv.ParseBool(strings.TrimSpace(val)); err == nil {
			cfg.Capabilities.Verify = b
		}
	}

	// §14.2 workspace policy: TOKENSAFE_OUMV_PERMISSIONS_QUERY=false disables oumv/query.
	if val, exists := os.LookupEnv("TOKENSAFE_OUMV_PERMISSIONS_QUERY"); exists {
		if b, err := strconv.ParseBool(strings.TrimSpace(val)); err == nil && !b {
			if cfg.Permissions == nil {
				cfg.Permissions = &PermissionsConfig{}
			}
			cfg.Permissions.QueryDisabled = true
		}
	}

	// In Staging or Production:
	// Mutate can NEVER be enabled by generic Enabled=true alone.
	// It requires explicit TOKENSAFE_OUMV_MUTATE=true.
	if (cfg.Environment == EnvProduction || cfg.Environment == EnvStaging) && cfg.Enabled {
		if mutateEnv, exists := os.LookupEnv("TOKENSAFE_OUMV_MUTATE"); exists {
			parsed, _ := strconv.ParseBool(strings.TrimSpace(mutateEnv))
			cfg.Capabilities.Mutate = parsed
		} else if base == nil || !base.Capabilities.Mutate {
			cfg.Capabilities.Mutate = false
		}
	}

	// Validate resulting configuration
	if err := cfg.Validate(); err != nil {
		return cfg, err
	}

	return cfg, nil
}

// Validate ensures the configuration satisfies all security rules.
// If OUMV is disabled, no security rules are violated and it returns nil.
func (c *RuntimeConfig) Validate() error {
	if !c.Enabled {
		return nil
	}

	if c.Transport.Enabled {
		// Rule 1: Non-loopback host must require authentication
		if !IsLoopbackHost(c.Transport.Host) {
			if strings.TrimSpace(c.Security.AuthToken) == "" {
				return fmt.Errorf("insecure OUMV configuration: non-loopback host %q requires authentication (TOKENSAFE_OUMV_AUTH_TOKEN must be set)", c.Transport.Host)
			}
		}

		// Rule 2: Wildcard CORS origin is forbidden when authentication is required
		if c.Security.AuthenticationRequired || strings.TrimSpace(c.Security.AuthToken) != "" {
			for _, o := range c.Security.AllowedOrigins {
				if o == "*" {
					return fmt.Errorf("insecure OUMV configuration: wildcard CORS origin '*' is strictly prohibited when authentication is required")
				}
			}
		}

		// Rule 3: Production specific constraints
		if c.Environment == EnvProduction {
			// In production, wildcard CORS is never allowed
			for _, o := range c.Security.AllowedOrigins {
				if o == "*" {
					return fmt.Errorf("insecure OUMV configuration: wildcard CORS origin '*' is strictly prohibited in production")
				}
			}

			// In production, mutations require explicit authentication
			if c.Capabilities.Mutate {
				if strings.TrimSpace(c.Security.AuthToken) == "" {
					return fmt.Errorf("insecure OUMV configuration: mutations cannot be enabled in production without authentication token")
				}
			}
		}
	}

	return nil
}

// DefaultDevelopmentRuntimeConfig builds the zero-config runtime used by the
// one-call AutoMount helpers (and therefore by `tokensafe inject`): mutations
// enabled with guardrails, transport on loopback, workspace = current directory.
// Outside a development environment the resolved config still fails closed, so a
// stray AutoMount in production cannot open a mutation endpoint.
func DefaultDevelopmentRuntimeConfig() *RuntimeConfig {
	workspace, err := os.Getwd()
	if err != nil {
		workspace = "."
	}
	return &RuntimeConfig{
		Enabled:      true,
		Environment:  DetectEnvironment(),
		Name:         "tokensafe-backend",
		Version:      "0.9.1",
		WorkspaceDir: workspace,
		Transport: TransportConfig{
			Enabled: true,
			Host:    "127.0.0.1",
			Port:    0,
			Path:    "/__oumv/rpc",
		},
		Capabilities: CapabilitiesConfig{
			Observe:    true,
			Understand: true,
			Mutate:     true,
			Verify:     true,
		},
	}
}
