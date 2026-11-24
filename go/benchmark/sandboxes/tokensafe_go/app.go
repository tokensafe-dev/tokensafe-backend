package tokensafe_go

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/tokensafe-dev/tokensafe-backend/go"
	"github.com/tokensafe-dev/tokensafe-backend/go/benchmark/sandboxes"
)

// TokenSafeApp encapsulates a TokenSafe-instrumented backend with native OUMV capabilities.
type TokenSafeApp struct {
	App *tokensafe.App
	DB  *sandboxes.MemoryDB
}

// NewTokenSafeApp creates a TokenSafe-instrumented app with native path-params, DTO validation, and dry-run rollback.
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

	// 1. Transactional dry-run rollback interceptor
	app.WithDryRunHook(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			snap := db.Clone()
			next.ServeHTTP(w, r)
			db.Restore(snap)
		})
	})

	// 2. Products (Type-safe routing + PathValue)
	app.Get("/api/products", func(c *tokensafe.Context) error {
		return c.JSON(http.StatusOK, db.ListProducts(c.Param("category")))
	})

	app.Get("/api/products/{id}", func(c *tokensafe.Context) error {
		id, err := strconv.Atoi(c.Param("id"))
		if err != nil {
			return tokensafe.NewHTTPError(http.StatusBadRequest, "invalid product id")
		}
		p, ok := db.GetProduct(id)
		if !ok {
			return tokensafe.NewHTTPError(http.StatusNotFound, "product not found")
		}
		return c.JSON(http.StatusOK, p)
	})

	tokensafe.Post(app, "/api/products", func(c *tokensafe.Context, req sandboxes.ProductCreate) (*sandboxes.Product, error) {
		p, err := db.CreateProduct(req)
		if err != nil {
			return nil, tokensafe.NewHTTPError(http.StatusUnprocessableEntity, err.Error())
		}
		c.Status(http.StatusCreated)
		return p, nil
	})

	// 3. Orders (Type-safe endpoints + Automatic validation + Clean HTTPError mapping)
	app.Get("/api/orders", func(c *tokensafe.Context) error {
		return c.JSON(http.StatusOK, db.ListOrders())
	})

	app.Get("/api/orders/{id}", func(c *tokensafe.Context) error {
		orderID := c.Param("id")
		o, ok := db.GetOrder(orderID)
		if !ok {
			return tokensafe.NewHTTPError(http.StatusNotFound, "order not found")
		}
		return c.JSON(http.StatusOK, o)
	})

	tokensafe.Post(app, "/api/orders", func(c *tokensafe.Context, req sandboxes.OrderCreate) (*sandboxes.OrderResponse, error) {
		order, err := db.CreateOrder(req)
		if err != nil {
			errMsg := err.Error()
			if strings.HasPrefix(errMsg, "CONFLICT:") {
				return nil, tokensafe.NewHTTPError(http.StatusConflict, errMsg)
			}
			if strings.HasPrefix(errMsg, "INSUFFICIENT_STOCK:") {
				return nil, tokensafe.NewHTTPError(http.StatusBadRequest, errMsg)
			}
			return nil, tokensafe.NewHTTPError(http.StatusBadRequest, errMsg)
		}
		c.Status(http.StatusCreated)
		return order, nil
	})

	app.Post("/api/orders/{id}/pay", func(c *tokensafe.Context) error {
		orderID := c.Param("id")
		order, err := db.PayOrder(orderID)
		if err != nil {
			if strings.Contains(err.Error(), "not found") {
				return tokensafe.NewHTTPError(http.StatusNotFound, err.Error())
			}
			return tokensafe.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		return c.JSON(http.StatusOK, order)
	})

	app.Post("/api/orders/{id}/ship", func(c *tokensafe.Context) error {
		orderID := c.Param("id")
		order, err := db.ShipOrder(orderID)
		if err != nil {
			if strings.Contains(err.Error(), "not found") {
				return tokensafe.NewHTTPError(http.StatusNotFound, err.Error())
			}
			return tokensafe.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		return c.JSON(http.StatusOK, order)
	})

	app.Post("/api/orders/{id}/cancel", func(c *tokensafe.Context) error {
		orderID := c.Param("id")
		order, err := db.CancelOrder(orderID)
		if err != nil {
			if strings.Contains(err.Error(), "not found") {
				return tokensafe.NewHTTPError(http.StatusNotFound, err.Error())
			}
			return tokensafe.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		return c.JSON(http.StatusOK, order)
	})

	return tsApp
}

func (a *TokenSafeApp) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.App.ServeHTTP(w, r)
}
