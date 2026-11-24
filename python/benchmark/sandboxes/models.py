"""
Shared schemas and models for Order Processing API benchmark.
"""

from enum import Enum
from typing import List, Optional
from pydantic import BaseModel, Field


class OrderStatus(str, Enum):
    PENDING = "PENDING"
    PAID = "PAID"
    SHIPPED = "SHIPPED"
    CANCELLED = "CANCELLED"


class ProductCreate(BaseModel):
    title: str = Field(..., min_length=1, json_schema_extra={"example": "Mechanical Keyboard"})
    price: float = Field(..., gt=0, json_schema_extra={"example": 129.99})
    stock: int = Field(default=10, ge=0, json_schema_extra={"example": 10})
    category: str = Field(default="electronics", json_schema_extra={"example": "electronics"})


class ProductResponse(BaseModel):
    id: int
    title: str
    price: float
    stock: int
    category: str


class OrderItemRequest(BaseModel):
    product_id: int = Field(..., gt=0)
    quantity: int = Field(..., gt=0)


class OrderItemResponse(BaseModel):
    product_id: int
    product_title: str
    quantity: int
    unit_price: float
    subtotal: float


class OrderCreate(BaseModel):
    idempotency_key: str = Field(..., min_length=4, json_schema_extra={"example": "key_abc123"})
    customer_id: str = Field(..., min_length=2, json_schema_extra={"example": "cust_99"})
    items: List[OrderItemRequest] = Field(..., min_length=1)


class OrderResponse(BaseModel):
    id: str
    idempotency_key: str
    customer_id: str
    total_amount: float
    status: OrderStatus
    items: List[OrderItemResponse]
    created_at: float
