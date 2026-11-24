"""
SQLAlchemy SQLite database setup and entities for Vanilla FastAPI sandbox.
"""

import time
from sqlalchemy import Column, Float, ForeignKey, Integer, String, create_engine
from sqlalchemy.orm import declarative_base, relationship, sessionmaker
from sqlalchemy.pool import StaticPool

Base = declarative_base()


class ProductDB(Base):
    __tablename__ = "products"

    id = Column(Integer, primary_key=True, autoincrement=True)
    title = Column(String(128), nullable=False)
    price = Column(Float, nullable=False)
    stock = Column(Integer, nullable=False, default=10)
    category = Column(String(64), nullable=False, default="electronics")


class OrderDB(Base):
    __tablename__ = "orders"

    id = Column(String(64), primary_key=True)
    idempotency_key = Column(String(64), unique=True, nullable=False, index=True)
    customer_id = Column(String(64), nullable=False)
    total_amount = Column(Float, nullable=False)
    status = Column(String(32), nullable=False, default="PENDING")
    created_at = Column(Float, nullable=False, default=time.time)

    items = relationship("OrderItemDB", back_populates="order", cascade="all, delete-orphan")


class OrderItemDB(Base):
    __tablename__ = "order_items"

    id = Column(Integer, primary_key=True, autoincrement=True)
    order_id = Column(String(64), ForeignKey("orders.id"), nullable=False)
    product_id = Column(Integer, ForeignKey("products.id"), nullable=False)
    quantity = Column(Integer, nullable=False)
    unit_price = Column(Float, nullable=False)

    order = relationship("OrderDB", back_populates="items")
    product = relationship("ProductDB")


def create_sqlite_engine(db_url: str = "sqlite:///:memory:"):
    engine = create_engine(
        db_url,
        connect_args={"check_same_thread": False},
        poolclass=StaticPool,
    )
    Base.metadata.create_all(bind=engine)
    return engine


def seed_database(session_factory):
    with session_factory() as session:
        # Check if already seeded
        if session.query(ProductDB).count() == 0:
            p1 = ProductDB(id=1, title="Mechanical Keyboard", price=129.99, stock=10, category="electronics")
            p2 = ProductDB(id=2, title="Ergonomic Mouse", price=79.50, stock=20, category="electronics")
            session.add_all([p1, p2])
            session.commit()


def count_dirty_records(session_factory) -> int:
    """Returns number of non-seed records created in database (orders + items + extra products)."""
    with session_factory() as session:
        orders_count = session.query(OrderDB).count()
        items_count = session.query(OrderItemDB).count()
        extra_products = session.query(ProductDB).filter(ProductDB.id > 2).count()
        return orders_count + items_count + extra_products
