using System.Diagnostics;
using System.Text;
using System.Text.Json.Nodes;

namespace TokenSafe.Oumv;

/// <summary>
/// The OUMV wire contract for one ASP.NET Core app: handshake, observe L0-L4,
/// understand, getSource, applyEdit (single and atomic batch) and verify with the
/// EF Core rollback sandbox. Transports live in <see cref="OumvMiddleware"/>.
/// </summary>
public sealed class OumvRuntime
{
    public const string ProtocolVersion = "1.6.0";

    private readonly OumvOptions _options;
    private readonly SourceEditor _editor;
    private readonly EndpointCatalog _catalog;
    private readonly IOumvDbSandbox _sandbox;
    private readonly IServiceProvider _services;
    private readonly TimeProvider _time;
    private readonly List<OumvTrace> _traces = new();
    // §3 cooperative cancellation: requestId -> the token its handler observes.
    private readonly System.Collections.Concurrent.ConcurrentDictionary<string, CancellationTokenSource> _inFlight = new();
    // §3 idempotency (wire 1.6.0): sessionId:idempotencyKey -> the first final result.
    private readonly System.Collections.Concurrent.ConcurrentDictionary<string, (DateTimeOffset StoredAt, JsonNode Result)> _idempotency = new(StringComparer.Ordinal);
    private volatile string _lastRequestId = string.Empty;
    private readonly object _traceGate = new();
    private readonly DateTimeOffset _started;
    // §8.5 closed registry: the writing methods are absent, not guarded. Built once so
    // no handler can be reached later and no setting can widen it at request time.
    private readonly HashSet<string> _registeredMethods;

    public OumvRuntime(
        OumvOptions options,
        SourceEditor editor,
        EndpointCatalog catalog,
        IOumvDbSandbox sandbox,
        IServiceProvider services,
        TimeProvider? time = null)
    {
        _options = options;
        _editor = editor;
        _catalog = catalog;
        _sandbox = sandbox;
        _services = services;
        _time = time ?? TimeProvider.System;
        _started = _time.GetUtcNow();
        Sessions = new OumvSessions(_time);
        Events = new OumvEventHub();
        _registeredMethods = new HashSet<string>(StringComparer.Ordinal)
        {
            "oumv/handshake", "oumv/disconnect", "oumv/observe", "oumv/understand",
            "oumv/getSource", "oumv/index", "oumv/cancel", "oumv/query",
        };
        if (!IsReadOnly)
        {
            _registeredMethods.Add("oumv/applyEdit");
            _registeredMethods.Add("oumv/previewEdit");
            _registeredMethods.Add("oumv/verify");
            _registeredMethods.Add("oumv/build");
        }
    }

    /// <summary>
    /// Spec §8.5: a read-only deployment registers no writing method at all. The
    /// legacy `EnableMutations = false` switch is the same shape, so it also drops
    /// `verify`/`build`, which execute code and commands.
    /// </summary>
    public bool IsReadOnly => _options.ReadOnly || !_options.EnableMutations;

    public OumvSessions Sessions { get; }

    public OumvEventHub Events { get; }

    public OumvOptions Options => _options;

    public SourceEditor Editor => _editor;

    public EndpointCatalog Catalog => _catalog;

    public bool SandboxAdvertised => _sandbox.HasEngine;

    /* ------------------------------------------------------------------ traces */

    public void RecordTrace(string method, string target, int statusCode, double durationMs, string? error = null)
    {
        var trace = new OumvTrace(method, target, target, method, statusCode, Math.Round(durationMs, 3),
            _time.GetUtcNow().ToString("yyyy-MM-ddTHH:mm:ssZ"), Error: error);
        lock (_traceGate)
        {
            _traces.Add(trace);
            if (_traces.Count > _options.TraceLimit) _traces.RemoveAt(0);
        }
        var payload = OumvJson.Obj(
            ("method", trace.Method), ("path", trace.Path), ("route", trace.Route), ("handler", trace.Handler),
            ("statusCode", trace.StatusCode), ("durationMs", trace.DurationMs), ("timestamp", trace.Timestamp));
        if (error is not null) payload["error"] = error;
        Events.Publish("trace", payload);
    }

    public IReadOnlyList<OumvTrace> Traces
    {
        get { lock (_traceGate) return _traces.ToArray(); }
    }

    private void PublishState(string path, string operation, string? sessionId)
    {
        var payload = OumvJson.Obj(("path", path), ("operation", operation));
        if (!string.IsNullOrEmpty(sessionId)) payload["sessionId"] = sessionId;
        Events.Publish("state", payload);
    }

    /* --------------------------------------------------------------- json-rpc */

    /// <summary>Handles a single request or a JSON-RPC batch; notifications get no reply.</summary>
    public async Task<JsonNode?> HandleAsync(JsonNode? payload, CancellationToken cancellationToken = default)
    {
        if (payload is JsonArray batch)
        {
            var responses = new JsonArray();
            foreach (var item in batch)
            {
                if (item is not JsonObject request) continue;
                var response = await HandleSingleAsync(request, cancellationToken);
                if (response is not null) responses.Add(response);
            }
            return responses.Count == 0 ? null : responses;
        }

        return payload is JsonObject single ? await HandleSingleAsync(single, cancellationToken) : null;
    }

    private async Task<JsonObject?> HandleSingleAsync(JsonObject request, CancellationToken cancellationToken)
    {
        var id = request["id"]?.DeepClone();
        var method = OumvJson.Str(request["method"]) ?? string.Empty;
        var isNotification = id is null || (id is JsonValue v && v.TryGetValue<string>(out var s) && s.Length == 0);
        var paramsNode = request["params"] as JsonObject ?? new JsonObject();
        var stopwatch = Stopwatch.StartNew();

        try
        {
            // The registry is consulted first, before session validation: a method the
            // runtime does not serve is METHOD_NOT_FOUND whatever the session state is
            // (§3.1 rule 5). Answering -32011 here would leak that the method exists.
            if (!_registeredMethods.Contains(method))
                throw new OumvException(OumvErrorCode.MethodNotFound, $"METHOD_NOT_FOUND: {method}");

            if (method != "oumv/handshake" && method != "oumv/disconnect")
            {
                var sessionId = OumvJson.Str(paramsNode["sessionId"]);
                if (!Sessions.Validate(sessionId))
                    throw OumvException.Validation(
                            "VALIDATION_FAILED: unknown or expired sessionId — re-run oumv/handshake",
                            new Dictionary<string, object?> { ["reason"] = "session_expired", ["sessionId"] = sessionId });
            }

            var requestId = OumvJson.Str(paramsNode["requestId"])?.Trim();
            var effectiveToken = cancellationToken;
            CancellationTokenSource? requestCts = null;
            if (!string.IsNullOrEmpty(requestId) && method != "oumv/cancel")
            {
                requestCts = CancellationTokenSource.CreateLinkedTokenSource(cancellationToken);
                _inFlight[requestId] = requestCts;
                _lastRequestId = requestId;
                effectiveToken = requestCts.Token;
            }

            JsonNode? result;
            try
            {
            result = method switch
            {
                "oumv/handshake" => Handshake(paramsNode),
                "oumv/disconnect" => Disconnect(paramsNode),
                "oumv/observe" => Observe(paramsNode),
                "oumv/understand" => await UnderstandAsync(paramsNode, cancellationToken),
                "oumv/getSource" => GetSource(paramsNode),
                "oumv/applyEdit" => ApplyEditIdempotent(paramsNode),
                "oumv/previewEdit" => PreviewEditIdempotent(paramsNode),
                "oumv/index" => IndexSymbols(paramsNode),
                "oumv/build" => await BuildAsync(paramsNode, cancellationToken),
                "oumv/cancel" => Cancel(paramsNode),
                "oumv/query" => await QueryAsync(paramsNode, cancellationToken),
                "oumv/verify" => await VerifyAsync(paramsNode, effectiveToken),
                _ => throw new OumvException(OumvErrorCode.MethodNotFound, $"METHOD_NOT_FOUND: {method}"),
            };
            }
            finally
            {
                if (requestCts is not null && requestId is not null)
                {
                    _inFlight.TryRemove(requestId, out _);
                    requestCts.Dispose();
                }
            }

            stopwatch.Stop();
            RecordTrace(method, TargetOf(paramsNode), 200, stopwatch.Elapsed.TotalMilliseconds);
            return isNotification ? null : Envelope(id, result);
        }
        catch (OumvException error)
        {
            stopwatch.Stop();
            RecordTrace(method, TargetOf(paramsNode), 500, stopwatch.Elapsed.TotalMilliseconds, error.Message);
            return isNotification ? null : ErrorEnvelope(id, error);
        }
        catch (Exception unexpected)
        {
            stopwatch.Stop();
            RecordTrace(method, TargetOf(paramsNode), 500, stopwatch.Elapsed.TotalMilliseconds, unexpected.Message);
            return isNotification
                ? null
                : ErrorEnvelope(id, OumvException.Internal($"INTERNAL_ERROR: {unexpected.Message}"));
        }
    }

    private static string TargetOf(JsonObject parameters) =>
        OumvJson.Str(parameters["target"]) ?? OumvJson.Str(parameters["path"]) ?? string.Empty;

