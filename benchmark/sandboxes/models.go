package sandboxes

// OrderStatus defines the lifecycle status of an order.
type OrderStatus string

const (
	StatusPending   OrderStatus = "PENDING"
	StatusPaid      OrderStatus = "PAID"
	StatusShipped   OrderStatus = "SHIPPED"
	StatusCancelled OrderStatus = "CANCELLED"
)

// Product represents an item in the store catalog.
type Product struct {
	ID       int     `json:"id"`
	Title    string  `json:"title"`
	Price    float64 `json:"price"`
	Stock    int     `json:"stock"`
	Category string  `json:"category"`
}

// ProductCreate represents the request payload to create a new product.
type ProductCreate struct {
	Title    string  `json:"title"`
	Price    float64 `json:"price"`
	Stock    int     `json:"stock"`
	Category string  `json:"category"`
}

// OrderItemRequest represents an item entry when placing an order.
type OrderItemRequest struct {
	ProductID int `json:"product_id"`
	Quantity  int `json:"quantity"`
}

// OrderItemResponse represents a serialized item within an order response.
type OrderItemResponse struct {
	ProductID    int     `json:"product_id"`
	ProductTitle string  `json:"product_title"`
	Quantity     int     `json:"quantity"`
	UnitPrice    float64 `json:"unit_price"`
	Subtotal     float64 `json:"subtotal"`
}

// OrderCreate represents the request payload to place an order.
type OrderCreate struct {
	IdempotencyKey string             `json:"idempotency_key"`
	CustomerID     string             `json:"customer_id"`
	Items          []OrderItemRequest `json:"items"`
}

// OrderResponse represents a serialized order.
type OrderResponse struct {
	ID             string              `json:"id"`
	IdempotencyKey string              `json:"idempotency_key"`
	CustomerID     string              `json:"customer_id"`
	TotalAmount    float64             `json:"total_amount"`
	Status         OrderStatus         `json:"status"`
	Items          []OrderItemResponse `json:"items"`
	CreatedAt      int64               `json:"created_at"`
}
