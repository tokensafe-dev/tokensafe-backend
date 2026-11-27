package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
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

type memoryTx struct{}

func (*memoryTx) Commit() error   { return nil }
func (*memoryTx) Rollback() error { return nil }

var registerMemoryDriver sync.Once

// NewMemorySandbox returns a sandbox backed by the rollback-only memory driver.
func NewMemorySandbox() *Sandbox {
	registerMemoryDriver.Do(func() { sql.Register("oumv-memory-sandbox", memoryDriver{}) })
	connection, err := sql.Open("oumv-memory-sandbox", "")
	if err != nil {
		return nil
	}
	return NewSandbox(connection)
}
