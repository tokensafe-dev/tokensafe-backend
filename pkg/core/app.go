package core

import (
	"database/sql"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/tokensafe-dev/tokensafe-backend/pkg/oumv"
	"github.com/tokensafe-dev/tokensafe-backend/pkg/protocol"
	"github.com/tokensafe-dev/tokensafe-backend/pkg/transport"
)

// HandlerFunc defines the standard handler signature for TokenSafe core.
type HandlerFunc func(c *Context) error

// RouteEntry stores registered route metadata and handler.
type RouteEntry struct {
	Method      string
	Path        string
	Handler     HandlerFunc
	RequestDTO  map[string]interface{}
	ResponseDTO map[string]interface{}
	Middlewares []string
}

// AppConfig defines options for initializing the framework application.
type AppConfig struct {
	Name         string
	Version      string
	WorkspaceDir string
	RPCPath      string // Default: "/__oumv/rpc"
	DB           *sql.DB
}

// App is the first-class agent-native Go backend framework.
type App struct {
	name         string
	version      string
	workspaceDir string
	rpcPath      string
	db           *sql.DB
	routes       []RouteEntry
	middlewares  []func(http.Handler) http.Handler
	runtime      *oumv.Runtime
	mux          *http.ServeMux
	mu           sync.RWMutex
}

// New creates a new TokenSafe application with native OUMV capabilities.
func New(configs ...AppConfig) *App {
	cfg := AppConfig{
		Name:         "tokensafe-app",
		Version:      "0.1.0",
		WorkspaceDir: ".",
		RPCPath:      "/__oumv/rpc",
	}
	if len(configs) > 0 {
		if configs[0].Name != "" {
			cfg.Name = configs[0].Name
		}
		if configs[0].Version != "" {
			cfg.Version = configs[0].Version
		}
		if configs[0].WorkspaceDir != "" {
			cfg.WorkspaceDir = configs[0].WorkspaceDir
		}
		if configs[0].RPCPath != "" {
			cfg.RPCPath = configs[0].RPCPath
		}
		cfg.DB = configs[0].DB
	}

	app := &App{
		name:         cfg.Name,
		version:      cfg.Version,
		workspaceDir: cfg.WorkspaceDir,
		rpcPath:      cfg.RPCPath,
		db:           cfg.DB,
		routes:       make([]RouteEntry, 0),
		middlewares:  make([]func(http.Handler) http.Handler, 0),
		mux:          http.NewServeMux(),
	}

	// Initialize internal OUMV runtime
	app.runtime = oumv.NewRuntime(oumv.Config{
		Name:         cfg.Name,
		Version:      cfg.Version,
		WorkspaceDir: cfg.WorkspaceDir,
		Handler:      app,
	})

	// Mount OUMV RPC endpoint
	dispatcher := transport.NewDispatcher(app.runtime)
	app.mux.Handle(app.rpcPath, transport.NewHTTPHandler(dispatcher))

	return app
}

// Runtime returns the internal OUMV runtime adapter.
func (a *App) Runtime() *oumv.Runtime {
	return a.runtime
}

// WithDB attaches a database connection to the application.
func (a *App) WithDB(database *sql.DB) *App {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.db = database
	return a
}

// Use adds global middlewares to the application.
func (a *App) Use(mw func(http.Handler) http.Handler) *App {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.middlewares = append(a.middlewares, mw)
	return a
}

// Handle registers an arbitrary HTTP method and path with full schema metadata.
func (a *App) Handle(method, path string, handler HandlerFunc, reqDTO, respDTO map[string]interface{}) {
	a.mu.Lock()
	defer a.mu.Unlock()

	entry := RouteEntry{
		Method:      strings.ToUpper(method),
		Path:        path,
		Handler:     handler,
		RequestDTO:  reqDTO,
		ResponseDTO: respDTO,
	}
	a.routes = append(a.routes, entry)

	// Register in OUMV L1 Surface
	a.runtime.RegisterRoute(entry.Method, entry.Path, "core.Handler")

	// Mount on internal ServeMux
	pattern := fmt.Sprintf("%s %s", entry.Method, entry.Path)
	a.mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		ctx := NewContext(w, r, a.db)
		if err := handler(ctx); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	})
}

// Get registers a GET endpoint.
func (a *App) Get(path string, handler HandlerFunc) {
	a.Handle("GET", path, handler, nil, nil)
}

// Post registers a POST endpoint.
func (a *App) Post(path string, handler HandlerFunc) {
	a.Handle("POST", path, handler, nil, nil)
}

// Put registers a PUT endpoint.
func (a *App) Put(path string, handler HandlerFunc) {
	a.Handle("PUT", path, handler, nil, nil)
}

// Delete registers a DELETE endpoint.
func (a *App) Delete(path string, handler HandlerFunc) {
	a.Handle("DELETE", path, handler, nil, nil)
}

// ServeHTTP satisfies http.Handler interface.
func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var handler http.Handler = a.mux
	// Apply global middlewares in reverse order
	for i := len(a.middlewares) - 1; i >= 0; i-- {
		handler = a.middlewares[i](handler)
	}
	handler.ServeHTTP(w, r)
}

// Run starts the HTTP server listening on the specified address.
func (a *App) Run(addr string) error {
	server := &http.Server{
		Addr:         addr,
		Handler:      a,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
	}

	fmt.Printf("🚀 TokenSafe backend application listening on %s\n", addr)
	fmt.Printf("⚡ OUMV JSON-RPC live runtime active on %s%s\n", addr, a.rpcPath)
	return server.ListenAndServe()
}

// L2ContractForTarget returns the reflected L2 contract for a route if available.
func (a *App) L2ContractForTarget(method, path string) *protocol.L2ContractData {
	a.mu.RLock()
	defer a.mu.RUnlock()

	for _, r := range a.routes {
		if (r.Method == method || method == "") && r.Path == path {
			return &protocol.L2ContractData{
				Target:      fmt.Sprintf("%s %s", r.Method, r.Path),
				RequestDTO:  r.RequestDTO,
				ResponseDTO: r.ResponseDTO,
			}
		}
	}
	return nil
}
