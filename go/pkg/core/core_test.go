package core_test

import (
	"net/http"
	"testing"

	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/core"
)

type ValidatedItem struct {
	Title string  `json:"title" validate:"required,min=2"`
	Price float64 `json:"price" validate:"required,gt=0"`
}

type ItemResponse struct {
	ID    string  `json:"id"`
	Title string  `json:"title"`
	Price float64 `json:"price"`
}

func TestParamPathValue(t *testing.T) {
	app := core.New()

	app.Get("/api/items/{id}", func(c *core.Context) error {
		id := c.Param("id")
		return c.JSON(http.StatusOK, map[string]string{"id": id})
	})

	res := app.Simulate(http.MethodGet, "/api/items/item_42", nil)
	if res.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", res.Code)
	}

	var data map[string]string
	if err := res.JSON(&data); err != nil {
		t.Fatalf("failed to decode JSON: %v", err)
	}
	if data["id"] != "item_42" {
		t.Errorf("expected id item_42, got %s", data["id"])
	}
}

func TestTypedHandlerHTTPErrorAndCustomStatus(t *testing.T) {
	app := core.New()

	core.PostTyped(app, "/api/items", func(c *core.Context, req ValidatedItem) (*ItemResponse, error) {
		if req.Title == "conflict" {
			return nil, core.NewHTTPError(http.StatusConflict, "item already exists", "duplicate title")
		}
		c.Status(http.StatusCreated)
		return &ItemResponse{ID: "new_1", Title: req.Title, Price: req.Price}, nil
	})

	// 1. Success with custom status 201
	res := app.Simulate(http.MethodPost, "/api/items", ValidatedItem{Title: "Widget", Price: 19.99})
	if res.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created, got %d", res.Code)
	}

	// 2. Custom HTTPError status 409
	errRes := app.Simulate(http.MethodPost, "/api/items", ValidatedItem{Title: "conflict", Price: 19.99})
	if errRes.Code != http.StatusConflict {
		t.Fatalf("expected 409 Conflict, got %d", errRes.Code)
	}

	// 3. Validation failure status 422
	valRes := app.Simulate(http.MethodPost, "/api/items", map[string]interface{}{"title": "A", "price": -5.0})
	if valRes.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 Unprocessable Entity, got %d", valRes.Code)
	}
}

func TestDryRunHookExecution(t *testing.T) {
	app := core.New()
	hookCalled := false

	app.WithDryRunHook(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hookCalled = true
			next.ServeHTTP(w, r)
		})
	})

	app.Get("/ping", func(c *core.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"msg": "pong"})
	})

	// Request without dry-run header
	app.Simulate(http.MethodGet, "/ping", nil)
	if hookCalled {
		t.Fatalf("expected hook NOT to be called without dry-run header")
	}

	// Request WITH dry-run header
	app.Simulate(http.MethodGet, "/ping", nil, map[string]string{"X-TokenSafe-Dry-Run": "true"})
	if !hookCalled {
		t.Fatalf("expected hook to be called when X-TokenSafe-Dry-Run is true")
	}
}
