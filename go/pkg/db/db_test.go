package db_test

import (
	"errors"
	"testing"

	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/db"
)

type SampleOrder struct {
	ID        string  `db:"order_id" json:"id" gorm:"primaryKey"`
	UserID    string  `db:"user_id" json:"userId"`
	Amount    float64 `db:"amount" json:"amount"`
	Notes     *string `db:"notes" json:"notes"` // Nullable
}

func (SampleOrder) TableName() string {
	return "custom_orders"
}

func TestReflectModel(t *testing.T) {
	contract := db.ReflectModel(SampleOrder{})

	if contract.TableName != "custom_orders" {
		t.Errorf("expected table custom_orders, got %s", contract.TableName)
	}

	if len(contract.Fields) != 4 {
		t.Fatalf("expected 4 fields, got %d", len(contract.Fields))
	}

	idField := contract.Fields[0]
	if !idField.IsPrimary || idField.DBColumn != "order_id" {
		t.Errorf("unexpected ID field: %+v", idField)
	}

	notesField := contract.Fields[3]
	if !notesField.IsNullable {
		t.Errorf("expected notes field to be nullable")
	}

	contractData := contract.AsContractData()
	if contractData.Target != "custom_orders" {
		t.Errorf("expected contract target custom_orders, got %s", contractData.Target)
	}
}

func TestDiagnoseSQLError(t *testing.T) {
	// 1. Unique constraint error
	uniqueErr := errors.New("pq: duplicate key value violates unique constraint \"orders_pkey\" on table \"orders\"")
	diag := db.DiagnoseSQLError(uniqueErr)

	if diag.Category != db.CategoryUniqueViolation {
		t.Errorf("expected UNIQUE_VIOLATION, got %s", diag.Category)
	}
	if diag.Constraint != "orders_pkey" {
		t.Errorf("expected constraint orders_pkey, got %s", diag.Constraint)
	}
	if diag.Table != "orders" {
		t.Errorf("expected table orders, got %s", diag.Table)
	}

	// 2. Foreign key violation
	fkErr := errors.New("ERROR: insert or update on table \"orders\" violates foreign key constraint \"orders_user_id_fkey\"")
	diagFK := db.DiagnoseSQLError(fkErr)
	if diagFK.Category != db.CategoryForeignKeyViolation {
		t.Errorf("expected FOREIGN_KEY_VIOLATION, got %s", diagFK.Category)
	}

	// 3. Not-null violation
	nnErr := errors.New("pq: null value in column \"amount\" of relation \"orders\" violates not-null constraint")
	diagNN := db.DiagnoseSQLError(nnErr)
	if diagNN.Category != db.CategoryNotNullViolation {
		t.Errorf("expected NOT_NULL_VIOLATION, got %s", diagNN.Category)
	}
	if diagNN.Column != "amount" {
		t.Errorf("expected column amount, got %s", diagNN.Column)
	}

	// 4. Causality result conversion
	causality := diag.AsCausalityResult("POST /api/v1/orders")
	if causality.StatusCode != 500 || causality.Target != "POST /api/v1/orders" {
		t.Errorf("unexpected causality result: %+v", causality)
	}
}