    private static JsonObject Envelope(JsonNode? id, JsonNode? result) =>
        new() { ["jsonrpc"] = "2.0", ["id"] = id?.DeepClone(), ["result"] = result };

    private static JsonObject ErrorEnvelope(JsonNode? id, OumvException error)
    {
        var data = new JsonObject();
        foreach (var (key, value) in error.Details) data[key] = OumvJson.ToNode(value);
        return new JsonObject
        {
            ["jsonrpc"] = "2.0",
            ["id"] = id?.DeepClone(),
            ["error"] = new JsonObject
            {
                ["code"] = error.Code,
                ["message"] = error.Message,
                ["data"] = data,
            },
        };
    }

    /* --------------------------------------------------------------- handshake */

    private JsonObject Handshake(JsonObject parameters)
    {
        var requestedVersion = OumvJson.Str(parameters["protocolVersion"])?.Trim();
        if (!string.IsNullOrEmpty(requestedVersion))
        {
            var parts = requestedVersion.Split('.');
            if (parts.Length < 2 || !int.TryParse(parts[0], out var major) || !int.TryParse(parts[1], out var minor) ||
                major != 1 || minor > 6)
                throw OumvException.Unsupported(
                    $"UNSUPPORTED_CAPABILITY: protocol version {requestedVersion} (this runtime serves {ProtocolVersion})",
                    new Dictionary<string, object?> { ["reason"] = "unsupported_version" });
        }

        var requestedTtl = OumvJson.Int(parameters["sessionTtlSeconds"]);
        var ttl = OumvSessions.Clamp(requestedTtl ?? _options.SessionTtlSeconds);
        var sessionId = Sessions.Create(ttl);

        return OumvJson.Obj(
            ("protocolVersion", ProtocolVersion),
            ("profile", "backend"),
            ("sessionId", sessionId),
            ("ttlSeconds", ttl),
            ("runtimeInfo", OumvJson.Obj(("name", _options.Name), ("version", _options.Version), ("framework", "ASP.NET Core"))),
            ("capabilities", Capabilities()));
    }

    private JsonObject Capabilities()
    {
        var capabilities = OumvJson.Obj(
            ("observe", OumvJson.Obj(("levels", new[] { "L0", "L1", "L2", "L3", "L4" }), ("formats", new[] { "json" }))),
            ("understand", OumvJson.Obj(("causality", true), ("impactAnalysis", true))),
            ("index", OumvJson.Obj(("symbols", true))),
            // §14.2 is opt-in per app: without a DbContext there is no read-only door.
            ("observeQuery", OumvJson.Obj(("readOnly", _sandbox.HasEngine))),
            ("sessions", OumvJson.Obj(("ttlSeconds", true), ("cancel", true), ("idempotency", true))),
            ("transports", new[] { "http", "sse", "ws" }));

        // §8.5: a read-only deployment advertises neither pillar and says so explicitly,
        // so a client never mistakes absence for an older adapter that lacks the method.
        if (IsReadOnly)
        {
            capabilities["readOnly"] = true;
            return capabilities;
        }

        capabilities["mutate"] = OumvJson.Obj(("occ", true), ("syntaxGuard", true), ("multiFile", true), ("preview", true));
        capabilities["verify"] = OumvJson.Obj(("multiMetric", true), ("sandboxRollback", _sandbox.HasEngine), ("build", true));
        return capabilities;
    }

    private JsonNode Disconnect(JsonObject parameters)
    {
        var sessionId = OumvJson.Str(parameters["sessionId"]);
        var closed = Sessions.Delete(sessionId);
        // §3: replay entries are session-scoped, so they die with the session.
        if (!string.IsNullOrEmpty(sessionId))
        {
            foreach (var entry in _idempotency)
            {
                if (entry.Key.StartsWith(sessionId + ":", StringComparison.Ordinal))
                    _idempotency.TryRemove(entry.Key, out _);
            }
        }
        return OumvJson.Obj(("closed", closed));
    }

    /* -------------------------------------------------------- result versioning */

    private static readonly HashSet<string> DigestSkipDirectories = new(StringComparer.Ordinal)
    {
        ".git", "node_modules", ".venv", "venv", "__pycache__", ".tokensafe", ".oumv",
        "dist", "build", "bin", "obj", ".pytest_cache", "target", ".vs",
    };

    // Process-local runtime artifacts, not source. A SQLite database inside the
    // workspace rewrites its journal on every query, so counting it would make the
    // §4.1 token flap between two consecutive calls and the handshake useless.
    private static readonly string[] DigestSkipSuffixes =
    {
        ".db", ".db-wal", ".db-shm", ".sqlite", ".sqlite3", ".sqlite-wal", ".sqlite-shm", ".log",
    };

    /// <summary>
    /// §4.1 result versioning (wire 1.6.0): a sha256 over the workspace inventory
    /// (relative path, size, mtime). Any touched file changes it, so an agent learns
    /// exactly when foreign edits made a cached observe/understand answer stale.
    /// </summary>
    public string WorkspaceDigest()
    {
        var entries = new List<string>();
        var root = _editor.Root;
        if (Directory.Exists(root))
        {
            var pending = new Stack<string>();
            pending.Push(root);
            while (pending.Count > 0)
            {
                var dir = pending.Pop();
                IEnumerable<string> children;
                try
                {
                    children = Directory.EnumerateFileSystemEntries(dir);
                }
                catch (Exception)
                {
                    continue;
                }
                foreach (var child in children)
                {
                    if (Directory.Exists(child))
                    {
                        var name = Path.GetFileName(child);
                        if (!DigestSkipDirectories.Contains(name)) pending.Push(child);
                        continue;
                    }
                    var lowerName = Path.GetFileName(child).ToLowerInvariant();
                    if (DigestSkipSuffixes.Any(suffix => lowerName.EndsWith(suffix, StringComparison.Ordinal))) continue;
                    try
                    {
                        var info = new FileInfo(child);
                        var relative = Path.GetRelativePath(root, child).Replace('\\', '/');
                        entries.Add($"{relative}:{info.Length}:{info.LastWriteTimeUtc.Ticks}");
                    }
                    catch (Exception)
                    {
                        // An unreadable entry simply does not contribute to the digest.
                    }
                }
            }
        }
        entries.Sort(StringComparer.Ordinal);
        return OumvJson.Sha256(string.Join("\n", entries));
    }

    /// <summary>
    /// Applies the §4.1 ifVersion handshake: a matching token answers the short
    /// envelope through this method's return value; a stale one throws -32010.
    /// Returns null when the caller sent no token (the caller then serves the full payload).
    /// </summary>
    private JsonObject? ShortCircuitIfVersion(JsonObject parameters, Func<string, JsonObject> shortEnvelope)
    {
        var token = OumvJson.Str(parameters["ifVersion"])?.Trim();
        if (string.IsNullOrEmpty(token)) return null;
        var digest = WorkspaceDigest();
        if (!string.Equals(token, digest, StringComparison.Ordinal))
        {
            throw OumvException.Stale(
                $"STALE_RESOURCE_VERSION: the workspace changed since the caller's snapshot (expected {token}, current is {digest}) — refetch without ifVersion",
                new Dictionary<string, object?>
                {
                    ["reason"] = "stale_version",
                    ["current"] = digest,
                    ["expected"] = token,
                });
        }
        return shortEnvelope(digest);
    }

    /* ----------------------------------------------------------------- observe */

    private JsonNode Observe(JsonObject parameters)
    {
        var level = (OumvJson.Str(parameters["level"]) ?? "L0").ToUpperInvariant();
        if (level is not ("L0" or "L1" or "L2" or "L3" or "L4"))
            throw OumvException.Unsupported($"UNSUPPORTED_CAPABILITY: unknown observe level {level}",
                new Dictionary<string, object?> { ["reason"] = "unsupported_level" });

        var shortAnswer = ShortCircuitIfVersion(parameters, digest => OumvJson.Obj(
            ("level", level), ("format", "json"), ("unchanged", true), ("workspaceVersion", digest)));
        if (shortAnswer is not null) return shortAnswer;

        var result = ObserveCore(parameters, level);
        var digestToken = WorkspaceDigest();
        if (result is JsonObject payload) payload["workspaceVersion"] = digestToken;
        return result;
    }

