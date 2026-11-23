package db

import (
	"errors"
	"fmt"
	"strings"

	"github.com/tokensafe-dev/tokensafe-backend/pkg/protocol"
)

// SQLFailureCategory categorizes SQL runtime errors for agent reasoning.
type SQLFailureCategory string

const (
	CategoryUniqueViolation     SQLFailureCategory = "UNIQUE_VIOLATION"
	CategoryForeignKeyViolation SQLFailureCategory = "FOREIGN_KEY_VIOLATION"
	CategoryNotNullViolation    SQLFailureCategory = "NOT_NULL_VIOLATION"
	CategoryCheckViolation      SQLFailureCategory = "CHECK_VIOLATION"
	CategoryDeadlock            SQLFailureCategory = "DEADLOCK_DETECTED"
	CategoryConnectionFailed    SQLFailureCategory = "CONNECTION_FAILED"
	CategorySyntaxError         SQLFailureCategory = "SQL_SYNTAX_ERROR"
	CategoryUnknown             SQLFailureCategory = "UNKNOWN_SQL_ERROR"
)

// SQLCausalityDiagnosis represents parsed SQL failure details.
type SQLCausalityDiagnosis struct {
	Category    SQLFailureCategory `json:"category"`
	Table       string             `json:"table,omitempty"`
	Constraint  string             `json:"constraint,omitempty"`
	Column      string             `json:"column,omitempty"`
	Detail      string             `json:"detail,omitempty"`
	Explanation string             `json:"explanation"`
	OriginalErr string             `json:"originalError"`
}

// DiagnoseSQLError inspects any error returned from DB drivers and extracts causal reasoning.
func DiagnoseSQLError(err error) *SQLCausalityDiagnosis {
	if err == nil {
		return nil
	}

	errMsg := err.Error()
	lowerMsg := strings.ToLower(errMsg)

	diag := &SQLCausalityDiagnosis{
		Category:    CategoryUnknown,
		OriginalErr: errMsg,
		Explanation: errMsg,
	}

	// 1. PostgreSQL error parsing (by convention or common string patterns)
	switch {
	case strings.Contains(lowerMsg, "duplicate key") || strings.Contains(lowerMsg, "unique constraint"):
		diag.Category = CategoryUniqueViolation
		diag.Explanation = "A unique database constraint was violated. A row with this primary or unique key already exists."
		extractConstraintAndTable(errMsg, diag)

	case strings.Contains(lowerMsg, "violates foreign key constraint"):
		diag.Category = CategoryForeignKeyViolation
		diag.Explanation = "Foreign key validation failed. Referenced entity does not exist in the foreign table."
		extractConstraintAndTable(errMsg, diag)

	case strings.Contains(lowerMsg, "violates not-null constraint"):
		diag.Category = CategoryNotNullViolation
		diag.Explanation = "A required column was null or missing in the INSERT/UPDATE statement."
		extractColumnAndTable(errMsg, diag)

	case strings.Contains(lowerMsg, "deadlock detected"):
		diag.Category = CategoryDeadlock
		diag.Explanation = "Concurrent database transactions deadlocked while acquiring conflicting row or table locks."

	case strings.Contains(lowerMsg, "connection refused") || strings.Contains(lowerMsg, "driver: bad connection"):
		diag.Category = CategoryConnectionFailed
		diag.Explanation = "Database network connection lost or rejected by server pool."

	case strings.Contains(lowerMsg, "syntax error at or near"):
		diag.Category = CategorySyntaxError
		diag.Explanation = "SQL query contains invalid syntax."
	}

	return diag
}

// AsCausalityResult converts the SQL diagnosis into standard protocol.CausalityResult.
func (d *SQLCausalityDiagnosis) AsCausalityResult(target string) *protocol.CausalityResult {
	traceChain := []string{
		fmt.Sprintf("Target: %s", target),
		fmt.Sprintf("DB Category: %s", d.Category),
	}
	if d.Table != "" {
		traceChain = append(traceChain, fmt.Sprintf("Table: %s", d.Table))
	}
	if d.Constraint != "" {
		traceChain = append(traceChain, fmt.Sprintf("Constraint: %s", d.Constraint))
	}
	if d.Column != "" {
		traceChain = append(traceChain, fmt.Sprintf("Column: %s", d.Column))
	}

	return &protocol.CausalityResult{
		Target:     target,
		StatusCode: 500,
		RootCause:  d.Explanation,
		TraceChain: traceChain,
	}
}

func extractConstraintAndTable(msg string, diag *SQLCausalityDiagnosis) {
	// Pattern e.g. violates foreign key constraint "orders_user_id_fkey"
	if idx := strings.Index(msg, "constraint \""); idx != -1 {
		rest := msg[idx+len("constraint \""):]
		if endIdx := strings.Index(rest, "\""); endIdx != -1 {
			diag.Constraint = rest[:endIdx]
		}
	}
	// Pattern e.g. on table "orders"
	if idx := strings.Index(msg, "table \""); idx != -1 {
		rest := msg[idx+len("table \""):]
		if endIdx := strings.Index(rest, "\""); endIdx != -1 {
			diag.Table = rest[:endIdx]
		}
	}
}

func extractColumnAndTable(msg string, diag *SQLCausalityDiagnosis) {
	if idx := strings.Index(msg, "column \""); idx != -1 {
		rest := msg[idx+len("column \""):]
		if endIdx := strings.Index(rest, "\""); endIdx != -1 {
			diag.Column = rest[:endIdx]
		}
	}
	if idx := strings.Index(msg, "relation \""); idx != -1 {
		rest := msg[idx+len("relation \""):]
		if endIdx := strings.Index(rest, "\""); endIdx != -1 {
			diag.Table = rest[:endIdx]
		}
	}
}

// IsDBError checks if an error matches database error categories.
func IsDBError(err error) bool {
	if err == nil {
		return false
	}
	var diag = DiagnoseSQLError(err)
	return diag.Category != CategoryUnknown
}

// WrapError provides annotated DB error.
func WrapError(err error, contextMsg string) error {
	if err == nil {
		return nil
	}
	return errors.New(contextMsg + ": " + err.Error())
}
