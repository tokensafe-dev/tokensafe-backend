package oumv_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/db"
	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/oumv"
	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/protocol"
)

// Minimal database/sql driver: enough to prove that verify's sandbox begins a
// transaction, injects it into the request context and rolls it back.
type sandboxDriver struct{}

func (sandboxDriver) Open(string) (driver.Conn, error) { return &sandboxConn{}, nil }

type sandboxConn struct{}

func (c *sandboxConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare unsupported")
}
func (c *sandboxConn) Close() error              { return nil }
func (c *sandboxConn) Begin() (driver.Tx, error) { return &sandboxTx{}, nil }
func (c *sandboxConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return &sandboxTx{}, nil
}
func (c *sandboxConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return driver.RowsAffected(1), nil
}

type sandboxTx struct{}

var sandboxCommits, sandboxRollbacks int32

func (*sandboxTx) Commit() error   { atomic.AddInt32(&sandboxCommits, 1); return nil }
func (*sandboxTx) Rollback() error { atomic.AddInt32(&sandboxRollbacks, 1); return nil }

func init() { sql.Register("oumv-sandbox-test", sandboxDriver{}) }

func TestVerifySandboxRollsBackWithEvidence(t *testing.T) {
	connection, err := sql.Open("oumv-sandbox-test", "")
	if err != nil {
		t.Fatalf("open fake driver: %v", err)
	}
	defer func() { _ = connection.Close() }()

	rt := oumv.NewRuntime(oumv.Config{
		Name:         "sandbox",
		WorkspaceDir: t.TempDir(),
		Runtime:      oumv.DefaultDevelopmentRuntimeConfig(),
	})
	rt.SetDBSandbox(db.NewSandbox(connection))
	rt.SetHandler(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		tx := db.GetTxFromContext(req.Context())
		if tx == nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if req.Header.Get("X-TokenSafe-Dry-Run") != "true" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if _, execErr := tx.Exec("UPDATE items SET name = 'b'"); execErr != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))

	result, oumvErr := rt.Verify(context.Background(), &protocol.VerifyParams{
		Target:  "POST /api/items",
		Sandbox: &protocol.VerifySandbox{Mode: "rollback"},
	})
	if oumvErr != nil {
		t.Fatalf("verify failed: %+v", oumvErr)
	}
	if result.Status != "passed" {
		t.Fatalf("status = %q, want passed", result.Status)
	}
	if result.RolledBack == nil || !*result.RolledBack {
		t.Fatalf("rolledBack must be true, got %v", result.RolledBack)
	}
	if result.RollbackStatus != "verified" {
		t.Fatalf("rollbackStatus = %q, want verified", result.RollbackStatus)
	}
	if result.DBDiff == nil || !result.DBDiff.Rollback {
		t.Fatalf("dbDiff must report rollback=true, got %+v", result.DBDiff)
	}
	for _, item := range result.Scope.NotExecuted {
		if item == "persistent database state" {
			t.Fatalf("scope.notExecuted must drop persistent database state after a verified rollback")
		}
	}
	if atomic.LoadInt32(&sandboxRollbacks) == 0 {
		t.Fatal("no ROLLBACK reached the driver")
	}
	if atomic.LoadInt32(&sandboxCommits) != 0 {
		t.Fatal("the dry-run transaction must never commit")
	}

	handshake, handshakeErr := rt.Handshake(context.Background(), &protocol.OumvHandshakeParams{})
	if handshakeErr != nil {
		t.Fatalf("handshake failed: %+v", handshakeErr)
	}
	if !handshake.Capabilities.Verify.SandboxRollback {
		t.Fatal("sandboxRollback must be advertised when a sandbox is configured")
	}
	if len(handshake.Capabilities.Transports) == 0 {
		t.Fatal("handshake must advertise the mounted transports")
	}
}

func TestVerifySandboxUnavailableWithoutSandbox(t *testing.T) {
	rt := oumv.NewRuntime(oumv.Config{
		Name:         "no-sandbox",
		WorkspaceDir: t.TempDir(),
		Runtime:      oumv.DefaultDevelopmentRuntimeConfig(),
	})
	rt.SetHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))

	_, oumvErr := rt.Verify(context.Background(), &protocol.VerifyParams{
		Target:  "GET /api/items",
		Sandbox: &protocol.VerifySandbox{Mode: "rollback"},
	})
	if oumvErr == nil || oumvErr.Code != protocol.ErrRuntimeUnavailable {
		t.Fatalf("want -32008 without a sandbox, got %+v", oumvErr)
	}
	_, unknown := rt.Verify(context.Background(), &protocol.VerifyParams{
		Target:  "GET /api/items",
		Sandbox: &protocol.VerifySandbox{Mode: "commit"},
	})
	if unknown == nil || unknown.Code != protocol.ErrUnsupportedCapability {
		t.Fatalf("want -32006 for an unknown mode, got %+v", unknown)
	}
}

func TestVerifyMultiStepStatusFailsWhenAStepFails(t *testing.T) {
	rt := oumv.NewRuntime(oumv.Config{
		Name:         "steps",
		WorkspaceDir: t.TempDir(),
		Runtime:      oumv.DefaultDevelopmentRuntimeConfig(),
	})
	rt.SetHandler(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/fail" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))

	result, oumvErr := rt.Verify(context.Background(), &protocol.VerifyParams{
		Steps: []protocol.VerifyStep{{Target: "POST /ok"}, {Target: "POST /fail"}},
	})
	if oumvErr != nil {
		t.Fatalf("verify failed: %+v", oumvErr)
	}
	if result.Status != "failed" || result.Success {
		t.Fatalf("a failed step must fail the request, got status=%q success=%v", result.Status, result.Success)
	}
}
