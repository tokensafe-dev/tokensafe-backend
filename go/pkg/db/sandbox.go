package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

type contextKey string

const (
	// TxContextKey is the context key for injecting an active dry-run transaction.
	TxContextKey contextKey = "oumv:db:tx"
)

// Sandbox manages transactional dry-run execution with guaranteed rollback.
type Sandbox struct {
	db *sql.DB
	// supportsReads states whether the wired engine can execute read-only
	// statements (§14.2). Demo/certification drivers that only prove rollback
	// construct the sandbox with NewSandboxWithoutReads.
	supportsReads bool
}

// NewSandbox creates a DB sandbox bound to a database connection. The engine is
// assumed able to execute read-only statements (§14.2); if it is not, construct
// with NewSandboxWithoutReads so the handshake stays honest.
func NewSandbox(db *sql.DB) *Sandbox {
	return &Sandbox{db: db, supportsReads: true}
}

// NewSandboxWithoutReads creates a sandbox for engines that can prove rollback
// but cannot execute queries (demo/certification drivers).
func NewSandboxWithoutReads(db *sql.DB) *Sandbox {
	return &Sandbox{db: db, supportsReads: false}
}

// SupportsReads reports whether oumv/query can be served by this engine.
func (s *Sandbox) SupportsReads() bool {
	return s != nil && s.supportsReads
}

// ReadQueryResult carries capped rows from a read-only statement (§14.2).
type ReadQueryResult struct {
	Columns   []string        `json:"columns"`
	Rows      [][]interface{} `json:"rows"`
	RowCount  int             `json:"rowCount"`
	Truncated bool            `json:"truncated"`
}

// allowedQueryPrefixes is the §14.2 statement allow-list; enforcement here is
// defence in depth — the connection should be read-only too.
var allowedQueryPrefixes = map[string]bool{
	"SELECT": true, "EXPLAIN": true, "PRAGMA": true, "WITH": true,
}

// ReadQuery executes a read-only statement with a hard row cap. Non-read-only
// statements are refused with an error the runtime maps to -32003.
func (s *Sandbox) ReadQuery(ctx context.Context, statement string, args []interface{}, limit int) (*ReadQueryResult, error) {
	if s.db == nil {
		return nil, fmt.Errorf("database connection is nil")
	}
	if !s.supportsReads {
		return nil, fmt.Errorf("this engine cannot execute read queries")
	}
	trimmed := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(statement), ";"))
	if trimmed == "" {
		return nil, fmt.Errorf("empty statement")
	}
	first := strings.FieldsFunc(trimmed, func(r rune) bool { return r == ' ' || r == '(' || r == '\n' || r == '\t' })[0]
	if !allowedQueryPrefixes[strings.ToUpper(first)] {
		return nil, fmt.Errorf("only SELECT/EXPLAIN/PRAGMA statements are accepted")
	}
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, trimmed, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	out := &ReadQueryResult{Columns: columns}
	for rows.Next() && out.RowCount < limit {
		values := make([]interface{}, len(columns))
		ptrs := make([]interface{}, len(columns))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		out.Rows = append(out.Rows, values)
		out.RowCount++
	}
	out.Truncated = out.RowCount >= limit
	return out, nil
}

// DryRunReport contains metrics and state diffs from a dry-run execution.
type DryRunReport struct {
	DurationMs   float64                `json:"durationMs"`
	Committed    bool                   `json:"committed"` // Always false in sandbox
	StateDiff    map[string]interface{} `json:"stateDiff"`
	ErrorMessage string                 `json:"errorMessage,omitempty"`
}

// ExecuteDryRun runs fn inside an isolated SQL transaction that is ALWAYS rolled back.
func (s *Sandbox) ExecuteDryRun(ctx context.Context, fn func(ctx context.Context, tx *sql.Tx) error) (*DryRunReport, error) {
	if s.db == nil {
		return nil, fmt.Errorf("database connection is nil")
	}

	start := time.Now()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, fmt.Errorf("failed to begin dry-run transaction: %w", err)
	}

	// Guarantee rollback regardless of panics or errors
	defer func() {
		_ = tx.Rollback()
	}()

	// Inject transaction into context so handlers can extract it
	ctxWithTx := context.WithValue(ctx, TxContextKey, tx)

	execErr := fn(ctxWithTx, tx)
	duration := float64(time.Since(start).Microseconds()) / 1000.0

	report := &DryRunReport{
		DurationMs: duration,
		Committed:  false,
		StateDiff: map[string]interface{}{
			"mode":        "DRY_RUN_SANDBOX",
			"rollback":    true,
			"persistence": "NONE",
		},
	}

	if execErr != nil {
		report.ErrorMessage = execErr.Error()
		return report, execErr
	}

	return report, nil
}

// GetTxFromContext retrieves an active dry-run transaction from context if present.
func GetTxFromContext(ctx context.Context) *sql.Tx {
	if ctx == nil {
		return nil
	}
	if val := ctx.Value(TxContextKey); val != nil {
		if tx, ok := val.(*sql.Tx); ok {
			return tx
		}
	}
	return nil
}
