package oumv

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/mutate"
	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/protocol"
)

// Config configures the runtime adapter.
type Config struct {
	Name         string
	Version      string
	WorkspaceDir string
	Handler      http.Handler
	Runtime      *RuntimeConfig
}

// Runtime implements protocol.OumvRuntimeAdapter.
type Runtime struct {
	name         string
	version      string
	workspaceDir string
	startTime    time.Time
	routes       []protocol.RouteInfo
	handler      http.Handler
	errCount1m   int64
	patcher      *mutate.Patcher
	config       RuntimeConfig
	traces       []requestTrace
	traceIdx     int
	mu           sync.RWMutex
}

// maxRequestTraces bounds the in-memory L4 trace ring buffer.
const maxRequestTraces = 128

// requestTrace is an observed request execution (runtime causality evidence).
type requestTrace struct {
	Method     string    `json:"method"`
	Path       string    `json:"path"`
	Route      string    `json:"route"`
	Handler    string    `json:"handler"`
	StatusCode int       `json:"statusCode"`
	DurationMs float64   `json:"durationMs"`
	Error      string    `json:"error,omitempty"`
	Timestamp  time.Time `json:"timestamp"`
}

// isSensitivePath checks if a path references secrets, keys, or VCS internals.
func isSensitivePath(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	if strings.HasPrefix(base, ".env") || base == ".git" || strings.HasPrefix(base, "id_rsa") || strings.HasPrefix(base, "id_ed25519") || strings.HasSuffix(base, ".pem") || strings.HasSuffix(base, ".key") {
		return true
	}
	clean := filepath.ToSlash(path)
	if strings.Contains(clean, "/.git/") || strings.Contains(clean, "/.tokensafe/keys") {
		return true
	}
	return false
}

// validateWorkspacePath ensures targetPath stays strictly within workspace boundary and does not access secrets.
func validateWorkspacePath(workspaceDir, targetPath string) (string, *protocol.OumvError) {
	if strings.TrimSpace(targetPath) == "" {
		return "", protocol.NewInvalidParamsError("path is required")
	}
	cleanPath := filepath.Clean(filepath.Join(workspaceDir, targetPath))
	absWorkspace, err := filepath.Abs(workspaceDir)
	if err != nil {
		return "", &protocol.OumvError{Code: protocol.ErrInternalError, Message: "failed to resolve workspace directory"}
	}
	absTarget, err := filepath.Abs(cleanPath)
	if err != nil {
		return "", &protocol.OumvError{Code: protocol.ErrInternalError, Message: "failed to resolve target path"}
	}
	rel, err := filepath.Rel(absWorkspace, absTarget)
	if err != nil || strings.HasPrefix(rel, "..") || rel == ".." {
		return "", protocol.NewSecurityViolationError(fmt.Sprintf("path traversal blocked: %q escapes workspace boundary", targetPath), nil)
	}
	if isSensitivePath(cleanPath) {
		return "", protocol.NewSecurityViolationError(fmt.Sprintf("access to sensitive credential or environment file blocked: %q", targetPath), nil)
	}
	return cleanPath, nil
}

// NewRuntime initializes a new OUMV backend runtime adapter.
func NewRuntime(cfg Config) *Runtime {
	if cfg.Name == "" {
		cfg.Name = "tokensafe-backend"
	}
	if cfg.Version == "" {
		// AutoMount-style callers pass only a Runtime config; honour the version it
		// declares instead of pinning a stale default in the handshake.
		if cfg.Runtime != nil && cfg.Runtime.Version != "" {
			cfg.Version = cfg.Runtime.Version
		} else {
			cfg.Version = "0.4.2"
		}
	}
	if cfg.WorkspaceDir == "" {
		cfg.WorkspaceDir = "."
	}

	var rCfg RuntimeConfig
	if cfg.Runtime != nil {
		rCfg = *cfg.Runtime
	} else {
		base := &RuntimeConfig{
			Name:         cfg.Name,
			Version:      cfg.Version,
			WorkspaceDir: cfg.WorkspaceDir,
		}
		resolved, err := ResolveRuntimeConfig(base)
		if err == nil {
			rCfg = resolved
		} else {
			// Fail safe: disabled if resolution errors out
			rCfg = RuntimeConfig{Enabled: false}
		}
	}

	return &Runtime{
		name:         cfg.Name,
		version:      cfg.Version,
		workspaceDir: cfg.WorkspaceDir,
		startTime:    time.Now(),
		handler:      cfg.Handler,
		patcher:      mutate.NewPatcher(cfg.WorkspaceDir),
		routes:       make([]protocol.RouteInfo, 0),
		config:       rCfg,
	}
}

