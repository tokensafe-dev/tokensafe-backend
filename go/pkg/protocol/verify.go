package protocol

// VerifyParams defines the request payload for `oumv/verify`.
type VerifyParams struct {
	Target  string            `json:"target"`            // e.g. "POST /api/v1/orders" or handler name
	Action  string            `json:"action"`            // e.g. "invoke", "execute"
	Payload interface{}       `json:"payload,omitempty"` // Request body / params
	Headers map[string]string `json:"headers,omitempty"`
	Method  string            `json:"method,omitempty"` // overrides the method in Target
}

// LatencyBreakdown decomposes execution time across layers (spec §4.4 `timings`).
type LatencyBreakdown struct {
	TActionMs      float64 `json:"t_action_ms"`
	TSchemaMs      float64 `json:"t_schema_ms"`
	TPropagationMs float64 `json:"t_propagation_ms"`
	TDomMs         float64 `json:"t_dom_ms"`
	TTotalMs       float64 `json:"t_total_ms"`
}

// VerifyScope states what a verification actually covered (spec §4 Pillar 4).
type VerifyScope struct {
	Checked      []string `json:"checked"`
	NotExecuted  []string `json:"notExecuted"`
	NotSupported []string `json:"notSupported"`
}

// VerifyResult defines the response payload for `oumv/verify`.
type VerifyResult struct {
	Success      bool                   `json:"-"`
	Status       string                 `json:"status"` // "passed" | "failed"
	Target       string                 `json:"target,omitempty"`
	StatusCode   int                    `json:"statusCode"`
	Diff         map[string]interface{} `json:"diff,omitempty"`   // State or payload diff
	DBDiff       map[string]interface{} `json:"-"` // not implemented; never advertised
	Timings      LatencyBreakdown       `json:"timings"`
	DurationMs   float64                `json:"durationMs"`
	Scope        *VerifyScope           `json:"scope,omitempty"`
	ErrorMessage string                 `json:"error,omitempty"`
}
