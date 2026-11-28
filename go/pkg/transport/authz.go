package transport

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/protocol"
)

// Authorizer is the single spec §12 credential check shared by the HTTP, SSE and
// WS bindings: `X-OUMV-Session-Token` or `Authorization: Bearer <token>`,
// compared in constant time. A configured token is always required — there is no
// fail-open mode where a token exists but an empty credential is accepted.
type Authorizer struct {
	Token    string
	Required bool
}

// NewAuthorizer trims the token and records whether auth is enforced.
func NewAuthorizer(token string, required bool) *Authorizer {
	return &Authorizer{Token: strings.TrimSpace(token), Required: required}
}

// Enabled reports whether a credential is demanded.
func (a *Authorizer) Enabled() bool { return a != nil && (a.Required || a.Token != "") }

// Credential extracts the offered token from the spec §2.2 headers: a Bearer
// token wins, otherwise the raw Authorization value, otherwise the dedicated
// session-token header.
func Credential(r *http.Request) string {
	authHeader := r.Header.Get("Authorization")
	token := ""
	if strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
		token = strings.TrimSpace(authHeader[7:])
	} else if authHeader != "" {
		token = strings.TrimSpace(authHeader)
	}
	if token == "" {
		token = strings.TrimSpace(r.Header.Get("X-OUMV-Session-Token"))
	}
	return token
}

// Authorized reports whether the request may proceed.
func (a *Authorizer) Authorized(r *http.Request) bool {
	if !a.Enabled() {
		return true
	}
	if a.Token == "" {
		return false
	}
	provided := Credential(r)
	return len(provided) > 0 && subtle.ConstantTimeCompare([]byte(provided), []byte(a.Token)) == 1
}

// Refuse writes the §12 refusal — HTTP 401 with the -32007 envelope — before any
// method dispatch.
func (a *Authorizer) Refuse(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(protocol.NewErrorResponse(nil,
		protocol.NewPermissionDeniedError("PERMISSION_DENIED: invalid OUMV authentication token")))
}