    private JsonNode ObserveCore(JsonObject parameters, string level)
    {
        switch (level)
        {
            case "L0":
            {
                var process = Process.GetCurrentProcess();
                return OumvJson.Obj(
                    ("level", "L0"), ("format", "json"), ("tokenEstimate", 30),
                    ("data", OumvJson.Obj(
                        ("status", "HEALTHY"),
                        ("uptimeSeconds", (int)(_time.GetUtcNow() - _started).TotalSeconds),
                        ("activeRoutes", _catalog.All().Count),
                        ("errorCount1m", 0),
                        ("memoryAllocMB", Math.Round(process.WorkingSet64 / 1024d / 1024d)))));
            }

            case "L1":
            {
                var entries = _catalog.All();
                var limit = Math.Clamp(OumvJson.Int(parameters["limit"]) ?? 50, 1, 200);
                var offset = Math.Max(0, int.TryParse(OumvJson.Str(parameters["cursor"]), out var cursor) ? cursor : 0);
                var page = entries.Skip(offset).Take(limit).ToArray();
                var data = new JsonObject
                {
                    ["endpoints"] = new JsonArray(page.Select(entry => (JsonNode)OumvJson.Obj(
                        ("method", entry.Method), ("path", entry.Path), ("handlerName", entry.HandlerName))).ToArray()),
                };
                if (offset + page.Length < entries.Count)
                {
                    data["hasMore"] = true;
                    data["nextCursor"] = (offset + page.Length).ToString();
                }
                return OumvJson.Obj(("level", "L1"), ("format", "json"), ("data", data),
                    ("tokenEstimate", 30 + page.Length * 15));
            }

            case "L2":
            {
                var target = OumvJson.Str(parameters["target"]) ?? throw Invalid("target is required for L2");
                var entry = _catalog.Match(target)
                    ?? throw OumvException.NotFound($"RESOURCE_NOT_FOUND: no endpoint matches {target}");
                return OumvJson.Obj(
                    ("level", "L2"), ("format", "json"),
                    ("data", OumvJson.Obj(
                        ("target", target),
                        ("method", entry.Method),
                        ("path", entry.Path),
                        ("handlerName", entry.HandlerName),
                        ("middlewares", Array.Empty<string>()),
                        ("note", "route contract from live endpoint metadata; request/response DTOs are not reflectable — use observe L3 for handler source"),
                        ("evidence", OumvJson.Obj(("provenance", "runtime"), ("sourceAdapter", _options.Name), ("confidence", "confirmed"))))));
            }

            case "L3":
            {
                var target = OumvJson.Str(parameters["target"]) ?? throw Invalid("target is required for L3");
                var source = GetSource(new JsonObject { ["path"] = target });
                return OumvJson.Obj(("level", "L3"), ("format", "json"), ("data", source.DeepClone()));
            }

            case "L4":
            {
                var traces = Traces;
                return OumvJson.Obj(
                    ("level", "L4"), ("format", "json"),
                    ("data", OumvJson.Obj(
                        ("traces", new JsonArray(traces.Select(trace => (JsonNode)OumvJson.Obj(
                            ("method", trace.Method), ("path", trace.Path), ("route", trace.Route),
                            ("handler", trace.Handler), ("statusCode", trace.StatusCode),
                            ("durationMs", trace.DurationMs), ("timestamp", trace.Timestamp))).ToArray())),
                        ("traceCount", traces.Count))));
            }

            default:
                throw OumvException.Unsupported($"UNSUPPORTED_CAPABILITY: unknown observe level {level}",
                    new Dictionary<string, object?> { ["reason"] = "unsupported_level" });
        }
    }

    private static OumvException Invalid(string message) =>
        new(OumvErrorCode.InvalidParams, $"INVALID_PARAMS: {message}");

    /* -------------------------------------------------------------- understand */

    private Task<JsonNode> UnderstandAsync(JsonObject parameters, CancellationToken cancellationToken)
    {
        var query = (OumvJson.Str(parameters["query"]) ?? string.Empty).ToLowerInvariant();
        if (query is not ("impact" or "causality"))
            throw OumvException.Unsupported($"UNSUPPORTED_CAPABILITY: unknown understand query {query}",
                new Dictionary<string, object?> { ["reason"] = "unsupported_query" });

        // §4.1: understand honours the same freshness handshake as observe.
        var shortAnswer = ShortCircuitIfVersion(parameters, digest => OumvJson.Obj(
            ("query", query), ("unchanged", true), ("workspaceVersion", digest)));
        if (shortAnswer is not null) return Task.FromResult<JsonNode>(shortAnswer);

        var result = UnderstandCore(parameters, query);
        if (result is JsonObject payload) payload["workspaceVersion"] = WorkspaceDigest();
        return Task.FromResult(result);
    }

    private JsonNode UnderstandCore(JsonObject parameters, string query)
    {
        var target = OumvJson.Str(parameters["target"]) ?? string.Empty;

        switch (query)
        {
            case "impact":
            {
                if (target.Length == 0) throw Invalid("target is required for impact");
                var entry = _catalog.Match(target);
                var affected = entry is null
                    ? Array.Empty<string>()
                    : _catalog.All()
                        .Where(other => other.HandlerName == entry.HandlerName && other.Path != entry.Path)
                        .Select(other => $"{other.Method} {other.Path}")
                        .ToArray();

                var impact = new JsonObject
                {
                    ["target"] = target,
                    ["affectedSymbols"] = new JsonArray(affected.Select(item => (JsonNode)JsonValue.Create(item)!).ToArray()),
                    ["breakingRisk"] = affected.Length == 0 ? "LOW" : "MEDIUM",
                    ["summary"] = entry is null
                        ? "no registered endpoint matches the target"
                        : $"{entry.HandlerName} backs {entry.Method} {entry.Path}; {affected.Length} sibling route(s) share the handler",
                    ["observed"] = false,
                    ["evidence"] = new JsonObject
                    {
                        ["provenance"] = "runtime",
                        ["sourceAdapter"] = _options.Name,
                        ["confidence"] = "probable",
                    },
                };

                return new JsonObject
                {
                    ["query"] = "impact",
                    ["target"] = target,
                    ["impact"] = impact,
                };
            }

            case "causality":
            {
                if (target.Length == 0) throw Invalid("target is required for causality");
                var (_, path) = EndpointCatalog.SplitTarget(target);
                var matches = Traces.Where(trace =>
                        trace.Method.StartsWith("oumv/", StringComparison.Ordinal) &&
                        (trace.Route.Contains(target, StringComparison.OrdinalIgnoreCase) ||
                         trace.Route.Contains(path, StringComparison.OrdinalIgnoreCase)))
                    .ToArray();
                var observed = matches.Length > 0;

                var causality = new JsonObject
                {
                    ["target"] = target,
                    ["observed"] = observed,
                    ["traceCount"] = matches.Length,
                    ["traceChain"] = new JsonArray(matches.Select(match => (JsonNode)JsonValue.Create(match.Method)!).ToArray()),
                    ["rootCause"] = observed ? matches[^1].Method : null,
                    ["note"] = observed
                        ? "observed from recorded executions"
                        : "no execution recorded for this target yet — the protocol never fabricates causality",
                    ["evidence"] = new JsonObject
                    {
                        ["provenance"] = "runtime",
                        ["sourceAdapter"] = _options.Name,
                        ["confidence"] = observed ? "confirmed" : "unknown",
                    },
                };
                if (matches.Length > 0)
                {
                    causality["statusCode"] = matches[^1].StatusCode;
                    causality["latencyMs"] = matches[^1].DurationMs;
                }

                return new JsonObject
                {
                    ["query"] = "causality",
                    ["target"] = target,
                    ["causality"] = causality,
                };
            }

            default:
                throw OumvException.Unsupported($"UNSUPPORTED_CAPABILITY: unknown understand query {query}",
                    new Dictionary<string, object?> { ["reason"] = "unsupported_query" });
        }
    }

    /* --------------------------------------------------------------- getSource */

    private JsonObject GetSource(JsonObject parameters)
    {
        var path = OumvJson.Str(parameters["path"]) ?? throw Invalid("path is required");
        var absolute = _editor.Resolve(path);
        if (!File.Exists(absolute)) throw OumvException.NotFound($"RESOURCE_NOT_FOUND: {path}");

        var bytes = File.ReadAllBytes(absolute);
        var version = SourceEditor.Version(bytes);
        var relative = _editor.Relative(absolute);

        var ifVersion = OumvJson.Str(parameters["ifVersion"]);
        if (!string.IsNullOrEmpty(ifVersion) && ifVersion == version)
            return OumvJson.Obj(("path", relative), ("version", version), ("unchanged", true));

        var text = Encoding.UTF8.GetString(bytes);
        var symbol = OumvJson.Str(parameters["symbol"]);
        int? startLine = null;
        int? endLine = null;

        if (!string.IsNullOrWhiteSpace(symbol))
        {
            text = _editor.ExtractSymbol(absolute, text, symbol);
        }
        else
        {
            var requestedStart = OumvJson.Int(parameters["startLine"]);
            var requestedEnd = OumvJson.Int(parameters["endLine"]);
            if (requestedStart is not null || requestedEnd is not null)
            {
                var lines = text.Split('\n');
                startLine = Math.Clamp(requestedStart ?? 1, 1, Math.Max(1, lines.Length));
                endLine = Math.Clamp(requestedEnd ?? lines.Length, startLine.Value, Math.Max(1, lines.Length));
                text = string.Join('\n', lines[(startLine.Value - 1)..endLine.Value]);
            }
        }

        var data = new JsonObject
        {
            ["path"] = relative,
            ["version"] = version,
            ["source"] = text,
        };
        if (!string.IsNullOrWhiteSpace(symbol)) data["symbol"] = symbol;
        if (startLine is not null) data["startLine"] = startLine;
        if (endLine is not null) data["endLine"] = endLine;
        return data;
    }

    /* --------------------------------------------------------------- applyEdit */

