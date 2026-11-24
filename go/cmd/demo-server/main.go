package main

import (
	"fmt"
	"log"

	"github.com/tokensafe-dev/tokensafe-backend/go"
	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/db"
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

type CreateOrderReq struct {
	Item     string  `json:"item"`
	Amount   float64 `json:"amount"`
	Customer string  `json:"customerId"`
}

func main() {
	// 1. Initialize native TokenSafe Agent-First application
	app := tokensafe.New(tokensafe.AppConfig{
		Name:    "demo-service",
		Version: "1.0.0",
	})

	// 2. Type-Safe Endpoints (L1 routes + L2 schemas automatically reflected)
	tokensafe.Get(app, "/api/v1/health", func(c *tokensafe.Context) (*map[string]string, error) {
		return &map[string]string{
			"status":  "healthy",
			"runtime": "tokensafe-backend (native core)",
		}, nil
	})

	tokensafe.Get(app, "/api/v1/orders", func(c *tokensafe.Context) (*[]Order, error) {
		orders := []Order{
			{ID: "ord-1", Item: "Widget A", Amount: 49.99, Customer: "cust-101"},
			{ID: "ord-2", Item: "Widget B", Amount: 19.99, Customer: "cust-102"},
		}
		return &orders, nil
	})

	tokensafe.Post(app, "/api/v1/orders", func(c *tokensafe.Context, req CreateOrderReq) (*Order, error) {
		newOrder := Order{
			ID:       "ord-new",
			Item:     req.Item,
			Amount:   req.Amount,
			Customer: req.Customer,
		}
		return &newOrder, nil
	})

	// Print reflected L2 DB model
	tableContract := db.ReflectModel(Order{})
	fmt.Printf("📦 DB Model Reflected: %s (%d fields)\n", tableContract.TableName, len(tableContract.Fields))

	// 3. Start server with built-in OUMV live RPC endpoint at /__oumv/rpc
	if err := app.Run(":8080"); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}
