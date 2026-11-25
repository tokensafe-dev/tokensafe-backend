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

	// 2. Trace middleware: records every request execution (L4 evidence for Understand causality)
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

	// 3. Mount OUMV JSON-RPC endpoint
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