    /// <summary>
    /// §3 idempotency: a retry carrying the same `idempotencyKey` gets the FIRST final
    /// result verbatim instead of a second write or a spurious -32001. Keyed by
    /// sessionId:key, pruned when the session goes away.
    /// </summary>
    private JsonObject ApplyEditIdempotent(JsonObject parameters) =>
        WithIdempotency(parameters, ApplyEdit);

    private JsonObject PreviewEditIdempotent(JsonObject parameters) =>
        WithIdempotency(parameters, PreviewEdit);

    private JsonObject WithIdempotency(JsonObject parameters, Func<JsonObject, JsonObject> invoke)
    {
        var key = OumvJson.Str(parameters["idempotencyKey"])?.Trim();
        if (string.IsNullOrEmpty(key)) return invoke(parameters);

        var sessionId = OumvJson.Str(parameters["sessionId"]) ?? "default";
        var cacheKey = sessionId + ":" + key;
        if (_idempotency.TryGetValue(cacheKey, out var cached))
            return (JsonObject)cached.Result.DeepClone();

        var result = invoke(parameters);
        PruneIdempotency();
        _idempotency[cacheKey] = (_time.GetUtcNow(), result.DeepClone());
        return result;
    }

    /// <summary>Drops replay entries whose session no longer exists, so the map cannot grow forever.</summary>
    private void PruneIdempotency()
    {
        if (_idempotency.Count < 256) return;
        foreach (var entry in _idempotency)
        {
            var separator = entry.Key.IndexOf(':');
            var sessionId = separator < 0 ? entry.Key : entry.Key[..separator];
            if (!Sessions.Validate(sessionId)) _idempotency.TryRemove(entry.Key, out _);
        }
    }

    private JsonObject ApplyEdit(JsonObject parameters)
    {
        if (!_options.EnableMutations)
            throw new OumvException(OumvErrorCode.PermissionDenied, "PERMISSION_DENIED: mutate capability is disabled by policy");

        if (parameters["edits"] is JsonArray edits)
            return ApplyEditBatch(parameters, edits);

        var result = ApplyEditSingle(parameters);
        PublishState(
            OumvJson.Str(result["path"]) ?? string.Empty,
            (OumvJson.Str(parameters["mode"]) ?? "replace").ToLowerInvariant(),
            OumvJson.Str(parameters["sessionId"]));
        return result;
    }

    private JsonObject ApplyEditSingle(JsonObject parameters)
    {
        var path = OumvJson.Str(parameters["path"]) ?? throw Invalid("path is required");
        var absolute = _editor.Resolve(path);
        var mode = (OumvJson.Str(parameters["mode"]) ?? "replace").ToLowerInvariant();
        var replacement = OumvJson.Str(parameters["replacement"]);
        var expectedVersion = OumvJson.Str(parameters["expectedVersion"])?.Trim();
        var symbol = OumvJson.Str(parameters["symbol"]);
        var renameTo = OumvJson.Str(parameters["renameTo"]);

        switch (mode)
        {
            case "create":
            {
                if (string.IsNullOrEmpty(replacement))
                    throw OumvException.Validation("replacement is required for mode=create",
                        new Dictionary<string, object?> { ["reason"] = "replacement_required" });
                if (!string.IsNullOrEmpty(expectedVersion))
                    throw OumvException.Validation("VALIDATION_FAILED: expectedVersion must be omitted for mode=create",
                        new Dictionary<string, object?> { ["reason"] = "create_forbids_version" });
                if (File.Exists(absolute) || Directory.Exists(absolute))
                    throw OumvException.Conflict("CONFLICT_DETECTED: file already exists; use mode=replace with its version",
                        new Dictionary<string, object?> { ["reason"] = "file_exists" });
                var syntax = _editor.Check(absolute, replacement);
                if (syntax.Status == "failed") throw SyntaxError(syntax);
                _editor.Write(absolute, replacement);
                return OumvJson.Obj(
                    ("status", "APPLIED"), ("path", _editor.Relative(absolute)), ("newVersion", OumvJson.Sha256(replacement)),
                    ("syntaxCheck", SyntaxJson(syntax)), ("created", true));
            }

            case "patch":
            {
                if (!File.Exists(absolute)) throw OumvException.NotFound($"RESOURCE_NOT_FOUND: {path}");
                var current = _editor.ReadText(absolute);
                var currentVersion = OumvJson.Sha256(current);
                // §4.3 guard order: splice + syntax-check the result (step 3) before
                // the expectedVersion rules (steps 4-5).
                var patched = _editor.ApplyHunks(current, parameters["hunks"] as JsonArray);
                var syntax = _editor.Check(absolute, patched);
                if (syntax.Status == "failed") throw SyntaxError(syntax);
                if (string.IsNullOrEmpty(expectedVersion))
                    throw OumvException.Validation(
                        "VALIDATION_FAILED: expectedVersion is required to edit an existing file — call oumv/getSource (or observe L3) and pass its version",
                        new Dictionary<string, object?> { ["reason"] = "expected_version_required", ["current"] = currentVersion });
                if (expectedVersion != currentVersion)
                    throw OumvException.Conflict($"version mismatch: expected {expectedVersion}, current is {currentVersion}",
                        new Dictionary<string, object?> { ["reason"] = "version_mismatch", ["expected"] = expectedVersion, ["current"] = currentVersion });
                var backup = _editor.Backup(absolute, Encoding.UTF8.GetBytes(current));
                _editor.Write(absolute, patched);
                return OumvJson.Obj(
                    ("status", "APPLIED"), ("path", _editor.Relative(absolute)), ("newVersion", OumvJson.Sha256(patched)),
                    ("syntaxCheck", SyntaxJson(syntax)), ("hunksApplied", (parameters["hunks"] as JsonArray)!.Count))
                    .With("backupPath", backup);
            }

            case "delete":
            {
                if (!string.IsNullOrEmpty(replacement))
                    throw OumvException.Validation("VALIDATION_FAILED: replacement must be omitted for mode=delete",
                        new Dictionary<string, object?> { ["reason"] = "replacement_forbidden" });
                if (!File.Exists(absolute)) throw OumvException.NotFound($"RESOURCE_NOT_FOUND: {path}");
                var current = File.ReadAllBytes(absolute);
                if (string.IsNullOrEmpty(expectedVersion))
                    throw OumvException.Validation("VALIDATION_FAILED: expectedVersion is required to delete a file",
                        new Dictionary<string, object?> { ["reason"] = "expected_version_required", ["path"] = path });
                if (expectedVersion != SourceEditor.Version(current))
                    throw OumvException.Conflict("CONFLICT_DETECTED: version mismatch on delete",
                        new Dictionary<string, object?> { ["reason"] = "version_mismatch" });
                File.Delete(absolute);
                return OumvJson.Obj(
                    ("status", "APPLIED"), ("path", _editor.Relative(absolute)),
                    ("syntaxCheck", OumvJson.Obj(("status", "skipped"))), ("deleted", true));
            }

            case "rename":
            {
                if (string.IsNullOrWhiteSpace(renameTo))
                    throw OumvException.Validation("VALIDATION_FAILED: renameTo is required for mode=rename",
                        new Dictionary<string, object?> { ["reason"] = "rename_to_required" });
                if (!string.IsNullOrEmpty(replacement))
                    throw OumvException.Validation("VALIDATION_FAILED: replacement must be omitted for mode=rename",
                        new Dictionary<string, object?> { ["reason"] = "replacement_forbidden" });
                var target = _editor.Resolve(renameTo);
                if (!File.Exists(absolute)) throw OumvException.NotFound($"RESOURCE_NOT_FOUND: {path}");
                if (File.Exists(target) || Directory.Exists(target))
                    throw OumvException.Conflict("CONFLICT_DETECTED: rename target already exists",
                        new Dictionary<string, object?> { ["reason"] = "target_exists", ["renameTo"] = renameTo });
                var current = File.ReadAllBytes(absolute);
                if (string.IsNullOrEmpty(expectedVersion))
                    throw OumvException.Validation("VALIDATION_FAILED: expectedVersion is required to rename a file",
                        new Dictionary<string, object?> { ["reason"] = "expected_version_required", ["path"] = path });
                if (expectedVersion != SourceEditor.Version(current))
                    throw OumvException.Conflict("CONFLICT_DETECTED: version mismatch on rename",
                        new Dictionary<string, object?> { ["reason"] = "version_mismatch" });
                var directory = Path.GetDirectoryName(target);
                if (!string.IsNullOrEmpty(directory)) Directory.CreateDirectory(directory);
                File.Move(absolute, target);
                return OumvJson.Obj(
                    ("status", "APPLIED"), ("path", _editor.Relative(absolute)), ("newPath", _editor.Relative(target)),
                    ("syntaxCheck", OumvJson.Obj(("status", "skipped"))));
            }

            case "symbol":
            {
                if (string.IsNullOrWhiteSpace(symbol))
                    throw OumvException.Validation("VALIDATION_FAILED: symbol is required for mode=symbol",
                        new Dictionary<string, object?> { ["reason"] = "symbol_required" });
                if (!File.Exists(absolute)) throw OumvException.NotFound($"RESOURCE_NOT_FOUND: {path}");
                var current = _editor.ReadText(absolute);
                var currentVersion = OumvJson.Sha256(current);
                // §4.3 guard order: build + syntax-check the patched text (step 3)
                // before the expectedVersion rules (steps 4-5).
                var patched = _editor.ReplaceSymbol(absolute, current, symbol, replacement ?? string.Empty);
                var syntax = _editor.Check(absolute, patched);
                if (syntax.Status == "failed") throw SyntaxError(syntax);
                if (string.IsNullOrEmpty(expectedVersion))
                    throw OumvException.Validation("VALIDATION_FAILED: expectedVersion is required to edit a symbol",
                        new Dictionary<string, object?> { ["reason"] = "expected_version_required", ["path"] = path });
                if (expectedVersion != currentVersion)
                    throw OumvException.Conflict($"version mismatch: expected {expectedVersion}, current is {currentVersion}",
                        new Dictionary<string, object?> { ["reason"] = "version_mismatch", ["expected"] = expectedVersion, ["current"] = currentVersion });
                var backup = _editor.Backup(absolute, Encoding.UTF8.GetBytes(current));
                _editor.Write(absolute, patched);
                return OumvJson.Obj(
                    ("status", "APPLIED"), ("path", _editor.Relative(absolute)), ("symbol", symbol),
                    ("newVersion", OumvJson.Sha256(patched)), ("syntaxCheck", SyntaxJson(syntax)))
                    .With("backupPath", backup);
            }

            default:
            {
                if (mode != "replace")
                    throw OumvException.Unsupported($"UNSUPPORTED_CAPABILITY: unknown applyEdit mode {mode}",
                        new Dictionary<string, object?> { ["reason"] = "unknown_mode" });
                if (string.IsNullOrEmpty(replacement))
                    throw OumvException.Validation("VALIDATION_FAILED: replacement is required for mode=replace",
                        new Dictionary<string, object?> { ["reason"] = "replacement_required" });
                if (!File.Exists(absolute)) throw OumvException.NotFound($"RESOURCE_NOT_FOUND: {path}");
                var current = File.ReadAllBytes(absolute);
                var currentVersion = SourceEditor.Version(current);
                // §4.3 guard order: syntax pre-flight (step 3) before the
                // expectedVersion rules (steps 4-5).
                var syntax = _editor.Check(absolute, replacement);
                if (syntax.Status == "failed") throw SyntaxError(syntax);
                if (string.IsNullOrEmpty(expectedVersion))
                    throw OumvException.Validation(
                        "VALIDATION_FAILED: expectedVersion is required to edit an existing file — call oumv/getSource (or observe L3) and pass its version",
                        new Dictionary<string, object?> { ["reason"] = "expected_version_required", ["current"] = currentVersion });
                if (expectedVersion != currentVersion)
                    throw OumvException.Conflict($"version mismatch: expected {expectedVersion}, current is {currentVersion}",
                        new Dictionary<string, object?> { ["reason"] = "version_mismatch", ["expected"] = expectedVersion, ["current"] = currentVersion });
                var backup = _editor.Backup(absolute, current);
                _editor.Write(absolute, replacement);
                return OumvJson.Obj(
                    ("status", "APPLIED"), ("path", _editor.Relative(absolute)), ("newVersion", OumvJson.Sha256(replacement)),
                    ("syntaxCheck", SyntaxJson(syntax)))
                    .With("backupPath", backup);
            }
        }
    }

