package transport

import (
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/protocol"
)

// HTTPOptions defines transport security, authentication, and origin policies.
type HTTPOptions struct {
	AuthToken              string
	AuthenticationRequired bool
	AllowedOrigins         []string
	Disabled               bool
}

// HTTPHandler implements http.Handler for serving JSON-RPC 2.0 OUMV requests.
type HTTPHandler struct {
	dispatcher *Dispatcher
	opts       HTTPOptions
}

// NewHTTPHandler wraps a dispatcher in a standard net/http handler with optional security settings.
func NewHTTPHandler(dispatcher *Dispatcher, opts ...HTTPOptions) *HTTPHandler {
	var opt HTTPOptions
	if len(opts) > 0 {
		opt = opts[0]
	}
	return &HTTPHandler{
		dispatcher: dispatcher,
		opts:       opt,
	}
}

// ServeHTTP handles incoming HTTP requests with authentication, CORS validation, and method enforcement.
func (h *HTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.opts.Disabled {
		http.NotFound(w, r)
		return
	}

	origin := r.Header.Get("Origin")

	// 1. CORS Origin validation
	if len(h.opts.AllowedOrigins) > 0 {
		allowed := false
		for _, o := range h.opts.AllowedOrigins {
			if o == "*" || (origin != "" && (o == origin || strings.EqualFold(o, origin))) {
				allowed = true
				break
			}
		}

		if origin != "" && !allowed {
			http.Error(w, "Forbidden origin", http.StatusForbidden)
			return
		}

		if allowed && origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
		}
	} else if h.opts.AuthenticationRequired || strings.TrimSpace(h.opts.AuthToken) != "" {
		// When authentication is required and no explicit allowed origins are set,
		// do not emit wildcard CORS. If Origin is present, reject cross-origin unless explicitly allowed.
		if origin != "" {
			http.Error(w, "Cross-origin requests forbidden without explicit allowed origins", http.StatusForbidden)
			return
		}
	} else {
		// Local unauthenticated development fallback
		if origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
		} else {
			w.Header().Set("Access-Control-Allow-Origin", "*")
		}
	}

	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-OUMV-Session-Token")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed, OUMV RPC requires POST", http.StatusMethodNotAllowed)
		return
	}

	// 2. Authentication enforcement
	if h.opts.AuthenticationRequired || strings.TrimSpace(h.opts.AuthToken) != "" {
		expected := []byte(strings.TrimSpace(h.opts.AuthToken))

		// Check Authorization: Bearer <token>
		authHeader := r.Header.Get("Authorization")
		token := ""
		if strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
			token = strings.TrimSpace(authHeader[7:])
		} else if authHeader != "" {
			token = strings.TrimSpace(authHeader)
		}

		// Fallback check X-OUMV-Session-Token
		if token == "" {
			token = strings.TrimSpace(r.Header.Get("X-OUMV-Session-Token"))
		}

		valid := len(token) > 0 && len(expected) > 0 && subtle.ConstantTimeCompare([]byte(token), expected) == 1
		if !valid {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(protocol.NewErrorResponse(nil, protocol.NewPermissionDeniedError("Unauthorized: valid OUMV authentication token required")))
			return
		}
	}

	// 3. Process JSON-RPC Request body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(protocol.NewErrorResponse(nil, &protocol.OumvError{
			Code:    protocol.ErrInternalError,
			Message: "Failed to read request body: " + err.Error(),
		}))
		return
	}
	defer r.Body.Close()

	resp := h.dispatcher.Handle(r.Context(), body)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}
