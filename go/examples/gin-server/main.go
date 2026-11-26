// Command gin-server is the smallest real OUMV-enabled Gin service: a normal app
// with two routes, one adapter call, and an agent-reachable JSON-RPC endpoint.
//
//	tokensafe gin demo          # or: go run ./examples/gin-server
//	curl -s localhost:8080/__oumv/rpc -H 'Content-Type: application/json' \
//	  -d '{"jsonrpc":"2.0","id":1,"method":"oumv/handshake","params":{}}'
package main

import (
	"log"
	"net/http"
	"os"
	"time"

	"github.com/gin-gonic/gin"
	ginAdapter "github.com/tokensafe-dev/tokensafe-backend/go/pkg/adapters/gin"
	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/oumv"
)

type Order struct {
	ID    string  `json:"id"`
	Total float64 `json:"total"`
}

func main() {
	workspace, err := os.Getwd()
	if err != nil {
		log.Fatal(err)
	}
	addr := "127.0.0.1:8080"
	if v := os.Getenv("OUMV_DEMO_ADDR"); v != "" {
		addr = v
	}

	// OUMV is enabled in development and fails closed elsewhere unless the
	// caller opts in explicitly (spec §8).
	rt := oumv.NewRuntime(oumv.Config{
		Name:         "gin-orders-demo",
		Version:      "0.4.0",
		WorkspaceDir: workspace,
		Runtime: &oumv.RuntimeConfig{
			Enabled:      true,
			Environment:  oumv.EnvDevelopment,
			WorkspaceDir: workspace,
			Transport:    oumv.TransportConfig{Enabled: true, Host: "127.0.0.1", Path: "/__oumv/rpc"},
			Capabilities: oumv.CapabilitiesConfig{Observe: true, Understand: true, Mutate: true, Verify: true},
		},
	})

	engine := gin.New()
	engine.Use(gin.Recovery())

	orders := []Order{{ID: "ord-1", Total: 42.5}}
	engine.GET("/api/v1/orders", func(c *gin.Context) {
		c.JSON(http.StatusOK, orders)
	})
	engine.POST("/api/v1/orders", func(c *gin.Context) {
		var in Order
		if err := c.ShouldBindJSON(&in); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		orders = append(orders, in)
		c.JSON(http.StatusCreated, in)
	})

	// One line wires route discovery, L4 tracing, in-memory verify and the RPC endpoint.
	ginAdapter.Mount(engine, rt)

	srv := &http.Server{
		Addr:              addr,
		Handler:           engine,
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Printf("orders API:  http://%s/api/v1/orders", addr)
	log.Printf("OUMV RPC:    http://%s/__oumv/rpc (POST JSON-RPC 2.0)", addr)
	log.Printf("try:         curl -s %s/__oumv/rpc -H 'Content-Type: application/json' -d '{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"oumv/observe\",\"params\":{\"level\":\"L1\"}}'", "http://"+addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
