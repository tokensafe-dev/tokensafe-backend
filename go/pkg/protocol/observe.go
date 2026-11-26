package protocol

// ObserveLevel defines the level of progressive context disclosure.
type ObserveLevel string

const (
	LevelL0Pulse    ObserveLevel = "L0" // Uptime, Goroutines, MemStats, Error count (~30 tokens)
	LevelL1Surface  ObserveLevel = "L1" // Route catalog, HTTP methods, gRPC endpoints (~150 tokens)
	LevelL2Contract ObserveLevel = "L2" // DTO schema, parameters, middleware chain (~80 tokens)
	LevelL3Source   ObserveLevel = "L3" // AST-sliced Go function with OCC version (~300 tokens)
	LevelL4Traces   ObserveLevel = "L4" // Request traces, OpenTelemetry spans, in-flight calls
)

// ObserveParams defines the request payload for `oumv/observe`.
type ObserveParams struct {
	Level  ObserveLevel `json:"level"`
	Format string       `json:"format,omitempty"` // "json" or "yaml"
	Target string       `json:"target,omitempty"` // Target route, handler or symbol
}

// ObserveResult defines the response payload for `oumv/observe`.
type ObserveResult struct {
	Level ObserveLevel `json:"level"`
	// Format is part of the response envelope for every level (spec §4.1); the Go
	// runtime only emits JSON, so it is always "json".
	Format        string      `json:"format"`
	Data          interface{} `json:"data"`
	TokenEstimate int         `json:"tokenEstimate,omitempty"`
}

// L0PulseData represents the lean L0 system pulse (~30 tokens).
type L0PulseData struct {
	UptimeSeconds   int64  `json:"uptimeSeconds"`
	Goroutines      int    `json:"goroutines"`
	MemoryAllocMB   uint64 `json:"memoryAllocMB"`
	ActiveRoutes    int    `json:"activeRoutes"`
	ErrorCount1m    int    `json:"errorCount1m"`
	Status          string `json:"status"` // "HEALTHY", "DEGRADED", "ERROR"
}

// RouteInfo represents a single endpoint in the L1 Surface map.
type RouteInfo struct {
	Method      string   `json:"method"`      // "GET", "POST", etc.
	Path        string   `json:"path"`        // "/api/v1/orders"
	HandlerName string   `json:"handlerName"` // "handlers.CreateOrder"
	Middlewares []string `json:"middlewares,omitempty"`
}

// L1SurfaceData represents the L1 API surface catalog.
type L1SurfaceData struct {
	Endpoints []RouteInfo `json:"endpoints"`
}

// L2ContractData represents the L2 contract of a specific endpoint or struct.
// Fields are only populated from observed route metadata or declared operations:
// adapters MUST NOT fabricate DTOs or middleware names they cannot prove.
type L2ContractData struct {
	Target      string                 `json:"target"`
	Method      string                 `json:"method,omitempty"`
	Path        string                 `json:"path,omitempty"`
	HandlerName string                 `json:"handlerName,omitempty"`
	RequestDTO  map[string]interface{} `json:"requestDTO,omitempty"`
	ResponseDTO map[string]interface{} `json:"responseDTO,omitempty"`
	Middlewares []string               `json:"middlewares,omitempty"`
	Note        string                 `json:"note,omitempty"`
	Evidence    *EvidenceMetadata      `json:"evidence,omitempty"`
}

// L3SourceData represents the L3 AST-sliced function and OCC version token.
type L3SourceData struct {
	Symbol    string `json:"symbol"`
	Path      string `json:"path"`
	Source    string `json:"source"`
	Version   string `json:"version"` // "sha256:..."
	StartLine int    `json:"startLine"`
	EndLine   int    `json:"endLine"`
}

// TraceEntry is one recorded request execution (L4 observation evidence).
type TraceEntry struct {
	Method     string  `json:"method"`
	Path       string  `json:"path"`
	Route      string  `json:"route"`
	Handler    string  `json:"handler"`
	StatusCode int     `json:"statusCode"`
	DurationMs float64 `json:"durationMs"`
	Error      string  `json:"error,omitempty"`
	Timestamp  string  `json:"timestamp"`
}

// L4TracesData represents recorded request traces for a target (or all targets).
// When no trace matches, Traces is empty and Note says why — never invented.
type L4TracesData struct {
	Target    string            `json:"target,omitempty"`
	Traces    []TraceEntry      `json:"traces"`
	TraceCount int              `json:"traceCount"`
	Note      string            `json:"note,omitempty"`
	Evidence  *EvidenceMetadata `json:"evidence,omitempty"`
}
