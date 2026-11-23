package core

import (
	"net/http"
)

// TypedHandlerFunc defines a strongly-typed handler with generic request and response types.
type TypedHandlerFunc[Req any, Resp any] func(c *Context, req Req) (*Resp, error)

// TypedGetHandlerFunc defines a strongly-typed handler for GET requests (response only).
type TypedGetHandlerFunc[Resp any] func(c *Context) (*Resp, error)

// PostTyped registers a type-safe POST endpoint with automatic JSON binding,
// response serialization, and zero-boilerplate OUMV L2 schema extraction.
func PostTyped[Req any, Resp any](app *App, path string, fn TypedHandlerFunc[Req, Resp]) {
	var emptyReq Req
	var emptyResp Resp

	reqSchema := ReflectStructSchema(emptyReq)
	respSchema := ReflectStructSchema(emptyResp)

	app.Handle("POST", path, func(c *Context) error {
		var req Req
		if err := c.BindJSON(&req); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{
				"error":   "Invalid request payload",
				"details": err.Error(),
			})
		}

		resp, err := fn(c, req)
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{
				"error": err.Error(),
			})
		}

		return c.JSON(http.StatusOK, resp)
	}, reqSchema, respSchema)
}

// GetTyped registers a type-safe GET endpoint with automatic response serialization
// and OUMV L2 response schema extraction.
func GetTyped[Resp any](app *App, path string, fn TypedGetHandlerFunc[Resp]) {
	var emptyResp Resp
	respSchema := ReflectStructSchema(emptyResp)

	app.Handle("GET", path, func(c *Context) error {
		resp, err := fn(c)
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{
				"error": err.Error(),
			})
		}
		return c.JSON(http.StatusOK, resp)
	}, nil, respSchema)
}
