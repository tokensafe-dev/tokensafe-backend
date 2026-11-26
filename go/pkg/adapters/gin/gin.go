package gin

import (
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/oumv"
	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/transport"
)

// Options defines configuration for the Gin OUMV adapter.
type Options struct {
	RPCPath string // Default: "/__oumv/rpc"
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

	// 1. Introspect all registered routes for L1 Surface
	for _, route := range r.Routes() {
		if route.Path == rpcPath {
			continue
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

	// 4. Mount OUMV JSON-RPC endpoint
	dispatcher := transport.NewDispatcher(rt)
	httpOpts := transport.HTTPOptions{
		AuthToken:              rt.Config().Security.AuthToken,
		AuthenticationRequired: rt.Config().Security.AuthenticationRequired,
		AllowedOrigins:         rt.Config().Security.AllowedOrigins,
		Disabled:               false,
	}
	handler := transport.NewHTTPHandler(dispatcher, httpOpts)

	r.Any(rpcPath, gin.WrapH(handler))
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