func (r *Runtime) Name() string          { return r.name }
func (r *Runtime) Version() string       { return r.version }
func (r *Runtime) Config() RuntimeConfig { return r.config }
func (r *Runtime) IsEnabled() bool       { return r.config.Enabled }

// RecordTrace records an observed request execution into the bounded L4 trace
// ring buffer. Feeds Understand (causality) with real runtime evidence and
// increments the 1-minute error counter on 5xx. Safe for concurrent use.
func (r *Runtime) RecordTrace(method, path, route, handler string, statusCode int, durationMs float64, reqErr string) {
	if r == nil {
		return
	}
	trace := requestTrace{
		Method:     method,
		Path:       path,
		Route:      route,
		Handler:    handler,
		StatusCode: statusCode,
		DurationMs: durationMs,
		Error:      reqErr,
		Timestamp:  time.Now(),
	}

	r.mu.Lock()
	if len(r.traces) < maxRequestTraces {
		r.traces = append(r.traces, trace)
	} else {
		r.traces[r.traceIdx] = trace
		r.traceIdx = (r.traceIdx + 1) % maxRequestTraces
	}
	r.mu.Unlock()

	if statusCode >= 500 {
		atomic.AddInt64(&r.errCount1m, 1)
	}
}

// TraceHandler wraps any http.Handler (e.g. a Gin engine, Chi router, or std mux)
// with request tracing, feeding Understand(causality) with real runtime evidence.
// Use it as the outermost handler: http.ListenAndServe(addr, oumv.TraceHandler(rt, engine)).
func TraceHandler(rt *Runtime, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if rt == nil || !rt.IsEnabled() {
			next.ServeHTTP(w, req)
			return
		}
		start := time.Now()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, req)
		rt.RecordTrace(
			req.Method,
			req.URL.Path,
			req.URL.Path,
			"",
			recorder.status,
			float64(time.Since(start).Microseconds())/1000.0,
			"",
		)
	})
}

// statusRecorder captures the response status code without buffering the body.
type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (r *statusRecorder) WriteHeader(code int) {
	if !r.wroteHeader {
		r.status = code
		r.wroteHeader = true
	}
	r.ResponseWriter.WriteHeader(code)
}

// matchingTraces returns recorded traces that match the understand target
// ("METHOD /path", "/path", route pattern, or handler name), newest first.
func (r *Runtime) matchingTraces(target string) []requestTrace {
	t := strings.TrimSpace(target)
	if t == "" {
		return nil
	}
	method := ""
	path := t
	if parts := strings.SplitN(t, " ", 2); len(parts) == 2 && strings.ContainsAny(parts[0], "ABCDEFGHIJKLMNOPQRSTUVWXYZ") && parts[0] == strings.ToUpper(parts[0]) {
		method = strings.ToUpper(parts[0])
		path = parts[1]
	}

	r.mu.RLock()
	snapshot := make([]requestTrace, len(r.traces))
	copy(snapshot, r.traces)
	r.mu.RUnlock()

	matched := make([]requestTrace, 0, len(snapshot))
	for _, tr := range snapshot {
		if method != "" && tr.Method != method {
			continue
		}
		if path != "" && tr.Route != path && tr.Path != path && tr.Handler != t &&
			!strings.HasPrefix(tr.Path, path) {
			continue
		}
		matched = append(matched, tr)
	}
	// newest first
	for i, j := 0, len(matched)-1; i < j; i, j = i+1, j-1 {
		matched[i], matched[j] = matched[j], matched[i]
	}
	return matched
}

