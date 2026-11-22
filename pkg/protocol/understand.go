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
	Query     UnderstandQuery   `json:"query"`
	Target    string            `json:"target"`
	Impact    *ImpactResult     `json:"impact,omitempty"`
	Causality *CausalityResult  `json:"causality,omitempty"`
	TypeGraph *TypeGraphResult  `json:"typeGraph,omitempty"`
}

// ImpactResult details what symbols, handlers and endpoints break if target changes.
type ImpactResult struct {
	Target          string   `json:"target"`
	AffectedSymbols []string `json:"affectedSymbols"`
	BreakingRisk    string   `json:"breakingRisk"` // "LOW", "MEDIUM", "HIGH"
	Summary         string   `json:"summary"`
}

// CausalityResult maps runtime failure root cause through the execution chain.
type CausalityResult struct {
	Target     string   `json:"target"`
	StatusCode int      `json:"statusCode"`
	RootCause  string   `json:"rootCause"`
	TraceChain []string `json:"traceChain"`
}

// TypeGraphResult represents dependencies and consumers of a Go type.
type TypeGraphResult struct {
	Type        string   `json:"type"`
	Package     string   `json:"package"`
	Fields      []string `json:"fields"`
	ReferencedBy []string `json:"referencedBy"`
}
