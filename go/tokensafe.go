package tokensafe

import (
	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/core"
)

// App is the first-class agent-native Go backend framework application.
type App = core.App

// Context represents the request execution context.
type Context = core.Context

// AppConfig defines options for application initialization.
type AppConfig = core.AppConfig

// HTTPError represents an error with an associated HTTP status code.
type HTTPError = core.HTTPError

// NewHTTPError creates an error with an associated status code and message.
var NewHTTPError = core.NewHTTPError

// SimulateResponse represents the result of an in-memory HTTP dispatch.
type SimulateResponse = core.SimulateResponse

// New creates a new TokenSafe application with native OUMV capabilities.
func New(configs ...AppConfig) *App {
	return core.New(configs...)
}

// Post registers a type-safe POST endpoint with automatic schema reflection.
func Post[Req any, Resp any](app *App, path string, fn func(c *Context, req Req) (*Resp, error)) {
	core.PostTyped[Req, Resp](app, path, fn)
}

// Get registers a type-safe GET endpoint with automatic response schema reflection.
func Get[Resp any](app *App, path string, fn func(c *Context) (*Resp, error)) {
	core.GetTyped[Resp](app, path, fn)
}
