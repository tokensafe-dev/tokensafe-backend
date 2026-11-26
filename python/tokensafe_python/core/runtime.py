"""
Main OUMV / UARP runtime coordinator.
"""

import ast
import hashlib
import os
import shutil
import subprocess
import tempfile
import time
import uuid
from typing import Any, Callable, Dict, List, Optional
from tokensafe_python.core.causality import CausalityGraph
from tokensafe_python.core.state import StateManager
from tokensafe_python.core.db_sandbox import DBSandbox
from tokensafe_python.core.discovery import DiscoveryManager
from tokensafe_python.core.session import SessionRegistry
from tokensafe_python.protocol.errors import (
    OumvError,
    CONFLICT_DETECTED,
    SYNTAX_ERROR,
    SECURITY_VIOLATION,
    RESOURCE_NOT_FOUND,
    UNSUPPORTED_CAPABILITY,
    VALIDATION_FAILED,
)
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
        self.sessions = SessionRegistry()
        self.rpc = JSONRPCHandler()

        self._routes: List[Dict[str, Any]] = []
        self._schemas: Dict[str, Any] = {}
        self._operations: Dict[str, Dict[str, Any]] = {}
        self._action_executor: Optional[Callable] = None
        # L4 evidence: bounded ring of observed RPC executions (never fabricated).
        self._traces: List[Dict[str, Any]] = []
        self._trace_limit = 128

        self._bind_rpc_methods()

    def register_operation(self, contract: Dict[str, Any]) -> None:
        """Registers a Level 3 semantic operation contract (spec §7.4/§10.4)."""
        op_id = contract.get("id")
        if not op_id:
            return
        self._operations[op_id] = contract
        self.causality.add_node(f"operation:{op_id}", "operation", op_id, metadata=contract)

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
        # Standard OUMV Protocol (UARP v1.1)
        bindings = [
            ("oumv/handshake", self.handle_handshake),
            ("oumv/disconnect", self.handle_disconnect),
            ("oumv/observe", self.handle_observe),
            ("oumv/understand", self.handle_understand),
            ("oumv/getSource", self.handle_get_source),
            ("oumv/mutate", self.handle_oumv_mutate),
            ("oumv/applyEdit", self.handle_oumv_mutate),
            ("oumv/verify", self.handle_oumv_verify),
            # Extended TokenSafe state & causality APIs
            ("state.read", self.handle_state_read),
            ("state.diff", self.handle_state_diff),
            ("causality.query", self.handle_causality_query),
            ("causality.trace", self.handle_causality_trace),
            ("mutation.simulate", self.handle_mutation_simulate),
            ("mutation.apply", self.handle_mutation_apply),
            ("verification.assert", self.handle_verification_assert),
        ]
        for method, handler in bindings:
            self.rpc.register(method, self._recorded(method, handler))

    def _recorded(self, method: str, handler: Callable):
        """Wraps a handler to record L4 trace evidence for every RPC execution."""

        async def wrapper(**kwargs):
            session_id = kwargs.get("sessionId")
            if method != "oumv/handshake" and method != "oumv/disconnect" and not self.sessions.validate(session_id):
                raise OumvError(
                    VALIDATION_FAILED,
                    "VALIDATION_FAILED: unknown or expired sessionId — re-run oumv/handshake",
                    data={"reason": "session_expired", "sessionId": session_id},
                )
            start = time.perf_counter()
            error = ""
            try:
                return await handler(**kwargs)
            except OumvError as exc:
                error = exc.message
                raise
            except Exception as exc:  # noqa: BLE001 - recorded, then re-raised
                error = str(exc)
                raise
            finally:
                elapsed_ms = (time.perf_counter() - start) * 1000.0
                self._record_trace(
                    method=method,
                    target=str(kwargs.get("target") or kwargs.get("path") or ""),
                    duration_ms=elapsed_ms,
                    error=error,
                )

        return wrapper

    def _record_trace(self, method: str, target: str, duration_ms: float, error: str = "") -> None:
        """Appends one observed execution to the bounded trace ring."""
        trace = {
            "method": method,
            "target": target,
            "statusCode": 500 if error else 200,
            "durationMs": round(duration_ms, 3),
            "error": error,
            "timestamp": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        }
        if len(self._traces) >= self._trace_limit:
            self._traces.pop(0)
        self._traces.append(trace)

    def _traces_for(self, target: Optional[str]) -> List[Dict[str, Any]]:
        """Returns recorded traces (newest first), filtered by target when given."""
        traces = list(reversed(self._traces))
        if not target:
            return traces
        return [t for t in traces if target in (t["target"], t["method"]) or t["target"].startswith(target)]

    def _read_source(self, path: Optional[str], symbol: Optional[str] = None) -> Dict[str, Any]:
        """Reads project source with its sha256 OCC token (L3 / getSource)."""
        abs_path = self._resolve_project_file(path or "")
        if not abs_path or not os.path.isfile(abs_path):
            raise OumvError(RESOURCE_NOT_FOUND, f"RESOURCE_NOT_FOUND: {path}")
        with open(abs_path, "rb") as f:
            data = f.read()
        try:
            text = data.decode("utf-8")
        except UnicodeDecodeError:
            raise OumvError(VALIDATION_FAILED, f"VALIDATION_FAILED: {path} is not valid UTF-8 source")
        version = "sha256:" + hashlib.sha256(data).hexdigest()
        start_line, end_line = 1, text.count("\n") + (0 if text.endswith("\n") else 1)
        source = text
        if symbol:
            lines = text.splitlines()
            start = next((i for i, line in enumerate(lines) if symbol in line), None)
            if start is None:
                raise OumvError(RESOURCE_NOT_FOUND, f"RESOURCE_NOT_FOUND: symbol {symbol!r} not found in {path}")
            indent = len(lines[start]) - len(lines[start].lstrip())
            end = start
            for i in range(start + 1, len(lines)):
                line = lines[i]
                if line.strip() and (len(line) - len(line.lstrip())) <= indent:
                    break
                end = i
            source = "\n".join(lines[start:end + 1])
            start_line, end_line = start + 1, end + 1
        return {
            "path": path,
            "symbol": symbol or "",
            "source": source,
            "version": version,
            "startLine": start_line,
            "endLine": end_line,
        }

    async def handle_handshake(self, **kwargs) -> Dict[str, Any]:
        """UARP / OUMV Protocol Handshake (session lifecycle & capability negotiation)."""
        session_id = self.sessions.create()
        caps = {
            # JSON only (no YAML emitter), no frontend signals graph: the backend
            # answers causality from its own causality graph, not reactive signals.
            "observe": {"levels": ["L0", "L1", "L2", "L3", "L4"], "formats": ["json"]},
            "understand": {"causality": True, "signalsGraph": False, "impactAnalysis": True},
            "mutate": {"occ": True, "syntaxGuard": True, "ephemeralDispatch": False},
            # No DOM/a11y surface exists on the backend profile.
            "verify": {"multiMetric": True, "surfaceDiff": False},
        }
        # §3.1: honour the client's request by narrowing the advertised set. Arrays
        # are filters; an empty intersection stays empty ("cannot serve").
        requested = kwargs.get("requestedCapabilities") or {}
        req_observe = requested.get("observe") or {} if isinstance(requested, dict) else {}
        req_levels = req_observe.get("levels") or []
        req_formats = req_observe.get("formats") or []
        if req_levels:
            caps["observe"]["levels"] = [lvl for lvl in caps["observe"]["levels"] if lvl in req_levels]
        if req_formats:
            caps["observe"]["formats"] = [fmt for fmt in caps["observe"]["formats"] if fmt in req_formats]
        return {
            "protocolVersion": "1.2.0",
            "profile": "backend",
            "sessionId": session_id,
            "ttlSeconds": self.sessions.ttl,
            "runtimeInfo": {
                "name": "tokensafe-python",
                "version": "0.3.0",
                "framework": self.framework,
            },
            "capabilities": caps,
        }

    async def handle_disconnect(self, sessionId: Optional[str] = None, **kwargs) -> Dict[str, Any]:  # noqa: N803 - wire name
        """Closes a handshake session (spec §3)."""
        return {"closed": self.sessions.delete(sessionId)}

    async def handle_get_source(self, path: Optional[str] = None, symbol: Optional[str] = None, **kwargs) -> Dict[str, Any]:
        """Pillar 3A: source with the sha256 OCC token (same contract as Go)."""
        return self._read_source(path, symbol)

    async def handle_observe(self, level: str = "L0", target: Optional[str] = None, limit: Optional[int] = None, cursor: Optional[str] = None, **kwargs) -> Dict[str, Any]:
        """Progressive context observation (L0 Pulse, L1 Surface, L2 Contract, L3 Source, L4 Traces).

        Returns the protocol envelope `{level, format, data, tokenEstimate}` so an
        agent parses one shape across the frontend and backend profiles.
        """
        lvl = (level or "L0").upper()
        if lvl == "L0":
            data: Dict[str, Any] = {
                "status": "HEALTHY",
                "framework": self.framework,
                "routesCount": len(self._routes),
                "modelsCount": len(self._schemas),
                "timestamp": self.state.to_timestamp(),
            }
            token_estimate = 30
        elif lvl == "L1":
            page, has_more, next_cursor = self._page(self._routes, limit, cursor)
            data = {
                "framework": self.framework,
                "endpoints": [
                    {"method": r["method"], "path": r["path"], "summary": r.get("summary", "")}
                    for r in page
                ],
                "models": list(self._schemas.keys()),
            }
            if limit:
                data["hasMore"] = has_more
                if next_cursor:
                    data["nextCursor"] = next_cursor
            token_estimate = 30 + 15 * len(page)
        elif lvl == "L2":
            evidence = {"provenance": "runtime", "sourceAdapter": "tokensafe-python", "confidence": "confirmed"}
            if target:
                if target in self._operations:
                    data = {"target": target, "operation": self._operations[target], "evidence": evidence}
                elif target in self._schemas:
                    data = {"target": target, "model": target, "schema": self._schemas[target], "evidence": evidence}
                else:
                    matching = [r for r in self._routes if target in (r["path"], f"{r['method']} {r['path']}")]
                    if not matching:
                        raise OumvError(
                            RESOURCE_NOT_FOUND,
                            f"RESOURCE_NOT_FOUND: no operation, model or route matches {target!r}",
                        )
                    data = {"target": target, "routes": matching, "evidence": evidence}
            else:
                data = {
                    "models": self._schemas,
                    "routes": self._routes,
                    "operations": list(self._operations.values()),
                    "evidence": evidence,
                }
            token_estimate = 80
        elif lvl == "L3":
            if not target:
                raise OumvError(VALIDATION_FAILED, "VALIDATION_FAILED: L3 observe requires a target (path or path#symbol)")
            path, _, symbol = target.partition("#")
            data = self._read_source(path, symbol or None)
            token_estimate = 300
        elif lvl == "L4":
            traces = self._traces_for(target)
            page, has_more, next_cursor = self._page(traces, limit, cursor)
            data = {
                "target": target or "",
                "traces": page,
                "traceCount": len(traces),
                "note": "" if traces else "no RPC traces recorded yet: call oumv/verify or execute a request first",
                "evidence": {"provenance": "runtime", "sourceAdapter": "tokensafe-python", "confidence": "confirmed"},
            }
            if limit:
                data["hasMore"] = has_more
                if next_cursor:
                    data["nextCursor"] = next_cursor
            token_estimate = 40 + 15 * len(page)
        else:
            raise OumvError(UNSUPPORTED_CAPABILITY, f"UNSUPPORTED_CAPABILITY: observe level {lvl}")

        return {"level": lvl, "format": "json", "data": data, "tokenEstimate": token_estimate}

    @staticmethod
    def _page(items: List[Any], limit: Optional[int], cursor: Optional[str]) -> "tuple[List[Any], bool, str]":
        """§4.1 limit/cursor: absent limit means the whole collection."""
        total = len(items)
        start = 0
        if cursor:
            try:
                start = int(cursor)
            except (TypeError, ValueError):
                start = 0
        start = max(0, min(start, total))
        if limit and limit > 0 and start + limit < total:
            end = start + limit
            return items[start:end], True, str(end)
        return items[start:], False, ""

    async def handle_understand(self, query: str = "causality", target: Optional[str] = None, **kwargs) -> Dict[str, Any]:
        """Understands runtime causality, why_state, or structural impact analysis.

        Causality is answered from recorded RPC traces when they exist and says so
        when they do not — the static graph is reported as evidence, never as an
        observed execution.
        """
        if query in ("causality", "why_state"):
            endpoint = kwargs.get("endpoint")
            model = kwargs.get("model")
            if target and not endpoint and not model:
                if any(m in target for m in ["GET", "POST", "PUT", "DELETE", "/"]):
                    endpoint = target
                else:
                    model = target
            graph = self.causality.query(endpoint=endpoint, model=model, depth=kwargs.get("depth", 2))
            traces = self._traces_for(target)
            latest = traces[0] if traces else None
            causality = {
                "target": target or "",
                "observed": bool(traces),
                "traceCount": len(traces),
                "traceChain": [f"{t['method']} {t['target']}".strip() for t in traces[:5]],
                "statusCode": latest["statusCode"] if latest else 0,
                "latencyMs": latest["durationMs"] if latest else 0.0,
                "rootCause": (latest["error"] or "OK — no error recorded in the observed execution") if latest else "",
                "note": "" if traces else "no RPC trace recorded for this target; static graph only",
                "staticGraph": graph.model_dump(),
                "evidence": {
                    "provenance": "runtime" if traces else "static",
                    "sourceAdapter": "tokensafe-python",
                    "confidence": "confirmed" if traces else "probable",
                },
            }
            # Keep the legacy graph keys at the top level for existing clients.
            result: Dict[str, Any] = {"query": query, "target": target or "", "causality": causality}
            result.update(graph.model_dump())
            return result
        elif query == "impact":
            endpoint = kwargs.get("endpoint") or target
            model = kwargs.get("model")
            res = self.causality.query(endpoint=endpoint, model=model, depth=kwargs.get("depth", 3))
            static = res.model_dump()
            impact = {
                "target": target or endpoint or model or "",
                "affectedSymbols": [n.get("id") for n in static.get("nodes", []) if isinstance(n, dict)],
                "breakingRisk": "UNKNOWN",
                "summary": f"{len(static.get('nodes', []))} nodes, {len(static.get('edges', []))} edges in the static causality graph",
                "observed": False,
                "evidence": {"provenance": "static", "sourceAdapter": "tokensafe-python", "confidence": "probable"},
            }
            return {"query": "impact", "target": impact["target"], "impact": impact, "impactAnalysis": static}
        return {"status": "ok", "query": query}

    async def handle_oumv_mutate(self, **kwargs) -> Dict[str, Any]:
        """Applies an OCC source edit (replace/create/symbol/delete/rename) or simulates an action."""
        if "action" in kwargs or ("path" in kwargs and ("method" in kwargs or "body" in kwargs)):
            sim = await self.handle_mutation_simulate(**kwargs)
            return sim.model_dump()

        path = kwargs.get("path") or kwargs.get("filePath")
        symbol = kwargs.get("symbol")
        replacement = kwargs.get("replacement")
        if replacement is None:
            replacement = kwargs.get("source")
        expected_version = kwargs.get("expectedVersion") or kwargs.get("expected_version")
        rename_to = kwargs.get("renameTo") or kwargs.get("rename_to")
        mode = str(kwargs.get("mode") or "replace").lower()
        if mode not in ("replace", "create", "symbol", "delete", "rename"):
            raise OumvError(
                UNSUPPORTED_CAPABILITY,
                f"UNSUPPORTED_CAPABILITY: unknown applyEdit mode {mode!r}",
                {"reason": "unknown_mode"},
            )

        if mode == "create":
            return self._create_source_file(path=path, replacement=replacement, expected_version=expected_version)
        if mode == "delete":
            return self._delete_source_file(path=path, replacement=replacement, expected_version=expected_version)
        if mode == "rename":
            return self._rename_source_file(
                path=path, rename_to=rename_to, replacement=replacement, expected_version=expected_version
            )
        if replacement is None:
            # No replacement: report the current OCC version (getSource-style probe).
            version = self._file_version(path)
            if version is None:
                raise OumvError(RESOURCE_NOT_FOUND, f"RESOURCE_NOT_FOUND: file not found inside project root: {path}")
            return {"status": "CURRENT", "path": path, "symbol": symbol, "version": version}
        if mode == "symbol":
            return self._apply_symbol_edit(
                path=path, symbol=symbol, replacement=str(replacement), expected_version=expected_version
            )
        return self._apply_source_edit(
            path=path, symbol=symbol, replacement=replacement, expected_version=expected_version
        )

    def _create_source_file(self, *, path: str, replacement: str, expected_version: Optional[str]) -> Dict[str, Any]:
        """mode=create (spec §4.3): write a file that must not exist yet."""
        if not path or not str(path).strip():
            raise OumvError(VALIDATION_FAILED, "VALIDATION_FAILED: path is required")
        if expected_version:
            raise OumvError(
                VALIDATION_FAILED,
                "VALIDATION_FAILED: expectedVersion must be omitted for mode=create",
                {"reason": "create_forbids_version"},
            )
        root = self._project_root_abs()
        candidate = os.path.abspath(os.path.join(root, path)) if not os.path.isabs(path) else os.path.abspath(path)
        if candidate != root and not candidate.startswith(root + os.sep):
            raise OumvError(SECURITY_VIOLATION, f"SECURITY_VIOLATION: path traversal blocked: {path}")
        if os.path.exists(candidate):
            raise OumvError(
                CONFLICT_DETECTED,
                "CONFLICT_DETECTED: file already exists; use mode=replace with its version",
                {"reason": "file_exists"},
            )
        syntax = self._syntax_check(candidate, str(replacement))
        os.makedirs(os.path.dirname(candidate) or root, exist_ok=True)
        with open(candidate, "w", encoding="utf-8") as handle:
            handle.write(str(replacement))
        version = "sha256:" + hashlib.sha256(str(replacement).encode("utf-8")).hexdigest()
        return {
            "status": "APPLIED",
            "path": os.path.relpath(candidate, root),
            "newVersion": version,
            "syntaxCheck": syntax,
            "created": True,
        }

    def _delete_source_file(self, *, path: str, replacement: Optional[str], expected_version: Optional[str]) -> Dict[str, Any]:
        """mode=delete (spec §4.3): remove a file guarded by OCC."""
        if replacement not in (None, ""):
            raise OumvError(
                VALIDATION_FAILED,
                "VALIDATION_FAILED: replacement must be omitted for mode=delete",
                {"reason": "replacement_forbidden"},
            )
        abs_path = self._resolve_project_file(path or "")
        if abs_path is None:
            raise OumvError(SECURITY_VIOLATION, f"SECURITY_VIOLATION: path traversal blocked: {path}")
        if not os.path.isfile(abs_path):
            raise OumvError(RESOURCE_NOT_FOUND, f"RESOURCE_NOT_FOUND: {path}")
        current_version = self._file_version(path)
        if not expected_version:
            raise OumvError(
                VALIDATION_FAILED,
                "VALIDATION_FAILED: expectedVersion is required to delete a file",
                {"reason": "expected_version_required", "path": path},
            )
        if expected_version != current_version:
            raise OumvError(CONFLICT_DETECTED, "CONFLICT_DETECTED: OCC version mismatch on delete")
        os.remove(abs_path)
        return {"status": "APPLIED", "path": path, "syntaxCheck": {"status": "skipped"}, "deleted": True}

    def _rename_source_file(
        self, *, path: str, rename_to: Optional[str], replacement: Optional[str], expected_version: Optional[str]
    ) -> Dict[str, Any]:
        """mode=rename (spec §4.3): move a file; both paths stay inside the jail."""
        if not rename_to or not str(rename_to).strip():
            raise OumvError(
                VALIDATION_FAILED,
                "VALIDATION_FAILED: renameTo is required for mode=rename",
                {"reason": "rename_to_required"},
            )
        if replacement not in (None, ""):
            raise OumvError(
                VALIDATION_FAILED,
                "VALIDATION_FAILED: replacement must be omitted for mode=rename",
                {"reason": "replacement_forbidden"},
            )
        abs_source = self._resolve_project_file(path or "")
        if abs_source is None:
            raise OumvError(SECURITY_VIOLATION, f"SECURITY_VIOLATION: path traversal blocked: {path}")
        abs_target = self._resolve_project_file(str(rename_to))
        if abs_target is None:
            raise OumvError(SECURITY_VIOLATION, f"SECURITY_VIOLATION: path traversal blocked: {rename_to}")
        if not os.path.isfile(abs_source):
            raise OumvError(RESOURCE_NOT_FOUND, f"RESOURCE_NOT_FOUND: {path}")
        if os.path.exists(abs_target):
            raise OumvError(
                CONFLICT_DETECTED,
                "CONFLICT_DETECTED: rename target already exists",
                {"reason": "target_exists", "renameTo": rename_to},
            )
        current_version = self._file_version(path)
        if not expected_version:
            raise OumvError(
                VALIDATION_FAILED,
                "VALIDATION_FAILED: expectedVersion is required to rename a file",
                {"reason": "expected_version_required", "path": path},
            )
        if expected_version != current_version:
            raise OumvError(CONFLICT_DETECTED, "CONFLICT_DETECTED: OCC version mismatch on rename")
        os.makedirs(os.path.dirname(abs_target) or self._project_root_abs(), exist_ok=True)
        os.rename(abs_source, abs_target)
        return {
            "status": "APPLIED",
            "path": path,
            "newPath": str(rename_to),
            "syntaxCheck": {"status": "skipped"},
        }

    def _apply_symbol_edit(
        self, *, path: str, symbol: Optional[str], replacement: str, expected_version: Optional[str]
    ) -> Dict[str, Any]:
        """mode=symbol (spec §4.3): replace one Python function/method/class range."""
        if not symbol or not str(symbol).strip():
            raise OumvError(
                VALIDATION_FAILED,
                "VALIDATION_FAILED: symbol is required for mode=symbol",
                {"reason": "symbol_required"},
            )
        abs_path = self._resolve_project_file(path or "")
        if abs_path is None:
            raise OumvError(SECURITY_VIOLATION, f"SECURITY_VIOLATION: path traversal blocked: {path}")
        if not os.path.isfile(abs_path):
            raise OumvError(RESOURCE_NOT_FOUND, f"RESOURCE_NOT_FOUND: {path}")
        with open(abs_path, "rb") as f:
            current_bytes = f.read()
        current_version = "sha256:" + hashlib.sha256(current_bytes).hexdigest()
        if not expected_version:
            raise OumvError(
                VALIDATION_FAILED,
                "VALIDATION_FAILED: expectedVersion is required to edit a symbol",
                {"reason": "expected_version_required", "path": path},
            )
        if expected_version != current_version:
            raise OumvError(CONFLICT_DETECTED, "CONFLICT_DETECTED: OCC version mismatch")

        source = current_bytes.decode("utf-8", errors="replace")
        try:
            tree = ast.parse(source)
        except SyntaxError as e:
            raise OumvError(SYNTAX_ERROR, f"SYNTAX_ERROR: cannot parse {path}: {e}")

        def names(node: Any) -> bool:
            return isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef, ast.ClassDef)) and node.name == symbol

        target = None
        for node in tree.body:
            if names(node):
                target = node
                break
            if isinstance(node, ast.ClassDef):
                for child in node.body:
                    if isinstance(child, (ast.FunctionDef, ast.AsyncFunctionDef)) and child.name == symbol:
                        target = child
                        break
                if target is not None:
                    break
        if target is None:
            raise OumvError(RESOURCE_NOT_FOUND, f"RESOURCE_NOT_FOUND: symbol {symbol!r} not found in {path}")

        start = min([d.lineno for d in getattr(target, "decorator_list", [])] or [target.lineno])
        end = target.end_lineno or target.lineno
        lines = source.splitlines()
        indent = lines[start - 1][: len(lines[start - 1]) - len(lines[start - 1].lstrip())]
        replacement_lines = replacement.splitlines()
        if indent and replacement_lines and not replacement_lines[0].startswith(indent):
            replacement_lines = [indent + line for line in replacement_lines]
        patched_source = "\n".join(lines[: start - 1] + replacement_lines + lines[end:])
        if source.endswith("\n"):
            patched_source += "\n"

        syntax_check = self._syntax_check(abs_path, patched_source)
        if syntax_check["status"] == "failed":
            raise OumvError(
                SYNTAX_ERROR,
                f"SYNTAX_ERROR: symbol edit rejected ({syntax_check['tool']}): {syntax_check['error']}",
                data={"syntaxCheck": syntax_check},
            )

        backup_path = None
        try:
            backup_dir = os.path.join(self._project_root_abs(), ".tokensafe", "backups")
            os.makedirs(backup_dir, exist_ok=True)
            digest = hashlib.sha256(current_bytes).hexdigest()[:8]
            backup_path = os.path.join(backup_dir, f"{digest}_{os.path.basename(abs_path)}.bak")
            with open(backup_path, "wb") as f:
                f.write(current_bytes)
        except Exception:
            backup_path = None

        with open(abs_path, "w", encoding="utf-8") as f:
            f.write(patched_source)
        new_version = "sha256:" + hashlib.sha256(patched_source.encode("utf-8")).hexdigest()
        result: Dict[str, Any] = {
            "status": "APPLIED",
            "path": path,
            "symbol": symbol,
            "newVersion": new_version,
            "syntaxCheck": syntax_check,
        }
        if backup_path:
            result["backupPath"] = backup_path
        return result

    # ---- OCC source editing (real file edits, no fabricated versions) ----

    def _project_root_abs(self) -> str:
        return os.path.abspath(self.discovery.project_root or os.getcwd())

    def _resolve_project_file(self, path: str) -> Optional[str]:
        """Resolves a project-relative path inside the project root boundary."""
        if not path:
            return None
        root_abs = self._project_root_abs()
        abs_path = os.path.normpath(os.path.join(root_abs, path))
        if abs_path == root_abs or abs_path.startswith(root_abs + os.sep):
            return abs_path
        return None

    def _file_version(self, path: Optional[str]) -> Optional[str]:
        abs_path = self._resolve_project_file(path or "")
        if not abs_path or not os.path.isfile(abs_path):
            return None
        with open(abs_path, "rb") as f:
            return "sha256:" + hashlib.sha256(f.read()).hexdigest()

    @staticmethod
    def _syntax_check(abs_path: str, content: str) -> Dict[str, Any]:
        """Pre-flight syntax validation; uncheckable languages are honestly 'skipped'."""
        ext = os.path.splitext(abs_path)[1].lower()
        if ext == ".py":
            try:
                ast.parse(content)
                return {"status": "passed", "tool": "python-ast"}
            except SyntaxError as e:
                return {"status": "failed", "tool": "python-ast", "error": f"line {e.lineno}: {e.msg}"}
        if ext in (".js", ".mjs", ".cjs"):
            node_bin = shutil.which("node")
            if not node_bin:
                return {"status": "skipped", "tool": "node-check", "error": "node not found on PATH"}
            tmp_fd, tmp_path = tempfile.mkstemp(suffix=ext, prefix="tokensafe_syntax_")
            try:
                with os.fdopen(tmp_fd, "w", encoding="utf-8") as f:
                    f.write(content)
                proc = subprocess.run(
                    [node_bin, "--check", tmp_path],
                    capture_output=True, text=True, timeout=3.0,
                )
                if proc.returncode == 0:
                    return {"status": "passed", "tool": "node --check"}
                err = (proc.stderr or proc.stdout or "").strip().splitlines()
                return {"status": "failed", "tool": "node --check", "error": err[0] if err else "syntax check failed"}
            except Exception as e:
                return {"status": "skipped", "tool": "node-check", "error": str(e)}
            finally:
                try:
                    os.unlink(tmp_path)
                except OSError:
                    pass
        return {"status": "skipped", "tool": "none", "error": f"no syntax checker for {ext or 'unknown extension'}"}

    def _apply_source_edit(
        self,
        path: str,
        symbol: Optional[str],
        replacement: str,
        expected_version: Optional[str],
    ) -> Dict[str, Any]:
        """Real OCC workspace edit: boundary check, version check, syntax pre-flight, backup, write."""
        if not path or not isinstance(replacement, str):
            raise OumvError(VALIDATION_FAILED, "VALIDATION_FAILED: 'path' and 'replacement' are required for source edits")

        root_abs = self._project_root_abs()
        abs_path = self._resolve_project_file(path)
        if abs_path is None:
            raise OumvError(SECURITY_VIOLATION, f"SECURITY_VIOLATION: path traversal blocked: {path}")
        if not os.path.isfile(abs_path):
            raise OumvError(RESOURCE_NOT_FOUND, f"RESOURCE_NOT_FOUND: {path}")

        with open(abs_path, "rb") as f:
            current_bytes = f.read()
        current_version = "sha256:" + hashlib.sha256(current_bytes).hexdigest()

        # Mandatory OCC: an edit of an existing file must name the version it was
        # based on, otherwise a blind write can clobber a concurrent change.
        if not expected_version:
            raise OumvError(
                VALIDATION_FAILED,
                "VALIDATION_FAILED: expectedVersion is required to edit an existing file — "
                "call oumv/getSource (or observe L3) and pass its version",
                data={"path": path, "current": current_version},
            )
        if expected_version != current_version:
            raise OumvError(
                CONFLICT_DETECTED,
                f"CONFLICT_DETECTED: OCC version mismatch: expected {expected_version}, current is {current_version}",
            )

        syntax_check = self._syntax_check(abs_path, replacement)
        if syntax_check["status"] == "failed":
            raise OumvError(
                SYNTAX_ERROR,
                f"SYNTAX_ERROR: edit rejected ({syntax_check['tool']}): {syntax_check['error']}",
                data={"syntaxCheck": syntax_check},
            )

        backup_path = None
        try:
            backup_dir = os.path.join(root_abs, ".tokensafe", "backups")
            os.makedirs(backup_dir, exist_ok=True)
            digest = hashlib.sha256(current_bytes).hexdigest()[:8]
            backup_path = os.path.join(backup_dir, f"{digest}_{os.path.basename(abs_path)}.bak")
            with open(backup_path, "wb") as f:
                f.write(current_bytes)
        except Exception:
            backup_path = None

        with open(abs_path, "w", encoding="utf-8") as f:
            f.write(replacement)

        new_version = "sha256:" + hashlib.sha256(replacement.encode("utf-8")).hexdigest()
        result = {
            "status": "APPLIED",
            "path": path,
            "symbol": symbol,
            "version": new_version,
            "newVersion": new_version,
            "mode": "RUNTIME_DIRECT_EDIT",
            "syntaxCheck": syntax_check,
        }
        if backup_path:
            result["backupPath"] = backup_path
            result["rollbackHint"] = f"Previous content backed up at {backup_path}"
        return result

    async def handle_oumv_verify(self, **kwargs) -> Dict[str, Any]:
        """Verifies state/DOM assertions or simulated mutation diffs."""
        steps = kwargs.get("steps") or []
        if isinstance(steps, list) and steps:
            results: List[Dict[str, Any]] = []
            total = 0.0
            for step in steps:
                step = step or {}
                target = str(step.get("target") or "")
                parts = target.strip().split(" ", 1)
                if len(parts) == 2:
                    method, path = parts[0].upper(), parts[1]
                else:
                    method, path = "POST", parts[0] if parts else target
                sim = await self.handle_mutation_simulate(
                    action={"type": "http_request", "path": path, "method": method, "body": step.get("payload")}
                )
                elapsed = float(sim.execution_time_ms or 0.0)
                total += elapsed
                results.append(
                    {
                        "target": target,
                        "statusCode": int(sim.status_code or 0),
                        "status": "passed" if sim.success else "failed",
                        "responseBody": sim.response,
                    }
                )
            last = results[-1]
            return {
                "status": "passed",
                "target": kwargs.get("target") or last["target"],
                "statusCode": last["statusCode"],
                "steps": results,
                "diff": {"stepCount": len(results), "status": last["statusCode"], "responseBody": last.get("responseBody")},
                "timings": {
                    "t_action_ms": total,
                    "t_schema_ms": 0.0,
                    "t_propagation_ms": 0.0,
                    "t_dom_ms": 0.0,
                    "t_total_ms": total,
                },
                "durationMs": total,
                "scope": {
                    "checked": ["ordered multi-step ASGI dispatch against one app instance"],
                    "notExecuted": ["external processes", "real network", "persistent database state"],
                    "notSupported": ["browser/DOM assertions"],
                },
            }
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
            elapsed = float(sim.execution_time_ms or 0.0)
            return {
                "status": "passed" if sim.success else "failed",
                "target": target,
                "diff": sim.diff.model_dump() if sim.diff else {},
                "timings": {
                    "t_action_ms": elapsed,
                    "t_schema_ms": 0.0,
                    "t_propagation_ms": 0.0,
                    "t_dom_ms": 0.0,
                    "t_total_ms": elapsed,
                },
                "durationMs": elapsed,
                "scope": {
                    "checked": ["in-process ASGI dispatch against the live app"],
                    "notExecuted": ["external processes", "real network", "persistent database state"],
                    "notSupported": ["browser/DOM assertions"],
                },
                "statusCode": sim.status_code,
                "response": sim.response,
                "rolledBack": sim.rolled_back,
                "rollbackStatus": sim.rollback_status,
            }
        elif "action" in kwargs and isinstance(kwargs["action"], dict):
            sim = await self.handle_mutation_simulate(**kwargs)
            elapsed = float(sim.execution_time_ms or 0.0)
            return {
                "status": "passed" if sim.success else "failed",
                "target": target,
                "diff": sim.diff.model_dump() if sim.diff else {},
                "timings": {
                    "t_action_ms": elapsed,
                    "t_schema_ms": 0.0,
                    "t_propagation_ms": 0.0,
                    "t_dom_ms": 0.0,
                    "t_total_ms": elapsed,
                },
                "durationMs": elapsed,
                "scope": {
                    "checked": ["in-process ASGI dispatch against the live app"],
                    "notExecuted": ["external processes", "real network", "persistent database state"],
                    "notSupported": ["browser/DOM assertions"],
                },
                "statusCode": sim.status_code,
                "response": sim.response,
                "rolledBack": sim.rolled_back,
                "rollbackStatus": sim.rollback_status,
            }
        # Nothing to check is not a pass (spec §4.4): ask for a target/action instead.
        raise OumvError(
            VALIDATION_FAILED,
            "VALIDATION_FAILED: verify requires a target, an action, or assertions",
        )

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

        # Evidence-based rollback reporting: only claim rolled_back when a real
        # ROLLBACK was observed on the engine during the dry-run window.
        if self.db_sandbox.has_engine():
            rollback_verified = self.db_sandbox.rollback_observed()
            rollback_status = "verified" if rollback_verified else "unverified"
            if not rollback_verified:
                warnings.append(
                    "Rollback unverified: no ROLLBACK observed on the DB engine during the dry-run window."
                )
        else:
            rollback_verified = False
            rollback_status = "not_applicable"

        return MutationSimulateResult(
            success=status_code < 400,
            status_code=status_code,
            response=sim_response,
            diff=diff,
            db_queries=db_queries,
            warnings=warnings,
            rolled_back=rollback_verified,
            rollback_status=rollback_status,
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
