package oumv

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
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
	"sort"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/db"
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
	routeSources map[string]routeSource
	handler      http.Handler
	errCount1m   int64
	patcher      *mutate.Patcher
	config       RuntimeConfig
	traces       []requestTrace
	traceIdx     int
	sessions     *SessionRegistry
	transports   []string
	dbSandbox    *db.Sandbox
	events       []protocol.StreamEvent
	eventSeq     uint64
	subs         map[chan protocol.StreamEvent]struct{}
	mu           sync.RWMutex
	// §3 idempotency (wire 1.6.0): session:key -> first final applyEdit result.
	idempotency sync.Map
}

// maxRequestTraces bounds the in-memory L4 trace ring buffer.
const maxRequestTraces = 128

// maxStreamEvents bounds the replay log behind SSE `Last-Event-ID` and WS
// notifications (spec §2.3: reconnecting replays what the client missed).
const maxStreamEvents = 256

// routeSource is where a registered handler lives on disk, so an endpoint target
// can be resolved to source (spec §4 target grammar: `"METHOD /path"`).
type routeSource struct {
	file string
	line int
}

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
			cfg.Version = "0.10.0"
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
		sessions:     NewSessionRegistry(0),
		name:         cfg.Name,
		version:      cfg.Version,
		workspaceDir: cfg.WorkspaceDir,
		startTime:    time.Now(),
		handler:      cfg.Handler,
		patcher:      mutate.NewPatcher(cfg.WorkspaceDir),
		routes:       make([]protocol.RouteInfo, 0),
		routeSources: make(map[string]routeSource),
		subs:         make(map[chan protocol.StreamEvent]struct{}),
		config:       rCfg,
	}
}

func (r *Runtime) Name() string          { return r.name }
func (r *Runtime) Version() string       { return r.version }
func (r *Runtime) Config() RuntimeConfig { return r.config }
func (r *Runtime) IsEnabled() bool       { return r.config.Enabled }

// SetDBSandbox wires a database sandbox. When set, `verify` with
// `sandbox:{mode:"rollback"}` runs the whole request inside a transaction that is
// always rolled back, and the capability is advertised (spec §4, wire 1.4.0).
func (r *Runtime) SetDBSandbox(sandbox *db.Sandbox) {
	r.mu.Lock()
	r.dbSandbox = sandbox
	r.mu.Unlock()
}

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

	r.appendEvent("trace", trace)

	if statusCode >= 500 {
		atomic.AddInt64(&r.errCount1m, 1)
	}
}

// RecordState publishes a mutation to streaming clients (spec §2.3 `state`,
// §2.4 `notifications/oumv.state`). Called on every successful applyEdit.
func (r *Runtime) RecordState(path, version, operation string) {
	if r == nil {
		return
	}
	r.appendEvent("state", map[string]string{
		"path":      path,
		"version":   version,
		"operation": operation,
	})
}

// appendEvent assigns the next monotonic id, appends to the bounded replay log and
// fans out to live subscribers without blocking them.
func (r *Runtime) appendEvent(kind string, payload interface{}) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return
	}
	event := protocol.StreamEvent{
		Type:      kind,
		Timestamp: time.Now(),
		Data:      raw,
	}

	r.mu.Lock()
	r.eventSeq++
	event.ID = r.eventSeq
	if len(r.events) >= maxStreamEvents {
		r.events = append(r.events[:0], r.events[1:]...)
	}
	r.events = append(r.events, event)
	subs := make([]chan protocol.StreamEvent, 0, len(r.subs))
	for ch := range r.subs {
		subs = append(subs, ch)
	}
	r.mu.Unlock()

	for _, ch := range subs {
		select {
		case ch <- event:
		default: // a slow subscriber drops events instead of stalling a mutation
		}
	}
}

// EventsSince returns every event with an id greater than lastID (0 = all).
func (r *Runtime) EventsSince(lastID uint64) []protocol.StreamEvent {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]protocol.StreamEvent, 0, len(r.events))
	for _, event := range r.events {
		if event.ID > lastID {
			out = append(out, event)
		}
	}
	return out
}

