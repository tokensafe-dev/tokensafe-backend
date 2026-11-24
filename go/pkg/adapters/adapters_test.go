package adapters_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	chiRouter "github.com/go-chi/chi/v5"
	chiAdapter "github.com/tokensafe-dev/tokensafe-backend/go/pkg/adapters/chi"
	ginAdapter "github.com/tokensafe-dev/tokensafe-backend/go/pkg/adapters/gin"
	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/adapters/stdhttp"
	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/oumv"
	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/protocol"
)

func TestStdHTTPAdapter(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	rt := oumv.NewRuntime(oumv.Config{Name: "stdhttp-test"})
	mux := stdhttp.NewServeMux(rt)

	mux.HandleFunc("GET /api/v1/users", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.Mount("/__oumv/rpc")

	obsResult, err := rt.Observe(context.Background(), &protocol.ObserveParams{Level: protocol.LevelL1Surface})
	if err != nil {
		t.Fatalf("observe L1 failed: %v", err)
	}

	surface, ok := obsResult.Data.(protocol.L1SurfaceData)
	if !ok || len(surface.Endpoints) != 1 {
		t.Fatalf("expected 1 route auto-discovered, got %d", len(surface.Endpoints))
	}
	if surface.Endpoints[0].Path != "/api/v1/users" {
		t.Errorf("expected path /api/v1/users, got %s", surface.Endpoints[0].Path)
	}
}

func TestChiAdapter(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	rt := oumv.NewRuntime(oumv.Config{Name: "chi-test"})
	r := chiRouter.NewRouter()

	r.Get("/api/v1/orders", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	r.Post("/api/v1/orders", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})

	if err := chiAdapter.Mount(r, rt); err != nil {
		t.Fatalf("failed to mount chi adapter: %v", err)
	}

	obsResult, err := rt.Observe(context.Background(), &protocol.ObserveParams{Level: protocol.LevelL1Surface})
	if err != nil {
		t.Fatalf("observe L1 failed: %v", err)
	}

	surface, ok := obsResult.Data.(protocol.L1SurfaceData)
	if !ok || len(surface.Endpoints) != 2 {
		t.Fatalf("expected 2 routes auto-discovered by chi adapter, got %d", len(surface.Endpoints))
	}
}

func TestGinAdapter(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	gin.SetMode(gin.TestMode)
	rt := oumv.NewRuntime(oumv.Config{Name: "gin-test"})
	r := gin.New()

	r.GET("/api/v1/products", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	ginAdapter.Mount(r, rt)

	obsResult, err := rt.Observe(context.Background(), &protocol.ObserveParams{Level: protocol.LevelL1Surface})
	if err != nil {
		t.Fatalf("observe L1 failed: %v", err)
	}

	surface, ok := obsResult.Data.(protocol.L1SurfaceData)
	if !ok || len(surface.Endpoints) != 1 {
		t.Fatalf("expected 1 route auto-discovered by gin adapter, got %d", len(surface.Endpoints))
	}

	// Test calling OUMV JSON-RPC endpoint through gin
	req := httptest.NewRequest(http.MethodPost, "/__oumv/rpc", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	// Handled by JSON-RPC handler
}
