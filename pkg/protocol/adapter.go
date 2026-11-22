package protocol

import "context"

// Standard OUMV RPC Methods
const (
	MethodHandshake  = "oumv/handshake"
	MethodObserve    = "oumv/observe"
	MethodUnderstand = "oumv/understand"
	MethodGetSource  = "oumv/getSource"
	MethodApplyEdit  = "oumv/applyEdit"
	MethodVerify     = "oumv/verify"
)

// OumvRuntimeAdapter is the core contract that every backend runtime adapter must satisfy.
// It exposes all four pillars of OUMV (Observe, Understand, Mutate, Verify) plus session handshake.
type OumvRuntimeAdapter interface {
	// Name returns the identifier of the runtime adapter (e.g. "tokensafe-backend", "oumv-gin").
	Name() string

	// Version returns the version of the runtime adapter.
	Version() string

	// Handshake initializes session and negotiates capabilities with the agent.
	Handshake(ctx context.Context, params *OumvHandshakeParams) (*OumvHandshakeResult, *OumvError)

	// Observe executes Pillar 1: Progressive semantic context disclosure (L0-L4).
	Observe(ctx context.Context, params *ObserveParams) (*ObserveResult, *OumvError)

	// Understand executes Pillar 2: Runtime causality and impact analysis.
	Understand(ctx context.Context, params *UnderstandParams) (*UnderstandResult, *OumvError)

	// GetSource executes Pillar 3A: Retrieves symbol-level source code with OCC version token.
	GetSource(ctx context.Context, params *GetSourceParams) (*GetSourceResult, *OumvError)

	// ApplyEdit executes Pillar 3B: Applies surgical edits with OCC checks and pre-flight syntax guards.
	ApplyEdit(ctx context.Context, params *ApplyEditParams) (*ApplyEditResult, *OumvError)

	// Verify executes Pillar 4: Sub-millisecond local action and state verification.
	Verify(ctx context.Context, params *VerifyParams) (*VerifyResult, *OumvError)
}
