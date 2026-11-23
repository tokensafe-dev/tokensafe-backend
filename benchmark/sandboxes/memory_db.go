package sandboxes

import (
	"fmt"
	"math"
	"math/rand"
	"sync"
	"time"
)

// MemoryDB provides thread-safe state storage with transactional dry-run rollback support.
type MemoryDB struct {
	mu           sync.RWMutex
	products     map[int]*Product
	orders       map[string]*OrderResponse
	orderKeyIdx  map[string]string // idempotency_key -> order_id
	nextProdID   int
}

// NewMemoryDB creates a pre-seeded database instance.
func NewMemoryDB() *MemoryDB {
	db := &MemoryDB{
		products:    make(map[int]*Product),
		orders:      make(map[string]*OrderResponse),
		orderKeyIdx: make(map[string]string),
		nextProdID:  3,
	}

	// Pre-seed catalog
	db.products[1] = &Product{ID: 1, Title: "Mechanical Keyboard", Price: 129.99, Stock: 10, Category: "electronics"}
	db.products[2] = &Product{ID: 2, Title: "Ergonomic Mouse", Price: 79.50, Stock: 20, Category: "electronics"}

	return db
}

// Clone creates a deep copy of current state for transactional rollback.
func (db *MemoryDB) Clone() *MemoryDB {
	db.mu.RLock()
	defer db.mu.RUnlock()

	clone := &MemoryDB{
		products:    make(map[int]*Product, len(db.products)),
		orders:      make(map[string]*OrderResponse, len(db.orders)),
		orderKeyIdx: make(map[string]string, len(db.orderKeyIdx)),
		nextProdID:  db.nextProdID,
	}

	for k, v := range db.products {
		clone.products[k] = &Product{
			ID:       v.ID,
			Title:    v.Title,
			Price:    v.Price,
			Stock:    v.Stock,
			Category: v.Category,
		}
	}

	for k, v := range db.orders {
		items := make([]OrderItemResponse, len(v.Items))
		copy(items, v.Items)
		clone.orders[k] = &OrderResponse{
			ID:             v.ID,
			IdempotencyKey: v.IdempotencyKey,
			CustomerID:     v.CustomerID,
			TotalAmount:    v.TotalAmount,
			Status:         v.Status,
			Items:          items,
			CreatedAt:      v.CreatedAt,
		}
	}

	for k, v := range db.orderKeyIdx {
		clone.orderKeyIdx[k] = v
	}

	return clone
}

// Restore restores state from a clone.
func (db *MemoryDB) Restore(snap *MemoryDB) {
	db.mu.Lock()
	defer db.mu.Unlock()

	db.products = snap.products
	db.orders = snap.orders
	db.orderKeyIdx = snap.orderKeyIdx
	db.nextProdID = snap.nextProdID
}

// CountDirtyRecords returns the number of non-seed records created in database.
func (db *MemoryDB) CountDirtyRecords() int {
	db.mu.RLock()
	defer db.mu.RUnlock()

	extraProducts := 0
	for id := range db.products {
		if id > 2 {
			extraProducts++
		}
	}

	totalItems := 0
	for _, o := range db.orders {
		totalItems += len(o.Items)
	}

	return len(db.orders) + totalItems + extraProducts
}

func (db *MemoryDB) ListProducts(category string) []*Product {
	db.mu.RLock()
	defer db.mu.RUnlock()

	res := make([]*Product, 0, len(db.products))
	for _, p := range db.products {
		if category == "" || p.Category == category {
			res = append(res, p)
		}
	}
	return res
}

func (db *MemoryDB) GetProduct(id int) (*Product, bool) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	p, ok := db.products[id]
	return p, ok
}

func (db *MemoryDB) CreateProduct(p ProductCreate) (*Product, error) {
	if p.Title == "" {
		return nil, fmt.Errorf("title cannot be empty")
	}
	if p.Price <= 0 {
		return nil, fmt.Errorf("price must be greater than 0")
	}

	db.mu.Lock()
	defer db.mu.Unlock()

	newID := db.nextProdID
	db.nextProdID++

	stock := p.Stock
	if stock < 0 {
		stock = 0
	}
	cat := p.Category
	if cat == "" {
		cat = "electronics"
	}

	prod := &Product{
		ID:       newID,
		Title:    p.Title,
		Price:    p.Price,
		Stock:    stock,
		Category: cat,
	}
	db.products[newID] = prod
	return prod, nil
}

