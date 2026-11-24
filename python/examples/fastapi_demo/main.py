"""
Example FastAPI application instrumented with TokenSafe UARP (Universal Agent-Runtime Protocol).
"""

from typing import List, Optional
from fastapi import FastAPI, HTTPException
from pydantic import BaseModel, Field

from tokensafe_python.adapters.fastapi import mount_tokensafe

app = FastAPI(
    title="Product Catalog API",
    description="Demo API demonstrating TokenSafe UARP runtime introspection, simulation and verification.",
    version="1.0.0",
)


class ProductCreate(BaseModel):
    title: str = Field(..., example="Wireless Noise-Cancelling Headphones")
    price: float = Field(..., gt=0, example=249.99)
    category: str = Field(default="electronics", example="electronics")
    in_stock: bool = True


class ProductResponse(BaseModel):
    id: int
    title: str
    price: float
    category: str
    in_stock: bool


# Mock Database
products_db: List[ProductResponse] = [
    ProductResponse(id=1, title="Mechanical Keyboard", price=129.99, category="electronics", in_stock=True),
    ProductResponse(id=2, title="Ergonomic Mouse", price=79.50, category="electronics", in_stock=True),
]


@app.get("/api/products", response_model=List[ProductResponse], tags=["Products"])
def get_products(category: Optional[str] = None):
    """Retrieve all products, optionally filtered by category."""
    if category:
        return [p for p in products_db if p.category == category]
    return products_db


@app.get("/api/products/{product_id}", response_model=ProductResponse, tags=["Products"])
def get_product(product_id: int):
    """Retrieve a single product by ID."""
    for p in products_db:
        if p.id == product_id:
            return p
    raise HTTPException(status_code=404, detail="Product not found")


@app.post("/api/products", response_model=ProductResponse, status_code=201, tags=["Products"])
def create_product(product: ProductCreate):
    """Create a new product."""
    new_id = len(products_db) + 1
    new_product = ProductResponse(
        id=new_id,
        title=product.title,
        price=product.price,
        category=product.category,
        in_stock=product.in_stock,
    )
    products_db.append(new_product)
    return new_product


# Mount TokenSafe UARP Runtime
# Automatically mounts /__oumv/rpc, /__oumv/health, and writes .tokensafe/runtime.json for auto-discovery
ts = mount_tokensafe(app, port=8000)

if __name__ == "__main__":
    import uvicorn
    uvicorn.run(app, host="127.0.0.1", port=8000)
