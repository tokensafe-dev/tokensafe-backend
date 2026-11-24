package oracle

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/tokensafe-dev/tokensafe-backend/go/benchmark"
	"github.com/tokensafe-dev/tokensafe-backend/go/benchmark/sandboxes"
)

// OrderApiOracle evaluates ground truth functional acceptance across 12 assertions.
type OrderApiOracle struct{}

func NewOrderApiOracle() *OrderApiOracle {
	return &OrderApiOracle{}
}

// Evaluate runs all 12 assertions against the provided HTTP handler.
func (o *OrderApiOracle) Evaluate(handler http.Handler, isTokenSafe bool) *benchmark.OracleEvaluationReport {
	startAll := time.Now()
	assertions := make([]benchmark.OracleAssertionResult, 0, 12)

	var createdProductID int
	var order1ID string

	runAssertion := func(id, title string, fn func() error) {
		start := time.Now()
		err := fn()
		duration := float64(time.Since(start).Microseconds()) / 1000.0

		result := benchmark.OracleAssertionResult{
			ID:         id,
			Title:      title,
			Passed:     err == nil,
			DurationMs: duration,
		}
		if err != nil {
			result.Error = err.Error()
		}
		assertions = append(assertions, result)
	}

	doReq := func(method, path string, body interface{}, headers map[string]string) (*http.Response, []byte, error) {
		var bodyReader io.Reader
		if body != nil {
			b, err := json.Marshal(body)
			if err != nil {
				return nil, nil, err
			}
			bodyReader = bytes.NewReader(b)
		}
		req := httptest.NewRequest(method, path, bodyReader)
		req.Header.Set("Content-Type", "application/json")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Result(), rec.Body.Bytes(), nil
	}

	// TC01: Initial catalog inspection
	runAssertion("TC01_INITIAL_CATALOG", "Initial catalog inspection & inventory check", func() error {
		resp, body, err := doReq(http.MethodGet, "/api/products", nil, nil)
		if err != nil {
			return err
		}
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("expected 200, got %d: %s", resp.StatusCode, string(body))
		}
		var products []sandboxes.Product
		if err := json.Unmarshal(body, &products); err != nil {
			return err
		}
		if len(products) < 2 {
			return fmt.Errorf("expected at least 2 pre-seeded products, got %d", len(products))
		}
		titles := ""
		for _, p := range products {
			titles += p.Title + " "
		}
		if !strings.Contains(titles, "Mechanical Keyboard") || !strings.Contains(titles, "Ergonomic Mouse") {
			return fmt.Errorf("missing pre-seeded products: %s", titles)
		}
		return nil
	})

	// TC02: Create valid product
	runAssertion("TC02_CREATE_PRODUCT", "Create new product with validation", func() error {
		payload := sandboxes.ProductCreate{Title: "USB-C Hub", Price: 39.99, Stock: 15, Category: "electronics"}
		resp, body, err := doReq(http.MethodPost, "/api/products", payload, nil)
		if err != nil {
			return err
		}
		if resp.StatusCode != http.StatusCreated {
			return fmt.Errorf("expected 201, got %d: %s", resp.StatusCode, string(body))
		}
		var prod sandboxes.Product
		if err := json.Unmarshal(body, &prod); err != nil {
			return err
		}
		if prod.ID < 3 || prod.Title != "USB-C Hub" || prod.Price != 39.99 {
			return fmt.Errorf("unexpected product data: %+v", prod)
		}
		createdProductID = prod.ID
		_ = createdProductID
		return nil
	})

	// TC03: Reject invalid product (negative price)
	runAssertion("TC03_REJECT_INVALID_PRODUCT", "Reject invalid product (negative price / validation failure)", func() error {
		payload := sandboxes.ProductCreate{Title: "Bad Product", Price: -10.0, Stock: 5}
		resp, _, err := doReq(http.MethodPost, "/api/products", payload, nil)
		if err != nil {
			return err
		}
		if resp.StatusCode != http.StatusUnprocessableEntity && resp.StatusCode != http.StatusBadRequest {
			return fmt.Errorf("expected 422 or 400, got %d", resp.StatusCode)
		}
		return nil
	})

	// TC04: Create valid order with stock decrement
	runAssertion("TC04_CREATE_ORDER_VALID", "Create order with stock calculation & decrement", func() error {
		payload := sandboxes.OrderCreate{
			IdempotencyKey: "order_key_001",
			CustomerID:     "cust_100",
			Items: []sandboxes.OrderItemRequest{
				{ProductID: 1, Quantity: 2}, // 129.99 * 2 = 259.98
				{ProductID: 2, Quantity: 1}, // 79.50 * 1 = 79.50
			},
		}
		resp, body, err := doReq(http.MethodPost, "/api/orders", payload, nil)
		if err != nil {
			return err
		}
		if resp.StatusCode != http.StatusCreated {
			return fmt.Errorf("expected 201, got %d: %s", resp.StatusCode, string(body))
		}
		var order sandboxes.OrderResponse
		if err := json.Unmarshal(body, &order); err != nil {
			return err
		}
		if order.Status != sandboxes.StatusPending {
			return fmt.Errorf("expected status PENDING, got %s", order.Status)
		}
		if order.TotalAmount != 339.48 {
			return fmt.Errorf("expected total 339.48, got %f", order.TotalAmount)
		}
		order1ID = order.ID

		// Verify stock for Product 1 decremented from 10 to 8
		_, pBody, err := doReq(http.MethodGet, "/api/products/1", nil, nil)
		if err != nil {
			return err
		}
		var p1 sandboxes.Product
		_ = json.Unmarshal(pBody, &p1)
		if p1.Stock != 8 {
			return fmt.Errorf("expected stock 8, got %d", p1.Stock)
		}
		return nil
	})

	// TC05: Insufficient stock rejection
	runAssertion("TC05_REJECT_INSUFFICIENT_STOCK", "Insufficient stock rejection", func() error {
		payload := sandboxes.OrderCreate{
			IdempotencyKey: "order_key_fail",
			CustomerID:     "cust_100",
			Items:          []sandboxes.OrderItemRequest{{ProductID: 1, Quantity: 999}},
		}
		resp, body, err := doReq(http.MethodPost, "/api/orders", payload, nil)
		if err != nil {
			return err
		}
		if resp.StatusCode != http.StatusBadRequest {
			return fmt.Errorf("expected 400, got %d: %s", resp.StatusCode, string(body))
		}
		if !strings.Contains(string(body), "Insufficient stock") && !strings.Contains(string(body), "INSUFFICIENT_STOCK") {
			return fmt.Errorf("expected insufficient stock error detail, got: %s", string(body))
		}
		return nil
	})

	// TC06: Idempotent order duplicate rejection
	runAssertion("TC06_REJECT_DUPLICATE_IDEMPOTENCY", "Idempotent duplicate order key rejection", func() error {
		payload := sandboxes.OrderCreate{
			IdempotencyKey: "order_key_001",
			CustomerID:     "cust_100",
			Items:          []sandboxes.OrderItemRequest{{ProductID: 2, Quantity: 1}},
		}
		resp, _, err := doReq(http.MethodPost, "/api/orders", payload, nil)
		if err != nil {
			return err
		}
		if resp.StatusCode != http.StatusConflict {
			return fmt.Errorf("expected 409 Conflict, got %d", resp.StatusCode)
		}
		return nil
	})

	// TC07: Query order by ID
	runAssertion("TC07_QUERY_ORDER_BY_ID", "Query order by ID & verify item structure", func() error {
		resp, body, err := doReq(http.MethodGet, "/api/orders/"+order1ID, nil, nil)
		if err != nil {
			return err
		}
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("expected 200, got %d: %s", resp.StatusCode, string(body))
		}
		var o sandboxes.OrderResponse
		if err := json.Unmarshal(body, &o); err != nil {
			return err
		}
		if o.ID != order1ID || len(o.Items) != 2 {
			return fmt.Errorf("unexpected order response: %+v", o)
		}
		return nil
	})

	// TC08: Pay order (PENDING -> PAID)
	runAssertion("TC08_PAY_ORDER", "Pay order transition (PENDING -> PAID)", func() error {
		resp, body, err := doReq(http.MethodPost, "/api/orders/"+order1ID+"/pay", nil, nil)
		if err != nil {
			return err
		}
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("expected 200, got %d: %s", resp.StatusCode, string(body))
		}
		var o sandboxes.OrderResponse
		_ = json.Unmarshal(body, &o)
		if o.Status != sandboxes.StatusPaid {
			return fmt.Errorf("expected status PAID, got %s", o.Status)
		}
		return nil
	})

	// TC09: Ship order (PAID -> SHIPPED)
	runAssertion("TC09_SHIP_ORDER", "Ship order transition (PAID -> SHIPPED)", func() error {
		resp, body, err := doReq(http.MethodPost, "/api/orders/"+order1ID+"/ship", nil, nil)
		if err != nil {
			return err
		}
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("expected 200, got %d: %s", resp.StatusCode, string(body))
		}
		var o sandboxes.OrderResponse
		_ = json.Unmarshal(body, &o)
		if o.Status != sandboxes.StatusShipped {
			return fmt.Errorf("expected status SHIPPED, got %s", o.Status)
		}
		return nil
	})

	// TC10: Cancel order and verify stock replenishment
	runAssertion("TC10_CANCEL_ORDER_AND_RESTOCK", "Cancel order and verify stock replenishment", func() error {
		// 1. Create a second cancelable order
		createPayload := sandboxes.OrderCreate{
			IdempotencyKey: "order_key_cancelable",
			CustomerID:     "cust_200",
			Items:          []sandboxes.OrderItemRequest{{ProductID: 1, Quantity: 3}},
		}
		resp, body, err := doReq(http.MethodPost, "/api/orders", createPayload, nil)
		if err != nil {
			return err
		}
		if resp.StatusCode != http.StatusCreated {
			return fmt.Errorf("failed to create order for cancel: %s", string(body))
		}
		var ord2 sandboxes.OrderResponse
		_ = json.Unmarshal(body, &ord2)

		// Product 1 stock was 8 - 3 = 5
		_, pBody, _ := doReq(http.MethodGet, "/api/products/1", nil, nil)
		var pBefore sandboxes.Product
		_ = json.Unmarshal(pBody, &pBefore)
		if pBefore.Stock != 5 {
			return fmt.Errorf("expected stock 5 before cancel, got %d", pBefore.Stock)
		}

		// 2. Cancel order
		cancelResp, cBody, err := doReq(http.MethodPost, "/api/orders/"+ord2.ID+"/cancel", nil, nil)
		if err != nil {
			return err
		}
		if cancelResp.StatusCode != http.StatusOK {
			return fmt.Errorf("cancel failed: %s", string(cBody))
		}
		var ordCancelled sandboxes.OrderResponse
		_ = json.Unmarshal(cBody, &ordCancelled)
		if ordCancelled.Status != sandboxes.StatusCancelled {
			return fmt.Errorf("expected status CANCELLED, got %s", ordCancelled.Status)
		}

		// 3. Verify stock replenished: 5 + 3 = 8
		_, pAfterBody, _ := doReq(http.MethodGet, "/api/products/1", nil, nil)
		var pAfter sandboxes.Product
		_ = json.Unmarshal(pAfterBody, &pAfter)
		if pAfter.Stock != 8 {
			return fmt.Errorf("expected stock replenished to 8, got %d", pAfter.Stock)
		}
		return nil
	})

	// TC11: Reject cancellation of already shipped order
	runAssertion("TC11_REJECT_CANCEL_SHIPPED_ORDER", "Reject cancellation of shipped order", func() error {
		resp, body, err := doReq(http.MethodPost, "/api/orders/"+order1ID+"/cancel", nil, nil)
		if err != nil {
			return err
		}
		if resp.StatusCode != http.StatusBadRequest {
			return fmt.Errorf("expected 400 Bad Request, got %d: %s", resp.StatusCode, string(body))
		}
		if !strings.Contains(strings.ToLower(string(body)), "already shipped") {
			return fmt.Errorf("expected 'already shipped' detail, got: %s", string(body))
		}
		return nil
	})

	// TC12: Dry-run simulation rollback & zero DB pollution guarantee
	runAssertion("TC12_DRY_RUN_SANDBOX_ROLLBACK", "Dry-run sandbox verification & rollback safety", func() error {
		if isTokenSafe {
			// In TokenSafe: test dry-run mutation with X-TokenSafe-Dry-Run header
			payload := sandboxes.OrderCreate{
				IdempotencyKey: "dryrun_sim_key",
				CustomerID:     "cust_dryrun",
				Items:          []sandboxes.OrderItemRequest{{ProductID: 1, Quantity: 2}},
			}
			resp, body, err := doReq(http.MethodPost, "/api/orders", payload, map[string]string{
				"X-TokenSafe-Dry-Run": "true",
			})
			if err != nil {
				return err
			}
			if resp.StatusCode != http.StatusCreated {
				return fmt.Errorf("dry-run failed with code %d: %s", resp.StatusCode, string(body))
			}

			// Verify Product 1 stock was NOT decremented by dry run (remains 8)
			_, pBody, _ := doReq(http.MethodGet, "/api/products/1", nil, nil)
			var p1 sandboxes.Product
			_ = json.Unmarshal(pBody, &p1)
			if p1.Stock != 8 {
				return fmt.Errorf("dry-run polluted state! Stock became %d", p1.Stock)
			}
		} else {
			// In Vanilla: verify that without dry-run, a direct test run consumes stock and mutates DB
			payload := sandboxes.OrderCreate{
				IdempotencyKey: "vanilla_test_key",
				CustomerID:     "cust_vanilla",
				Items:          []sandboxes.OrderItemRequest{{ProductID: 1, Quantity: 1}},
			}
			resp, _, err := doReq(http.MethodPost, "/api/orders", payload, nil)
			if err != nil {
				return err
			}
			if resp.StatusCode != http.StatusCreated {
				return fmt.Errorf("vanilla mutation failed")
			}
		}
		return nil
	})

	durationMs := float64(time.Since(startAll).Microseconds()) / 1000.0
	passedCount := 0
	for _, a := range assertions {
		if a.Passed {
			passedCount++
		}
	}

	return &benchmark.OracleEvaluationReport{
		PassedCount: passedCount,
		TotalCount:  len(assertions),
		Assertions:  assertions,
		AllPassed:   passedCount == len(assertions),
		DurationMs:  durationMs,
	}
}