func (db *MemoryDB) ListOrders() []*OrderResponse {
	db.mu.RLock()
	defer db.mu.RUnlock()

	res := make([]*OrderResponse, 0, len(db.orders))
	for _, o := range db.orders {
		res = append(res, o)
	}
	return res
}

func (db *MemoryDB) GetOrder(id string) (*OrderResponse, bool) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	o, ok := db.orders[id]
	return o, ok
}

func (db *MemoryDB) CreateOrder(req OrderCreate) (*OrderResponse, error) {
	if req.IdempotencyKey == "" {
		return nil, fmt.Errorf("idempotency_key is required")
	}
	if len(req.Items) == 0 {
		return nil, fmt.Errorf("order must contain at least one item")
	}

	db.mu.Lock()
	defer db.mu.Unlock()

	// 1. Idempotency Check
	if _, exists := db.orderKeyIdx[req.IdempotencyKey]; exists {
		return nil, fmt.Errorf("CONFLICT: order with key '%s' already exists", req.IdempotencyKey)
	}

	// 2. Validate Items & Stock
	total := 0.0
	resItems := make([]OrderItemResponse, 0, len(req.Items))

	for _, item := range req.Items {
		if item.Quantity <= 0 {
			return nil, fmt.Errorf("item quantity must be > 0")
		}
		prod, ok := db.products[item.ProductID]
		if !ok {
			return nil, fmt.Errorf("product %d not found", item.ProductID)
		}
		if prod.Stock < item.Quantity {
			return nil, fmt.Errorf("INSUFFICIENT_STOCK: Insufficient stock for '%s' (requested: %d, available: %d)",
				prod.Title, item.Quantity, prod.Stock)
		}

		// Decrement stock
		prod.Stock -= item.Quantity
		subtotal := float64(item.Quantity) * prod.Price
		total += subtotal

		resItems = append(resItems, OrderItemResponse{
			ProductID:    prod.ID,
			ProductTitle: prod.Title,
			Quantity:     item.Quantity,
			UnitPrice:    prod.Price,
			Subtotal:     math.Round(subtotal*100) / 100,
		})
	}

	orderID := fmt.Sprintf("ord_%08x", rand.Int31())
	order := &OrderResponse{
		ID:             orderID,
		IdempotencyKey: req.IdempotencyKey,
		CustomerID:     req.CustomerID,
		TotalAmount:    math.Round(total*100) / 100,
		Status:         StatusPending,
		Items:          resItems,
		CreatedAt:      time.Now().Unix(),
	}

	db.orders[orderID] = order
	db.orderKeyIdx[req.IdempotencyKey] = orderID

	return order, nil
}

func (db *MemoryDB) PayOrder(id string) (*OrderResponse, error) {
	db.mu.Lock()
	defer db.mu.Unlock()

	order, ok := db.orders[id]
	if !ok {
		return nil, fmt.Errorf("order %s not found", id)
	}
	if order.Status != StatusPending {
		return nil, fmt.Errorf("order cannot be paid from status %s", order.Status)
	}

	order.Status = StatusPaid
	return order, nil
}

func (db *MemoryDB) ShipOrder(id string) (*OrderResponse, error) {
	db.mu.Lock()
	defer db.mu.Unlock()

	order, ok := db.orders[id]
	if !ok {
		return nil, fmt.Errorf("order %s not found", id)
	}
	if order.Status != StatusPaid {
		return nil, fmt.Errorf("order cannot be shipped from status %s", order.Status)
	}

	order.Status = StatusShipped
	return order, nil
}

func (db *MemoryDB) CancelOrder(id string) (*OrderResponse, error) {
	db.mu.Lock()
	defer db.mu.Unlock()

	order, ok := db.orders[id]
	if !ok {
		return nil, fmt.Errorf("order %s not found", id)
	}
	if order.Status == StatusShipped {
		return nil, fmt.Errorf("cannot cancel an order that has already shipped")
	}
	if order.Status == StatusCancelled {
		return nil, fmt.Errorf("order is already cancelled")
	}

	// Replenish stock
	for _, item := range order.Items {
		if prod, ok := db.products[item.ProductID]; ok {
			prod.Stock += item.Quantity
		}
	}

	order.Status = StatusCancelled
	return order, nil
}
