using System.Text;
using Microsoft.AspNetCore.Http;
using Microsoft.AspNetCore.Mvc.Controllers;
using Microsoft.AspNetCore.Routing;
using Microsoft.AspNetCore.Routing.Patterns;

namespace TokenSafe.Oumv;

/// <summary>One addressable endpoint discovered from the app's EndpointDataSource.</summary>
public sealed record CatalogEntry(
    string Method,
    string Path,
    string HandlerName,
    string DisplayName,
    Endpoint Endpoint);

/// <summary>Result of an in-memory dispatch through a real RequestDelegate.</summary>
public sealed record DispatchResult(int StatusCode, string Body);

/// <summary>
/// Endpoint introspection and in-memory dispatch: minimal APIs and MVC controllers
/// are both `RouteEndpoint`s, so one code path serves L1/L2 and verify.
/// </summary>
public sealed class EndpointCatalog
{
    private readonly EndpointDataSource _dataSource;

    public EndpointCatalog(EndpointDataSource dataSource) => _dataSource = dataSource;

    public IReadOnlyList<CatalogEntry> All()
    {
        var entries = new List<CatalogEntry>();
        foreach (var endpoint in _dataSource.Endpoints.OfType<RouteEndpoint>())
        {
            var methods = endpoint.Metadata.GetMetadata<HttpMethodMetadata>()?.HttpMethods;
            var pattern = endpoint.RoutePattern.RawText ?? string.Empty;
            if (pattern.Length == 0) continue;
            var handler = HandlerName(endpoint);
            if (methods is null || methods.Count == 0)
            {
                entries.Add(new CatalogEntry("ANY", Normalize(pattern), handler, endpoint.DisplayName ?? pattern, endpoint));
                continue;
            }
            foreach (var method in methods)
                entries.Add(new CatalogEntry(method.ToUpperInvariant(), Normalize(pattern), handler,
                    endpoint.DisplayName ?? pattern, endpoint));
        }

        return entries
            .GroupBy(e => (e.Method, e.Path))
            .Select(g => g.First())
            .OrderBy(e => e.Path, StringComparer.Ordinal)
            .ThenBy(e => e.Method, StringComparer.Ordinal)
            .ToArray();
    }

    private static string HandlerName(Endpoint endpoint)
    {
        var action = endpoint.Metadata.GetMetadata<ControllerActionDescriptor>();
        if (action is not null) return $"{action.ControllerName}.{action.ActionName}";
        var display = endpoint.DisplayName ?? string.Empty;
        var colon = display.LastIndexOf(':');
        return colon >= 0 ? display[(colon + 1)..].Trim() : display;
    }

    private static string Normalize(string path) => "/" + path.Trim('/');

    /// <summary>Resolves "METHOD /path" (method optional, defaults to GET) to one endpoint.</summary>
    public CatalogEntry? Match(string target)
    {
        var (method, path) = SplitTarget(target);
        var entries = All();
        var exact = entries.FirstOrDefault(e =>
            string.Equals(e.Path, path, StringComparison.OrdinalIgnoreCase) &&
            (string.Equals(e.Method, method, StringComparison.OrdinalIgnoreCase) || e.Method == "ANY"));
        if (exact is not null) return exact;

        foreach (var entry in entries)
        {
            if (!string.Equals(entry.Method, method, StringComparison.OrdinalIgnoreCase) && entry.Method != "ANY")
                continue;
            if (TryMatch(entry.Path, path, out _)) return entry;
        }

        return null;
    }

    public static (string Method, string Path) SplitTarget(string target)
    {
        var text = (target ?? string.Empty).Trim();
        if (text.Length == 0) throw new OumvException(OumvErrorCode.InvalidParams, "INVALID_PARAMS: target is required");
        var space = text.IndexOf(' ');
        if (space < 0) return ("GET", Normalize(text));
        var method = text[..space].Trim().ToUpperInvariant();
        var path = text[(space + 1)..].Trim();
        return (method, Normalize(path));
    }

    /// <summary>Matches a path against a route pattern; `{name}` and `{*name}` are wildcards.</summary>
    public static bool TryMatch(string pattern, string path, out Dictionary<string, string> values)
    {
        values = new Dictionary<string, string>(StringComparer.OrdinalIgnoreCase);
        var patternSegments = pattern.Trim('/').Split('/', StringSplitOptions.RemoveEmptyEntries);
        var pathSegments = path.Trim('/').Split('/', StringSplitOptions.RemoveEmptyEntries);

        var index = 0;
        foreach (var segment in patternSegments)
        {
            var catchAll = segment.StartsWith("{*", StringComparison.Ordinal);
            var isParameter = segment.StartsWith('{') && segment.EndsWith('}');
            if (catchAll)
            {
                var name = segment[2..^1].Split(':')[0];
                values[name] = string.Join('/', pathSegments[index..]);
                return true;
            }
            if (isParameter)
            {
                var optional = segment.EndsWith("?}", StringComparison.Ordinal);
                if (index >= pathSegments.Length)
                {
                    if (optional) continue;
                    return false;
                }
                var name = segment[1..^1].Split(':')[0].TrimEnd('?');
                values[name] = pathSegments[index];
                index++;
                continue;
            }
            if (index >= pathSegments.Length ||
                !string.Equals(segment, pathSegments[index], StringComparison.OrdinalIgnoreCase))
                return false;
            index++;
        }

        return index == pathSegments.Length;
    }

    /// <summary>
    /// Runs the endpoint's real RequestDelegate on a synthetic HttpContext: the same
    /// model binding, filters and DI a live request gets, with no network hop.
    /// </summary>
    public async Task<DispatchResult> DispatchAsync(
        CatalogEntry entry,
        string method,
        string path,
        string? body,
        IReadOnlyDictionary<string, string>? headers,
        IServiceProvider services,
        CancellationToken cancellationToken)
    {
        var delegateHandler = entry.Endpoint.RequestDelegate
            ?? throw OumvException.Unsupported($"UNSUPPORTED_CAPABILITY: {entry.DisplayName} has no RequestDelegate");

        var httpContext = new DefaultHttpContext { RequestServices = services };
        httpContext.Request.Method = method;
        httpContext.Request.Path = Normalize(path);
        httpContext.Request.Scheme = "http";
        httpContext.Request.Host = new HostString("localhost");
        httpContext.Request.ContentType = "application/json";
        if (body is not null)
        {
            var bytes = Encoding.UTF8.GetBytes(body);
            httpContext.Request.Body = new MemoryStream(bytes);
            httpContext.Request.ContentLength = bytes.Length;
        }
        else
        {
            httpContext.Request.Body = new MemoryStream();
            httpContext.Request.ContentLength = 0;
        }

        if (headers is not null)
            foreach (var (key, value) in headers)
                httpContext.Request.Headers[key] = value;

        if (TryMatch(entry.Path, path, out var routeValues))
            foreach (var (key, value) in routeValues)
                httpContext.Request.RouteValues[key] = value;
        var responseBody = new MemoryStream();
        httpContext.Response.Body = responseBody;
        httpContext.RequestAborted = cancellationToken;
        httpContext.SetEndpoint(entry.Endpoint);

        try
        {
            await delegateHandler(httpContext);
        }
        catch (Exception ex)
        {
            return new DispatchResult(500, $"endpoint threw: {ex.GetType().Name}: {ex.Message}");
        }

        responseBody.Position = 0;
        using var reader = new StreamReader(responseBody, Encoding.UTF8);
        var text = await reader.ReadToEndAsync(cancellationToken);
        return new DispatchResult(httpContext.Response.StatusCode, text);
    }
}
