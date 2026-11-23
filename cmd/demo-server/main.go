package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"

	"github.com/tokensafe-dev/tokensafe-backend/pkg/adapters/stdhttp"
	"github.com/tokensafe-dev/tokensafe-backend/pkg/db"
	"github.com/tokensafe-dev/tokensafe-backend/pkg/oumv"
)

// Order represents an e-commerce order with database tags for OUMV L2 reflection.
type Order struct {
	ID       string  `db:"order_id" json:"id" gorm:"primaryKey"`
	Item     string  `db:"item_name" json:"item"`
	Amount   float64 `db:"amount" json:"amount"`
	Customer string  `db:"customer_id" json:"customerId"`
	Notes    *string `db:"notes" json:"notes"`
}

func (Order) TableName() string {
	return "orders"
}

func main() {
	// Initialize OUMV Runtime Adapter (UARP)
	rt := oumv.NewRuntime(oumv.Config{
		Name:         "demo-service",
		Version:      "1.0.0",
		WorkspaceDir: ".",
	})

	// Wrap ServeMux with OUMV auto-introspection
	mux := stdhttp.NewServeMux(rt)

	// Business API routes (automatically registered in L1 Surface)
	mux.HandleFunc("GET /api/v1/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status":  "healthy",
			"runtime": "tokensafe-backend",
		})
	})

	mux.HandleFunc("GET /api/v1/orders", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		orders := []Order{
			{ID: "ord-1", Item: "Widget A", Amount: 49.99, Customer: "cust-101"},
			{ID: "ord-2", Item: "Widget B", Amount: 19.99, Customer: "cust-102"},
		}
		_ = json.NewEncoder(w).Encode(orders)
	})

	// Mount OUMV JSON-RPC endpoint at /__oumv/rpc
	mux.Mount("/__oumv/rpc")

	// Print reflected L2 DB model
	tableContract := db.ReflectModel(Order{})
	fmt.Printf("📦 DB Model Reflected: %s (%d fields)\n", tableContract.TableName, len(tableContract.Fields))

	port := 8080
	fmt.Printf("🚀 Demo backend listening on http://localhost:%d\n", port)
	fmt.Printf("⚡ OUMV JSON-RPC endpoint active at http://localhost:%d/__oumv/rpc\n", port)

	if err := http.ListenAndServe(fmt.Sprintf(":%d", port), mux); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}
