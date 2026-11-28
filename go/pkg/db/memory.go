package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"sync"
)

// The memory driver backs NewMemorySandbox: a database/sql driver whose
// transactions always roll back and whose statements touch nothing. It exists so
// a demo or certification server can prove verify.sandboxRollback without
// shipping a SQL engine; production adopters pass their own *sql.DB.
type memoryDriver struct{}

func (memoryDriver) Open(string) (driver.Conn, error) { return &memoryConn{}, nil }

type memoryConn struct{}

func (c *memoryConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare unsupported")
}
func (c *memoryConn) Close() error              { return nil }
func (c *memoryConn) Begin() (driver.Tx, error) { return &memoryTx{}, nil }
func (c *memoryConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return &memoryTx{}, nil
}
func (c *memoryConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return driver.RowsAffected(1), nil
}

// QueryContext answers any read with one synthetic row: the policy certificate
// needs an engine that can execute a SELECT (observeQuery.readOnly) without
// shipping a SQL engine.
func (c *memoryConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return &memoryRows{}, nil
}

// memoryRows is the single-row synthetic result set the memory driver returns.
type memoryRows struct{ read bool }

func (*memoryRows) Columns() []string { return []string{"v"} }
func (*memoryRows) Close() error      { return nil }
func (r *memoryRows) Next(dest []driver.Value) error {
	if r.read {
		return io.EOF
	}
	r.read = true
	if len(dest) > 0 {
		dest[0] = int64(1)
	}
	return nil
}

type memoryTx struct{}

func (*memoryTx) Commit() error   { return nil }
func (*memoryTx) Rollback() error { return nil }

var registerMemoryDriver sync.Once

// NewMemorySandbox returns a sandbox backed by the rollback-only memory driver.
func NewMemorySandbox() *Sandbox {
	connection := openMemorySandbox()
	if connection == nil {
		return nil
	}
	// The memory driver proves rollback only: the handshake must not advertise
	// observeQuery for an engine that cannot execute queries.
	return NewSandboxWithoutReads(connection)
}

// NewMemorySandboxWithReads returns the same rollback-only sandbox, but with the
// driver's synthetic SELECT behind it, so observeQuery.readOnly is advertised and
// the §14.2 policy certificate has a real query method to refuse.
func NewMemorySandboxWithReads() *Sandbox {
	connection := openMemorySandbox()
	if connection == nil {
		return nil
	}
	return NewSandbox(connection)
}

func openMemorySandbox() *sql.DB {
	registerMemoryDriver.Do(func() { sql.Register("oumv-memory-sandbox", memoryDriver{}) })
	connection, err := sql.Open("oumv-memory-sandbox", "")
	if err != nil {
		return nil
	}
	return connection
}
