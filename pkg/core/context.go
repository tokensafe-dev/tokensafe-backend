package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"

	"github.com/tokensafe-dev/tokensafe-backend/pkg/db"
)

// Context represents the execution context of an HTTP request within the TokenSafe core framework.
type Context struct {
	Response http.ResponseWriter
	Request  *http.Request
	Params   map[string]string
	rawDB    *sql.DB
}

// NewContext creates a new framework context.
func NewContext(w http.ResponseWriter, r *http.Request, rawDB *sql.DB) *Context {
	return &Context{
		Response: w,
		Request:  r,
		Params:   make(map[string]string),
		rawDB:    rawDB,
	}
}

// Context returns the standard Go context.Context from the request.
func (c *Context) Context() context.Context {
	return c.Request.Context()
}

// Param retrieves a path or URL parameter.
func (c *Context) Param(key string) string {
	if val, ok := c.Params[key]; ok {
		return val
	}
	return c.Request.URL.Query().Get(key)
}

// JSON sends a JSON response with status code.
func (c *Context) JSON(status int, v interface{}) error {
	c.Response.Header().Set("Content-Type", "application/json")
	c.Response.WriteHeader(status)
	return json.NewEncoder(c.Response).Encode(v)
}

// BindJSON decodes the request body into a target struct.
func (c *Context) BindJSON(target interface{}) error {
	return json.NewDecoder(c.Request.Body).Decode(target)
}

// DB returns the active database connection.
// If the request is executing inside an OUMV dry-run sandbox, it automatically returns
// nil or you can inspect GetTx() to execute queries within the guaranteed rollback transaction.
func (c *Context) DB() *sql.DB {
	return c.rawDB
}

// Tx returns the active dry-run transaction if the request was triggered by OUMV Verify.
func (c *Context) Tx() *sql.Tx {
	return db.GetTxFromContext(c.Request.Context())
}
