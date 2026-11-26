package protocol

// VerifyParams defines the request payload for `oumv/verify`.
type VerifyParams struct {
	Target  string            `json:"target"`            // e.g. "POST /api/v1/orders" or handler name
	Action  string            `json:"action"`            // e.g. "invoke", "execute"
	Payload interface{}       `json:"payload,omitempty"` // Request body / params
	Headers map[string]string `json:"headers,omitempty"`
	Method  string            `json:"method,omitempty"` // overrides the method in Target
}

// LatencyBreakdown decomposes execution time across layers.
type LatencyBreakdown struct {
	RoutingMs    float64 `json:"routingMs,omitempty"`
	MiddlewareMs float64 `json:"middlewareMs,omitempty"`
	HandlerMs    float64 `json:"handlerMs,omitempty"`
	DBMs         float64 `json:"dbMs,omitempty"`
	TotalMs      float64 `json:"totalMs"`
}

// VerifyScope states what a verification actually covered (spec §4 Pillar 4).
type VerifyScope struct {
	Checked      []string `json:"checked"`
	NotExecuted  []string `json:"notExecuted"`
	NotSupported []string `json:"notSupported"`
}

// VerifyResult defines the response payload for `oumv/verify`.
type VerifyResult struct {
	Success      bool                   `json:"success"`
	Status       string                 `json:"status"` // "passed" | "failed"
	StatusCode   int                    `json:"statusCode"`
	Diff         map[string]interface{} `json:"diff,omitempty"`   // State or payload diff
	DBDiff       map[string]interface{} `json:"dbDiff,omitempty"` // Database table row diff
	Latency      LatencyBreakdown       `json:"latency"`
	Scope        *VerifyScope           `json:"scope,omitempty"`
	ErrorMessage string                 `json:"errorMessage,omitempty"`
}