    private sealed record StagedEdit(
        string Absolute, string Relative, string Content, byte[]? Previous, bool Created, int HunksApplied);

    /// <summary>Multi-file atomic edit (wire 1.5.0): stage everything, then write, then report per item.</summary>
    private JsonObject ApplyEditBatch(JsonObject parameters, JsonArray edits)
    {
        if (edits.Count == 0 || edits.Count > 16)
            throw OumvException.Validation("VALIDATION_FAILED: edits must hold 1..16 items",
                new Dictionary<string, object?> { ["reason"] = "batch_size", ["count"] = edits.Count });

        var sessionId = OumvJson.Str(parameters["sessionId"]);
        var staged = new List<StagedEdit>(edits.Count);
        var seen = new Dictionary<string, int>(StringComparer.Ordinal);

        for (var index = 0; index < edits.Count; index++)
        {
            var item = edits[index] as JsonObject ?? new JsonObject();
            var path = OumvJson.Str(item["path"])?.Trim() ?? string.Empty;
            var fail = new Func<OumvException, OumvException>(error =>
            {
                error.Details["editIndex"] = index;
                if (path.Length > 0) error.Details["path"] = path;
                return error;
            });

            if (path.Length == 0)
                throw fail(OumvException.Validation("INVALID_PARAMS: path is required",
                    new Dictionary<string, object?> { ["reason"] = "path_required" }));

            var absolute = _editor.Resolve(path);
            var key = _editor.Relative(absolute);
            if (seen.TryGetValue(key, out var previousIndex))
                throw fail(OumvException.Validation(
                    $"VALIDATION_FAILED: duplicate path in batch (also item {previousIndex})",
                    new Dictionary<string, object?> { ["reason"] = "duplicate_path" }));
            seen[key] = index;

            var mode = (OumvJson.Str(item["mode"]) ?? "replace").ToLowerInvariant();
            if (mode is not ("replace" or "create" or "patch"))
                throw fail(OumvException.Unsupported(
                    $"UNSUPPORTED_CAPABILITY: applyEdit batch mode {mode} (only replace/create/patch are atomic)",
                    new Dictionary<string, object?> { ["reason"] = "unknown_mode" }));

            var replacement = OumvJson.Str(item["replacement"]);
            var expectedVersion = OumvJson.Str(item["expectedVersion"])?.Trim();

            if (mode == "create")
            {
                if (string.IsNullOrEmpty(replacement))
                    throw fail(OumvException.Validation("VALIDATION_FAILED: replacement is required for mode=create",
                        new Dictionary<string, object?> { ["reason"] = "replacement_required" }));
                if (!string.IsNullOrEmpty(expectedVersion))
                    throw fail(OumvException.Validation("VALIDATION_FAILED: expectedVersion must be omitted for mode=create",
                        new Dictionary<string, object?> { ["reason"] = "create_forbids_version" }));
                if (File.Exists(absolute) || Directory.Exists(absolute))
                    throw fail(OumvException.Conflict("CONFLICT_DETECTED: file already exists; use mode=replace with its version",
                        new Dictionary<string, object?> { ["reason"] = "file_exists" }));
                var syntax = _editor.Check(absolute, replacement);
                if (syntax.Status == "failed") throw fail(SyntaxError(syntax));
                staged.Add(new StagedEdit(absolute, key, replacement, null, true, 0));
                continue;
            }

            if (!File.Exists(absolute))
                throw fail(OumvException.NotFound($"RESOURCE_NOT_FOUND: {path}"));
            var current = File.ReadAllText(absolute, Encoding.UTF8);
            var currentVersion = OumvJson.Sha256(current);

            // §4.3 guard order: the syntax pre-flight (step 3) runs before the
            // expectedVersion rules (steps 4-5) — the resulting bytes are known
            // first, the version is asserted second.
            string stagedContent;
            var hunksApplied = 0;
            if (mode == "patch")
            {
                var hunks = item["hunks"] as JsonArray;
                if (hunks is null || hunks.Count == 0)
                    throw fail(OumvException.Validation("VALIDATION_FAILED: hunks is required for mode=patch",
                        new Dictionary<string, object?> { ["reason"] = "hunks_required" }));
                try { stagedContent = _editor.ApplyHunks(current, hunks); }
                catch (OumvException hunkError) { throw fail(hunkError); }
                hunksApplied = hunks.Count;
            }
            else
            {
                if (string.IsNullOrEmpty(replacement))
                    throw fail(OumvException.Validation("VALIDATION_FAILED: replacement is required for mode=replace",
                        new Dictionary<string, object?> { ["reason"] = "replacement_required" }));
                stagedContent = replacement;
            }
            var stagedSyntax = _editor.Check(absolute, stagedContent);
            if (stagedSyntax.Status == "failed") throw fail(SyntaxError(stagedSyntax));

            if (string.IsNullOrEmpty(expectedVersion))
                throw fail(OumvException.Validation("VALIDATION_FAILED: expectedVersion is required to edit an existing file",
                    new Dictionary<string, object?> { ["reason"] = "expected_version_required", ["current"] = currentVersion }));
            if (expectedVersion != currentVersion)
                throw fail(OumvException.Conflict($"version mismatch: expected {expectedVersion}, current is {currentVersion}",
                    new Dictionary<string, object?> { ["reason"] = "version_mismatch", ["expected"] = expectedVersion, ["current"] = currentVersion }));

            staged.Add(new StagedEdit(absolute, key, stagedContent, Encoding.UTF8.GetBytes(current), false, hunksApplied));
        }

        var written = new List<StagedEdit>(staged.Count);
        for (var index = 0; index < staged.Count; index++)
        {
            var edit = staged[index];
            try
            {
                _editor.Write(edit.Absolute, edit.Content);
                written.Add(edit);
            }
            catch (Exception writeError)
            {
                var failures = new List<string>();
                for (var i = written.Count - 1; i >= 0; i--)
                {
                    var done = written[i];
                    try
                    {
                        if (done.Created) File.Delete(done.Absolute);
                        else File.WriteAllBytes(done.Absolute, done.Previous!);
                    }
                    catch (Exception restoreError)
                    {
                        failures.Add($"{done.Relative}: {restoreError.Message}");
                    }
                }

                var error = OumvException.Internal($"failed to persist edit: {writeError.Message}",
                    new Dictionary<string, object?>
                    {
                        ["editIndex"] = index,
                        ["path"] = edit.Relative,
                        ["rolledBack"] = written.Count > 0 && failures.Count == 0,
                    });
                if (failures.Count > 0) error.Details["rollbackErrors"] = failures;
                throw error;
            }
        }

        var results = new JsonArray();
        foreach (var edit in staged)
        {
            var entry = OumvJson.Obj(("path", edit.Relative), ("newVersion", OumvJson.Sha256(edit.Content)));
            if (edit.Created) entry["created"] = true;
            if (edit.HunksApplied > 0) entry["hunksApplied"] = edit.HunksApplied;
            results.Add(entry);
            PublishState(edit.Relative, edit.Created ? "create" : edit.HunksApplied > 0 ? "patch" : "replace", sessionId);
        }

        return OumvJson.Obj(("status", "APPLIED"), ("edits", results));
    }

