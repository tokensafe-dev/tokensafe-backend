"""
UARP (Universal Agent-Runtime Protocol) protocol types and contracts.
"""

from typing import Any, Dict, List, Optional, Union
from pydantic import BaseModel, Field


class JSONRPCRequest(BaseModel):
    jsonrpc: str = "2.0"
    id: Union[str, int]
    method: str
    params: Optional[Dict[str, Any]] = None


class JSONRPCError(BaseModel):
    code: int
    message: str
    data: Optional[Any] = None


class JSONRPCResponse(BaseModel):
    jsonrpc: str = "2.0"
    id: Optional[Union[str, int]] = None
    result: Optional[Any] = None
    error: Optional[JSONRPCError] = None


class CausalityNode(BaseModel):
    id: str
    type: str  # "route", "handler", "model", "db_table", "event"
    label: str
    metadata: Dict[str, Any] = Field(default_factory=dict)


class CausalityEdge(BaseModel):
    source: str
    target: str
    relation: str  # "handles", "validates_with", "queries", "mutates", "emits"
    metadata: Dict[str, Any] = Field(default_factory=dict)


class CausalityQueryResult(BaseModel):
    nodes: List[CausalityNode] = Field(default_factory=list)
    edges: List[CausalityEdge] = Field(default_factory=list)
    total_nodes: int = 0
    total_edges: int = 0


class StateReadResult(BaseModel):
    runtime_type: str = "backend/python"
    framework: str = "fastapi"
    schema_models: Dict[str, Any] = Field(default_factory=dict)
    routes: List[Dict[str, Any]] = Field(default_factory=list)
    tracked_state: Dict[str, Any] = Field(default_factory=dict)
    timestamp: str = ""


class StateDiffResult(BaseModel):
    added: Dict[str, Any] = Field(default_factory=dict)
    modified: Dict[str, Any] = Field(default_factory=dict)
    removed: Dict[str, Any] = Field(default_factory=dict)
    has_changes: bool = False


class MutationAction(BaseModel):
    type: str = "http_request"  # "http_request", "state_patch", "db_query"
    path: Optional[str] = None
    method: Optional[str] = "GET"
    body: Optional[Any] = None
    headers: Optional[Dict[str, str]] = None
    query_params: Optional[Dict[str, Any]] = None


class MutationSimulateResult(BaseModel):
    success: bool
    status_code: int = 200
    response: Any = None
    diff: StateDiffResult = Field(default_factory=StateDiffResult)
    db_queries: List[Dict[str, Any]] = Field(default_factory=list)
    warnings: List[str] = Field(default_factory=list)
    rolled_back: bool = True
    execution_time_ms: float = 0.0


class MutationApplyResult(BaseModel):
    success: bool
    status_code: int = 200
    response: Any = None
    mutation_id: str = ""
    db_queries: List[Dict[str, Any]] = Field(default_factory=list)
    applied_at: str = ""
    execution_time_ms: float = 0.0


class AssertionRule(BaseModel):
    target: str  # "status_code", "response.id", "db_queries.count"
    operator: str  # "eq", "neq", "gt", "lt", "contains", "exists"
    expected: Any = None


class VerificationAssertResult(BaseModel):
    passed: bool
    results: List[Dict[str, Any]] = Field(default_factory=list)
    total_assertions: int = 0
    passed_count: int = 0
    failed_count: int = 0
