package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"

	"github.com/tokensafe-dev/tokensafe-backend/pkg/oumv"
	"github.com/tokensafe-dev/tokensafe-backend/pkg/transport"
)

type Order struct {
	ID     string  `json:"id"`
	Item   string  `json:"item"`
	Amount float64 `json:"amount"`
}

func main() {
	mux := http.NewServeMux()

	// Business API routes
	mux.HandleFunc("GET /api/v1/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "healthy"})
	})

	mux.HandleFunc("GET /api/v1/orders", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		orders := []Order{
			{ID: "ord-1", Item: "Widget A", Amount: 49.99},
			{ID: "ord-2", Item: "Widget B", Amount: 19.99},
		}
		_ = json.NewEncoder(w).Encode(orders)
	})

	// Initialize OUMV Runtime Adapter
	rt := oumv.NewRuntime(oumv.Config{
		Name:         "demo-service",
		Version:      "1.0.0",
		WorkspaceDir: ".",
		Handler:      mux,
	})

	// Register routes in L1 Surface
	rt.RegisterRoute("GET", "/api/v1/health", "healthHandler")
	rt.RegisterRoute("GET", "/api/v1/orders", "listOrdersHandler")

	// Mount OUMV JSON-RPC endpoint
	dispatcher := transport.NewDispatcher(rt)
	mux.Handle("/__oumv/rpc", transport.NewHTTPHandler(dispatcher))

	port := 8080
	fmt.Printf("🚀 Demo backend listening on http://localhost:%d\n", port)
	fmt.Printf("⚡ OUMV JSON-RPC endpoint active at http://localhost:%d/__oumv/rpc\n", port)

	if err := http.ListenAndServe(fmt.Sprintf(":%d", port), mux); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}