    private static OumvException SyntaxError(SyntaxCheck check) =>
        OumvException.Syntax($"SYNTAX_ERROR: pre-flight syntax check failed: {check.Error}",
            new Dictionary<string, object?>
            {
                ["syntaxCheck"] = OumvJson.Obj(("status", check.Status), ("tool", check.Tool), ("error", check.Error)),
            });

    private static JsonNode SyntaxJson(SyntaxCheck check) => check.Status == "skipped"
        ? OumvJson.Obj(("status", "skipped"))
        : OumvJson.Obj(("status", check.Status), ("tool", check.Tool));

    /* ------------------------------------------------------------------ verify */

    private async Task<JsonNode> VerifyAsync(JsonObject parameters, CancellationToken cancellationToken)
    {
        var sandboxRequested = parameters["sandbox"] is JsonObject sandbox && OumvJson.Str(sandbox["mode"]) == "rollback";
        if (sandboxRequested && !_sandbox.HasEngine)
            throw OumvException.Unsupported("UNSUPPORTED_CAPABILITY: verify.sandboxRollback is not available in this app",
                new Dictionary<string, object?> { ["reason"] = "sandbox_unavailable" });

        var steps = parameters["steps"] as JsonArray;
        var headers = ReadHeaders(parameters["headers"]);
        if (sandboxRequested) headers["X-TokenSafe-Dry-Run"] = "true";

        var stopwatch = Stopwatch.StartNew();
        var (outcome, results) = await _sandbox.RunAsync(_services, async services =>
        {
            var collected = new List<JsonObject>();
            if (steps is { Count: > 0 })
            {
                foreach (var step in steps)
                {
                    if (step is JsonObject stepObject) collected.Add(await RunVerifyStepAsync(stepObject, headers, services, cancellationToken));
                }
            }
            else
            {
                collected.Add(await RunVerifyStepAsync(parameters, headers, services, cancellationToken));
            }
            return collected;
        }, cancellationToken);
        stopwatch.Stop();

        var passed = results is { Count: > 0 } && results.All(step => (int)step["statusCode"]! < 400);
        var last = results?.LastOrDefault();
        var payload = OumvJson.Obj(
            ("status", passed ? "passed" : "failed"),
            ("target", OumvJson.Str(parameters["target"]) ?? string.Empty),
            ("statusCode", last?["statusCode"]?.DeepClone()),
            ("diff", OumvJson.Obj(("statusCode", last?["statusCode"]?.DeepClone()))),
            ("timings", OumvJson.Obj(("t_action_ms", Math.Round(stopwatch.Elapsed.TotalMilliseconds, 3)),
                ("t_total_ms", Math.Round(stopwatch.Elapsed.TotalMilliseconds, 3)))),
            ("durationMs", Math.Round(stopwatch.Elapsed.TotalMilliseconds, 3)),
            ("scope", ScopeJson(outcome)),
            ("rollbackStatus", outcome.RollbackStatus));

        if (steps is { Count: > 0 } && results is not null)
            payload["steps"] = new JsonArray(results.Select(step => (JsonNode)step.DeepClone()).ToArray());
        if (outcome.RolledBack)
        {
            payload["rolledBack"] = true;
            payload["dbDiff"] = OumvJson.Obj(("rollback", true), ("driver", _sandbox.Driver));
        }
        if (outcome.Errors.Count > 0)
            payload["rollbackErrors"] = new JsonArray(outcome.Errors.Select(error => (JsonNode)JsonValue.Create(error)!).ToArray());

        return payload;
    }

    private static JsonObject ScopeJson(SandboxOutcome outcome)
    {
        var notExecuted = new List<string> { "real network" };
        // "persistent database state" leaves the list only when the rollback was observed.
        if (outcome.RollbackStatus != "verified") notExecuted.Add("persistent database state");
        return OumvJson.Obj(
            ("checked", new[] { "in-memory endpoint dispatch", "status code" }),
            ("notExecuted", notExecuted),
            ("notSupported", new[] { "browser DOM" }));
    }

    private async Task<JsonObject> RunVerifyStepAsync(
        JsonObject step, Dictionary<string, string> headers, IServiceProvider services, CancellationToken cancellationToken)
    {
        var target = OumvJson.Str(step["target"]) ?? throw Invalid("target is required for verify");
        var (method, path) = EndpointCatalog.SplitTarget(target);
        var entry = _catalog.Match(target)
            ?? throw OumvException.NotFound($"RESOURCE_NOT_FOUND: no endpoint matches {target}");

        var payload = step["payload"];
        var body = payload switch
        {
            null => null,
            JsonValue value when value.TryGetValue<string>(out var text) => text,
            _ => payload.ToJsonString(),
        };

        var stepWatch = Stopwatch.StartNew();
        var dispatch = await _catalog.DispatchAsync(entry, method, path, body, headers, services, cancellationToken);
        stepWatch.Stop();
        RecordTrace(method, target, dispatch.StatusCode, stepWatch.Elapsed.TotalMilliseconds);

        return OumvJson.Obj(
            ("target", target),
            ("method", method),
            ("path", path),
            ("statusCode", dispatch.StatusCode),
            ("status", dispatch.StatusCode < 400 ? "passed" : "failed"),
            ("durationMs", Math.Round(stepWatch.Elapsed.TotalMilliseconds, 3)),
            ("responseBody", dispatch.Body.Length > 2000 ? dispatch.Body[..2000] : dispatch.Body));
    }

    /* ------------------------------------- wire 1.6.0 (§3 cancellation, §4.4/§14) */

    /// <summary>§3 cancellation: best-effort, and an unknown id is a result, not an error.</summary>
    private JsonNode Cancel(JsonObject parameters)
    {
        var requestId = OumvJson.Str(parameters["requestId"])?.Trim();
        var target = OumvJson.Str(parameters["target"])?.Trim();
        if (string.IsNullOrEmpty(requestId) && string.Equals(target, "last", StringComparison.OrdinalIgnoreCase))
            requestId = _lastRequestId;
        if (string.IsNullOrEmpty(requestId))
        {
            throw OumvException.Validation("VALIDATION_FAILED: requestId is required (or target=\"last\")",
                new Dictionary<string, object?> { ["reason"] = "request_id_required" });
        }

        if (_inFlight.TryGetValue(requestId, out var source))
        {
            try { source.Cancel(); } catch (ObjectDisposedException) { /* finished between lookup and cancel */ }
            return OumvJson.Obj(("cancelled", true), ("requestId", requestId));
        }
        return OumvJson.Obj(("cancelled", false), ("requestId", requestId));
    }

    /// <summary>§4.4 dry-run: the applyEdit guard chain without a single byte written.</summary>
    private JsonObject PreviewEdit(JsonObject parameters)
    {
        if (!_options.EnableMutations)
            throw new OumvException(OumvErrorCode.PermissionDenied, "PERMISSION_DENIED: mutate capability is disabled by policy");

        var previews = new JsonArray();
        if (parameters["edits"] is JsonArray edits)
        {
            if (edits.Count == 0 || edits.Count > 16)
            {
                throw OumvException.Validation("VALIDATION_FAILED: edits must hold 1..16 items",
                    new Dictionary<string, object?> { ["reason"] = "edits_size" });
            }
            foreach (var item in edits.OfType<JsonObject>()) previews.Add(PreviewSingle(item));
        }
        else
        {
            previews.Add(PreviewSingle(parameters));
        }

        return OumvJson.Obj(("status", "PREVIEW"), ("applied", false), ("previews", previews), ("files", previews.Count));
    }

