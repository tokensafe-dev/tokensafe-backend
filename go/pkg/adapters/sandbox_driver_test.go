package adapters_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/db"
)

// certSandboxDriver is the minimal database/sql driver the certification harness
// needs to prove verify.sandboxRollback: BeginTx, Exec and Rollback.
type certSandboxDriver struct{}

func (certSandboxDriver) Open(string) (driver.Conn, error) { return &certSandboxConn{}, nil }

type certSandboxConn struct{}

func (c *certSandboxConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare unsupported")
}
func (c *certSandboxConn) Close() error              { return nil }
func (c *certSandboxConn) Begin() (driver.Tx, error) { return &certSandboxTx{}, nil }
func (c *certSandboxConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return &certSandboxTx{}, nil
}
func (c *certSandboxConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return driver.RowsAffected(1), nil
}

type certSandboxTx struct{}

var certSandboxRollbacks int32

func (*certSandboxTx) Commit() error   { return nil }
func (*certSandboxTx) Rollback() error { atomic.AddInt32(&certSandboxRollbacks, 1); return nil }

func init() { sql.Register("oumv-cert-sandbox", certSandboxDriver{}) }

// newCertSandbox returns a sandbox wired to the fake driver.
func newCertSandbox(t *testing.T) *db.Sandbox {
	t.Helper()
	connection, err := sql.Open("oumv-cert-sandbox", "")
	if err != nil {
		t.Fatalf("open cert sandbox driver: %v", err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	return db.NewSandbox(connection)
}

// sandboxProbe is the route the capability case dispatches: it only writes when
// the runtime opened a transaction and set the dry-run header.
func sandboxProbe(w http.ResponseWriter, req *http.Request) {
	tx := db.GetTxFromContext(req.Context())
	if tx == nil || req.Header.Get("X-TokenSafe-Dry-Run") != "true" {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if _, err := tx.Exec("UPDATE cert_probe SET v = 1"); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"ok":true}`))
}
