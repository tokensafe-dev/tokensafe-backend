package tokensafe_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tokensafe-dev/tokensafe-backend"
	"github.com/tokensafe-dev/tokensafe-backend/pkg/protocol"
)

type TestCreateItemReq struct {
	Name  string  `json:"name"`
	Price float64 `json:"price"`
}

type TestItemResp struct {
	ID    string  `json:"id"`
	Name  string  `json:"name"`
	Price float64 `json:"price"`
}

func TestCoreFrameworkLifecycle(t *testing.T) {
	app := tokensafe.New(tokensafe.AppConfig{
		Name:    "core-test-app",
		Version: "1.0.0",
	})

	// 1. Register Type-Safe Endpoints
	tokensafe.Get(app, "/api/v1/ping", func(c *tokensafe.Context) (*map[string]string, error) {
		return &map[string]string{"message": "pong"}, nil
	})

	tokensafe.Post(app, "/api/v1/items", func(c *tokensafe.Context, req TestCreateItemReq) (*TestItemResp, error) {
		return &TestItemResp{
			ID:    "item-101",
			Name:  req.Name,
			Price: req.Price,
		}, nil
	})

	// 2. Test standard HTTP execution
	postBody, _ := json.Marshal(TestCreateItemReq{Name: "Book", Price: 19.99})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/items", bytes.NewReader(postBody))
	rec := httptest.NewRecorder()

	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}

	var resp TestItemResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.ID != "item-101" || resp.Name != "Book" {
		t.Errorf("unexpected response: %+v", resp)
	}

	// 3. Test OUMV L1 Surface Discovery
	rt := app.Runtime()
	obsResult, oumvErr := rt.Observe(context.Background(), &protocol.ObserveParams{Level: protocol.LevelL1Surface})
	if oumvErr != nil {
		t.Fatalf("OUMV observe failed: %v", oumvErr)
	}

	surface, ok := obsResult.Data.(protocol.L1SurfaceData)
	if !ok {
		t.Fatalf("expected L1SurfaceData, got %T", obsResult.Data)
	}
	if len(surface.Endpoints) != 2 {
		t.Errorf("expected 2 endpoints in L1 Surface, got %d", len(surface.Endpoints))
	}

	// 4. Test OUMV Verify (Pillar 4)
	verifyResult, oumvErr := rt.Verify(context.Background(), &protocol.VerifyParams{
		Target: "GET /api/v1/ping",
		Action: "invoke",
	})
	if oumvErr != nil {
		t.Fatalf("OUMV verify failed: %v", oumvErr)
	}
	if !verifyResult.Success || verifyResult.StatusCode != http.StatusOK {
		t.Errorf("expected verify success, got: %+v", verifyResult)
	}
}
