package chi

import (
	"fmt"
	"net/http"
	"reflect"
	"runtime"

	"github.com/go-chi/chi/v5"
	"github.com/tokensafe-dev/tokensafe-backend/pkg/oumv"
	"github.com/tokensafe-dev/tokensafe-backend/pkg/transport"
)

// Options defines configuration for the Chi OUMV adapter.
type Options struct {
	RPCPath string // Default: "/__oumv/rpc"
}

// Mount auto-discovers all routes in the Chi router and mounts the OUMV JSON-RPC endpoint.
func Mount(r chi.Router, rt *oumv.Runtime, opts ...Options) error {
	rpcPath := "/__oumv/rpc"
	if len(opts) > 0 && opts[0].RPCPath != "" {
		rpcPath = opts[0].RPCPath
	}

	// 1. Walk the route tree and populate L1 Surface
	walkErr := chi.Walk(r, func(method, route string, handler http.Handler, middlewares ...func(http.Handler) http.Handler) error {
		// Avoid registering the internal OUMV endpoint in the business surface
		if route == rpcPath {
			return nil
		}

		handlerName := "Handler"
		if handler != nil {
			handlerName = runtime.FuncForPC(reflect.ValueOf(handler).Pointer()).Name()
		}

		mwNames := make([]string, len(middlewares))
		for i, mw := range middlewares {
			fnName := runtime.FuncForPC(reflect.ValueOf(mw).Pointer()).Name()
			if fnName != "" {
				mwNames[i] = fnName
			} else {
				mwNames[i] = fmt.Sprintf("Middleware#%d", i+1)
			}
		}

		rt.RegisterRoute(method, route, handlerName, mwNames...)
		return nil
	})

	if walkErr != nil {
		return walkErr
	}

	// 2. Mount OUMV JSON-RPC endpoint
	dispatcher := transport.NewDispatcher(rt)
	r.Handle(rpcPath, transport.NewHTTPHandler(dispatcher))

	return nil
}
