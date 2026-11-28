using System.Collections.Concurrent;
using System.Security.Cryptography;
using System.Text;
using System.Text.Json;
using System.Text.Json.Nodes;

namespace TokenSafe.Oumv;

/// <summary>OUMV wire error codes (spec §5). The numeric table is the contract.</summary>
public static class OumvErrorCode
{
    public const int ConflictDetected = -32001;
    public const int SyntaxError = -32002;
    public const int SecurityViolation = -32003;
    public const int TargetNotFound = -32004;
    public const int ActionNotFound = -32005;
    public const int UnsupportedCapability = -32006;
    public const int PermissionDenied = -32007;
    public const int RuntimeUnavailable = -32008;
    public const int ResourceNotFound = -32009;
    public const int StaleResourceVersion = -32010;
    public const int ValidationFailed = -32011;
    public const int ParseError = -32700;
    public const int MethodNotFound = -32601;
    public const int InvalidParams = -32602;
    public const int InternalError = -32603;
}

/// <summary>A typed OUMV error; the JSON-RPC layer maps it to the wire envelope.</summary>
public sealed class OumvException : Exception
{
    public OumvException(int code, string message, IDictionary<string, object?>? data = null)
        : base(message)
    {
        Code = code;
        Details = data is null
            ? new Dictionary<string, object?>()
            : new Dictionary<string, object?>(data);
    }

    public int Code { get; }

    public Dictionary<string, object?> Details { get; }

    public OumvException With(string key, object? value)
    {
        Details[key] = value;
        return this;
    }

    public static OumvException Conflict(string message, IDictionary<string, object?>? data = null) =>
        new(OumvErrorCode.ConflictDetected, message, data);

    public static OumvException Syntax(string message, IDictionary<string, object?>? data = null) =>
        new(OumvErrorCode.SyntaxError, message, data);

    public static OumvException Security(string message) =>
        new(OumvErrorCode.SecurityViolation, message);

    public static OumvException NotFound(string message) =>
        new(OumvErrorCode.ResourceNotFound, message);

    public static OumvException Validation(string message, IDictionary<string, object?>? data = null) =>
        new(OumvErrorCode.ValidationFailed, message, data);

    public static OumvException Unsupported(string message, IDictionary<string, object?>? data = null) =>
        new(OumvErrorCode.UnsupportedCapability, message, data);

    /// <summary>§4.1 freshness mismatch (wire 1.6.0): the caller's workspace token is stale.</summary>
    public static OumvException Stale(string message, IDictionary<string, object?>? data = null) =>
        new(OumvErrorCode.StaleResourceVersion, message, data);

    public static OumvException Internal(string message, IDictionary<string, object?>? data = null) =>
        new(OumvErrorCode.InternalError, message, data);
}

/// <summary>Adapter configuration; every value has a working default.</summary>
public sealed class OumvOptions
{
    /// <summary>Workspace root the path jail is anchored to (default: content root).</summary>
    public string? WorkspaceRoot { get; set; }

    /// <summary>Runtime name reported in the handshake.</summary>
    public string Name { get; set; } = "tokensafe-aspnetcore";

    /// <summary>Runtime version reported in the handshake.</summary>
    public string Version { get; set; } = "0.1.0";

    /// <summary>Default session lifetime in seconds (spec §3).</summary>
    public int SessionTtlSeconds { get; set; } = 3600;

    /// <summary>Number of request traces kept for observe L4.</summary>
    public int TraceLimit { get; set; } = 128;

    /// <summary>JSON-RPC path for HTTP and WebSocket.</summary>
    public string RpcPath { get; set; } = "/__oumv/rpc";

    /// <summary>SSE events path.</summary>
    public string EventsPath { get; set; } = "/__oumv/events";

    /// <summary>
    /// Spec §12 session token: when set, every RPC/SSE/WS request must carry it
    /// (X-OUMV-Session-Token or Authorization: Bearer). Empty keeps the loopback
    /// development allowance; a non-loopback bind without a token is refused.
    /// </summary>
    public string? AuthToken { get; set; }

    /// <summary>Set false to mount the transports but refuse mutations.</summary>
    public bool EnableMutations { get; set; } = true;
}

/// <summary>Handshake sessions; expiry is enforced on read, absent ids are stateless.</summary>
public sealed class OumvSessions
{
    private readonly ConcurrentDictionary<string, DateTimeOffset> _sessions = new();
    private readonly TimeProvider _time;

    public OumvSessions(TimeProvider time) => _time = time;

    public const int MinTtlSeconds = 1;
    public const int MaxTtlSeconds = 86400;

    public static int Clamp(int seconds) => Math.Clamp(seconds, MinTtlSeconds, MaxTtlSeconds);