// RegisterRoute registers a route in the L1 surface catalog.
func (r *Runtime) RegisterRoute(method, path, handlerName string, middlewares ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.routes = append(r.routes, protocol.RouteInfo{
		Method:      method,
		Path:        path,
		HandlerName: handlerName,
		Middlewares: middlewares,
	})
}

// Handshake performs session initialization and capability negotiation.
func (r *Runtime) Handshake(ctx context.Context, params *protocol.OumvHandshakeParams) (*protocol.OumvHandshakeResult, *protocol.OumvError) {
	caps := protocol.DefaultBackendCapabilities()
	if !r.config.Capabilities.Observe {
		caps.Observe = protocol.ObserveCapability{}
	}
	if !r.config.Capabilities.Understand {
		caps.Understand = protocol.UnderstandCapability{}
	}
	if !r.config.Capabilities.Mutate {
		caps.Mutate = protocol.MutateCapability{}
	}
	if !r.config.Capabilities.Verify {
		caps.Verify = protocol.VerifyCapability{}
	}

	return &protocol.OumvHandshakeResult{
		ProtocolVersion: protocol.CurrentProtocolVersion,
		Profile:         "backend",
		RuntimeInfo: protocol.RuntimeInfo{
			Name:    r.name,
			Version: r.version,
		},
		Capabilities: caps,
	}, nil
}

// Observe executes Pillar 1: Progressive Context Disclosure (L0 to L4).
func (r *Runtime) Observe(ctx context.Context, params *protocol.ObserveParams) (*protocol.ObserveResult, *protocol.OumvError) {
	if !r.config.Capabilities.Observe {
		return nil, protocol.NewPermissionDeniedError("observe capability is disabled by policy")
	}

	level := params.Level
	if level == "" {
		level = protocol.LevelL0Pulse
	}

	switch level {
	case protocol.LevelL0Pulse:
		var memStats runtime.MemStats
		runtime.ReadMemStats(&memStats)

		r.mu.RLock()
		routeCount := len(r.routes)
		r.mu.RUnlock()

		pulse := protocol.L0PulseData{
			UptimeSeconds: int64(time.Since(r.startTime).Seconds()),
			Goroutines:    runtime.NumGoroutine(),
			MemoryAllocMB: memStats.Alloc / (1024 * 1024),
			ActiveRoutes:  routeCount,
			ErrorCount1m:  int(atomic.LoadInt64(&r.errCount1m)),
			Status:        "HEALTHY",
		}
		return &protocol.ObserveResult{
			Level:         protocol.LevelL0Pulse,
			Data:          pulse,
			TokenEstimate: 30,
		}, nil

	case protocol.LevelL1Surface:
		r.mu.RLock()
		routes := make([]protocol.RouteInfo, len(r.routes))
		copy(routes, r.routes)
		r.mu.RUnlock()

		return &protocol.ObserveResult{
			Level:         protocol.LevelL1Surface,
			Data:          protocol.L1SurfaceData{Endpoints: routes},
			TokenEstimate: 30 + len(routes)*15,
		}, nil

	case protocol.LevelL2Contract:
		if params.Target == "" {
			return nil, protocol.NewInvalidParamsError("Target endpoint or handler required for L2 contract")
		}
		route, ok := r.findRoute(params.Target)
		if !ok {
			return nil, protocol.NewResourceNotFoundError(params.Target)
		}
		return &protocol.ObserveResult{
			Level: protocol.LevelL2Contract,
			Data: protocol.L2ContractData{
				Target:      params.Target,
				Method:      route.Method,
				Path:        route.Path,
				HandlerName: route.HandlerName,
				Middlewares: route.Middlewares,
				// Mounted routers expose handler names, not request/response structs;
				// saying so is honest, inventing DTOs is not.
				Note: "route contract from live router registration; request/response DTOs are not reflectable from a mounted router — use observe L3 for the handler source",
				Evidence: &protocol.EvidenceMetadata{
					Provenance:    "runtime",
					SourceAdapter: r.name,
					Confidence:    "confirmed",
				},
			},
			TokenEstimate: 80,
		}, nil

	case protocol.LevelL3Source:
		if params.Target == "" {
			return nil, protocol.NewInvalidParamsError("target of the form path (or path#symbol) required for L3 source")
		}
		path, symbol := splitSourceTarget(params.Target)
		src, srcErr := r.GetSource(ctx, &protocol.GetSourceParams{Path: path, Symbol: symbol})
		if srcErr != nil {
			return nil, srcErr
		}
		return &protocol.ObserveResult{
			Level: protocol.LevelL3Source,
			Data: protocol.L3SourceData{
				Symbol:    src.Symbol,
				Path:      src.Path,
				Source:    src.Source,
				Version:   src.Version,
				StartLine: src.StartLine,
				EndLine:   src.EndLine,
			},
			TokenEstimate: 300,
		}, nil

	case protocol.LevelL4Traces:
		traces, note := r.tracesForTarget(params.Target)
		count := len(traces)
		if count == 0 && note == "" {
			note = "no request traces recorded yet: call the endpoint, or use oumv/verify which feeds the trace ring"
		}
		return &protocol.ObserveResult{
			Level: protocol.LevelL4Traces,
			Data: protocol.L4TracesData{
				Target:     params.Target,
				Traces:     traces,
				TraceCount: count,
				Note:       note,
				Evidence: &protocol.EvidenceMetadata{
					Provenance:    "runtime",
					SourceAdapter: r.name,
					Confidence:    "confirmed",
				},
			},
			TokenEstimate: 40 + count*15,
		}, nil

	default:
		return nil, protocol.NewUnsupportedCapabilityError(fmt.Sprintf("observe level %s", level))
	}
}

