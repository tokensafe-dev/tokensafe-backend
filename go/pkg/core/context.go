package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"

	"github.com/go-playground/validator/v10"
	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/db"
)

var defaultValidator = validator.New()

// HTTPError represents an error with an associated HTTP status code.
type HTTPError struct {
	StatusCode int    `json:"statusCode"`
	Message    string `json:"message"`
	Details    string `json:"details,omitempty"`
}

func (e *HTTPError) Error() string {
	if e.Details != "" {
		return e.Message + ": " + e.Details
	}
	return e.Message
}

// NewHTTPError creates a new HTTPError with status code and message.
func NewHTTPError(code int, message string, details ...string) *HTTPError {
	d := ""
	if len(details) > 0 {
		d = details[0]
	}
	return &HTTPError{
		StatusCode: code,
		Message:    message,
		Details:    d,
	}
}

// Context represents the execution context of an HTTP request within the TokenSafe core framework.
type Context struct {
	Response     http.ResponseWriter
	Request      *http.Request
	Params       map[string]string
	rawDB        *sql.DB
	customStatus int
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

// Param retrieves a path parameter (Go 1.22+ PathValue or Params map) or URL query parameter.
func (c *Context) Param(key string) string {
	if val, ok := c.Params[key]; ok {
		return val
	}
	if c.Request != nil {
		if val := c.Request.PathValue(key); val != "" {
			return val
		}
		return c.Request.URL.Query().Get(key)
	}
	return ""
}

// SetStatus sets a custom HTTP status code to be used on completion.
func (c *Context) SetStatus(status int) {
	c.customStatus = status
}

// Status sets custom status code fluently and returns context.
func (c *Context) Status(status int) *Context {
	c.customStatus = status
	return c
}

// CustomStatus returns the custom status code if set, or 0.
func (c *Context) CustomStatus() int {
	return c.customStatus
}

// JSON sends a JSON response with status code.
func (c *Context) JSON(status int, v interface{}) error {
	c.Response.Header().Set("Content-Type", "application/json")
	c.Response.WriteHeader(status)
	return json.NewEncoder(c.Response).Encode(v)
}

// BindJSON decodes the request body into a target struct and validates field constraints.
func (c *Context) BindJSON(target interface{}) error {
	if err := json.NewDecoder(c.Request.Body).Decode(target); err != nil {
		return err
	}
	if err := defaultValidator.Struct(target); err != nil {
		if _, ok := err.(*validator.InvalidValidationError); !ok {
			return err
		}
	}
	return nil
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