// SubscribeEvents returns a live event channel plus the unsubscribe func.
func (r *Runtime) SubscribeEvents() (<-chan protocol.StreamEvent, func()) {
	ch := make(chan protocol.StreamEvent, 32)
	if r == nil {
		return ch, func() {}
	}
	r.mu.Lock()
	r.subs[ch] = struct{}{}
	r.mu.Unlock()
	return ch, func() {
		r.mu.Lock()
		if _, ok := r.subs[ch]; ok {
			delete(r.subs, ch)
			close(ch)
		}
		r.mu.Unlock()
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

// RegisterRouteSource registers a route together with where its handler lives on
// disk. Adapters that hold the handler function (gin, net/http muxes) pass the
// reflection-derived file/line; L3 can then resolve the endpoint target itself.
func (r *Runtime) RegisterRouteSource(method, path, handlerName, file string, line int, middlewares ...string) {
	if file != "" {
		if abs, err := filepath.Abs(file); err == nil {
			file = abs
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.routes = append(r.routes, protocol.RouteInfo{
		Method:      method,
		Path:        path,
		HandlerName: handlerName,
		Middlewares: middlewares,
	})
	if file != "" && line > 0 {
		r.routeSources[routeKey(method, path)] = routeSource{file: file, line: line}
	}
}

func routeKey(method, path string) string {
	return strings.ToUpper(strings.TrimSpace(method)) + " " + path
}

func (r *Runtime) routeSourceFor(method, path string) (routeSource, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ref, ok := r.routeSources[routeKey(method, path)]
	return ref, ok
}

// findRouteExact matches path and (when given) method without the fuzzy handler
// matching findRoute allows.
func (r *Runtime) findRouteExact(method, path string) (protocol.RouteInfo, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, route := range r.routes {
		if route.Path != path {
			continue
		}
		if method == "" || strings.EqualFold(route.Method, method) {
			return route, true
		}
	}
	return protocol.RouteInfo{}, false
}

// Handshake performs session initialization and capability negotiation.
func (r *Runtime) Handshake(ctx context.Context, params *protocol.OumvHandshakeParams) (*protocol.OumvHandshakeResult, *protocol.OumvError) {
	// §11: serve our own version and every earlier MINOR of the same MAJOR; another
	// MAJOR (or a MINOR this runtime does not implement) is -32006, never a silent
	// echo of a version the runtime cannot honour.
	if params != nil && strings.TrimSpace(params.ProtocolVersion) != "" {
		major, minor, ok := parseProtocolVersion(params.ProtocolVersion)
		if !ok || major != 1 || minor > 6 {
			return nil, protocol.NewUnsupportedCapabilityError(
				"protocol version " + params.ProtocolVersion + " (this runtime serves " + protocol.CurrentProtocolVersion + ")",
			)
		}
	}

	caps := protocol.DefaultBackendCapabilities()
	r.mu.RLock()
	caps.Verify.SandboxRollback = r.dbSandbox != nil
	// §14.2: read-only data access is honest only with a wired, query-capable engine.
	caps.ObserveQuery = &protocol.ObserveQueryCapability{ReadOnly: r.dbSandbox.SupportsReads()}
	r.mu.RUnlock()
	caps.Transports = r.transportsOrDefault()
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
	// §8.5 read-only deployment: both writing pillars off means the writing methods
	// are absent from the registry, so the handshake says so explicitly.
	caps.ReadOnly = !r.config.Capabilities.Mutate && !r.config.Capabilities.Verify
	// §3.1: honour the client's request by narrowing the advertised set. Arrays
	// are filters (levels/formats); boolean flags express requirements, so the
	// runtime answers with what it actually serves and the client decides.
	if params != nil && params.RequestedCapabilities != nil {
		caps = narrowCapabilities(caps, *params.RequestedCapabilities)
	}

	sessionID := ""
	ttl := 0
	if r.sessions != nil {
		// Wire 1.5.0: honour a requested lifetime, clamp it, echo what was granted.
		if params != nil && params.SessionTTLSeconds != nil {
			sessionID, ttl = r.sessions.CreateWithTTL(*params.SessionTTLSeconds)
		} else {
			sessionID = r.sessions.Create()
			ttl = r.sessions.TTLSeconds()
		}
	}
	return &protocol.OumvHandshakeResult{
		ProtocolVersion: protocol.CurrentProtocolVersion,
		Profile:         "backend",
		SessionID:       sessionID,
		TTLSeconds:      ttl,
		RuntimeInfo: protocol.RuntimeInfo{
			Name:    r.name,
			Version: r.version,
		},
		Capabilities: caps,
	}, nil
}

// RegisteredMethods states the closed dispatch registry for this configuration
// (spec §3.1 rule 5 / §8.5, revision v1.11). A capability that is off contributes no
// methods at all: the dispatcher then answers -32601 before session validation, so a
// read-only deployment has no reachable code path to a write or a command execution.
func (r *Runtime) RegisteredMethods() []string {
	methods := []string{protocol.MethodHandshake, protocol.MethodDisconnect, protocol.MethodCancel, protocol.MethodQuery}
	if r.config.Capabilities.Observe {
		methods = append(methods, protocol.MethodObserve, protocol.MethodIndex)
	}
	if r.config.Capabilities.Understand {
		methods = append(methods, protocol.MethodUnderstand)
	}
	// Source read is an observation capability, not a mutation: a read-only agent
	// must be able to inspect code, so getSource survives either pillar being on.
	if r.config.Capabilities.Observe || r.config.Capabilities.Mutate {
		methods = append(methods, protocol.MethodGetSource)
	}
	if r.config.Capabilities.Mutate {
		methods = append(methods, protocol.MethodApplyEdit, protocol.MethodPreviewEdit)
	}
	if r.config.Capabilities.Verify {
		// Build executes a command, so it belongs to the writing surface and leaves
		// the registry together with verify.
		methods = append(methods, protocol.MethodVerify, protocol.MethodBuild)
	}
	return methods
}

// narrowCapabilities intersects the advertised observe levels/formats with the
// client's request. An empty intersection is reported as [] ("cannot serve"),
// never widened back to the implemented set.
func narrowCapabilities(impl, requested protocol.OumvCapabilities) protocol.OumvCapabilities {
	out := impl
	if len(requested.Observe.Levels) > 0 {
		out.Observe.Levels = intersectStrings(impl.Observe.Levels, requested.Observe.Levels)
	}
	if len(requested.Observe.Formats) > 0 {
		out.Observe.Formats = intersectStrings(impl.Observe.Formats, requested.Observe.Formats)
	}
	return out
}

func intersectStrings(implemented, requested []string) []string {
	wanted := map[string]bool{}
	for _, item := range requested {
		wanted[item] = true
	}
	out := make([]string, 0, len(implemented))
	for _, item := range implemented {
		if wanted[item] {
			out = append(out, item)
		}
	}
	return out
}

// Observe executes Pillar 1: Progressive Context Disclosure (L0 to L4).
// Observe executes Pillar 1 with the §8 ifVersion freshness handshake
// (wire 1.6.0): a matching workspaceVersion token answers the short unchanged
// envelope; a stale one answers -32010 STALE_RESOURCE_VERSION with data.current.
func (r *Runtime) Observe(ctx context.Context, params *protocol.ObserveParams) (*protocol.ObserveResult, *protocol.OumvError) {
	digest := r.WorkspaceDigest()
	if params != nil && strings.TrimSpace(params.IfVersion) != "" {
		if params.IfVersion != digest {
			return nil, &protocol.OumvError{
				Code:    protocol.ErrStaleResourceVersion,
				Message: fmt.Sprintf("STALE_RESOURCE_VERSION: the workspace changed since the caller's snapshot (expected %s, current is %s) — refetch without ifVersion", params.IfVersion, digest),
				Data:    map[string]string{"reason": "stale_version", "current": digest, "expected": params.IfVersion},
			}
		}
		level := params.Level
		if level == "" {
			level = protocol.LevelL0Pulse
		}
		return &protocol.ObserveResult{Level: level, Format: "json", Unchanged: true, WorkspaceVersion: digest}, nil
	}
	res, oerr := r.observeUncached(ctx, params)
	if oerr == nil && res != nil {
		res.WorkspaceVersion = digest
	}
	return res, oerr
}

func (r *Runtime) observeUncached(ctx context.Context, params *protocol.ObserveParams) (*protocol.ObserveResult, *protocol.OumvError) {
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
			Format:        "json",
			Data:          pulse,
			TokenEstimate: 30,
		}, nil

	case protocol.LevelL1Surface:
		r.mu.RLock()
		routes := make([]protocol.RouteInfo, len(r.routes))
		copy(routes, r.routes)
		r.mu.RUnlock()

		start, end, hasMore, next := pageBounds(len(routes), params.Limit, params.Cursor)
		page := routes[start:end]
		var more *bool
		if params.Limit > 0 {
			value := hasMore
			more = &value
		}
		return &protocol.ObserveResult{
			Level:         protocol.LevelL1Surface,
			Format:        "json",
			Data:          protocol.L1SurfaceData{Endpoints: page, HasMore: more, NextCursor: next},
			TokenEstimate: 30 + len(page)*15,
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
			Level:  protocol.LevelL2Contract,
			Format: "json",
			Data: protocol.L2ContractData{
				Target:      params.Target,
				Method:      route.Method,
				Path:        route.Path,
				HandlerName: route.HandlerName,
				Middlewares: route.Middlewares,
				// Mounted routers expose handler names, not request/response structs;
				// saying so is honest, inventing DTOs is not.
				Note: "route contract from live router registration; request/response DTOs are not reflectable from a mounted router — observe L3 with this endpoint target for the handler source",
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
		// §4 target grammar: an endpoint target is valid for L3 too. Resolving it
		// here is what turns "POST /api/products" into the handler source, instead
		// of making the agent search the workspace for the route.
		if method, endpoint, isEndpoint := parseEndpointTarget(params.Target); isEndpoint {
			route, found := r.findRouteExact(method, endpoint)
			if !found {
				return nil, protocol.NewResourceNotFoundError(params.Target)
			}
			ref, hasRef := r.routeSourceFor(route.Method, route.Path)
			if !hasRef {
				return nil, protocol.NewInvalidParamsError(
					"endpoint " + params.Target + " has no source location: the adapter registered the route without RegisterRouteSource")
			}
			data, readErr := os.ReadFile(ref.file)
			if readErr != nil {
				return nil, protocol.NewResourceNotFoundError(params.Target)
			}
			source, start, end := sliceFunctionAt(data, ref.line)
			hash := sha256.Sum256(data)
			displayPath := ref.file
			if root, absErr := filepath.Abs(r.workspaceDir); absErr == nil {
				if rel, relErr := filepath.Rel(root, ref.file); relErr == nil && !strings.HasPrefix(rel, "..") {
					displayPath = rel
				}
			}
			return &protocol.ObserveResult{
				Level:  protocol.LevelL3Source,
				Format: "json",
				Data: protocol.L3SourceData{
					Symbol:    handlerShortName(route.HandlerName),
					Path:      displayPath,
					Source:    source,
					Version:   "sha256:" + hex.EncodeToString(hash[:]),
					StartLine: start,
					EndLine:   end,
				},
				TokenEstimate: 300,
			}, nil
		}
		path, symbol := splitSourceTarget(params.Target)
		src, srcErr := r.GetSource(ctx, &protocol.GetSourceParams{Path: path, Symbol: symbol})
		if srcErr != nil {
			return nil, srcErr
		}
		return &protocol.ObserveResult{
			Level:  protocol.LevelL3Source,
			Format: "json",
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
		start, end, hasMore, next := pageBounds(count, params.Limit, params.Cursor)
		page := traces[start:end]
		var more *bool
		if params.Limit > 0 {
			value := hasMore
			more = &value
		}
		if count == 0 && note == "" {
			note = "no request traces recorded yet: call the endpoint, or use oumv/verify which feeds the trace ring"
		}
		return &protocol.ObserveResult{
			Level:  protocol.LevelL4Traces,
			Format: "json",
			Data: protocol.L4TracesData{
				Target:     params.Target,
				Traces:     page,
				TraceCount: count,
				HasMore:    more,
				NextCursor: next,
				Note:       note,
				Evidence: &protocol.EvidenceMetadata{
					Provenance:    "runtime",
					SourceAdapter: r.name,
					Confidence:    "confirmed",
				},
			},
			TokenEstimate: 40 + len(page)*15,
		}, nil

	default:
		return nil, protocol.NewUnsupportedCapabilityError(fmt.Sprintf("observe level %s", level))
	}
}

// parseProtocolVersion splits "MAJOR.MINOR[.PATCH]" into its numeric head.
func parseProtocolVersion(v string) (int, int, bool) {
	parts := strings.SplitN(strings.TrimSpace(v), ".", 3)
	if len(parts) < 2 {
		return 0, 0, false
	}
	major, err1 := strconv.Atoi(parts[0])
	minor, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return major, minor, true
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

// RecentTracesSince returns recorded traces newer than since, oldest first.
// It powers the SSE `trace` event stream (spec §2.3).
func (r *Runtime) RecentTracesSince(since time.Time) []protocol.TraceEntry {
	r.mu.RLock()
	snapshot := make([]requestTrace, len(r.traces))
	copy(snapshot, r.traces)
	r.mu.RUnlock()

	out := make([]protocol.TraceEntry, 0, len(snapshot))
	for _, tr := range snapshot {
		if !tr.Timestamp.After(since) {
			continue
		}
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
	return out
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
// Understand executes Pillar 2 with the §8 ifVersion freshness handshake
// (wire 1.6.0), mirroring Observe.
func (r *Runtime) Understand(ctx context.Context, params *protocol.UnderstandParams) (*protocol.UnderstandResult, *protocol.OumvError) {
	digest := r.WorkspaceDigest()
	if params != nil && strings.TrimSpace(params.IfVersion) != "" {
		if params.IfVersion != digest {
			return nil, &protocol.OumvError{
				Code:    protocol.ErrStaleResourceVersion,
				Message: fmt.Sprintf("STALE_RESOURCE_VERSION: the workspace changed since the caller's snapshot (expected %s, current is %s) — refetch without ifVersion", params.IfVersion, digest),
				Data:    map[string]string{"reason": "stale_version", "current": digest, "expected": params.IfVersion},
			}
		}
		query := params.Query
		if query == "" {
			query = protocol.QueryCausality
		}
		return &protocol.UnderstandResult{Query: query, Unchanged: true, WorkspaceVersion: digest}, nil
	}
	res, oerr := r.understandUncached(ctx, params)
	if oerr == nil && res != nil {
		res.WorkspaceVersion = digest
	}
	return res, oerr
}

func (r *Runtime) understandUncached(ctx context.Context, params *protocol.UnderstandParams) (*protocol.UnderstandResult, *protocol.OumvError) {
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

	version := hashVersion(data)

	// Dedup pointer (wire 1.3.0): the file still has the version the agent already
	// saw, so there is nothing to resend — version alone keeps OCC usable.
	if params.IfVersion != "" && params.IfVersion == version {
		return &protocol.GetSourceResult{
			Path:      params.Path,
			Symbol:    params.Symbol,
			Unchanged: true,
			Version:   version,
		}, nil
	}

	lines := strings.Split(string(data), "\n")
	startLine, endLine := 1, len(lines)

	// If symbol is specified, parse the file and locate the exact declaration. A
	// symbol that is not there is -32009; returning the whole file under a symbol
	// label would make the agent believe it read the thing it asked for.
	if params.Symbol != "" {
		fset := token.NewFileSet()
		node, parseErr := parser.ParseFile(fset, cleanPath, data, parser.ParseComments)
		if parseErr != nil {
			return nil, protocol.NewSyntaxError(
				fmt.Sprintf("failed to parse %s: %v", params.Path, parseErr),
				parseErr.Error(),
			)
		}
		found := false
		for _, decl := range node.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == params.Symbol {
				startLine = fset.Position(fn.Pos()).Line
				endLine = fset.Position(fn.End()).Line
				found = true
				break
			}
		}
		if !found {
			return nil, protocol.NewResourceNotFoundError(params.Symbol + " in " + params.Path)
		}
	}

	// Window (wire 1.3.0): 1-based inclusive; an endLine past EOF is clamped.
	if params.StartLine > 0 {
		startLine = params.StartLine
	}
	if params.EndLine > 0 {
		endLine = params.EndLine
	}
	if startLine < 1 {
		startLine = 1
	}
	if startLine > len(lines) {
		startLine = len(lines)
	}
	if endLine > len(lines) {
		endLine = len(lines)
	}
	if endLine < startLine {
		endLine = startLine
	}

	return &protocol.GetSourceResult{
		Symbol:    params.Symbol,
		Path:      params.Path,
		Source:    strings.Join(lines[startLine-1:endLine], "\n"),
		Version:   version,
		StartLine: startLine,
		EndLine:   endLine,
	}, nil
}

// ApplyEdit executes Pillar 3B: Applies source edit with OCC and pre-flight syntax checks.
// ApplyEdit applies a mutation and, on success, publishes the new resource state
// to streaming clients (spec §2.3 `event: state`, §2.4 notifications/oumv.state).
// idempotencyKey scopes a client key to its session (§3).
func idempotencyKey(sessionID, key string) string {
	return sessionID + ":" + key
}

// ApplyEdit applies an OCC edit — with §3 idempotency replay when a key is given.
func (r *Runtime) ApplyEdit(ctx context.Context, params *protocol.ApplyEditParams) (*protocol.ApplyEditResult, *protocol.OumvError) {
	if params != nil && strings.TrimSpace(params.IdempotencyKey) != "" {
		key := idempotencyKey(params.SessionID, params.IdempotencyKey)
		if cached, ok := r.idempotency.Load(key); ok {
			return cached.(*protocol.ApplyEditResult), nil
		}
		result, err := r.applyEditUncached(ctx, params)
		if err == nil && result != nil && result.Status == "APPLIED" {
			r.idempotency.Store(key, result)
		}
		return result, err
	}
	return r.applyEditUncached(ctx, params)
}

func (r *Runtime) applyEditUncached(ctx context.Context, params *protocol.ApplyEditParams) (*protocol.ApplyEditResult, *protocol.OumvError) {
	// Wire 1.5.0: a request carrying `edits` is a multi-file atomic batch.
	if params != nil && len(params.Edits) > 0 {
		result, err := r.applyEditBatch(ctx, params)
		if err == nil && result != nil {
			for _, item := range result.Edits {
				r.RecordState(item.Path, item.NewVersion, "batch")
			}
		}
		return result, err
	}
	result, err := r.applyEdit(ctx, params)
	if err == nil && result != nil && result.Status == "APPLIED" {
		operation := strings.ToLower(strings.TrimSpace(params.Mode))
		if operation == "" {
			operation = "replace"
		}
		r.RecordState(params.Path, result.NewVersion, operation)
	}
	return result, err
}

// batchEdit is one validated, staged file mutation.
type batchEdit struct {
	cleanPath    string
	relPath      string
	content      []byte
	created      bool
	hunksApplied int
}

// batchWrite records a file already written so a later failure can restore it.
type batchWrite struct {
	path     string
	previous []byte
	created  bool
}

// applyEditBatch executes a multi-file atomic mutation (wire 1.5.0, spec §4.3):
// every item is jail-checked, version-checked, hunk-resolved and syntax-checked
// before the first byte is written; a write failure restores what was written.
func (r *Runtime) applyEditBatch(ctx context.Context, params *protocol.ApplyEditParams) (*protocol.ApplyEditResult, *protocol.OumvError) {
	if !r.config.Capabilities.Mutate {
		return nil, protocol.NewPermissionDeniedError("mutate capability is disabled by policy")
	}
	items := params.Edits
	if len(items) == 0 || len(items) > 16 {
		return nil, protocol.NewValidationFailedError(
			"VALIDATION_FAILED: edits must hold 1..16 items",
			map[string]interface{}{"reason": "batch_size", "count": len(items)},
		)
	}

	seen := make(map[string]int, len(items))
	staged := make([]batchEdit, 0, len(items))
	for index, item := range items {
		annotate := func(err *protocol.OumvError) *protocol.OumvError {
			return annotateEditError(err, index, item.Path)
		}
		cleanPath, pathErr := validateWorkspacePath(r.workspaceDir, item.Path)
		if pathErr != nil {
			return nil, annotate(pathErr)
		}
		key := filepath.Clean(cleanPath)
		if previous, duplicate := seen[key]; duplicate {
			return nil, annotate(protocol.NewValidationFailedError(
				"VALIDATION_FAILED: duplicate path in batch (also item "+strconv.Itoa(previous)+")",
				map[string]interface{}{"reason": "duplicate_path"},
			))
		}
		seen[key] = index

		mode := strings.ToLower(strings.TrimSpace(item.Mode))
		if mode == "" {
			mode = "replace"
		}
		switch mode {
		case "replace", "create", "patch":
		default:
			return nil, annotate(&protocol.OumvError{
				Code:    protocol.ErrUnsupportedCapability,
				Message: "UNSUPPORTED_CAPABILITY: applyEdit batch mode " + item.Mode + " (only replace/create/patch are atomic)",
				Data:    map[string]interface{}{"reason": "unknown_mode"},
			})
		}

		switch mode {
		case "create":
			if strings.TrimSpace(item.Replacement) == "" {
				return nil, annotate(protocol.NewValidationFailedError(
					"replacement is required for mode=create", map[string]interface{}{"reason": "replacement_required"}))
			}
			if item.ExpectedVersion != "" {
				return nil, annotate(protocol.NewValidationFailedError(
					"VALIDATION_FAILED: expectedVersion must be omitted for mode=create",
					map[string]interface{}{"reason": "create_forbids_version"}))
			}
			if _, statErr := os.Stat(cleanPath); statErr == nil {
				return nil, annotate(protocol.NewConflictError(
					"CONFLICT_DETECTED: file already exists; use mode=replace with its version",
					map[string]interface{}{"reason": "file_exists"}))
			}
			if syntaxErr := checkGoSyntax(cleanPath, item.Replacement); syntaxErr != nil {
				return nil, annotate(syntaxErr)
			}
			staged = append(staged, batchEdit{cleanPath: cleanPath, relPath: item.Path, content: []byte(item.Replacement), created: true})

		case "replace":
			currentData, readErr := os.ReadFile(cleanPath)
			if readErr != nil {
				return nil, annotate(protocol.NewResourceNotFoundError(item.Path))
			}
			if strings.TrimSpace(item.Replacement) == "" {
				return nil, annotate(protocol.NewValidationFailedError(
					"VALIDATION_FAILED: replacement is required for mode=replace",
					map[string]interface{}{"reason": "replacement_required"}))
			}
			currentVersion := hashVersion(currentData)
			if strings.TrimSpace(item.ExpectedVersion) == "" {
				return nil, annotate(protocol.NewValidationFailedError(
					"VALIDATION_FAILED: expectedVersion is required to edit an existing file",
					map[string]interface{}{"reason": "expected_version_required", "current": currentVersion}))
			}
			if item.ExpectedVersion != currentVersion {
				return nil, annotate(protocol.NewConflictError(
					"version mismatch: expected "+item.ExpectedVersion+", current is "+currentVersion,
					map[string]interface{}{"reason": "version_mismatch", "expected": item.ExpectedVersion, "current": currentVersion}))
			}
			if syntaxErr := checkGoSyntax(cleanPath, item.Replacement); syntaxErr != nil {
				return nil, annotate(syntaxErr)
			}
			staged = append(staged, batchEdit{cleanPath: cleanPath, relPath: item.Path, content: []byte(item.Replacement)})

		case "patch":
			currentData, readErr := os.ReadFile(cleanPath)
			if readErr != nil {
				return nil, annotate(protocol.NewResourceNotFoundError(item.Path))
			}
			if len(item.Hunks) == 0 {
				return nil, annotate(protocol.NewValidationFailedError(
					"VALIDATION_FAILED: hunks is required for mode=patch",
					map[string]interface{}{"reason": "hunks_required"}))
			}
			currentVersion := hashVersion(currentData)
			if strings.TrimSpace(item.ExpectedVersion) == "" {
				return nil, annotate(protocol.NewValidationFailedError(
					"VALIDATION_FAILED: expectedVersion is required to edit an existing file",
					map[string]interface{}{"reason": "expected_version_required", "current": currentVersion}))
			}
			if item.ExpectedVersion != currentVersion {
				return nil, annotate(protocol.NewConflictError(
					"version mismatch: expected "+item.ExpectedVersion+", current is "+currentVersion,
					map[string]interface{}{"reason": "version_mismatch", "expected": item.ExpectedVersion, "current": currentVersion}))
			}
			patched, patchErr := applyHunks(string(currentData), item.Hunks)
			if patchErr != nil {
				return nil, annotate(patchErr)
			}
			if syntaxErr := checkGoSyntax(cleanPath, patched); syntaxErr != nil {
				return nil, annotate(syntaxErr)
			}
			staged = append(staged, batchEdit{
				cleanPath: cleanPath, relPath: item.Path, content: []byte(patched), hunksApplied: len(item.Hunks),
			})
		}
	}

	// Write phase. Nothing has touched the disk until here, so a failure in
	// staging above left the workspace exactly as it was.
	written := make([]batchWrite, 0, len(staged))
	for index, edit := range staged {
		var previous []byte
		if !edit.created {
			previous, _ = os.ReadFile(edit.cleanPath)
		}
		if edit.created {
			if mkdirErr := os.MkdirAll(filepath.Dir(edit.cleanPath), 0o755); mkdirErr != nil {
				return nil, batchWriteError(written, index, edit.relPath, mkdirErr)
			}
		}
		if writeErr := os.WriteFile(edit.cleanPath, edit.content, 0o644); writeErr != nil {
			return nil, batchWriteError(written, index, edit.relPath, writeErr)
		}
		written = append(written, batchWrite{path: edit.cleanPath, previous: previous, created: edit.created})
	}

	results := make([]protocol.ApplyEditBatchResultItem, 0, len(staged))
	for _, edit := range staged {
		results = append(results, protocol.ApplyEditBatchResultItem{
			Path:         edit.relPath,
			NewVersion:   hashVersion(edit.content),
			Created:      edit.created,
			HunksApplied: edit.hunksApplied,
		})
	}
	return &protocol.ApplyEditResult{Status: "APPLIED", Edits: results, Success: true}, nil
}

// batchWriteError restores what the batch already wrote and reports honestly
// whether the restore succeeded.
func batchWriteError(written []batchWrite, index int, relPath string, cause error) *protocol.OumvError {
	restored, failures := restoreBatchWrites(written)
	data := map[string]interface{}{
		"editIndex":  index,
		"path":       relPath,
		"rolledBack": restored,
	}
	if len(failures) > 0 {
		data["rollbackErrors"] = failures
	}
	return &protocol.OumvError{
		Code:    protocol.ErrInternalError,
		Message: "failed to persist edit: " + cause.Error(),
		Data:    data,
	}
}

// restoreBatchWrites rewinds already-written files in reverse order: created
// files are removed, replaced files get their previous bytes back.
func restoreBatchWrites(written []batchWrite) (bool, []string) {
	ok := true
	var failures []string
	for i := len(written) - 1; i >= 0; i-- {
		w := written[i]
		if w.created {
			if err := os.Remove(w.path); err != nil && !os.IsNotExist(err) {
				ok = false
				failures = append(failures, w.path+": "+err.Error())
			}
			continue
		}
		if err := os.WriteFile(w.path, w.previous, 0o644); err != nil {
			ok = false
			failures = append(failures, w.path+": "+err.Error())
		}
	}
	return ok, failures
}

// annotateEditError adds the failing item index (and path) to an item-scoped error.
func annotateEditError(err *protocol.OumvError, index int, path string) *protocol.OumvError {
	if err == nil {
		return nil
	}
	data := map[string]interface{}{}
	switch existing := err.Data.(type) {
	case map[string]string:
		for key, value := range existing {
			data[key] = value
		}
	case map[string]interface{}:
		for key, value := range existing {
			data[key] = value
		}
	}
	data["editIndex"] = index
	if path != "" {
		data["path"] = path
	}
	err.Data = data
	return err
}

// checkGoSyntax runs the pre-flight parser over staged content.
// checkGoSyntax runs the §4.3 syntax pre-flight for Go content. Conforming
// agents may edit fragment-style files (fixture text, config-like sources that
// lack a package clause), so a full-file parse failure caused ONLY by a missing
// package clause falls back to parsing the content as a function body fragment —
// the same leniency the Python/Node/PHP pre-flights show. Real syntax errors
// still fail with -32002.
func checkGoSyntax(path, content string) *protocol.OumvError {
	fset := token.NewFileSet()
	if _, parseErr := parser.ParseFile(fset, path, []byte(content), parser.AllErrors); parseErr == nil {
		return nil
	} else if !isPackageClauseError(parseErr) {
		return protocol.NewSyntaxError(fmt.Sprintf("pre-flight syntax check failed: %v", parseErr), parseErr.Error())
	}
	wrapped := "package __oumv_fragment\n\nfunc __oumv_fragment() {\n" + content + "\n}\n"
	if _, wrapErr := parser.ParseFile(fset, path, []byte(wrapped), parser.AllErrors); wrapErr != nil {
		return protocol.NewSyntaxError(fmt.Sprintf("pre-flight syntax check failed: %v", wrapErr), wrapErr.Error())
	}
	return nil
}

// isPackageClauseError reports whether the parse failed because the content is
// not a self-contained Go file (no package clause), not because it is broken.
func isPackageClauseError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "expected 'package'")
}

func (r *Runtime) applyEdit(ctx context.Context, params *protocol.ApplyEditParams) (*protocol.ApplyEditResult, *protocol.OumvError) {
	if !r.config.Capabilities.Mutate {
		return nil, protocol.NewPermissionDeniedError("mutate capability is disabled by policy")
	}

	cleanPath, pathErr := validateWorkspacePath(r.workspaceDir, params.Path)
	if pathErr != nil {
		return nil, pathErr
	}

	mode := strings.ToLower(strings.TrimSpace(params.Mode))
	if mode == "" {
		mode = "replace"
	}
	switch mode {
	case "replace", "create", "symbol", "delete", "rename", "patch":
	default:
		return nil, &protocol.OumvError{
			Code:    protocol.ErrUnsupportedCapability,
			Message: "UNSUPPORTED_CAPABILITY: unknown applyEdit mode " + params.Mode,
			Data:    map[string]string{"reason": "unknown_mode"},
		}
	}

	// create mode (spec §4.3): the file must not exist and expectedVersion is forbidden,
	// so an agent can add new code without falling back to shell redirects.
	if mode == "create" {
		if strings.TrimSpace(params.Replacement) == "" {
			return nil, protocol.NewInvalidParamsError("replacement is required for mode=create")
		}
		if params.ExpectedVersion != "" {
			return nil, protocol.NewValidationFailedError(
				"VALIDATION_FAILED: expectedVersion must be omitted for mode=create",
				map[string]string{"reason": "create_forbids_version"},
			)
		}
		if _, statErr := os.Stat(cleanPath); statErr == nil {
			return nil, protocol.NewConflictError(
				"CONFLICT_DETECTED: file already exists; use mode=replace with its version",
				map[string]string{"reason": "file_exists"},
			)
		}
		if syntaxErr := checkGoSyntax(cleanPath, params.Replacement); syntaxErr != nil {
			return nil, syntaxErr
		}
		if err := os.MkdirAll(filepath.Dir(cleanPath), 0o755); err != nil {
			return nil, &protocol.OumvError{Code: protocol.ErrInternalError, Message: "failed to create directory: " + err.Error()}
		}
		if err := os.WriteFile(cleanPath, []byte(params.Replacement), 0o644); err != nil {
			return nil, &protocol.OumvError{Code: protocol.ErrInternalError, Message: "failed to persist new file: " + err.Error()}
		}
		newHash := sha256.Sum256([]byte(params.Replacement))
		return &protocol.ApplyEditResult{
			Status:      "APPLIED",
			Path:        params.Path,
			NewVersion:  "sha256:" + hex.EncodeToString(newHash[:]),
			SyntaxCheck: &protocol.SyntaxCheck{Status: "passed", Tool: "go/parser"},
			Created:     true,
			Success:     true,
		}, nil
	}

	// patch mode (spec §4.3, wire 1.3.0): splice exact snippets into an existing file.
	// Hunks apply in order to the evolving text, so a later hunk matches what the
	// earlier ones produced; the first failure aborts with no bytes written.
	if mode == "patch" {
		currentData, readErr := os.ReadFile(cleanPath)
		if readErr != nil {
			return nil, protocol.NewResourceNotFoundError(params.Path)
		}
		if len(params.Hunks) == 0 {
			return nil, protocol.NewValidationFailedError(
				"VALIDATION_FAILED: hunks is required for mode=patch",
				map[string]string{"reason": "hunks_required"},
			)
		}
		// §4.3 guard order: splice the hunks, syntax-check the RESULTING file
		// (step 3), and only then assert the version rules (steps 4-5). Nothing
		// is written until every guard passed.
		patched, patchErr := applyHunks(string(currentData), params.Hunks)
		if patchErr != nil {
			return nil, patchErr
		}
		if syntaxErr := checkGoSyntax(cleanPath, patched); syntaxErr != nil {
			return nil, syntaxErr
		}
		currentVersion := hashVersion(currentData)
		if strings.TrimSpace(params.ExpectedVersion) == "" {
			return nil, protocol.NewValidationFailedError(
				"VALIDATION_FAILED: expectedVersion is required to edit an existing file — call oumv/getSource (or observe L3) and pass its version",
				map[string]string{"path": params.Path, "current": currentVersion},
			)
		}
		if params.ExpectedVersion != currentVersion {
			return nil, protocol.NewConflictError(
				fmt.Sprintf("version mismatch: expected %s, current is %s", params.ExpectedVersion, currentVersion),
				map[string]string{"expected": params.ExpectedVersion, "current": currentVersion},
			)
		}
		backupPath := r.backupPrevious(cleanPath, currentData)
		if err := os.WriteFile(cleanPath, []byte(patched), 0644); err != nil {
			return nil, &protocol.OumvError{
				Code:    protocol.ErrInternalError,
				Message: "failed to persist edit: " + err.Error(),
			}
		}
		return &protocol.ApplyEditResult{
			Status:       "APPLIED",
			Path:         params.Path,
			NewVersion:   hashVersion([]byte(patched)),
			SyntaxCheck:  &protocol.SyntaxCheck{Status: "passed", Tool: "go/parser"},
			HunksApplied: len(params.Hunks),
			BackupPath:   backupPath,
			RollbackHint: orOmit(backupPath),
			Success:      true,
			Message:      fmt.Sprintf("%d hunk(s) applied", len(params.Hunks)),
		}, nil
	}

	if mode == "delete" {
		if strings.TrimSpace(params.Replacement) != "" {
			return nil, protocol.NewValidationFailedError(
				"VALIDATION_FAILED: replacement must be omitted for mode=delete",
				map[string]string{"reason": "replacement_forbidden"},
			)
		}
		currentData, readErr := os.ReadFile(cleanPath)
		if readErr != nil {
			return nil, protocol.NewResourceNotFoundError(params.Path)
		}
		if strings.TrimSpace(params.ExpectedVersion) == "" {
			return nil, protocol.NewValidationFailedError(
				"VALIDATION_FAILED: expectedVersion is required to delete a file",
				map[string]string{"reason": "expected_version_required", "path": params.Path},
			)
		}
		if params.ExpectedVersion != hashVersion(currentData) {
			return nil, protocol.NewConflictError(
				"CONFLICT_DETECTED: version mismatch on delete",
				map[string]string{"expected": params.ExpectedVersion, "current": hashVersion(currentData)},
			)
		}
		if removeErr := os.Remove(cleanPath); removeErr != nil {
			return nil, &protocol.OumvError{Code: protocol.ErrInternalError, Message: "failed to delete file: " + removeErr.Error()}
		}
		return &protocol.ApplyEditResult{
			Status:      "APPLIED",
			Path:        params.Path,
			SyntaxCheck: &protocol.SyntaxCheck{Status: "skipped"},
			Deleted:     true,
			Success:     true,
		}, nil
	}

	if mode == "rename" {
		if strings.TrimSpace(params.RenameTo) == "" {
			return nil, protocol.NewValidationFailedError(
				"VALIDATION_FAILED: renameTo is required for mode=rename",
				map[string]string{"reason": "rename_to_required"},
			)
		}
		if strings.TrimSpace(params.Replacement) != "" {
			return nil, protocol.NewValidationFailedError(
				"VALIDATION_FAILED: replacement must be omitted for mode=rename",
				map[string]string{"reason": "replacement_forbidden"},
			)
		}
		targetPath, targetErr := validateWorkspacePath(r.workspaceDir, params.RenameTo)
		if targetErr != nil {
			return nil, targetErr
		}
		currentData, readErr := os.ReadFile(cleanPath)
		if readErr != nil {
			return nil, protocol.NewResourceNotFoundError(params.Path)
		}
		if _, statErr := os.Stat(targetPath); statErr == nil {
			return nil, protocol.NewConflictError(
				"CONFLICT_DETECTED: rename target already exists",
				map[string]string{"reason": "target_exists", "renameTo": params.RenameTo},
			)
		}
		if strings.TrimSpace(params.ExpectedVersion) == "" {
			return nil, protocol.NewValidationFailedError(
				"VALIDATION_FAILED: expectedVersion is required to rename a file",
				map[string]string{"reason": "expected_version_required", "path": params.Path},
			)
		}
		if params.ExpectedVersion != hashVersion(currentData) {
			return nil, protocol.NewConflictError(
				"CONFLICT_DETECTED: version mismatch on rename",
				map[string]string{"expected": params.ExpectedVersion, "current": hashVersion(currentData)},
			)
		}
		if mkdirErr := os.MkdirAll(filepath.Dir(targetPath), 0o755); mkdirErr != nil {
			return nil, &protocol.OumvError{Code: protocol.ErrInternalError, Message: "failed to create rename target directory: " + mkdirErr.Error()}
		}
		if renameErr := os.Rename(cleanPath, targetPath); renameErr != nil {
			return nil, &protocol.OumvError{Code: protocol.ErrInternalError, Message: "failed to rename file: " + renameErr.Error()}
		}
		return &protocol.ApplyEditResult{
			Status:      "APPLIED",
			Path:        params.Path,
			NewPath:     params.RenameTo,
			SyntaxCheck: &protocol.SyntaxCheck{Status: "skipped"},
			Success:     true,
		}, nil
	}

	if mode == "symbol" {
		if strings.TrimSpace(params.Symbol) == "" {
			return nil, protocol.NewValidationFailedError(
				"VALIDATION_FAILED: symbol is required for mode=symbol",
				map[string]string{"reason": "symbol_required"},
			)
		}
		if _, statErr := os.Stat(cleanPath); statErr != nil {
			return nil, protocol.NewResourceNotFoundError(params.Path)
		}
		if strings.TrimSpace(params.ExpectedVersion) == "" {
			return nil, protocol.NewValidationFailedError(
				"VALIDATION_FAILED: expectedVersion is required to edit a symbol",
				map[string]string{"reason": "expected_version_required", "path": params.Path},
			)
		}
		if r.patcher == nil {
			return nil, &protocol.OumvError{Code: protocol.ErrUnsupportedCapability, Message: "UNSUPPORTED_CAPABILITY: symbol edits need the AST patcher"}
		}
		previous, _ := os.ReadFile(cleanPath)
		backupPath := r.backupPrevious(cleanPath, previous)
		patcher := mutate.NewPatcher(r.workspaceDir)
		patchRes, patchErr := patcher.ApplySymbolPatch(params.Path, params.Symbol, params.Replacement, params.ExpectedVersion)
		if patchErr != nil {
			return nil, patchErr
		}
		return &protocol.ApplyEditResult{
			Status:       "APPLIED",
			Path:         params.Path,
			Symbol:       params.Symbol,
			NewVersion:   patchRes.NewVersion,
			SyntaxCheck:  &protocol.SyntaxCheck{Status: "passed", Tool: "go/parser"},
			BackupPath:   backupPath,
			RollbackHint: orOmit(backupPath),
			Success:      true,
			LinesChanged: patchRes.LinesChanged,
		}, nil
	}

	if strings.TrimSpace(params.Replacement) == "" {
		return nil, protocol.NewValidationFailedError(
			"VALIDATION_FAILED: replacement is required for mode=replace",
			map[string]string{"reason": "replacement_required"},
		)
	}

	currentData, err := os.ReadFile(cleanPath)
	if err != nil {
		return nil, protocol.NewResourceNotFoundError(params.Path)
	}

	currentHash := sha256.Sum256(currentData)
	currentVersion := "sha256:" + hex.EncodeToString(currentHash[:])

	// §4.3 guard order: syntax pre-flight (step 3) runs before the version rules
	// (steps 4-5), matching the normative table and the other runtimes.
	if syntaxErr := checkGoSyntax(cleanPath, params.Replacement); syntaxErr != nil {
		return nil, syntaxErr
	}

	// 4. expectedVersion presence: every edit of an existing file must name the
	// version it was based on, so a blind write can never clobber a concurrent change.
	if strings.TrimSpace(params.ExpectedVersion) == "" {
		return nil, protocol.NewValidationFailedError(
			"VALIDATION_FAILED: expectedVersion is required to edit an existing file — call oumv/getSource (or observe L3) and pass its version",
			map[string]string{"path": params.Path, "current": currentVersion},
		)
	}

	// 5. Optimistic Concurrency Control (OCC) guard
	if params.ExpectedVersion != currentVersion {
		return nil, protocol.NewConflictError(
			fmt.Sprintf("version mismatch: expected %s, current is %s", params.ExpectedVersion, currentVersion),
			map[string]string{"expected": params.ExpectedVersion, "current": currentVersion},
		)
	}

	// 6. Backup the previous bytes, then write validated content to disk.
	backupPath := r.backupPrevious(cleanPath, currentData)
	if err := os.WriteFile(cleanPath, []byte(params.Replacement), 0644); err != nil {
		return nil, &protocol.OumvError{
			Code:    protocol.ErrInternalError,
			Message: "failed to persist edit: " + err.Error(),
		}
	}

	newHash := sha256.Sum256([]byte(params.Replacement))
	newVersion := "sha256:" + hex.EncodeToString(newHash[:])

	return &protocol.ApplyEditResult{
		Status:       "APPLIED",
		Path:         params.Path,
		Symbol:       params.Symbol,
		NewVersion:   newVersion,
		SyntaxCheck:  &protocol.SyntaxCheck{Status: "passed", Tool: "go/parser"},
		BackupPath:   backupPath,
		RollbackHint: orOmit(backupPath),
		Success:      true,
		LinesChanged: len(strings.Split(params.Replacement, "\n")),
	}, nil
}

// ValidateSession reports whether a handshake-issued session is still live.
// An empty id is accepted: stateless callers are allowed by spec §3.
func (r *Runtime) ValidateSession(id string) bool {
	if r == nil || r.sessions == nil {
		return id == ""
	}
	return r.sessions.Validate(id)
}

// Disconnect closes a session (oumv/disconnect).
func (r *Runtime) Disconnect(id string) bool {
	if r == nil || r.sessions == nil {
		return false
	}
	return r.sessions.Delete(id)
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
// Verify dispatches an action and, when `sandbox:{mode:"rollback"}` is requested,
// runs the whole request inside a database transaction that is always rolled back
// (spec §4, wire 1.4.0). Evidence is reported, never assumed: rolledBack is set
// only after ExecuteDryRun confirms the transaction was not committed.
func (r *Runtime) Verify(ctx context.Context, params *protocol.VerifyParams) (*protocol.VerifyResult, *protocol.OumvError) {
	if params != nil && params.Sandbox != nil && strings.TrimSpace(params.Sandbox.Mode) != "" {
		mode := strings.ToLower(strings.TrimSpace(params.Sandbox.Mode))
		if mode != "rollback" {
			return nil, &protocol.OumvError{
				Code:    protocol.ErrUnsupportedCapability,
				Message: "UNSUPPORTED_CAPABILITY: unknown sandbox mode " + params.Sandbox.Mode,
				Data:    map[string]string{"reason": "unknown_sandbox_mode"},
			}
		}
		r.mu.RLock()
		sandbox := r.dbSandbox
		r.mu.RUnlock()
		if sandbox == nil {
			return nil, &protocol.OumvError{
				Code:    protocol.ErrRuntimeUnavailable,
				Message: "RUNTIME_UNAVAILABLE: sandbox=rollback requested but no database sandbox is configured",
			}
		}

		var result *protocol.VerifyResult
		var verifyErr *protocol.OumvError
		report, err := sandbox.ExecuteDryRun(ctx, func(dryCtx context.Context, tx *sql.Tx) error {
			result, verifyErr = r.verify(dryCtx, params, tx)
			return nil
		})
		if err != nil {
			return nil, &protocol.OumvError{
				Code:    protocol.ErrInternalError,
				Message: "DB_SANDBOX_ERROR: " + err.Error(),
			}
		}
		if verifyErr != nil {
			return nil, verifyErr
		}

		rolledBack := report != nil && !report.Committed
		result.RolledBack = &rolledBack
		if rolledBack {
			result.RollbackStatus = "verified"
			result.DBDiff = &protocol.VerifyDBDiff{Rollback: true, Driver: "database/sql"}
			if result.Scope != nil {
				kept := make([]string, 0, len(result.Scope.NotExecuted))
				for _, item := range result.Scope.NotExecuted {
					if item != "persistent database state" {
						kept = append(kept, item)
					}
				}
				result.Scope.NotExecuted = kept
			}
		} else {
			result.RollbackStatus = "unverified"
		}
		return result, nil
	}
	return r.verify(ctx, params, nil)
}

func (r *Runtime) verify(ctx context.Context, params *protocol.VerifyParams, tx *sql.Tx) (*protocol.VerifyResult, *protocol.OumvError) {
	if !r.config.Capabilities.Verify {
		return nil, protocol.NewPermissionDeniedError("verify capability is disabled by policy")
	}
	r.mu.RLock()
	handler := r.handler
	r.mu.RUnlock()
	if handler == nil {
		return nil, &protocol.OumvError{
			Code:    protocol.ErrRuntimeUnavailable,
			Message: "RUNTIME_UNAVAILABLE: no active HTTP handler configured for in-memory verification",
		}
	}

	dispatch := func(target, methodOverride string, payload interface{}, headers map[string]string) (string, string, *httptest.ResponseRecorder, float64) {
		stepStart := time.Now()
		method := "GET"
		path := target
		if parts := strings.SplitN(target, " ", 2); len(parts) == 2 {
			method, path = parts[0], parts[1]
		}
		if override := strings.ToUpper(strings.TrimSpace(methodOverride)); override != "" {
			method = override
		}
		var body io.Reader
		if payload != nil {
			if raw, marshalErr := json.Marshal(payload); marshalErr == nil {
				body = bytes.NewReader(raw)
			}
		}
		req := httptest.NewRequest(method, path, body)
		if tx != nil && ctx != nil {
			req = req.WithContext(context.WithValue(ctx, db.TxContextKey, tx))
			req.Header.Set("X-TokenSafe-Dry-Run", "true")
		} else if ctx != nil {
			req = req.WithContext(ctx)
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return method, path, rec, float64(time.Since(stepStart).Microseconds()) / 1000.0
	}

	// Multi-step scenario (spec §4.4): every step hits the same handler instance, so
	// state created by an earlier request is visible to the next one.
	if len(params.Steps) > 0 {
		results := make([]protocol.VerifyStepResult, 0, len(params.Steps))
		total := 0.0
		for _, step := range params.Steps {
			m, p, rec, duration := dispatch(step.Target, step.Method, step.Payload, step.Headers)
			total += duration
			stepStatus := "failed"
			if rec.Code < 400 {
				stepStatus = "passed"
			}
			results = append(results, protocol.VerifyStepResult{
				Target: step.Target, StatusCode: rec.Code, Status: stepStatus, ResponseBody: rec.Body.String(),
			})
			r.RecordTrace(m, p, p, "", rec.Code, duration, "")
		}
		last := results[len(results)-1]
		status := "passed"
		for _, step := range results {
			if step.Status != "passed" {
				status = "failed"
				break
			}
		}
		return &protocol.VerifyResult{
			Success:    status == "passed",
			Status:     status,
			Target:     params.Target,
			StatusCode: last.StatusCode,
			Diff: map[string]interface{}{
				"stepCount":    len(results),
				"status":       last.StatusCode,
				"responseBody": last.ResponseBody,
			},
			Timings:    protocol.LatencyBreakdown{TActionMs: total, TTotalMs: total},
			DurationMs: total,
			Scope: &protocol.VerifyScope{
				Checked:      []string{"ordered multi-step dispatch against one application instance"},
				NotExecuted:  []string{"external processes", "real network", "persistent database state"},
				NotSupported: []string{"browser/DOM assertions"},
			},
			Steps: results,
		}, nil
	}

	method, path, rec, duration := dispatch(params.Target, params.Method, params.Payload, params.Headers)

	// Feed the L4 trace ring so Understand(causality) has real evidence after verification
	r.RecordTrace(method, path, path, "", rec.Code, duration, "")

	status := "failed"
	if rec.Code < 400 {
		status = "passed"
	}
	return &protocol.VerifyResult{
		Success:    rec.Code < 400,
		Status:     status,
		Target:     params.Target,
		StatusCode: rec.Code,
		Diff: map[string]interface{}{
			"responseBody": rec.Body.String(),
			"status":       rec.Code,
		},
		Timings: protocol.LatencyBreakdown{
			TActionMs: duration,
			TTotalMs:  duration,
		},
		DurationMs: duration,
		Scope: &protocol.VerifyScope{
			Checked:      []string{"in-memory dispatch against the live handler"},
			NotExecuted:  []string{"external processes", "real network", "persistent database state"},
			NotSupported: []string{"browser/DOM assertions"},
		},
	}, nil
}

// ---- endpoint → source resolution (L3 with a route target) ----------------

var httpMethods = map[string]bool{
	"GET": true, "POST": true, "PUT": true, "PATCH": true,
	"DELETE": true, "HEAD": true, "OPTIONS": true,
}

// parseEndpointTarget implements the §4 target grammar for the two endpoint
// forms: "METHOD /path" and "/path".
func parseEndpointTarget(target string) (method, path string, ok bool) {
	trimmed := strings.TrimSpace(target)
	if strings.HasPrefix(trimmed, "/") {
		return "", trimmed, true
	}
	parts := strings.SplitN(trimmed, " ", 2)
	if len(parts) == 2 && strings.HasPrefix(parts[1], "/") && httpMethods[strings.ToUpper(parts[0])] {
		return strings.ToUpper(parts[0]), parts[1], true
	}
	return "", "", false
}

// handlerShortName strips the package path from a fully-qualified handler name.
func handlerShortName(name string) string {
	if i := strings.LastIndex(name, "/"); i >= 0 {
		return name[i+1:]
	}
	return name
}

// sliceFunctionAt returns the innermost function (declaration or closure) that
// encloses a line. Handlers registered by a builder are closures, so the smallest
// enclosing function is the handler body, not the whole builder.
func sliceFunctionAt(data []byte, line int) (string, int, int) {
	lines := strings.Split(string(data), "\n")
	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, "handler.go", data, parser.ParseComments)
	if err != nil {
		return string(data), 1, len(lines)
	}
	bestStart, bestEnd := 0, 0
	ast.Inspect(node, func(n ast.Node) bool {
		switch n.(type) {
		case *ast.FuncDecl, *ast.FuncLit:
		default:
			return true
		}
		start := fset.Position(n.Pos()).Line
		end := fset.Position(n.End()).Line
		if start <= line && line <= end && (bestStart == 0 || end-start < bestEnd-bestStart) {
			bestStart, bestEnd = start, end
		}
		return true
	})
	if bestStart == 0 || bestStart > len(lines) || bestEnd > len(lines) {
		return string(data), 1, len(lines)
	}
	return strings.Join(lines[bestStart-1:bestEnd], "\n"), bestStart, bestEnd
}

// hashVersion is the OCC token for file bytes (spec §4.3).
// applyHunks splices exact snippets into text, in order (spec §4.3 mode=patch).
// A target must occur exactly once unless replaceAll is set or a line window
// narrows the search; the first failing hunk returns an error and no text.
func applyHunks(text string, hunks []protocol.PatchHunk) (string, *protocol.OumvError) {
	for i, hunk := range hunks {
		if hunk.Target == "" {
			return "", protocol.NewValidationFailedError(
				fmt.Sprintf("VALIDATION_FAILED: hunk %d has an empty target", i),
				map[string]interface{}{"reason": "target_required", "hunk": i},
			)
		}
		startOff, endOff, windowErr := hunkWindow(text, hunk)
		if windowErr != nil {
			return "", windowErr
		}
		region := text[startOff:endOff]
		count := strings.Count(region, hunk.Target)
		if count == 0 {
			return "", &protocol.OumvError{
				Code:    protocol.ErrResourceNotFound,
				Message: fmt.Sprintf("RESOURCE_NOT_FOUND: hunk %d target does not occur in the file", i),
				Data:    map[string]interface{}{"reason": "target_not_found", "hunk": i},
			}
		}
		if count > 1 && !hunk.ReplaceAll {
			lineOffset := 0
			if hunk.StartLine > 0 {
				lineOffset = hunk.StartLine - 1
			}
			occurrences := occurrenceDetails(region, hunk.Target, lineOffset)
			lines := make([]int, 0, len(occurrences))
			suggested := make([]map[string]interface{}, 0, len(occurrences))
			for _, occurrence := range occurrences {
				lines = append(lines, occurrence.Line)
				start := occurrence.Line - 3
				if start < 1 {
					start = 1
				}
				end := occurrence.Line + strings.Count(hunk.Target, "\n") + 2
				suggested = append(suggested, map[string]interface{}{
					"target":      hunk.Target,
					"replacement": hunk.Replacement,
					"startLine":   start,
					"endLine":     end,
				})
			}
			return "", &protocol.OumvError{
				Code:    protocol.ErrValidationFailed,
				Message: fmt.Sprintf("VALIDATION_FAILED: hunk %d target occurs %d times; narrow it with startLine/endLine or set replaceAll", i, count),
				Data: map[string]interface{}{
					"reason":         "target_not_unique",
					"hunk":           i,
					"count":          count,
					"lines":          lines,
					"occurrences":    occurrences,
					"suggestedHunks": suggested,
				},
			}
		}
		limit := 1
		if hunk.ReplaceAll {
			limit = -1
		}
		text = text[:startOff] + strings.Replace(region, hunk.Target, hunk.Replacement, limit) + text[endOff:]
	}
	return text, nil
}

// hunkWindow returns the byte range a hunk searches, from its 1-based inclusive
// startLine/endLine. An endLine past EOF is clamped; an inverted range is -32011.
func hunkWindow(text string, hunk protocol.PatchHunk) (int, int, *protocol.OumvError) {
	if hunk.StartLine <= 0 && hunk.EndLine <= 0 {
		return 0, len(text), nil
	}
	startLine := hunk.StartLine
	if startLine <= 0 {
		startLine = 1
	}
	lineCount := strings.Count(text, "\n") + 1
	endLine := hunk.EndLine
	if endLine <= 0 || endLine > lineCount {
		endLine = lineCount
	}
	if startLine > lineCount {
		startLine = lineCount
	}
	if endLine < startLine {
		return 0, 0, protocol.NewValidationFailedError(
			"VALIDATION_FAILED: endLine precedes startLine",
			map[string]string{"reason": "inverted_line_range"},
		)
	}
	startOff := 0
	for line := 1; line < startLine; line++ {
		idx := strings.IndexByte(text[startOff:], '\n')
		if idx < 0 {
			return len(text), len(text), nil
		}
		startOff += idx + 1
	}
	endOff := startOff
	for line := startLine; line <= endLine; line++ {
		idx := strings.IndexByte(text[endOff:], '\n')
		if idx < 0 {
			endOff = len(text)
			break
		}
		endOff += idx + 1
	}
	if endOff > startOff && text[endOff-1] == '\n' {
		endOff--
	}
	return startOff, endOff, nil
}

// occurrenceLines reports the 1-based line numbers of up to five target matches.
func occurrenceLines(region, target string, lineOffset int) []int {
	details := occurrenceDetails(region, target, lineOffset)
	lines := make([]int, 0, len(details))
	for _, detail := range details {
		lines = append(lines, detail.Line)
	}
	return lines
}

// occurrence describes one ambiguous hunk match: the absolute 1-based line and the
// text of that line, so a caller can retry with a scoped hunk without another read.
type Occurrence struct {
	Line int    `json:"line"`
	Text string `json:"text"`
}

func occurrenceDetails(region, target string, lineOffset int) []Occurrence {
	out := []Occurrence{}
	offset := 0
	for len(out) < 5 {
		idx := strings.Index(region[offset:], target)
		if idx < 0 {
			break
		}
		pos := offset + idx
		line := lineOffset + 1 + strings.Count(region[:pos], "\n")
		lineStart := strings.LastIndex(region[:pos], "\n") + 1
		lineEnd := strings.Index(region[pos:], "\n")
		if lineEnd < 0 {
			lineEnd = len(region)
		} else {
			lineEnd += pos
		}
		text := strings.TrimSpace(region[lineStart:lineEnd])
		if len(text) > 200 {
			text = text[:200]
		}
		out = append(out, Occurrence{Line: line, Text: text})
		offset = pos + len(target)
	}
	return out
}

func hashVersion(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// workspaceDigestSkipDirs are the inventory directories whose churn does not
// describe the workspace's analysis surface (build output, tool state).
var workspaceDigestSkipDirs = map[string]bool{
	".git": true, "node_modules": true, ".venv": true, "venv": true,
	"__pycache__": true, ".tokensafe": true, ".oumv": true, "dist": true,
	"build": true, ".pytest_cache": true, "target": true,
}

// WorkspaceDigest is the §8 result-versioning token (wire 1.6.0): a sha256 over
// the workspace inventory (relative path, size, mtime). Any touched file
// changes it, so an agent can learn exactly when foreign edits made a cached
// observe/understand answer stale.
func (r *Runtime) WorkspaceDigest() string {
	root := r.config.WorkspaceDir
	entries := []string{}
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if workspaceDigestSkipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		info, statErr := d.Info()
		if statErr != nil {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		entries = append(entries, fmt.Sprintf("%s:%d:%d", filepath.ToSlash(rel), info.Size(), info.ModTime().UnixNano()))
		return nil
	})
	sort.Strings(entries)
	return hashVersion([]byte(strings.Join(entries, "\n")))
}

// backupPrevious copies the current bytes into .tokensafe/backups before a
// destructive edit (spec §8: automatic backup of previous content). The path
// is reported as `backupPath` with a `rollbackHint`; failures are non-fatal —
// a backup that cannot be written must not block the guarded edit itself.
func (r *Runtime) backupPrevious(cleanPath string, previous []byte) string {
	if len(previous) == 0 {
		return ""
	}
	sum := sha256.Sum256(previous)
	backupDir := filepath.Join(r.workspaceDir, ".tokensafe", "backups")
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		return ""
	}
	name := fmt.Sprintf("%s_%s.bak", hex.EncodeToString(sum[:])[:8], filepath.Base(cleanPath))
	backupPath := filepath.Join(backupDir, name)
	if err := os.WriteFile(backupPath, previous, 0o644); err != nil {
		return ""
	}
	rel, err := filepath.Rel(r.workspaceDir, backupPath)
	if err != nil {
		return backupPath
	}
	return filepath.ToSlash(rel)
}

// pageBounds implements the §4.1 limit/cursor contract: a negative or absent limit
// means "all"; an invalid cursor is treated as the first page.
func pageBounds(total, limit int, cursor string) (start, end int, hasMore bool, next string) {
	start = 0
	if cursor != "" {
		if parsed, err := strconv.Atoi(cursor); err == nil && parsed >= 0 {
			start = parsed
		}
	}
	if start > total {
		start = total
	}
	end = total
	if limit > 0 && start+limit < total {
		end = start + limit
		hasMore = true
		next = strconv.Itoa(end)
	}
	return start, end, hasMore, next
}
