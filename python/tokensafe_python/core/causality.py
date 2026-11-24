"""
Causality graph engine for runtime route, model, and database dependency mapping.
"""

from typing import Dict, List, Optional, Set
from tokensafe_python.protocol.types import (
    CausalityNode,
    CausalityEdge,
    CausalityQueryResult,
)


class CausalityGraph:
    """Maintains an in-memory graph of runtime endpoints, models, handlers, and tables."""

    def __init__(self):
        self._nodes: Dict[str, CausalityNode] = {}
        self._edges: List[CausalityEdge] = []

    def add_node(self, node_id: str, node_type: str, label: str, metadata: Optional[dict] = None):
        """Adds or updates a node in the graph."""
        self._nodes[node_id] = CausalityNode(
            id=node_id,
            type=node_type,
            label=label,
            metadata=metadata or {},
        )

    def add_edge(self, source_id: str, target_id: str, relation: str, metadata: Optional[dict] = None):
        """Adds a directional edge between two nodes."""
        edge = CausalityEdge(
            source=source_id,
            target=target_id,
            relation=relation,
            metadata=metadata or {},
        )
        # Avoid duplicate edges
        for existing in self._edges:
            if (
                existing.source == source_id
                and existing.target == target_id
                and existing.relation == relation
            ):
                return
        self._edges.append(edge)

    def query(
        self,
        endpoint: Optional[str] = None,
        model: Optional[str] = None,
        node_type: Optional[str] = None,
        depth: int = 2,
    ) -> CausalityQueryResult:
        """Queries the causality graph filtered by entrypoint, model, or type."""
        if not endpoint and not model and not node_type:
            # Return full graph
            nodes = list(self._nodes.values())
            return CausalityQueryResult(
                nodes=nodes,
                edges=self._edges,
                total_nodes=len(nodes),
                total_edges=len(self._edges),
            )

        # Breadth-first search from matching starter nodes
        starting_ids: Set[str] = set()
        for nid, node in self._nodes.items():
            if endpoint and (endpoint in node.label or endpoint in nid):
                starting_ids.add(nid)
            elif model and (model in node.label or model in nid):
                starting_ids.add(nid)
            elif node_type and node.type == node_type:
                starting_ids.add(nid)

        visited_nodes: Set[str] = set(starting_ids)
        collected_edges: List[CausalityEdge] = []
        current_layer = set(starting_ids)

        for _ in range(depth):
            next_layer = set()
            for edge in self._edges:
                if edge.source in current_layer and edge.target not in visited_nodes:
                    visited_nodes.add(edge.target)
                    next_layer.add(edge.target)
                    collected_edges.append(edge)
                elif edge.target in current_layer and edge.source not in visited_nodes:
                    visited_nodes.add(edge.source)
                    next_layer.add(edge.source)
                    collected_edges.append(edge)
                elif edge.source in current_layer and edge.target in visited_nodes:
                    if edge not in collected_edges:
                        collected_edges.append(edge)
            current_layer = next_layer
            if not current_layer:
                break

        res_nodes = [self._nodes[nid] for nid in visited_nodes if nid in self._nodes]
        return CausalityQueryResult(
            nodes=res_nodes,
            edges=collected_edges,
            total_nodes=len(res_nodes),
            total_edges=len(collected_edges),
        )

    def trace(self, trigger_type: str, identifier: str) -> List[Dict[str, str]]:
        """Generates a sequential causal chain starting from a trigger."""
        chain = []
        start_node = self._nodes.get(identifier)
        if not start_node:
            for nid, node in self._nodes.items():
                if identifier in node.label or identifier in nid:
                    start_node = node
                    break

        if not start_node:
            return chain

        chain.append({"step": "1", "node_id": start_node.id, "type": start_node.type, "label": start_node.label})
        current_id = start_node.id
        step = 2

        for edge in self._edges:
            if edge.source == current_id:
                target = self._nodes.get(edge.target)
                if target:
                    chain.append({
                        "step": str(step),
                        "node_id": target.id,
                        "type": target.type,
                        "label": target.label,
                        "relation": edge.relation,
                    })
                    step += 1
                    current_id = target.id

        return chain
