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

// UnderstandCapability defines supported reasoning features.
type UnderstandCapability struct {
	Causality   bool `json:"causality"`
	TypeGraph   bool `json:"typeGraph"`
	ImpactGraph bool `json:"impactGraph"`
}

// MutateCapability defines supported mutation safeguards.
type MutateCapability struct {
	OCC         bool `json:"occ"`
	SyntaxGuard bool `json:"syntaxGuard"`
	TypeGuard   bool `json:"typeGuard"`
}

// VerifyCapability defines supported verification features.
type VerifyCapability struct {
	MultiMetric bool `json:"multiMetric"`
	DBSandbox   bool `json:"dbSandbox"`
	PayloadDiff bool `json:"payloadDiff"`
}

// OumvCapabilities aggregates all functional pillars supported by client/runtime.
type OumvCapabilities struct {
	Observe    ObserveCapability    `json:"observe"`
	Understand UnderstandCapability `json:"understand"`
	Mutate     MutateCapability     `json:"mutate"`
	Verify     VerifyCapability     `json:"verify"`
}

// OumvHandshakeParams is sent by the AI Agent during initialization.
type OumvHandshakeParams struct {
	ProtocolVersion       string            `json:"protocolVersion"`
	ClientInfo            *ClientInfo       `json:"clientInfo,omitempty"`
	SupportedTransports   []string          `json:"supportedTransports,omitempty"`
	RequestedCapabilities *OumvCapabilities `json:"requestedCapabilities,omitempty"`
}

// OumvHandshakeResult is returned by the runtime adapter acknowledging the session.
type OumvHandshakeResult struct {
	ProtocolVersion string           `json:"protocolVersion"`
	RuntimeInfo     RuntimeInfo      `json:"runtimeInfo"`
	Capabilities    OumvCapabilities `json:"capabilities"`
}

// DefaultBackendCapabilities provides the default capabilities for Go backend runtimes.
func DefaultBackendCapabilities() OumvCapabilities {
	return OumvCapabilities{
		Observe: ObserveCapability{
			Levels:  []string{"L0", "L1", "L2", "L3", "L4"},
			Formats: []string{"json", "yaml"},
		},
		Understand: UnderstandCapability{
			Causality:   true,
			TypeGraph:   true,
			ImpactGraph: true,
		},
		Mutate: MutateCapability{
			OCC:         true,
			SyntaxGuard: true,
			TypeGuard:   true,
		},
		Verify: VerifyCapability{
			MultiMetric: true,
			DBSandbox:   true,
			PayloadDiff: true,
		},
	}
}