// splitSourceTarget parses "path#symbol" / "path:symbol" into its parts.
func splitSourceTarget(target string) (string, string) {
	t := strings.TrimSpace(target)
	for _, sep := range []string{"#", ":"} {
		if i := strings.LastIndex(t, sep); i > 0 && i < len(t)-1 && !strings.Contains(t[i+1:], "/") {
			return t[:i], t[i+1:]
		}
	}
	return t, ""
}

// findRoute resolves an L2 target ("METHOD /path", "/path", or handler name)
// against the live router catalog.
func (r *Runtime) findRoute(target string) (protocol.RouteInfo, bool) {
	t := strings.TrimSpace(target)
	method := ""
	path := t
	if parts := strings.SplitN(t, " ", 2); len(parts) == 2 {
		method = strings.ToUpper(parts[0])
		path = strings.TrimSpace(parts[1])
	}

	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, route := range r.routes {
		if method != "" && !strings.EqualFold(route.Method, method) {
			continue
		}
		if strings.EqualFold(route.Path, path) || route.HandlerName == t ||
			(path != "" && strings.HasSuffix(route.HandlerName, "."+path)) {
			return route, true
		}
	}
	return protocol.RouteInfo{}, false
}

// tracesForTarget returns recorded traces for a target (all traces when the
// target is empty), newest first, plus a note when nothing matched.
func (r *Runtime) tracesForTarget(target string) ([]protocol.TraceEntry, string) {
	var source []requestTrace
	note := ""
	if strings.TrimSpace(target) == "" {
		r.mu.RLock()
		source = make([]requestTrace, len(r.traces))
		copy(source, r.traces)
		r.mu.RUnlock()
		for i, j := 0, len(source)-1; i < j; i, j = i+1, j-1 {
			source[i], source[j] = source[j], source[i]
		}
	} else {
		source = r.matchingTraces(target)
		if len(source) == 0 {
			note = fmt.Sprintf("no recorded trace matches %q", target)
		}
	}

	out := make([]protocol.TraceEntry, 0, len(source))
	for _, tr := range source {
		out = append(out, protocol.TraceEntry{
			Method:     tr.Method,
			Path:       tr.Path,
			Route:      tr.Route,
			Handler:    tr.Handler,
			StatusCode: tr.StatusCode,
			DurationMs: tr.DurationMs,
			Error:      tr.Error,
			Timestamp:  tr.Timestamp.UTC().Format(time.RFC3339Nano),
		})
	}
	return out, note
}

