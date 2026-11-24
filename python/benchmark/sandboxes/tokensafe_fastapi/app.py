"""
TokenSafe UARP Instrumented FastAPI Application implementing Order Processing API.
Features automatic dry-run rollback and UARP JSON-RPC endpoints.
"""

import time
import uuid
from typing import List, Optional
from fastapi import FastAPI, HTTPException, Depends, Request
from sqlalchemy.orm import Session, sessionmaker

from tokensafe_python.adapters.fastapi import mount_tokensafe
from benchmark.sandboxes.models import (
    ProductCreate,
    ProductResponse,
    OrderCreate,
    OrderResponse,
    OrderItemResponse,
    OrderStatus,
)
from benchmark.sandboxes.tokensafe_fastapi.database import (
    ProductDB,
    OrderDB,
    OrderItemDB,
    create_sqlite_engine,
    seed_database,
)


class DryRunSessionProxy:
    """Proxies SQLAlchemy Session during dry-run simulation to flush instead of commit, and rollback on exit."""

    def __init__(self, session: Session):
        self._session = session
        self.rolled_back = False

    def commit(self):
        # Flush pending queries to execute constraints and get IDs without committing to disk
        self._session.flush()

    def rollback(self):
        self.rolled_back = True
        self._session.rollback()

    def __getattr__(self, name):
        return getattr(self._session, name)