    public string Create(int ttlSeconds)
    {
        var id = "sess-" + Convert.ToHexString(RandomNumberGenerator.GetBytes(8)).ToLowerInvariant();
        _sessions[id] = _time.GetUtcNow().AddSeconds(Clamp(ttlSeconds));
        return id;
    }

    public bool Validate(string? id)
    {
        if (string.IsNullOrEmpty(id)) return true; // stateless callers are allowed
        if (!_sessions.TryGetValue(id, out var expiresAt)) return false;
        if (_time.GetUtcNow() > expiresAt)
        {
            _sessions.TryRemove(id, out _);
            return false;
        }
        return true;
    }

    public bool Delete(string? id) => !string.IsNullOrEmpty(id) && _sessions.TryRemove(id, out _);

    public int Count => _sessions.Count;
}

/// <summary>One recorded request trace (observe L4) or one pushed event.</summary>
public sealed record OumvTrace(
    string Method,
    string Path,
    string Route,
    string Handler,
    int StatusCode,
    double DurationMs,
    string Timestamp,
    string? SessionId = null,
    string? Error = null);

/// <summary>Monotonic event log with replay, shared by SSE and WebSocket subscribers (spec §2.3/§2.4).</summary>
public sealed class OumvEventHub
{
    private readonly object _gate = new();
    private readonly List<(long Id, string Type, string Json)> _events = new();
    private readonly List<Action<long, string, string>> _subscribers = new();
    private long _sequence;

    public long LastId
    {
        get { lock (_gate) return _sequence; }
    }

    public long Publish(string type, JsonObject data)
    {
        long id;
        string json = data.ToJsonString();
        lock (_gate)
        {
            id = ++_sequence;
            _events.Add((id, type, json));
            if (_events.Count > 512) _events.RemoveAt(0);
            foreach (var subscriber in _subscribers.ToArray())
            {
                try { subscriber(id, type, json); }
                catch { /* a broken subscriber must never fail the mutation that published it */ }
            }
        }
        return id;
    }

    /// <summary>Events with an id greater than <paramref name="lastId"/> (0 = the whole log).</summary>
    public IReadOnlyList<(long Id, string Type, string Json)> Since(long lastId)
    {
        lock (_gate) return _events.Where(e => e.Id > lastId).ToArray();
    }

    public IDisposable Subscribe(Action<long, string, string> handler)
    {
        lock (_gate) _subscribers.Add(handler);
        return new Subscription(this, handler);
    }

    private sealed class Subscription(OumvEventHub hub, Action<long, string, string> handler) : IDisposable
    {
        public void Dispose()
        {
            lock (hub._gate) hub._subscribers.Remove(handler);
        }
    }
}

/// <summary>Small JSON helpers shared by the runtime and the transports.</summary>
public static class OumvJson
{
    public static readonly JsonSerializerOptions Options = new()
    {
        PropertyNamingPolicy = JsonNamingPolicy.CamelCase,
        DefaultIgnoreCondition = System.Text.Json.Serialization.JsonIgnoreCondition.WhenWritingNull,
    };

    public static JsonObject Obj(params (string Key, object? Value)[] pairs)
    {
        var obj = new JsonObject();
        foreach (var (key, value) in pairs) obj[key] = ToNode(value);
        return obj;
    }

    public static JsonNode? ToNode(object? value) => value switch
    {
        null => null,
        JsonNode node => node,
        string s => JsonValue.Create(s),
        bool b => JsonValue.Create(b),
        int i => JsonValue.Create(i),
        long l => JsonValue.Create(l),
        double d => JsonValue.Create(d),
        IDictionary<string, object?> map => new JsonObject(map.Select(kv => KeyValuePair.Create(kv.Key, ToNode(kv.Value)))),
        System.Collections.IEnumerable list => new JsonArray(list.Cast<object?>().Select(ToNode).ToArray()),
        _ => JsonSerializer.SerializeToNode(value, Options),
    };

    public static string Sha256(string content) =>
        "sha256:" + Convert.ToHexString(SHA256.HashData(Encoding.UTF8.GetBytes(content))).ToLowerInvariant();

    public static string Sha256(byte[] content) =>
        "sha256:" + Convert.ToHexString(SHA256.HashData(content)).ToLowerInvariant();

    public static string? Str(JsonNode? node) => node is JsonValue value && value.TryGetValue<string>(out var s) ? s : null;

    public static int? Int(JsonNode? node) => node is JsonValue value && value.TryGetValue<int>(out var i) ? i : null;

    public static bool Bool(JsonNode? node) => node is JsonValue value && value.TryGetValue<bool>(out var b) && b;
}
