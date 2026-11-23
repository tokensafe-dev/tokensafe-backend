package gin

import (
	"github.com/gin-gonic/gin"
	"github.com/tokensafe-dev/tokensafe-backend/pkg/oumv"
	"github.com/tokensafe-dev/tokensafe-backend/pkg/transport"
)

// Options defines configuration for the Gin OUMV adapter.
type Options struct {
	RPCPath string // Default: "/__oumv/rpc"
}

// Mount auto-discovers all routes in the Gin engine and mounts the OUMV JSON-RPC endpoint.
func Mount(r *gin.Engine, rt *oumv.Runtime, opts ...Options) {
	rpcPath := "/__oumv/rpc"
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

	// 2. Mount OUMV JSON-RPC endpoint
	dispatcher := transport.NewDispatcher(rt)
	handler := transport.NewHTTPHandler(dispatcher)

	r.Any(rpcPath, gin.WrapH(handler))
}