def create_tokensafe_app(engine=None) -> FastAPI:
    if engine is None:
        engine = create_sqlite_engine()

    session_factory = sessionmaker(autocommit=False, autoflush=False, bind=engine)
    seed_database(session_factory)

    app = FastAPI(
        title="Order Processing API (TokenSafe UARP)",
        version="1.0.0",
        description="FastAPI app instrumented with TokenSafe UARP runtime introspection & transactional sandbox.",
    )
    app.state.session_factory = session_factory
    app.state.engine = engine

    def get_db(request: Request):
        raw_db = session_factory()
        is_dry_run = request.headers.get("X-TokenSafe-Dry-Run") == "true"

        if is_dry_run:
            db = DryRunSessionProxy(raw_db)
        else:
            db = raw_db

        try:
            yield db
            if is_dry_run:
                raw_db.rollback()
        except Exception:
            raw_db.rollback()
            raise
        finally:
            raw_db.close()

    @app.get("/api/products", response_model=List[ProductResponse], tags=["Products"])
    def list_products(category: Optional[str] = None, db: Session = Depends(get_db)):
        query = db.query(ProductDB)
        if category:
            query = query.filter(ProductDB.category == category)
        products = query.all()
        return [
            ProductResponse(id=p.id, title=p.title, price=p.price, stock=p.stock, category=p.category)
            for p in products
        ]

    @app.get("/api/products/{product_id}", response_model=ProductResponse, tags=["Products"])
    def get_product(product_id: int, db: Session = Depends(get_db)):
        p = db.query(ProductDB).filter(ProductDB.id == product_id).first()
        if not p:
            raise HTTPException(status_code=404, detail=f"Product {product_id} not found")
        return ProductResponse(id=p.id, title=p.title, price=p.price, stock=p.stock, category=p.category)

    @app.post("/api/products", response_model=ProductResponse, status_code=201, tags=["Products"])
    def create_product(product: ProductCreate, db: Session = Depends(get_db)):
        new_prod = ProductDB(
            title=product.title,
            price=product.price,
            stock=product.stock,
            category=product.category,
        )
        db.add(new_prod)
        db.commit()
        db.refresh(new_prod)
        return ProductResponse(
            id=new_prod.id,
            title=new_prod.title,
            price=new_prod.price,
            stock=new_prod.stock,
            category=new_prod.category,
        )

    def _to_order_response(order: OrderDB) -> OrderResponse:
        items_resp = [
            OrderItemResponse(
                product_id=it.product_id,
                product_title=it.product.title if it.product else f"Product {it.product_id}",
                quantity=it.quantity,
                unit_price=it.unit_price,
                subtotal=round(it.quantity * it.unit_price, 2),
            )
            for it in order.items
        ]
        return OrderResponse(
            id=order.id,
            idempotency_key=order.idempotency_key,
            customer_id=order.customer_id,
            total_amount=round(order.total_amount, 2),
            status=OrderStatus(order.status),
            items=items_resp,
            created_at=order.created_at,
        )

    @app.get("/api/orders", response_model=List[OrderResponse], tags=["Orders"])
    def list_orders(db: Session = Depends(get_db)):
        orders = db.query(OrderDB).all()
        return [_to_order_response(o) for o in orders]

    @app.get("/api/orders/{order_id}", response_model=OrderResponse, tags=["Orders"])
    def get_order(order_id: str, db: Session = Depends(get_db)):
        order = db.query(OrderDB).filter(OrderDB.id == order_id).first()
        if not order:
            raise HTTPException(status_code=404, detail=f"Order {order_id} not found")
        return _to_order_response(order)

    @app.post("/api/orders", response_model=OrderResponse, status_code=201, tags=["Orders"])
    def create_order(order_req: OrderCreate, db: Session = Depends(get_db)):
        # 1. Idempotency check
        existing = db.query(OrderDB).filter(OrderDB.idempotency_key == order_req.idempotency_key).first()
        if existing:
            raise HTTPException(
                status_code=409,
                detail=f"Order with idempotency key '{order_req.idempotency_key}' already exists",
            )

        # 2. Validate products & stock
        total_amount = 0.0
        db_items_to_create = []
        for req_item in order_req.items:
            prod = db.query(ProductDB).filter(ProductDB.id == req_item.product_id).first()
            if not prod:
                raise HTTPException(status_code=404, detail=f"Product {req_item.product_id} not found")

            if prod.stock < req_item.quantity:
                raise HTTPException(
                    status_code=400,
                    detail=f"Insufficient stock for '{prod.title}' (requested: {req_item.quantity}, available: {prod.stock})",
                )

            # Decrement stock
            prod.stock -= req_item.quantity
            item_total = req_item.quantity * prod.price
            total_amount += item_total

            db_items_to_create.append((req_item.product_id, req_item.quantity, prod.price))

        # 3. Create Order
        new_order_id = f"ord_{uuid.uuid4().hex[:8]}"
        order_db = OrderDB(
            id=new_order_id,
            idempotency_key=order_req.idempotency_key,
            customer_id=order_req.customer_id,
            total_amount=round(total_amount, 2),
            status=OrderStatus.PENDING.value,
            created_at=time.time(),
        )
        db.add(order_db)
        db.flush()

        for pid, qty, price in db_items_to_create:
            order_item = OrderItemDB(
                order_id=new_order_id,
                product_id=pid,
                quantity=qty,
                unit_price=price,
            )
            db.add(order_item)

        db.commit()
        db.refresh(order_db)
        return _to_order_response(order_db)

    @app.post("/api/orders/{order_id}/pay", response_model=OrderResponse, tags=["Orders"])
    def pay_order(order_id: str, db: Session = Depends(get_db)):
        order = db.query(OrderDB).filter(OrderDB.id == order_id).first()
        if not order:
            raise HTTPException(status_code=404, detail=f"Order {order_id} not found")
        if order.status != OrderStatus.PENDING.value:
            raise HTTPException(status_code=400, detail=f"Order cannot be paid from status {order.status}")

        order.status = OrderStatus.PAID.value
        db.commit()
        db.refresh(order)
        return _to_order_response(order)

    @app.post("/api/orders/{order_id}/ship", response_model=OrderResponse, tags=["Orders"])
    def ship_order(order_id: str, db: Session = Depends(get_db)):
        order = db.query(OrderDB).filter(OrderDB.id == order_id).first()
        if not order:
            raise HTTPException(status_code=404, detail=f"Order {order_id} not found")
        if order.status != OrderStatus.PAID.value:
            raise HTTPException(status_code=400, detail=f"Order cannot be shipped from status {order.status}")

        order.status = OrderStatus.SHIPPED.value
        db.commit()
        db.refresh(order)
        return _to_order_response(order)

    @app.post("/api/orders/{order_id}/cancel", response_model=OrderResponse, tags=["Orders"])
    def cancel_order(order_id: str, db: Session = Depends(get_db)):
        order = db.query(OrderDB).filter(OrderDB.id == order_id).first()
        if not order:
            raise HTTPException(status_code=404, detail=f"Order {order_id} not found")
        if order.status == OrderStatus.SHIPPED.value:
            raise HTTPException(status_code=400, detail="Cannot cancel an order that has already shipped")
        if order.status == OrderStatus.CANCELLED.value:
            raise HTTPException(status_code=400, detail="Order is already cancelled")

        # Replenish stock
        for item in order.items:
            prod = db.query(ProductDB).filter(ProductDB.id == item.product_id).first()
            if prod:
                prod.stock += item.quantity

        order.status = OrderStatus.CANCELLED.value
        db.commit()
        db.refresh(order)
        return _to_order_response(order)

    # Mount TokenSafe UARP Runtime
    mount_tokensafe(app, db_engine=engine, auto_discover=False)

    return app
