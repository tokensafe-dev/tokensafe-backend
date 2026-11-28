package adapters_test

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/adapters/stdhttp"
	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/oumv"
)

// SetDB is the explicit Go half of the ORM autodetect story (the .NET
// AddDbContext scan is the template; Go has no container, so the app passes the
// handle it already owns). The handshake must then advertise the rollback sandbox.
func TestServeMuxSetDBAdvertisesTheRollbackSandbox(t *testing.T) {
	connection, err := sql.Open("oumv-cert-sandbox", "")
	if err != nil {
		t.Fatalf("open fake driver: %v", err)
	}
	t.Cleanup(func() { _ = connection.Close() })

	rt := oumv.NewRuntime(oumv.Config{Runtime: oumv.DefaultDevelopmentRuntimeConfig()})
	mux := stdhttp.NewServeMux(rt)
	mux.SetDB(connection)
	mux.Mount("/__oumv/rpc")

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/__oumv/rpc",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"oumv/handshake","params":{}}`))
	request.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(recorder, request)

	var payload struct {
		Result struct {
			Capabilities struct {
				Verify struct {
					SandboxRollback bool `json:"sandboxRollback"`
				} `json:"verify"`
			} `json:"capabilities"`
		} `json:"result"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("handshake body is not JSON: %v (%s)", err, recorder.Body.String())
	}
	if !payload.Result.Capabilities.Verify.SandboxRollback {
		t.Fatalf("SetDB must advertise verify.sandboxRollback, body: %s", recorder.Body.String())
	}
}

func TestServeMuxWithoutDBDoesNotClaimASandbox(t *testing.T) {
	rt := oumv.NewRuntime(oumv.Config{Runtime: oumv.DefaultDevelopmentRuntimeConfig()})
	mux := stdhttp.NewServeMux(rt)
	mux.Mount("/__oumv/rpc")

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/__oumv/rpc",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"oumv/handshake","params":{}}`))
	request.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(recorder, request)

	var payload struct {
		Result struct {
			Capabilities struct {
				Verify struct {
					SandboxRollback bool `json:"sandboxRollback"`
				} `json:"verify"`
			} `json:"capabilities"`
		} `json:"result"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("handshake body is not JSON: %v", err)
	}
	if payload.Result.Capabilities.Verify.SandboxRollback {
		t.Fatal("a runtime without an engine must not claim a rollback sandbox")
	}
}
