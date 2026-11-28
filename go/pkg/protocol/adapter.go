package protocol

import "context"

// Standard OUMV RPC Methods
const (
	MethodHandshake   = "oumv/handshake"
	MethodDisconnect  = "oumv/disconnect"
	MethodObserve     = "oumv/observe"
	MethodUnderstand  = "oumv/understand"
	MethodGetSource   = "oumv/getSource"
	MethodApplyEdit   = "oumv/applyEdit"
	MethodVerify      = "oumv/verify"
	MethodPreviewEdit = "oumv/previewEdit" // wire 1.6.0
	MethodCancel      = "oumv/cancel"      // wire 1.6.0
	MethodIndex       = "oumv/index"       // wire 1.6.0
	MethodQuery       = "oumv/query"       // wire 1.6.0
	MethodBuild       = "oumv/build"       // wire 1.6.0
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

// Optional wire 1.6.0 capabilities (spec §3/§4.4/§14). A runtime that does not
// implement them answers -32601 like any unknown method; the dispatcher probes
// these interfaces so existing adapters stay valid unchanged.
type (
	// PreviewEditor is the §4.4 dry-run of applyEdit.
	PreviewEditor interface {
		PreviewEdit(ctx context.Context, params *PreviewEditParams) (*PreviewEditResult, *OumvError)
	}
	// Canceller answers §3 best-effort cancellation.
	Canceller interface {
		Cancel(ctx context.Context, params *CancelParams) (*CancelResult, *OumvError)
	}
	// Indexer serves the §14.1 repo-wide symbol search.
	Indexer interface {
		Index(ctx context.Context, params *IndexParams) (*IndexResult, *OumvError)
	}
	// Querier serves the §14.2 read-only data access.
	Querier interface {
		Query(ctx context.Context, params *QueryParams) (*QueryResult, *OumvError)
	}
	// Builder serves the §14.3 build/test diagnostics.
	Builder interface {
		Build(ctx context.Context, params *BuildParams) (*BuildResult, *OumvError)
	}
)

// MethodRegistry is implemented by runtimes that can state, at boot, the exact set
// of JSON-RPC methods they serve (spec §3.1 rule 5 / §8.5, revision v1.11). The
// dispatcher reads it once: a method that is not in the registry is not reachable at
// all and answers -32601 METHOD_NOT_FOUND, before session validation and before any
// handler runs. That is what makes a read-only deployment a shape of the runtime
// instead of a check inside each handler.
type MethodRegistry interface {
	RegisteredMethods() []string
}
