package stdhttp

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/oumv"
	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/transport"
)

// ServeMux is a drop-in replacement for standard http.ServeMux that automatically
// registers routes in the OUMV L1 Surface catalog.
type ServeMux struct {
	*http.ServeMux
	runtime *oumv.Runtime
	routes  []string
	mu      sync.RWMutex
}

// NewServeMux wraps or creates a ServeMux with OUMV auto-introspection enabled.
func NewServeMux(rt *oumv.Runtime) *ServeMux {
	return &ServeMux{
		ServeMux: http.NewServeMux(),
		runtime:  rt,
		routes:   make([]string, 0),
	}
}

// Handle registers the handler for the given pattern and exposes it to OUMV.
func (m *ServeMux) Handle(pattern string, handler http.Handler) {
	m.registerPattern(pattern)
	m.ServeMux.Handle(pattern, handler)
}

// HandleFunc registers the handler function for the given pattern and exposes it to OUMV.
func (m *ServeMux) HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request)) {
	m.registerPattern(pattern)
	m.ServeMux.HandleFunc(pattern, handler)
}

func (m *ServeMux) registerPattern(pattern string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	method := "ALL"
	path := pattern

	// Go 1.22+ supports "METHOD /path" pattern syntax
	parts := strings.SplitN(pattern, " ", 2)
	if len(parts) == 2 {
		method = parts[0]
		path = parts[1]
	}

	m.routes = append(m.routes, pattern)
	if m.runtime != nil {
		m.runtime.RegisterRoute(method, path, "stdhttp.Handler")
	}
}

// Mount mounts the OUMV JSON-RPC endpoint (default: /__oumv/rpc) onto the mux if enabled.
func (m *ServeMux) Mount(rpcPath string) {
	if m.runtime == nil || !m.runtime.IsEnabled() || !m.runtime.Config().Transport.Enabled {
		return
	}
	if rpcPath == "" {
		rpcPath = m.runtime.Config().Transport.Path
	}
	if rpcPath == "" {
		rpcPath = "/__oumv/rpc"
	}
	dispatcher := transport.NewDispatcher(m.runtime)
	httpOpts := transport.HTTPOptions{
		AuthToken:              m.runtime.Config().Security.AuthToken,
		AuthenticationRequired: m.runtime.Config().Security.AuthenticationRequired,
		AllowedOrigins:         m.runtime.Config().Security.AllowedOrigins,
		Disabled:               false,
	}
	m.ServeMux.Handle(rpcPath, transport.NewHTTPHandler(dispatcher, httpOpts))
}

// ServeHTTP dispatches with request tracing so Understand(causality) gets
// real runtime evidence (L4 traces) for every observed execution.
func (m *ServeMux) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if m.runtime == nil {
		m.ServeMux.ServeHTTP(w, req)
		return
	}
	start := time.Now()
	recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	m.ServeMux.ServeHTTP(recorder, req)
	m.runtime.RecordTrace(
		req.Method,
		req.URL.Path,
		req.URL.Path,
		"stdhttp.Handler",
		recorder.status,
		float64(time.Since(start).Microseconds())/1000.0,
		"",
	)
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
