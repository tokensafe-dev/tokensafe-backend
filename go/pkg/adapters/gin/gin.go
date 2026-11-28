package gin

import (
	"database/sql"
	"reflect"
	stdruntime "runtime"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/db"
	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/oumv"
	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/transport"
)

// Options defines configuration for the Gin OUMV adapter.
type Options struct {
	RPCPath string // Default: "/__oumv/rpc"
	// DB, when set, wires the database/sql rollback sandbox (spec §4 verify.sandbox
	// and §14.2 oumv/query) — the explicit equivalent of the .NET AddDbContext scan.
	DB *sql.DB
}

// Mount auto-discovers all routes in the Gin engine and mounts the OUMV JSON-RPC endpoint if enabled.
func Mount(r *gin.Engine, rt *oumv.Runtime, opts ...Options) {
	if rt == nil || !rt.IsEnabled() || !rt.Config().Transport.Enabled {
		return
	}

	rpcPath := rt.Config().Transport.Path
	if rpcPath == "" {
		rpcPath = "/__oumv/rpc"
	}
	if len(opts) > 0 && opts[0].RPCPath != "" {
		rpcPath = opts[0].RPCPath
	}

	// 1. Introspect all registered routes for L1 Surface. When the handler func is
	// available, register its source location too, so observe L3 also accepts the
	// endpoint target (e.g. "POST /api/products").
	for _, route := range r.Routes() {
		if route.Path == rpcPath {
			continue
		}
		if route.HandlerFunc != nil {
			pc := reflect.ValueOf(route.HandlerFunc).Pointer()
			if fn := stdruntime.FuncForPC(pc); fn != nil {
				file, line := fn.FileLine(pc)
				rt.RegisterRouteSource(route.Method, route.Path, route.Handler, file, line)
				continue
			}
		}
		rt.RegisterRoute(route.Method, route.Path, route.Handler)
	}

	// 2. Trace middleware: records every request execution (L4 evidence for Understand causality).
	// Gin freezes a route's handler chain at registration, so a middleware added here only
	// reaches routes registered afterwards. Install it only when Mount runs before routing;
	// otherwise the caller must wrap the engine: http.ListenAndServe(addr, oumv.TraceHandler(rt, engine)).
	if len(r.Routes()) == 0 {
		r.Use(func(c *gin.Context) {
			start := time.Now()
			c.Next()
			route := c.FullPath()
			if route == "" {
				route = c.Request.URL.Path
			}
			errMsg := ""
			if len(c.Errors) > 0 {
				errMsg = c.Errors.String()
			}
			rt.RecordTrace(
				c.Request.Method,
				c.Request.URL.Path,
				route,
				c.HandlerName(),
				c.Writer.Status(),
				float64(time.Since(start).Microseconds())/1000.0,
				errMsg,
			)
		})
	}

	// 3. Wire the engine for in-memory oumv/verify (no extra configuration needed).
	rt.SetHandler(r)
	if len(opts) > 0 && opts[0].DB != nil {
		rt.SetDBSandbox(db.NewSandbox(opts[0].DB))
	}

	// 4. Mount OUMV JSON-RPC endpoint
	dispatcher := transport.NewDispatcher(rt)
	httpOpts := transport.HTTPOptions{
		AuthToken:              rt.Config().Security.AuthToken,
		AuthenticationRequired: rt.Config().Security.AuthenticationRequired,
		AllowedOrigins:         rt.Config().Security.AllowedOrigins,
		Disabled:               false,
	}
	handler := transport.NewHTTPHandler(dispatcher, httpOpts)

	dispatcherForWS := transport.NewDispatcher(rt)
	eventsPath := strings.TrimSuffix(rpcPath, "/rpc") + "/events"
	// §12: one authorizer for every binding, so SSE/WS cannot be the unguarded door.
	authorizer := transport.NewAuthorizer(rt.Config().Security.AuthToken, rt.Config().Security.AuthenticationRequired)
	r.GET(eventsPath, gin.WrapH(transport.NewSSEHandler(rt, transport.SSEOptions{Auth: authorizer})))
	r.GET(rpcPath, gin.WrapH(transport.NewWSHandler(dispatcherForWS, rt, transport.WSOptions{Auth: authorizer})))
	// POST/OPTIONS only: GET on this path is the WebSocket upgrade.
	r.POST(rpcPath, gin.WrapH(handler))
	r.OPTIONS(rpcPath, gin.WrapH(handler))
	rt.SetTransports("http", "sse", "ws")
}

// AutoMount creates a development runtime rooted at the current working directory
// and mounts it on the engine — the one-call integration used by `tokensafe inject`
// and by generated projects. Production still fails closed: the resolved runtime
// config disables the transport outside development.
func AutoMount(r *gin.Engine) *oumv.Runtime {
	rt := oumv.NewRuntime(oumv.Config{Runtime: oumv.DefaultDevelopmentRuntimeConfig()})
	Mount(r, rt)
	return rt
}

// AutoMountAndServe replaces r.Run(addr): it mounts the runtime and serves through
// oumv.ListenAndServe, so the §9.2 discovery descriptor carries the real bound port
// and disappears on shutdown.
func AutoMountAndServe(r *gin.Engine, addr string) error {
	return oumv.ListenAndServe(AutoMount(r), addr, r)
}
