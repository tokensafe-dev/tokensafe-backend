package protocol

// UnderstandQuery defines the reasoning query type.
type UnderstandQuery string

const (
	QueryImpact    UnderstandQuery = "impact"
	QueryCausality UnderstandQuery = "causality"
	QueryTypeGraph UnderstandQuery = "type_graph"
)

// UnderstandParams defines the request payload for `oumv/understand`.
type UnderstandParams struct {
	Query  UnderstandQuery `json:"query"`
	Target string          `json:"target"` // Endpoint, function, error ID or type name
}

// UnderstandResult is the container for understand query responses.
type UnderstandResult struct {
	Query     UnderstandQuery  `json:"query"`
	Target    string           `json:"target"`
	Impact    *ImpactResult    `json:"impact,omitempty"`
	Causality *CausalityResult `json:"causality,omitempty"`
	TypeGraph *TypeGraphResult `json:"typeGraph,omitempty"`
}

// EvidenceMetadata carries provenance for understand results (spec §6.2).
type EvidenceMetadata struct {
	Provenance    string `json:"provenance"` // "runtime" | "static" | "contract" | "inferred"
	SourceAdapter string `json:"sourceAdapter"`
	Confidence    string `json:"confidence"` // "confirmed" | "probable" | "unknown"
}

// ImpactResult details what symbols, handlers and endpoints break if target changes.
type ImpactResult struct {
	Target          string            `json:"target"`
	AffectedSymbols []string          `json:"affectedSymbols"`
	BreakingRisk    string            `json:"breakingRisk"` // "LOW", "MEDIUM", "HIGH", "UNKNOWN"
	Summary         string            `json:"summary"`
	Observed        bool              `json:"observed"`
	Evidence        *EvidenceMetadata `json:"evidence,omitempty"`
}

// CausalityResult maps runtime failure root cause through the execution chain.
// StatusCode/RootCause/TraceChain are meaningful only when Observed is true
// (derived from an actual recorded request trace, never fabricated).
type CausalityResult struct {
	Target     string            `json:"target"`
	StatusCode int               `json:"statusCode"`
	RootCause  string            `json:"rootCause"`
	TraceChain []string          `json:"traceChain"`
	Observed   bool              `json:"observed"`
	TraceCount int               `json:"traceCount"`
	LatencyMs  float64           `json:"latencyMs"`
	LastError  string            `json:"lastError,omitempty"`
	Note       string            `json:"note,omitempty"`
	Evidence   *EvidenceMetadata `json:"evidence,omitempty"`
}

// TypeGraphResult represents dependencies and consumers of a Go type.
type TypeGraphResult struct {
	Type         string   `json:"type"`
	Package      string   `json:"package"`
	Fields       []string `json:"fields"`
	ReferencedBy []string `json:"referencedBy"`
}