    private JsonObject PreviewSingle(JsonObject parameters)
    {
        var path = OumvJson.Str(parameters["path"]) ?? throw Invalid("path is required");
        var absolute = _editor.Resolve(path);
        var relative = _editor.Relative(absolute);
        var mode = (OumvJson.Str(parameters["mode"]) ?? "replace").ToLowerInvariant();
        var replacement = OumvJson.Str(parameters["replacement"]);
        var expectedVersion = OumvJson.Str(parameters["expectedVersion"])?.Trim();
        var symbol = OumvJson.Str(parameters["symbol"]);
        var renameTo = OumvJson.Str(parameters["renameTo"]);

        switch (mode)
        {
            case "create":
            {
                if (string.IsNullOrEmpty(replacement))
                    throw OumvException.Validation("replacement is required for mode=create",
                        new Dictionary<string, object?> { ["reason"] = "replacement_required" });
                if (!string.IsNullOrEmpty(expectedVersion))
                    throw OumvException.Validation("VALIDATION_FAILED: expectedVersion must be omitted for mode=create",
                        new Dictionary<string, object?> { ["reason"] = "create_forbids_version" });
                if (File.Exists(absolute) || Directory.Exists(absolute))
                    throw OumvException.Conflict("CONFLICT_DETECTED: file already exists; use mode=replace with its version",
                        new Dictionary<string, object?> { ["reason"] = "file_exists" });
                var createSyntax = _editor.Check(absolute, replacement);
                if (createSyntax.Status == "failed") throw SyntaxError(createSyntax);
                return OumvJson.Obj(
                    ("path", relative), ("mode", "create"), ("wouldApply", true), ("created", true),
                    ("newVersion", OumvJson.Sha256(replacement)), ("syntaxCheck", SyntaxJson(createSyntax)));
            }

            case "delete":
            {
                if (!string.IsNullOrEmpty(replacement))
                    throw OumvException.Validation("VALIDATION_FAILED: replacement must be omitted for mode=delete",
                        new Dictionary<string, object?> { ["reason"] = "replacement_forbidden" });
                if (!File.Exists(absolute)) throw OumvException.NotFound($"RESOURCE_NOT_FOUND: {path}");
                RequireVersion(absolute, expectedVersion, path);
                return OumvJson.Obj(("path", relative), ("mode", "delete"), ("wouldApply", true), ("deleted", true));
            }

            case "rename":
            {
                if (string.IsNullOrWhiteSpace(renameTo))
                    throw OumvException.Validation("VALIDATION_FAILED: renameTo is required for mode=rename",
                        new Dictionary<string, object?> { ["reason"] = "rename_to_required" });
                var target = _editor.Resolve(renameTo);
                if (!File.Exists(absolute)) throw OumvException.NotFound($"RESOURCE_NOT_FOUND: {path}");
                if (File.Exists(target) || Directory.Exists(target))
                    throw OumvException.Conflict("CONFLICT_DETECTED: rename target already exists",
                        new Dictionary<string, object?> { ["reason"] = "target_exists", ["renameTo"] = renameTo });
                RequireVersion(absolute, expectedVersion, path);
                return OumvJson.Obj(
                    ("path", relative), ("mode", "rename"), ("wouldApply", true), ("newPath", _editor.Relative(target)));
            }

            case "symbol":
            {
                if (string.IsNullOrWhiteSpace(symbol))
                    throw OumvException.Validation("VALIDATION_FAILED: symbol is required for mode=symbol",
                        new Dictionary<string, object?> { ["reason"] = "symbol_required" });
                if (!File.Exists(absolute)) throw OumvException.NotFound($"RESOURCE_NOT_FOUND: {path}");
                var current = _editor.ReadText(absolute);
                RequireVersion(absolute, expectedVersion, path, current);
                var patched = _editor.ReplaceSymbol(absolute, current, symbol, replacement ?? string.Empty);
                var syntax = _editor.Check(absolute, patched);
                if (syntax.Status == "failed") throw SyntaxError(syntax);
                return OumvJson.Obj(
                    ("path", relative), ("mode", "symbol"), ("symbol", symbol), ("wouldApply", true),
                    ("newVersion", OumvJson.Sha256(patched)), ("syntaxCheck", SyntaxJson(syntax)));
            }

            case "patch":
            {
                if (!File.Exists(absolute)) throw OumvException.NotFound($"RESOURCE_NOT_FOUND: {path}");
                var current = _editor.ReadText(absolute);
                RequireVersion(absolute, expectedVersion, path, current);
                var patched = _editor.ApplyHunks(current, parameters["hunks"] as JsonArray);
                var syntax = _editor.Check(absolute, patched);
                if (syntax.Status == "failed") throw SyntaxError(syntax);
                return OumvJson.Obj(
                    ("path", relative), ("mode", "patch"), ("wouldApply", true),
                    ("newVersion", OumvJson.Sha256(patched)), ("hunksApplied", (parameters["hunks"] as JsonArray)!.Count),
                    ("syntaxCheck", SyntaxJson(syntax)));
            }

            default:
            {
                if (mode != "replace")
                    throw OumvException.Unsupported($"UNSUPPORTED_CAPABILITY: unknown applyEdit mode {mode}",
                        new Dictionary<string, object?> { ["reason"] = "unknown_mode" });
                if (string.IsNullOrEmpty(replacement))
                    throw OumvException.Validation("VALIDATION_FAILED: replacement is required for mode=replace",
                        new Dictionary<string, object?> { ["reason"] = "replacement_required" });
                if (!File.Exists(absolute)) throw OumvException.NotFound($"RESOURCE_NOT_FOUND: {path}");
                RequireVersion(absolute, expectedVersion, path);
                var syntax = _editor.Check(absolute, replacement);
                if (syntax.Status == "failed") throw SyntaxError(syntax);
                return OumvJson.Obj(
                    ("path", relative), ("mode", "replace"), ("wouldApply", true),
                    ("newVersion", OumvJson.Sha256(replacement)), ("syntaxCheck", SyntaxJson(syntax)));
            }
        }
    }

    private void RequireVersion(string absolute, string? expectedVersion, string path, string? currentText = null)
    {
        var current = currentText is null ? SourceEditor.Version(File.ReadAllBytes(absolute)) : OumvJson.Sha256(currentText);
        if (string.IsNullOrEmpty(expectedVersion))
        {
            throw OumvException.Validation(
                "VALIDATION_FAILED: expectedVersion is required to edit an existing file — call oumv/getSource (or observe L3) and pass its version",
                new Dictionary<string, object?> { ["reason"] = "expected_version_required", ["current"] = current });
        }
        if (expectedVersion != current)
        {
            throw OumvException.Conflict($"version mismatch: expected {expectedVersion}, current is {current}",
                new Dictionary<string, object?> { ["reason"] = "version_mismatch", ["expected"] = expectedVersion, ["current"] = current });
        }
    }

    /* ---------------------------------------------------- §14.1 oumv/index */

    private static readonly HashSet<string> IndexedExtensions = new(StringComparer.OrdinalIgnoreCase)
    {
        ".cs", ".fs", ".vb", ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs",
        ".py", ".go", ".java", ".kt", ".php", ".rb", ".rs", ".swift", ".vue", ".svelte",
    };

    private static readonly System.Text.RegularExpressions.Regex SymbolPattern =
        new("[A-Za-z_][A-Za-z0-9_]*", System.Text.RegularExpressions.RegexOptions.Compiled);

    /// <summary>
    /// §14.1 repo-wide symbol search: exact &gt; prefix &gt; qualified-suffix &gt; substring,
    /// ranked from the files a build would read — no index file, no stale cache.
    /// </summary>
    private JsonObject IndexSymbols(JsonObject parameters)
    {
        var query = OumvJson.Str(parameters["query"])?.Trim();
        if (string.IsNullOrEmpty(query))
            throw OumvException.Validation("VALIDATION_FAILED: query is required", new Dictionary<string, object?> { ["reason"] = "query_required" });
        var limit = Math.Clamp(OumvJson.Int(parameters["limit"]) ?? 20, 1, 100);

        var matches = new List<(int Rank, string Symbol, string Path, int Line)>();
        var filesScanned = 0;
        var truncated = false;
        var root = _editor.Root;
        if (Directory.Exists(root))
        {
            var pending = new Stack<string>();
            pending.Push(root);
            while (pending.Count > 0 && filesScanned < 2000)
            {
                var dir = pending.Pop();
                IEnumerable<string> children;
                try { children = Directory.EnumerateFileSystemEntries(dir); }
                catch (Exception) { continue; }
                foreach (var child in children)
                {
                    if (Directory.Exists(child))
                    {
                        if (!DigestSkipDirectories.Contains(Path.GetFileName(child))) pending.Push(child);
                        continue;
                    }
                    if (!IndexedExtensions.Contains(Path.GetExtension(child))) continue;
                    FileInfo info;
                    try { info = new FileInfo(child); } catch (Exception) { continue; }
                    if (info.Length == 0 || info.Length > 512 * 1024) continue;
                    filesScanned++;
                    string[] lines;
                    try { lines = File.ReadAllLines(child, Encoding.UTF8); }
                    catch (Exception) { continue; }
                    var relative = Path.GetRelativePath(root, child).Replace('\\', '/');
                    for (var lineIndex = 0; lineIndex < lines.Length; lineIndex++)
                    {
                        foreach (System.Text.RegularExpressions.Match match in SymbolPattern.Matches(lines[lineIndex]))
                        {
                            var rank = Rank(match.Value, query);
                            if (rank == 0) continue;
                            matches.Add((rank, match.Value, relative, lineIndex + 1));
                        }
                    }
                }
            }
        }

        var ordered = matches
            .Where(match => match.Rank > 0)
            .OrderByDescending(match => match.Rank)
            .ThenBy(match => match.Symbol.Length)
            .ThenBy(match => match.Path, StringComparer.Ordinal)
            .ThenBy(match => match.Line)
            .ToList();
        if (ordered.Count > limit) truncated = true;

        var array = new JsonArray();
        foreach (var match in ordered.Take(limit))
        {
            array.Add(OumvJson.Obj(
                ("symbol", match.Symbol), ("path", match.Path), ("line", match.Line),
                ("kind", "symbol"), ("rank", match.Rank)));
        }
        return OumvJson.Obj(("matches", array), ("truncated", truncated), ("filesScanned", filesScanned));
    }

