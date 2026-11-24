package vanilla_gin

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tokensafe-dev/tokensafe-backend/go/benchmark/sandboxes"
)

func init() {
	gin.SetMode(gin.ReleaseMode)
}

// VanillaApp encapsulates a standard Gin backend without OUMV capabilities.
type VanillaApp struct {
	Engine *gin.Engine
	DB     *sandboxes.MemoryDB
}

// NewVanillaApp creates a new standard Gin app with pre-seeded database.
func NewVanillaApp(db *sandboxes.MemoryDB) *VanillaApp {
	if db == nil {
		db = sandboxes.NewMemoryDB()
	}

	r := gin.New()
	r.Use(gin.Recovery())

	app := &VanillaApp{
		Engine: r,
		DB:     db,
	}

	// 1. Products
	r.GET("/api/products", func(c *gin.Context) {
		cat := c.Query("category")
		c.JSON(http.StatusOK, db.ListProducts(cat))
	})

	r.GET("/api/products/:id", func(c *gin.Context) {
		id, err := strconv.Atoi(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid product id"})
			return
		}
		p, ok := db.GetProduct(id)
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "product not found"})
			return
		}
		c.JSON(http.StatusOK, p)
	})

	r.POST("/api/products", func(c *gin.Context) {
		var req sandboxes.ProductCreate
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": err.Error()})
			return
		}
		p, err := db.CreateProduct(req)
		if err != nil {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusCreated, p)
	})

	// 2. Orders
	r.GET("/api/orders", func(c *gin.Context) {
		c.JSON(http.StatusOK, db.ListOrders())
	})

	r.GET("/api/orders/:id", func(c *gin.Context) {
		id := c.Param("id")
		o, ok := db.GetOrder(id)
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "order not found"})
			return
		}
		c.JSON(http.StatusOK, o)
	})

	r.POST("/api/orders", func(c *gin.Context) {
		var req sandboxes.OrderCreate
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": err.Error()})
			return
		}
		order, err := db.CreateOrder(req)
		if err != nil {
			errMsg := err.Error()
			if strings.HasPrefix(errMsg, "CONFLICT:") {
				c.JSON(http.StatusConflict, gin.H{"error": errMsg})
				return
			}
			if strings.HasPrefix(errMsg, "INSUFFICIENT_STOCK:") {
				c.JSON(http.StatusBadRequest, gin.H{"error": errMsg})
				return
			}
			c.JSON(http.StatusBadRequest, gin.H{"error": errMsg})
			return
		}
		c.JSON(http.StatusCreated, order)
	})

	r.POST("/api/orders/:id/pay", func(c *gin.Context) {
		id := c.Param("id")
		order, err := db.PayOrder(id)
		if err != nil {
			if strings.Contains(err.Error(), "not found") {
				c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, order)
	})

	r.POST("/api/orders/:id/ship", func(c *gin.Context) {
		id := c.Param("id")
		order, err := db.ShipOrder(id)
		if err != nil {
			if strings.Contains(err.Error(), "not found") {
				c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, order)
	})

	r.POST("/api/orders/:id/cancel", func(c *gin.Context) {
		id := c.Param("id")
		order, err := db.CancelOrder(id)
		if err != nil {
			if strings.Contains(err.Error(), "not found") {
				c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, order)
	})

	return app
}

func (a *VanillaApp) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.Engine.ServeHTTP(w, r)
}
