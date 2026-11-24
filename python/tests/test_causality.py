"""
Tests for CausalityGraph engine.
"""

from tokensafe_python.core.causality import CausalityGraph


def test_causality_graph_nodes_and_edges():
    graph = CausalityGraph()

    graph.add_node("route:POST:/users", "route", "POST /users")
    graph.add_node("handler:create_user", "handler", "create_user")
    graph.add_node("model:UserCreate", "model", "UserCreate")
    graph.add_node("table:users", "db_table", "users")

    graph.add_edge("route:POST:/users", "handler:create_user", "handles")
    graph.add_edge("handler:create_user", "model:UserCreate", "validates_with")
    graph.add_edge("handler:create_user", "table:users", "mutates")

    # Full query
    res_all = graph.query()
    assert res_all.total_nodes == 4
    assert res_all.total_edges == 3

    # Filtered query
    res_filtered = graph.query(endpoint="/users", depth=1)
    assert res_filtered.total_nodes >= 2
    assert any(n.id == "route:POST:/users" for n in res_filtered.nodes)
    assert any(n.id == "handler:create_user" for n in res_filtered.nodes)


def test_causality_graph_trace():
    graph = CausalityGraph()

    graph.add_node("route:GET:/orders", "route", "GET /orders")
    graph.add_node("handler:list_orders", "handler", "list_orders")
    graph.add_node("table:orders", "db_table", "orders")

    graph.add_edge("route:GET:/orders", "handler:list_orders", "handles")
    graph.add_edge("handler:list_orders", "table:orders", "queries")

    trace = graph.trace("http", "route:GET:/orders")
    assert len(trace) == 3
    assert trace[0]["node_id"] == "route:GET:/orders"
    assert trace[1]["node_id"] == "handler:list_orders"
    assert trace[2]["node_id"] == "table:orders"
