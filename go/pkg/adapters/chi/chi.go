package chi

import (
	"fmt"
	"net/http"
	"reflect"
	"runtime"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/oumv"
	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/transport"
)

// Options defines configuration for the Chi OUMV adapter.
type Options struct {
	RPCPath string // Default: "/__oumv/rpc"
}

// Mount auto-discovers all routes in the Chi router and mounts the OUMV JSON-RPC endpoint if enabled.
func Mount(r chi.Router, rt *oumv.Runtime, opts ...Options) error {
	if rt == nil || !rt.IsEnabled() || !rt.Config().Transport.Enabled {
		return nil
	}

	rpcPath := rt.Config().Transport.Path
	if rpcPath == "" {
		rpcPath = "/__oumv/rpc"
	}
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

	// 2. Wire the router for in-memory oumv/verify (no extra configuration needed).
	rt.SetHandler(r)

	// 3. Mount OUMV JSON-RPC endpoint.
	// NOTE: chi requires all middlewares before route registration, so Mount
	// cannot inject request tracing on already-registered routes. For runtime
	// causality evidence, serve through the universal wrapper instead:
	//   http.ListenAndServe(addr, oumv.TraceHandler(rt, r))
	dispatcher := transport.NewDispatcher(rt)
	httpOpts := transport.HTTPOptions{
		AuthToken:              rt.Config().Security.AuthToken,
		AuthenticationRequired: rt.Config().Security.AuthenticationRequired,
		AllowedOrigins:         rt.Config().Security.AllowedOrigins,
		Disabled:               false,
	}
	// POST/OPTIONS for JSON-RPC; GET on the same path is the WebSocket upgrade.
	r.Post(rpcPath, transport.NewHTTPHandler(dispatcher, httpOpts).ServeHTTP)
	r.Options(rpcPath, transport.NewHTTPHandler(dispatcher, httpOpts).ServeHTTP)
	eventsPath := strings.TrimSuffix(rpcPath, "/rpc") + "/events"
	// §12: one authorizer for every binding, so SSE/WS cannot be the unguarded door.
	authorizer := transport.NewAuthorizer(rt.Config().Security.AuthToken, rt.Config().Security.AuthenticationRequired)
	sseHandler := transport.NewSSEHandler(rt, transport.SSEOptions{Auth: authorizer})
	wsHandler := transport.NewWSHandler(transport.NewDispatcher(rt), rt, transport.WSOptions{Auth: authorizer})
	r.Get(eventsPath, func(w http.ResponseWriter, req *http.Request) {
		sseHandler.ServeHTTP(w, req)
	})
	r.Get(rpcPath, func(w http.ResponseWriter, req *http.Request) {
		wsHandler.ServeHTTP(w, req)
	})
	rt.SetTransports("http", "sse", "ws")

	return nil
}

// AutoMount creates a development runtime rooted at the current working directory
// and mounts it on the router. See gin.AutoMount for the fail-closed semantics.
func AutoMount(r chi.Router) (*oumv.Runtime, error) {
	rt := oumv.NewRuntime(oumv.Config{Runtime: oumv.DefaultDevelopmentRuntimeConfig()})
	if err := Mount(r, rt); err != nil {
		return nil, err
	}
	return rt, nil
}

// AutoMountAndServe replaces http.ListenAndServe(addr, r): it mounts the runtime
// and serves through oumv.ListenAndServe, so the §9.2 discovery descriptor carries
// the real bound port and disappears on shutdown.
func AutoMountAndServe(r chi.Router, addr string) error {
	rt, err := AutoMount(r)
	if err != nil {
		return err
	}
	return oumv.ListenAndServe(rt, addr, r)
}