// Understand executes Pillar 2: Runtime Causality and Impact Analysis.
// Causality results are derived from actual recorded request traces
// (adapters' trace middleware, or in-memory oumv/verify dispatches) —
// never fabricated. When no trace exists, Observed=false with an
// actionable note.
func (r *Runtime) Understand(ctx context.Context, params *protocol.UnderstandParams) (*protocol.UnderstandResult, *protocol.OumvError) {
	if !r.config.Capabilities.Understand {
		return nil, protocol.NewPermissionDeniedError("understand capability is disabled by policy")
	}

	switch params.Query {
	case protocol.QueryImpact:
		return r.understandImpact(params.Target), nil

	case protocol.QueryCausality:
		return r.understandCausality(params.Target), nil

	default:
		return nil, protocol.NewUnsupportedCapabilityError(fmt.Sprintf("query %s", params.Query))
	}
}

// understandCausality builds the causality result from the newest matching trace.
func (r *Runtime) understandCausality(target string) *protocol.UnderstandResult {
	matched := r.matchingTraces(target)

	if len(matched) == 0 {
		return &protocol.UnderstandResult{
			Query:  protocol.QueryCausality,
			Target: target,
			Causality: &protocol.CausalityResult{
				Target:     target,
				Observed:   false,
				RootCause:  "unknown",
				TraceChain: []string{},
				Note:       "No runtime trace observed for this target yet. Send a request through the instrumented adapter or run oumv/verify to generate one.",
				Evidence: &protocol.EvidenceMetadata{
					Provenance:    "runtime",
					SourceAdapter: "tokensafe-backend-go",
					Confidence:    "unknown",
				},
			},
		}
	}

	latest := matched[0]
	rootCause := "OK — handler completed without error (observed)"
	if latest.StatusCode >= 500 {
		rootCause = fmt.Sprintf("HTTP %d returned by handler %s", latest.StatusCode, latest.Handler)
	} else if latest.StatusCode >= 400 {
		rootCause = fmt.Sprintf("HTTP %d returned by handler %s", latest.StatusCode, latest.Handler)
	} else if latest.Error != "" {
		rootCause = latest.Error
	}

	chain := make([]string, 0, 4)
	chain = append(chain, "adapter.ServeHTTP")
	if latest.Route != "" && latest.Route != latest.Path {
		chain = append(chain, "route "+latest.Route)
	}
	if latest.Handler != "" {
		chain = append(chain, latest.Handler)
	}

	return &protocol.UnderstandResult{
		Query:  protocol.QueryCausality,
		Target: target,
		Causality: &protocol.CausalityResult{
			Target:     target,
			StatusCode: latest.StatusCode,
			RootCause:  rootCause,
			TraceChain: chain,
			Observed:   true,
			TraceCount: len(matched),
			LatencyMs:  latest.DurationMs,
			LastError:  latest.Error,
			Evidence: &protocol.EvidenceMetadata{
				Provenance:    "runtime",
				SourceAdapter: "tokensafe-backend-go",
				Confidence:    "confirmed",
			},
		},
	}
}

// understandImpact derives affected handlers/middlewares from the registered
// route catalog. Breaking risk stays UNKNOWN unless real evidence supports it.
func (r *Runtime) understandImpact(target string) *protocol.UnderstandResult {
	t := strings.TrimSpace(target)
	affected := make([]string, 0)
	routeMatched := false

	r.mu.RLock()
	for _, route := range r.routes {
		if t == "" || route.Path == t || route.HandlerName == t ||
			(t == route.Method+" "+route.Path) {
			routeMatched = true
			if route.HandlerName != "" {
				affected = append(affected, route.HandlerName)
			}
			for _, mw := range route.Middlewares {
				affected = append(affected, mw)
			}
		}
	}
	r.mu.RUnlock()

	observed := len(r.matchingTraces(t)) > 0
	summary := fmt.Sprintf("Impact derived from registered route metadata for %q; runtime execution evidence: %s", t,
		map[bool]string{true: "matching request traces observed", false: "no matching traces observed"}[observed])

	return &protocol.UnderstandResult{
		Query:  protocol.QueryImpact,
		Target: t,
		Impact: &protocol.ImpactResult{
			Target:          t,
			AffectedSymbols: affected,
			BreakingRisk:    "UNKNOWN",
			Summary:         summary,
			Observed:        routeMatched || observed,
			Evidence: &protocol.EvidenceMetadata{
				Provenance:    "static",
				SourceAdapter: "tokensafe-backend-go",
				Confidence:    "probable",
			},
		},
	}
}