    private static int Rank(string candidate, string query)
    {
        if (candidate.Equals(query, StringComparison.OrdinalIgnoreCase)) return 4;
        if (candidate.StartsWith(query, StringComparison.OrdinalIgnoreCase)) return 3;
        if (candidate.EndsWith(query, StringComparison.OrdinalIgnoreCase)) return 2;
        if (candidate.Contains(query, StringComparison.OrdinalIgnoreCase)) return 1;
        return 0;
    }

    /* ---------------------------------------------------- §14.3 oumv/build */

    private async Task<JsonNode> BuildAsync(JsonObject parameters, CancellationToken cancellationToken)
    {
        var command = OumvJson.Str(parameters["command"])?.Trim();
        if (string.IsNullOrEmpty(command)) command = _options.BuildCommand;
        var timeoutMs = Math.Clamp(OumvJson.Int(parameters["timeoutMs"]) ?? 120_000, 1_000, 600_000);
        if (string.IsNullOrEmpty(command))
        {
            return OumvJson.Obj(
                ("status", "skipped"), ("command", "none"), ("diagnostics", new JsonArray()),
                ("skipReason", "no build command configured: set OumvOptions.BuildCommand or pass params.command"));
        }

        var isWindows = OperatingSystem.IsWindows();
        var startInfo = new ProcessStartInfo
        {
            FileName = isWindows ? "cmd.exe" : "/bin/sh",
            WorkingDirectory = _editor.Root,
            RedirectStandardOutput = true,
            RedirectStandardError = true,
            UseShellExecute = false,
        };
        if (isWindows) { startInfo.ArgumentList.Add("/c"); startInfo.ArgumentList.Add(command); }
        else { startInfo.ArgumentList.Add("-c"); startInfo.ArgumentList.Add(command); }

        var stopwatch = Stopwatch.StartNew();
        using var process = new Process { StartInfo = startInfo };
        try
        {
            process.Start();
        }
        catch (Exception error)
        {
            return OumvJson.Obj(
                ("status", "failed"), ("command", command), ("diagnostics", new JsonArray()),
                ("error", $"could not start the command: {error.Message}"));
        }

        var stdoutTask = process.StandardOutput.ReadToEndAsync(cancellationToken);
        var stderrTask = process.StandardError.ReadToEndAsync(cancellationToken);
        using var timeout = CancellationTokenSource.CreateLinkedTokenSource(cancellationToken);
        timeout.CancelAfter(timeoutMs);
        var timedOut = false;
        try
        {
            await process.WaitForExitAsync(timeout.Token);
        }
        catch (OperationCanceledException)
        {
            timedOut = true;
            try { process.Kill(entireProcessTree: true); } catch (Exception) { /* already gone */ }
        }
        stopwatch.Stop();

        var stdout = await stdoutTask;
        var stderr = await stderrTask;
        var diagnostics = ParseDiagnostics(stdout, stderr);
        var exitCode = timedOut ? -1 : process.ExitCode;
        return OumvJson.Obj(
            ("status", timedOut ? "timeout" : exitCode == 0 ? "passed" : "failed"),
            ("command", command),
            ("exitCode", exitCode),
            ("diagnostics", diagnostics),
            ("durationMs", Math.Round(stopwatch.Elapsed.TotalMilliseconds, 3)),
            ("truncated", stdout.Length + stderr.Length > 64_000));
    }

    private static readonly System.Text.RegularExpressions.Regex DiagnosticPattern =
        new("^(?<file>[^\\r\\n:]+?)\\((?(?:\\d+),(?:\\d+)|:(?:\\d+))\\)?:\\s*(?<severity>error|warning)\\b(?<rest>.*)$",
            System.Text.RegularExpressions.RegexOptions.Compiled | System.Text.RegularExpressions.RegexOptions.IgnoreCase);

    private static JsonArray ParseDiagnostics(string stdout, string stderr)
    {
        var diagnostics = new JsonArray();
        var lines = (stderr + "\n" + stdout).Split('\n', StringSplitOptions.RemoveEmptyEntries);
        foreach (var raw in lines)
        {
            var line = raw.TrimEnd();
            if (line.Length == 0) continue;
            var match = DiagnosticPattern.Match(line);
            if (match.Success)
            {
                diagnostics.Add(OumvJson.Obj(
                    ("severity", match.Groups["severity"].Value.ToLowerInvariant()),
                    ("path", match.Groups["file"].Value.Trim()),
                    ("message", match.Groups["rest"].Value.Trim().TrimStart(':', ' '))));
            }
            else if (line.Contains(": error", StringComparison.OrdinalIgnoreCase))
            {
                diagnostics.Add(OumvJson.Obj(("severity", "error"), ("message", line)));
            }
            else if (line.Contains(": warning", StringComparison.OrdinalIgnoreCase))
            {
                diagnostics.Add(OumvJson.Obj(("severity", "warning"), ("message", line)));
            }
            if (diagnostics.Count >= 200) break;
        }
        return diagnostics;
    }

    /* ---------------------------------------------------- §14.2 oumv/query */

    private async Task<JsonNode> QueryAsync(JsonObject parameters, CancellationToken cancellationToken)
    {
        var sql = OumvJson.Str(parameters["sql"])?.Trim();
        if (string.IsNullOrEmpty(sql))
            throw OumvException.Validation("VALIDATION_FAILED: sql is required", new Dictionary<string, object?> { ["reason"] = "sql_required" });
        if (!IsReadOnlySql(sql))
        {
            throw new OumvException(OumvErrorCode.SecurityViolation,
                "SECURITY_VIOLATION: only read-only statements are allowed",
                new Dictionary<string, object?> { ["reason"] = "query_forbidden" });
        }
        var limit = Math.Clamp(OumvJson.Int(parameters["limit"]) ?? 100, 1, 1000);
        var outcome = await _sandbox.QueryAsync(_services, sql, limit, cancellationToken);

        var rows = new JsonArray();
        foreach (var row in outcome.Rows)
        {
            var cells = new JsonArray();
            foreach (var value in row) cells.Add(OumvJson.ToNode(value));
            rows.Add(cells);
        }
        var columns = new JsonArray();
        foreach (var column in outcome.Columns) columns.Add(JsonValue.Create(column));
        return OumvJson.Obj(
            ("columns", columns), ("rows", rows), ("rowCount", outcome.Rows.Count),
            ("truncated", outcome.Truncated), ("driver", _sandbox.Driver));
    }

    private static bool IsReadOnlySql(string sql)
    {
        var trimmed = sql.TrimStart();
        while (true)
        {
            if (trimmed.StartsWith("--", StringComparison.Ordinal))
            {
                var newline = trimmed.IndexOf('\n');
                if (newline < 0) return false;
                trimmed = trimmed[(newline + 1)..].TrimStart();
                continue;
            }
            if (trimmed.StartsWith("/*", StringComparison.Ordinal))
            {
                var end = trimmed.IndexOf("*/", StringComparison.Ordinal);
                if (end < 0) return false;
                trimmed = trimmed[(end + 2)..].TrimStart();
                continue;
            }
            break;
        }

        var match = System.Text.RegularExpressions.Regex.Match(trimmed, "^[A-Za-z]+");
        if (!match.Success) return false;
        var keyword = match.Value.ToUpperInvariant();
        if (keyword is not ("SELECT" or "EXPLAIN" or "PRAGMA" or "WITH" or "VALUES" or "SHOW" or "DESCRIBE")) return false;
        // One statement only: a stacked write after a semicolon is a write.
        var withoutTrailing = trimmed.TrimEnd().TrimEnd(';');
        return !withoutTrailing.Contains(';');
    }

    private static Dictionary<string, string> ReadHeaders(JsonNode? node)
    {
        var headers = new Dictionary<string, string>(StringComparer.OrdinalIgnoreCase);
        if (node is JsonObject obj)
            foreach (var (key, value) in obj)
                if (OumvJson.Str(value) is { } text)
                    headers[key] = text;
        return headers;
    }
}

internal static class JsonObjectExtensions
{
    /// <summary>Adds an optional field only when it has a value.</summary>
    public static JsonObject With(this JsonObject obj, string key, object? value)
    {
        if (value is not null) obj[key] = OumvJson.ToNode(value);
        return obj;
    }
}
