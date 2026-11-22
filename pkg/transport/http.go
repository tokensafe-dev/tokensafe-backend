package transport

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/tokensafe-dev/tokensafe-backend/pkg/protocol"
)

// HTTPHandler implements http.Handler for serving JSON-RPC 2.0 OUMV requests.
type HTTPHandler struct {
	dispatcher *Dispatcher
}

// NewHTTPHandler wraps a dispatcher in a standard net/http handler.
func NewHTTPHandler(dispatcher *Dispatcher) *HTTPHandler {
	return &HTTPHandler{
		dispatcher: dispatcher,
	}
}

// ServeHTTP handles incoming HTTP requests.
func (h *HTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Enable CORS for local dev-tools and browser adapters
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS, GET")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed, OUMV RPC requires POST", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
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
