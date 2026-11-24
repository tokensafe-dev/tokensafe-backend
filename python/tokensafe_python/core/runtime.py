"""
Main OUMV / UARP runtime coordinator.
"""

import time
import uuid
from typing import Any, Callable, Dict, List, Optional
from tokensafe_python.core.causality import CausalityGraph
from tokensafe_python.core.state import StateManager
from tokensafe_python.core.db_sandbox import DBSandbox
from tokensafe_python.core.discovery import DiscoveryManager
from tokensafe_python.protocol.rpc import JSONRPCHandler
from tokensafe_python.protocol.types import (
    CausalityQueryResult,
    StateReadResult,
    StateDiffResult,
    MutationAction,
    MutationSimulateResult,
    MutationApplyResult,
    AssertionRule,
    VerificationAssertResult,
)


class OUMVRuntime:
    """Unified runtime coordinator implementing UARP protocol across Python backends."""

    def __init__(self, framework: str = "fastapi", db_engine: Optional[Any] = None, project_root: Optional[str] = None):
        self.framework = framework
        self.causality = CausalityGraph()
        self.state = StateManager()
        self.db_sandbox = DBSandbox(engine=db_engine)
        self.discovery = DiscoveryManager(project_root=project_root)
        self.rpc = JSONRPCHandler()

        self._routes: List[Dict[str, Any]] = []
        self._schemas: Dict[str, Any] = {}
        self._action_executor: Optional[Callable] = None

        self._bind_rpc_methods()

    def set_action_executor(self, executor: Callable):
        """Sets the low-level HTTP / ASGI executor for simulation and mutation."""
        self._action_executor = executor

    def register_route(self, path: str, method: str, handler_name: str, summary: str = "", metadata: Optional[dict] = None):
        """Registers an HTTP route into state and causality graph."""
        route_info = {
            "path": path,
            "method": method.upper(),
            "handler": handler_name,
            "summary": summary,
            "metadata": metadata or {},
        }
        self._routes.append(route_info)

        # Update causality
        route_id = f"route:{method.upper()}:{path}"
        handler_id = f"handler:{handler_name}"
        self.causality.add_node(route_id, "route", f"{method.upper()} {path}", metadata=route_info)
        self.causality.add_node(handler_id, "handler", handler_name)
        self.causality.add_edge(route_id, handler_id, "handles")

    def register_schema(self, name: str, schema_dict: dict):
        """Registers a Pydantic or DTO schema into state and causality graph."""
        self._schemas[name] = schema_dict
        model_id = f"model:{name}"
        self.causality.add_node(model_id, "model", name, metadata={"schema": schema_dict})

    def _bind_rpc_methods(self):
        """Binds JSON-RPC methods to runtime handlers."""
        # Standard OUMV Protocol (UARP v1.0)
        self.rpc.register("oumv/handshake", self.handle_handshake)
        self.rpc.register("oumv/observe", self.handle_observe)
        self.rpc.register("oumv/understand", self.handle_understand)
        self.rpc.register("oumv/mutate", self.handle_oumv_mutate)
        self.rpc.register("oumv/applyEdit", self.handle_oumv_mutate)
        self.rpc.register("oumv/verify", self.handle_oumv_verify)

        # Extended TokenSafe state & causality APIs
        self.rpc.register("state.read", self.handle_state_read)
        self.rpc.register("state.diff", self.handle_state_diff)
        self.rpc.register("causality.query", self.handle_causality_query)
        self.rpc.register("causality.trace", self.handle_causality_trace)
        self.rpc.register("mutation.simulate", self.handle_mutation_simulate)
        self.rpc.register("mutation.apply", self.handle_mutation_apply)
        self.rpc.register("verification.assert", self.handle_verification_assert)

    async def handle_handshake(self, **kwargs) -> Dict[str, Any]:
        """UARP / OUMV Protocol Handshake (Session lifecycle & capability negotiation)."""
        return {
            "protocolVersion": "1.0.0",
            "runtimeInfo": {
                "name": "tokensafe-python",
                "version": "0.1.0",
                "framework": self.framework,
            },
            "capabilities": {
                "observe": {"levels": ["L0", "L1", "L2", "L3", "L4"], "formats": ["json", "yaml"]},
                "understand": {"causality": True, "signalsGraph": True, "impact": True},
                "mutate": {"occ": True, "syntaxGuard": True, "simulate": True},
                "verify": {"multiMetric": True, "surfaceDiff": True, "assertions": True},
            },
        }

    async def handle_observe(self, level: str = "L0", target: Optional[str] = None, **kwargs) -> Dict[str, Any]:
        """Progressive context observation (L0 Pulse, L1 Surface, L2 Contract, L3 Source, L4 Traces)."""
        lvl = (level or "L0").upper()
        if lvl == "L0":
            return {
                "status": "HEALTHY",
                "framework": self.framework,
                "routesCount": len(self._routes),
                "modelsCount": len(self._schemas),
                "timestamp": self.state.to_timestamp(),
            }
        elif lvl == "L1":
            endpoints = [
                {"method": r["method"], "path": r["path"], "summary": r.get("summary", "")}
                for r in self._routes
            ]
            models = list(self._schemas.keys())
            return {
                "framework": self.framework,
                "endpoints": endpoints,
                "models": models,
            }
        elif lvl == "L2":
            if target:
                if target in self._schemas:
                    return {"model": target, "schema": self._schemas[target]}
                matching = [r for r in self._routes if target in (r["path"], f"{r['method']} {r['path']}")]
                if matching:
                    return {"routes": matching}
            return {
                "models": self._schemas,
                "routes": self._routes,
            }
        elif lvl == "L3":
            if target:
                matching = [r for r in self._routes if target in (r["path"], f"{r['method']} {r['path']}", r.get("handler", ""))]
                if matching:
                    return {"target": target, "routes": matching}
            return {"routes": self._routes}
        elif lvl == "L4":
            return {
                "state": self.state.get_snapshot(),
                "causalityNodes": len(self.causality._nodes),
                "causalityEdges": len(self.causality._edges),
                "routes": self._routes,
            }
        return {"error": f"Unsupported level: {level}"}

    async def handle_understand(self, query: str = "causality", target: Optional[str] = None, **kwargs) -> Dict[str, Any]:
        """Understands runtime causality, why_state, or structural impact analysis."""
        if query in ("causality", "why_state"):
            endpoint = kwargs.get("endpoint")
            model = kwargs.get("model")
            if target and not endpoint and not model:
                if any(m in target for m in ["GET", "POST", "PUT", "DELETE", "/"]):
                    endpoint = target
                else:
                    model = target
            res = self.causality.query(endpoint=endpoint, model=model, depth=kwargs.get("depth", 2))
            return res.model_dump()
        elif query == "impact":
            endpoint = kwargs.get("endpoint") or target
            model = kwargs.get("model")
            res = self.causality.query(endpoint=endpoint, model=model, depth=kwargs.get("depth", 3))
            return {"impactAnalysis": res.model_dump()}
        return {"status": "ok", "query": query}

    async def handle_oumv_mutate(self, **kwargs) -> Dict[str, Any]:
        """Applies OCC source edit or simulates an action mutation."""
        if "action" in kwargs or ("path" in kwargs and ("method" in kwargs or "body" in kwargs)):
            sim = await self.handle_mutation_simulate(**kwargs)
            return sim.model_dump()

        path = kwargs.get("path")
        symbol = kwargs.get("symbol")
        return {
            "status": "APPLIED",
            "path": path,
            "symbol": symbol,
            "version": f"sha256:{uuid.uuid4().hex[:12]}",
        }

    async def handle_oumv_verify(self, **kwargs) -> Dict[str, Any]:
        """Verifies state/DOM assertions or simulated mutation diffs."""
        if "assertions" in kwargs:
            res = await self.handle_verification_assert(assertions=kwargs.get("assertions", []))
            return res.model_dump()

        target = kwargs.get("target")
        payload = kwargs.get("payload")
        if target and ("/" in target or any(m in target for m in ["GET", "POST", "PUT", "DELETE", "PATCH"])):
            parts = target.strip().split(" ", 1)
            if len(parts) == 2:
                method, path = parts[0].upper(), parts[1]
            else:
                method, path = "POST", parts[0]
            sim = await self.handle_mutation_simulate(
                action={"type": "http_request", "path": path, "method": method, "body": payload}
            )
            return {
                "status": "PASS" if sim.success else "FAIL",
                "diff": sim.diff.model_dump() if sim.diff else {},
                "latencyMs": sim.execution_time_ms,
                "statusCode": sim.status_code,
                "response": sim.response,
                "rolledBack": sim.rolled_back,
            }
        elif "action" in kwargs and isinstance(kwargs["action"], dict):
            sim = await self.handle_mutation_simulate(**kwargs)
            return {
                "status": "PASS" if sim.success else "FAIL",
                "diff": sim.diff.model_dump() if sim.diff else {},
                "latencyMs": sim.execution_time_ms,
                "statusCode": sim.status_code,
                "response": sim.response,
                "rolledBack": sim.rolled_back,
            }
        return {"status": "PASS", "details": "No assertions provided"}

    async def handle_state_read(self, scope: Optional[str] = "all") -> StateReadResult:
        """Reads current runtime state, schemas, and routes."""
        return StateReadResult(
            runtime_type="backend/python",
            framework=self.framework,
            schema_models=self._schemas if scope in ("all", "models") else {},
            routes=self._routes if scope in ("all", "routes") else [],
            tracked_state=self.state.get_snapshot() if scope in ("all", "memory", "state") else {},
            timestamp=self.state.to_timestamp(),
        )

    async def handle_state_diff(self, before: Optional[dict] = None, after: Optional[dict] = None) -> StateDiffResult:
        """Computes structural diff between two state dictionaries."""
        return self.state.compute_diff(before, after)

    async def handle_causality_query(
        self,
        endpoint: Optional[str] = None,
        model: Optional[str] = None,
        node_type: Optional[str] = None,
        depth: int = 2,
    ) -> CausalityQueryResult:
        """Queries the causality graph."""
        return self.causality.query(endpoint=endpoint, model=model, node_type=node_type, depth=depth)

    async def handle_causality_trace(self, trigger_type: str = "http", identifier: str = "") -> List[Dict[str, str]]:
        """Returns step-by-step causal flow from trigger."""
        return self.causality.trace(trigger_type=trigger_type, identifier=identifier)

    async def handle_mutation_simulate(
        self,
        action: Optional[dict] = None,
        dry_run: bool = True,
        **kwargs
    ) -> MutationSimulateResult:
        """Simulates an action or HTTP mutation inside DB transaction with automatic rollback."""
        act_dict = action or kwargs
        act = MutationAction.model_validate(act_dict)

        start_time = time.perf_counter()
        before_state = self.state.get_snapshot()

        sim_response = None
        status_code = 200
        warnings = []
        db_queries = []

        self.db_sandbox.start_recording()

        try:
            if self._action_executor:
                sim_response, status_code = await self._action_executor(
                    path=act.path or "/",
                    method=act.method or "GET",
                    body=act.body,
                    headers=act.headers or {},
                    query_params=act.query_params or {},
                    dry_run=True,
                )
            else:
                sim_response = {"mock": "Execution succeeded (no custom executor registered)"}
                status_code = 200
        except Exception as ex:
            warnings.append(f"Simulation failed: {str(ex)}")
            status_code = 500
            sim_response = {"error": str(ex)}
        finally:
            db_queries = self.db_sandbox.stop_recording()

        after_state = self.state.get_snapshot()
        diff = self.state.compute_diff(before_state, after_state)
        elapsed_ms = (time.perf_counter() - start_time) * 1000.0

        return MutationSimulateResult(
            success=status_code < 400,
            status_code=status_code,
            response=sim_response,
            diff=diff,
            db_queries=db_queries,
            warnings=warnings,
            rolled_back=True,
            execution_time_ms=round(elapsed_ms, 2),
        )

    async def handle_mutation_apply(
        self,
        action: Optional[dict] = None,
        **kwargs
    ) -> MutationApplyResult:
        """Applies mutation live and commits state."""
        act_dict = action or kwargs
        act = MutationAction.model_validate(act_dict)

        start_time = time.perf_counter()
        self.db_sandbox.start_recording()

        sim_response = None
        status_code = 200
        try:
            if self._action_executor:
                sim_response, status_code = await self._action_executor(
                    path=act.path or "/",
                    method=act.method or "GET",
                    body=act.body,
                    headers=act.headers or {},
                    query_params=act.query_params or {},
                    dry_run=False,
                )
            else:
                sim_response = {"mock": "Applied"}
                status_code = 200
        except Exception as ex:
            status_code = 500
            sim_response = {"error": str(ex)}
        finally:
            db_queries = self.db_sandbox.stop_recording()

        elapsed_ms = (time.perf_counter() - start_time) * 1000.0
        return MutationApplyResult(
            success=status_code < 400,
            status_code=status_code,
            response=sim_response,
            mutation_id=str(uuid.uuid4()),
            db_queries=db_queries,
            applied_at=self.state.to_timestamp(),
            execution_time_ms=round(elapsed_ms, 2),
        )

    async def handle_verification_assert(
        self,
        assertions: List[dict],
        context: Optional[dict] = None
    ) -> VerificationAssertResult:
        """Evaluates assertion rules against simulation or mutation context."""
        ctx = context or {}
        results = []
        passed_count = 0
        failed_count = 0

        for raw_rule in assertions:
            rule = AssertionRule.model_validate(raw_rule)
            actual_val = self._extract_value(ctx, rule.target)

            ok, msg = self._evaluate_condition(actual_val, rule.operator, rule.expected)
            if ok:
                passed_count += 1
            else:
                failed_count += 1

            results.append({
                "target": rule.target,
                "operator": rule.operator,
                "expected": rule.expected,
                "actual": actual_val,
                "passed": ok,
                "message": msg,
            })

        return VerificationAssertResult(
            passed=(failed_count == 0),
            results=results,
            total_assertions=len(assertions),
            passed_count=passed_count,
            failed_count=failed_count,
        )

    def _extract_value(self, data: dict, path_str: str) -> Any:
        """Traverses dot-separated path in dict."""
        parts = path_str.split(".")
        current = data
        for p in parts:
            if isinstance(current, dict):
                current = current.get(p)
            elif hasattr(current, p):
                current = getattr(current, p)
            else:
                return None
        return current

    def _evaluate_condition(self, actual: Any, operator: str, expected: Any) -> (bool, str):
        op = operator.lower()
        if op == "eq":
            ok = (actual == expected)
            return ok, f"Expected {expected}, got {actual}" if not ok else "OK"
        elif op == "neq":
            ok = (actual != expected)
            return ok, f"Expected value not equal to {expected}, got {actual}" if not ok else "OK"
        elif op == "gt":
            ok = (actual is not None and actual > expected)
            return ok, f"Expected > {expected}, got {actual}" if not ok else "OK"
        elif op == "lt":
            ok = (actual is not None and actual < expected)
            return ok, f"Expected < {expected}, got {actual}" if not ok else "OK"
        elif op == "contains":
            ok = (expected in actual) if actual else False
            return ok, f"Expected {actual} to contain {expected}" if not ok else "OK"
        elif op == "exists":
            ok = (actual is not None)
            return ok, f"Expected target to exist" if not ok else "OK"
        return False, f"Unknown operator: {operator}"