// GetSource executes Pillar 3A: Retrieves symbol source with SHA256 OCC version token.
func (r *Runtime) GetSource(ctx context.Context, params *protocol.GetSourceParams) (*protocol.GetSourceResult, *protocol.OumvError) {
	// Source read is an observation capability, not a mutation: a read-only agent
	// must be able to inspect code without being allowed to write it.
	if !r.config.Capabilities.Observe && !r.config.Capabilities.Mutate {
		return nil, protocol.NewPermissionDeniedError("observe capability is disabled by policy")
	}

	cleanPath, pathErr := validateWorkspacePath(r.workspaceDir, params.Path)
	if pathErr != nil {
		return nil, pathErr
	}

	data, err := os.ReadFile(cleanPath)
	if err != nil {
		return nil, protocol.NewResourceNotFoundError(params.Path)
	}

	hash := sha256.Sum256(data)
	version := "sha256:" + hex.EncodeToString(hash[:])

	// If symbol is specified, parse file AST and locate exact function
	if params.Symbol != "" {
		fset := token.NewFileSet()
		node, err := parser.ParseFile(fset, cleanPath, data, parser.ParseComments)
		if err == nil {
			for _, decl := range node.Decls {
				if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == params.Symbol {
					start := fset.Position(fn.Pos())
					end := fset.Position(fn.End())
					lines := strings.Split(string(data), "\n")
					if start.Line <= len(lines) && end.Line <= len(lines) {
						symbolSource := strings.Join(lines[start.Line-1:end.Line], "\n")
						return &protocol.GetSourceResult{
							Symbol:    params.Symbol,
							Path:      params.Path,
							Source:    symbolSource,
							Version:   version,
							StartLine: start.Line,
							EndLine:   end.Line,
						}, nil
					}
				}
			}
		}
	}

	return &protocol.GetSourceResult{
		Symbol:    params.Symbol,
		Path:      params.Path,
		Source:    string(data),
		Version:   version,
		StartLine: 1,
		EndLine:   len(strings.Split(string(data), "\n")),
	}, nil
}

// ApplyEdit executes Pillar 3B: Applies source edit with OCC and pre-flight syntax checks.
func (r *Runtime) ApplyEdit(ctx context.Context, params *protocol.ApplyEditParams) (*protocol.ApplyEditResult, *protocol.OumvError) {
	if !r.config.Capabilities.Mutate {
		return nil, protocol.NewPermissionDeniedError("mutate capability is disabled by policy")
	}

	cleanPath, pathErr := validateWorkspacePath(r.workspaceDir, params.Path)
	if pathErr != nil {
		return nil, pathErr
	}

	currentData, err := os.ReadFile(cleanPath)
	if err != nil {
		return nil, protocol.NewResourceNotFoundError(params.Path)
	}

	currentHash := sha256.Sum256(currentData)
	currentVersion := "sha256:" + hex.EncodeToString(currentHash[:])

	// 1. Mandatory OCC: every edit of an existing file must name the version it was
	// based on. Without this a blind write can silently clobber a concurrent change.
	if strings.TrimSpace(params.ExpectedVersion) == "" {
		return nil, protocol.NewValidationFailedError(
			"VALIDATION_FAILED: expectedVersion is required to edit an existing file — call oumv/getSource (or observe L3) and pass its version",
			map[string]string{"path": params.Path, "current": currentVersion},
		)
	}

	// If a specific symbol is targeted, use surgical AST patcher (it re-checks OCC).
	if params.Symbol != "" && r.patcher != nil {
		patchRes, patchErr := r.patcher.ApplySymbolPatch(params.Path, params.Symbol, params.Replacement, params.ExpectedVersion)
		if patchErr != nil {
			return nil, patchErr
		}
		return &protocol.ApplyEditResult{
			Success:      true,
			Status:       "APPLIED",
			NewVersion:   patchRes.NewVersion,
			LinesChanged: patchRes.LinesChanged,
			Message:      "Surgical AST symbol patch applied with pre-flight compiler validation passed",
		}, nil
	}

	// 2. Optimistic Concurrency Control (OCC) guard
	if params.ExpectedVersion != currentVersion {
		return nil, protocol.NewConflictError(
			fmt.Sprintf("version mismatch: expected %s, current is %s", params.ExpectedVersion, currentVersion),
			map[string]string{"expected": params.ExpectedVersion, "current": currentVersion},
		)
	}

	// 3. Pre-flight Syntax Guard: Parse in-memory before writing to disk
	fset := token.NewFileSet()
	_, parseErr := parser.ParseFile(fset, cleanPath, []byte(params.Replacement), parser.AllErrors)
	if parseErr != nil {
		return nil, protocol.NewSyntaxError(
			fmt.Sprintf("pre-flight syntax check failed: %v", parseErr),
			parseErr.Error(),
		)
	}

	// 4. Write validated content to disk
	if err := os.WriteFile(cleanPath, []byte(params.Replacement), 0644); err != nil {
		return nil, &protocol.OumvError{
			Code:    protocol.ErrInternalError,
			Message: "failed to persist edit: " + err.Error(),
		}
	}

	newHash := sha256.Sum256([]byte(params.Replacement))
	newVersion := "sha256:" + hex.EncodeToString(newHash[:])

	return &protocol.ApplyEditResult{
		Success:      true,
		Status:       "APPLIED",
		NewVersion:   newVersion,
		LinesChanged: len(strings.Split(params.Replacement, "\n")),
		Message:      "Edit applied with pre-flight syntax verification passed",
	}, nil
}

