package transport_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/protocol"
	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/transport"
)

func TestAuthorizerRequiresAConfiguredToken(t *testing.T) {
	authz := transport.NewAuthorizer("secret-token", false)
	cases := []struct {
		name    string
		headers map[string]string
		want    bool
	}{
		{"missing", nil, false},
		{"bearer", map[string]string{"Authorization": "Bearer secret-token"}, true},
		{"session header", map[string]string{"X-OUMV-Session-Token": "secret-token"}, true},
		{"invalid", map[string]string{"X-OUMV-Session-Token": "nope"}, false},
		{"wrong scheme", map[string]string{"Authorization": "Basic c2VjcmV0LXRva2Vu"}, false},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodPost, "/__oumv/rpc", nil)
		for key, value := range tc.headers {
			req.Header.Set(key, value)
		}
		if got := authz.Authorized(req); got != tc.want {
			t.Errorf("%s: Authorized = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestAuthorizerIsOpenOnlyWhenNothingIsConfigured(t *testing.T) {
	open := transport.NewAuthorizer("", false)
	if open.Enabled() || !open.Authorized(httptest.NewRequest(http.MethodPost, "/", nil)) {
		t.Fatal("no token and no requirement must stay open for loopback development")
	}
	required := transport.NewAuthorizer("", true)
	if required.Authorized(httptest.NewRequest(http.MethodPost, "/", nil)) {
		t.Fatal("required auth with no token must refuse everything")
	}
}

func TestRefuseWritesA401WithTheEnvelope(t *testing.T) {
	recorder := httptest.NewRecorder()
	transport.NewAuthorizer("secret", true).Refuse(recorder)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}
	var payload struct {
		Error struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("refusal body is not JSON: %v", err)
	}
	if payload.Error.Code != protocol.ErrPermissionDenied {
		t.Fatalf("error code = %d, want %d", payload.Error.Code, protocol.ErrPermissionDenied)
	}
}

// stubStream satisfies both optional-transport surfaces without a runtime.
type stubStream struct{}

func (stubStream) EventsSince(uint64) []protocol.StreamEvent { return nil }

func (stubStream) SubscribeEvents() (<-chan protocol.StreamEvent, func()) {
	events := make(chan protocol.StreamEvent)
	return events, func() {}
}

func TestOptionalTransportsRefuseAnUnauthenticatedClient(t *testing.T) {
	authz := transport.NewAuthorizer("secret-token", false)

	sse := transport.NewSSEHandler(stubStream{}, transport.SSEOptions{Auth: authz})
	sseRecorder := httptest.NewRecorder()
	sse.ServeHTTP(sseRecorder, httptest.NewRequest(http.MethodGet, "/__oumv/events", nil))
	if sseRecorder.Code != http.StatusUnauthorized {
		t.Errorf("SSE without a credential: status = %d, want 401", sseRecorder.Code)
	}

	ws := transport.NewWSHandler(nil, stubStream{}, transport.WSOptions{Auth: authz})
	wsRecorder := httptest.NewRecorder()
	ws.ServeHTTP(wsRecorder, httptest.NewRequest(http.MethodGet, "/__oumv/rpc", nil))
	if wsRecorder.Code != http.StatusUnauthorized {
		t.Errorf("WS without a credential: status = %d, want 401", wsRecorder.Code)
	}
}
