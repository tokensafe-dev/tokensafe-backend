package tokensafe_go

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/tokensafe-dev/tokensafe-backend"
	"github.com/tokensafe-dev/tokensafe-backend/benchmark/sandboxes"
)

// TokenSafeApp encapsulates a TokenSafe-instrumented backend with OUMV runtime.
type TokenSafeApp struct {
	App *tokensafe.App
	DB  *sandboxes.MemoryDB
}

// NewTokenSafeApp creates a TokenSafe-instrumented app with dry-run rollback support.
func NewTokenSafeApp(db *sandboxes.MemoryDB) *TokenSafeApp {
	if db == nil {
		db = sandboxes.NewMemoryDB()
	}

	app := tokensafe.New(tokensafe.AppConfig{
		Name:    "order-processing-api",
		Version: "1.0.0",
		RPCPath: "/__oumv/rpc",
	})

	tsApp := &TokenSafeApp{
		App: app,
		DB:  db,
	}

	// Middleware: Dry-run transaction rollback interceptor
	app.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			isDryRun := r.Header.Get("X-TokenSafe-Dry-Run") == "true"
			var snap *sandboxes.MemoryDB
			if isDryRun {
				snap = db.Clone()
			}

			next.ServeHTTP(w, r)

			if isDryRun && snap != nil {
				db.Restore(snap)
			}
		})
	})

	// 1. Products
	app.Get("/api/products", func(c *tokensafe.Context) error {
		cat := c.Request.URL.Query().Get("category")
		return c.JSON(http.StatusOK, db.ListProducts(cat))
	})

	app.Get("/api/products/", func(c *tokensafe.Context) error {
		path := c.Request.URL.Path
		parts := strings.Split(strings.Trim(path, "/"), "/")
		if len(parts) < 3 {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid product id"})
		}
		id, err := strconv.Atoi(parts[2])
		if err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid product id"})
		}
		p, ok := db.GetProduct(id)
		if !ok {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "product not found"})
		}
		return c.JSON(http.StatusOK, p)
	})

	app.Post("/api/products", func(c *tokensafe.Context) error {
		var req sandboxes.ProductCreate
		if err := json.NewDecoder(c.Request.Body).Decode(&req); err != nil {
			return c.JSON(http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		}
		p, err := db.CreateProduct(req)
		if err != nil {
			return c.JSON(http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		}
		return c.JSON(http.StatusCreated, p)
	})

	// 2. Orders
	app.Get("/api/orders", func(c *tokensafe.Context) error {
		return c.JSON(http.StatusOK, db.ListOrders())
	})

	app.Post("/api/orders", func(c *tokensafe.Context) error {
		var req sandboxes.OrderCreate
		if err := json.NewDecoder(c.Request.Body).Decode(&req); err != nil {
			return c.JSON(http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		}
		order, err := db.CreateOrder(req)
		if err != nil {
			errMsg := err.Error()
			if strings.HasPrefix(errMsg, "CONFLICT:") {
				return c.JSON(http.StatusConflict, map[string]string{"error": errMsg})
			}
			if strings.HasPrefix(errMsg, "INSUFFICIENT_STOCK:") {
				return c.JSON(http.StatusBadRequest, map[string]string{"error": errMsg})
			}
			return c.JSON(http.StatusBadRequest, map[string]string{"error": errMsg})
		}
		return c.JSON(http.StatusCreated, order)
	})

	// Sub-resource dispatcher for /api/orders/
	ordersHandler := func(c *tokensafe.Context) error {
		path := c.Request.URL.Path
		parts := strings.Split(strings.Trim(path, "/"), "/")
		if len(parts) < 3 {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "missing order id"})
		}
		orderID := parts[2]

		if c.Request.Method == http.MethodGet && len(parts) == 3 {
			o, ok := db.GetOrder(orderID)
			if !ok {
				return c.JSON(http.StatusNotFound, map[string]string{"error": "order not found"})
			}
			return c.JSON(http.StatusOK, o)
		}

		if c.Request.Method == http.MethodPost && len(parts) == 4 {
			action := parts[3]
			switch action {
			case "pay":
				order, err := db.PayOrder(orderID)
				if err != nil {
					if strings.Contains(err.Error(), "not found") {
						return c.JSON(http.StatusNotFound, map[string]string{"error": err.Error()})
					}
					return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
				}
				return c.JSON(http.StatusOK, order)
			case "ship":
				order, err := db.ShipOrder(orderID)
				if err != nil {
					if strings.Contains(err.Error(), "not found") {
						return c.JSON(http.StatusNotFound, map[string]string{"error": err.Error()})
					}
					return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
				}
				return c.JSON(http.StatusOK, order)
			case "cancel":
				order, err := db.CancelOrder(orderID)
				if err != nil {
					if strings.Contains(err.Error(), "not found") {
						return c.JSON(http.StatusNotFound, map[string]string{"error": err.Error()})
					}
					return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
				}
				return c.JSON(http.StatusOK, order)
			default:
				return c.JSON(http.StatusNotFound, map[string]string{"error": fmt.Sprintf("unknown action %s", action)})
			}
		}

		return c.JSON(http.StatusNotFound, map[string]string{"error": "endpoint not found"})
	}

	app.Get("/api/orders/", ordersHandler)
	app.Post("/api/orders/", ordersHandler)

	return tsApp
}

func (a *TokenSafeApp) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.App.ServeHTTP(w, r)
}