// SetHandler wires the application router so oumv/verify can dispatch actions
// in-process. Adapters call it at mount time; callers may also set Config.Handler.
func (r *Runtime) SetHandler(h http.Handler) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.handler = h
	r.mu.Unlock()
}

// Verify executes Pillar 4: Sub-millisecond action verification via in-memory httptest.
func (r *Runtime) Verify(ctx context.Context, params *protocol.VerifyParams) (*protocol.VerifyResult, *protocol.OumvError) {
	if !r.config.Capabilities.Verify {
		return nil, protocol.NewPermissionDeniedError("verify capability is disabled by policy")
	}
	r.mu.RLock()
	handler := r.handler
	r.mu.RUnlock()
	if handler == nil {
		return nil, protocol.NewUnsupportedCapabilityError("no active HTTP handler configured for in-memory verification")
	}

	start := time.Now()

	// Parse method and path from target (e.g. "GET /api/v1/orders" or path alone)
	method := "GET"
	path := params.Target
	parts := strings.SplitN(params.Target, " ", 2)
	if len(parts) == 2 {
		method = parts[0]
		path = parts[1]
	}
	if strings.TrimSpace(params.Method) != "" {
		method = strings.ToUpper(strings.TrimSpace(params.Method))
	}

	// Request bodies are supported: a POST/PUT endpoint can only be verified with
	// the payload it expects.
	var body io.Reader
	if params.Payload != nil {
		if raw, marshalErr := json.Marshal(params.Payload); marshalErr == nil {
			body = bytes.NewReader(raw)
		}
	}
	req := httptest.NewRequest(method, path, body)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range params.Headers {
		req.Header.Set(k, v)
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	duration := float64(time.Since(start).Microseconds()) / 1000.0

	// Feed the L4 trace ring so Understand(causality) has real evidence after verification
	r.RecordTrace(method, path, path, "", rec.Code, duration, "")

	status := "failed"
	if rec.Code < 400 {
		status = "passed"
	}
	return &protocol.VerifyResult{
		Success:    rec.Code < 400,
		Status:     status,
		StatusCode: rec.Code,
		Diff: map[string]interface{}{
			"responseBody": rec.Body.String(),
			"status":       rec.Code,
		},
		Latency: protocol.LatencyBreakdown{
			HandlerMs: duration,
			TotalMs:   duration,
		},
		Scope: &protocol.VerifyScope{
			Checked:      []string{"in-memory dispatch against the live handler"},
			NotExecuted:  []string{"external processes", "real network", "persistent database state"},
			NotSupported: []string{"browser/DOM assertions"},
		},
	}, nil
}
