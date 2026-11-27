package protocol

// ClientInfo describes the connected AI agent.
type ClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// RuntimeInfo describes the executing Go runtime adapter.
type RuntimeInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ObserveCapability defines supported observe levels and formats.
type ObserveCapability struct {
	Levels  []string `json:"levels"`            // ["L0", "L1", "L2", "L3", "L4"]
	Formats []string `json:"formats,omitempty"` // ["json", "yaml"]
}

// UnderstandCapability defines supported reasoning features. Field names are the
// §3.1 capability names; false values are omitted so the handshake never advertises
// (or even mentions) something the runtime does not implement.
type UnderstandCapability struct {
	Causality      bool `json:"causality"`
	SignalsGraph   bool `json:"signalsGraph,omitempty"`
	ImpactAnalysis bool `json:"impactAnalysis,omitempty"`
	TypeGraph      bool `json:"typeGraph,omitempty"`
}

// MutateCapability defines supported mutation safeguards.
type MutateCapability struct {
	OCC         bool `json:"occ"`
	SyntaxGuard bool `json:"syntaxGuard"`
	// MultiFile advertises the atomic batch envelope (wire 1.5.0).
	MultiFile bool `json:"multiFile,omitempty"`
	// Not implemented: kept off the wire entirely (schema forbids unknown fields).
	TypeGuard bool `json:"-"`
}

// SessionCapability defines the session-lifecycle features (wire 1.5.0).
type SessionCapability struct {
	// TTLSeconds is true when the handshake honours a client-requested
	// `sessionTtlSeconds` and echoes the granted lifetime as `ttlSeconds`.
	TTLSeconds bool `json:"ttlSeconds,omitempty"`
}

// VerifyCapability defines supported verification features.
type VerifyCapability struct {
	MultiMetric bool `json:"multiMetric"`
	SurfaceDiff bool `json:"surfaceDiff,omitempty"`
	// SandboxRollback is advertised only when a database sandbox is configured.
	SandboxRollback bool `json:"sandboxRollback,omitempty"`
	DBSandbox       bool `json:"-"`
	PayloadDiff     bool `json:"-"`
}

// OumvCapabilities aggregates all functional pillars supported by client/runtime.
type OumvCapabilities struct {
	Observe    ObserveCapability    `json:"observe"`
	Understand UnderstandCapability `json:"understand"`
	Mutate     MutateCapability     `json:"mutate"`
	Verify     VerifyCapability     `json:"verify"`
	// Transports lists the bindings this runtime actually mounted (spec §2.3/§2.4).
	Transports []string `json:"transports,omitempty"`
	// Sessions advertises the TTL negotiation the handshake honours (wire 1.5.0).
	Sessions *SessionCapability `json:"sessions,omitempty"`
}

// OumvHandshakeParams is sent by the AI Agent during initialization.
type OumvHandshakeParams struct {
	ProtocolVersion       string            `json:"protocolVersion"`
	ClientInfo            *ClientInfo       `json:"clientInfo,omitempty"`
	SupportedTransports   []string          `json:"supportedTransports,omitempty"`
	RequestedCapabilities *OumvCapabilities `json:"requestedCapabilities,omitempty"`
	// SessionTTLSeconds asks for a session lifetime (1..86400, clamped). Wire 1.5.0.
	SessionTTLSeconds *int `json:"sessionTtlSeconds,omitempty"`
}

// OumvHandshakeResult is returned by the runtime adapter acknowledging the session.
type OumvHandshakeResult struct {
	ProtocolVersion string           `json:"protocolVersion"`
	Profile         string           `json:"profile"`
	SessionID       string           `json:"sessionId"`
	TTLSeconds      int              `json:"ttlSeconds,omitempty"`
	RuntimeInfo     RuntimeInfo      `json:"runtimeInfo"`
	Capabilities    OumvCapabilities `json:"capabilities"`
}

// DefaultBackendCapabilities provides the default capabilities for Go backend runtimes.
func DefaultBackendCapabilities() OumvCapabilities {
	return OumvCapabilities{
		Observe: ObserveCapability{
			Levels: []string{"L0", "L1", "L2", "L3", "L4"},
			// JSON only: the Go runtime returns typed data, never YAML.
			Formats: []string{"json"},
		},
		Understand: UnderstandCapability{
			Causality: true,
			// Impact comes from the registered route graph, not Go type
			// inference — advertising typeGraph would be a false claim.
			ImpactAnalysis: true,
			TypeGraph:      false,
		},
		Mutate: MutateCapability{
			OCC:         true,
			SyntaxGuard: true,
			MultiFile:   true,
			TypeGuard:   false,
		},
		Verify: VerifyCapability{
			MultiMetric: true,
			// Verification runs the real handler in-memory; no DB sandbox and no
			// expected/actual payload diff are produced.
			DBSandbox:   false,
			PayloadDiff: false,
		},
		Sessions: &SessionCapability{TTLSeconds: true},
	}
}
