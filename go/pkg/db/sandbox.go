package db

import (
	"context"
	"database/sql"
	"fmt"
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
}

// NewSandbox creates a DB sandbox bound to a database connection.
func NewSandbox(db *sql.DB) *Sandbox {
	return &Sandbox{db: db}
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
