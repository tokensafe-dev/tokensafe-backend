using System.Diagnostics;
using System.Net.WebSockets;
using System.Text;
using System.Text.Json;
using System.Text.Json.Nodes;
using Microsoft.AspNetCore.Builder;
using Microsoft.AspNetCore.Hosting;
using Microsoft.AspNetCore.Http;
using Microsoft.AspNetCore.Routing;
using Microsoft.Extensions.DependencyInjection;
using Microsoft.Extensions.DependencyInjection.Extensions;

namespace TokenSafe.Oumv;

/// <summary>Registration helpers: `builder.Services.AddTokensafe()` + `app.UseTokensafe()`.</summary>
public static class OumvApplicationBuilderExtensions
{
    /// <summary>
    /// Registers the runtime. The call is optional for apps without EF Core; it is
    /// required for the EF rollback sandbox, because ASP.NET Core does not expose
    /// the service collection after <c>Build()</c> and DbContext discovery needs it.
    /// </summary>
    public static IServiceCollection AddTokensafe(this IServiceCollection services, Action<OumvOptions>? configure = null)
    {
        var options = new OumvOptions();
        configure?.Invoke(options);
        // Spec §8.5: the universal read-only switch, the same variable Go, the
        // FastAPI autoload and the Node runtime read.
        var envReadOnly = Environment.GetEnvironmentVariable("TOKENSAFE_OUMV_READ_ONLY");
        if (!string.IsNullOrEmpty(envReadOnly) &&
            (envReadOnly == "1" || envReadOnly.Equals("true", StringComparison.OrdinalIgnoreCase) ||
             envReadOnly.Equals("yes", StringComparison.OrdinalIgnoreCase) ||
             envReadOnly.Equals("on", StringComparison.OrdinalIgnoreCase)))
            options.ReadOnly = true;

        services.TryAddSingleton(options);
        services.TryAddSingleton(TimeProvider.System);
        services.TryAddSingleton(serviceProvider => new SourceEditor(
            options.WorkspaceRoot ?? serviceProvider.GetRequiredService<IWebHostEnvironment>().ContentRootPath));
        services.TryAddSingleton(serviceProvider => new EndpointCatalog(
            serviceProvider.GetRequiredService<EndpointDataSource>()));
        services.TryAddSingleton<IOumvDbSandbox>(_ =>
        {
            // Discovery runs when the provider is built, so AddDbContext calls that
            // come after AddTokensafe are still seen.
            var contextTypes = EfCoreSandbox.Discover(services);
            return contextTypes.Count > 0 ? new EfCoreSandbox(contextTypes) : NoDbSandbox.Instance;
        });
        services.TryAddSingleton(serviceProvider => new OumvRuntime(
            serviceProvider.GetRequiredService<OumvOptions>(),
            serviceProvider.GetRequiredService<SourceEditor>(),
            serviceProvider.GetRequiredService<EndpointCatalog>(),
            serviceProvider.GetRequiredService<IOumvDbSandbox>(),
            serviceProvider,
            serviceProvider.GetService<TimeProvider>()));

        return services;
    }

    /// <summary>Mounts JSON-RPC over HTTP, the SSE event stream and the WebSocket binding.</summary>
    public static IApplicationBuilder UseTokensafe(this IApplicationBuilder app)
    {
        var runtime = app.ApplicationServices.GetService<OumvRuntime>() ?? BuildDefault(app);
        return app.UseMiddleware<OumvMiddleware>(runtime);
    }

    /// <summary>`UseTokensafe()` without `AddTokensafe()`: every option falls back to a default.</summary>
    private static OumvRuntime BuildDefault(IApplicationBuilder app)
    {
        var provider = app.ApplicationServices;
        var options = new OumvOptions();
        var environment = provider.GetService<IWebHostEnvironment>();
        var editor = new SourceEditor(options.WorkspaceRoot ?? environment?.ContentRootPath ?? Directory.GetCurrentDirectory());
        var catalog = new EndpointCatalog(provider.GetRequiredService<EndpointDataSource>());
        return new OumvRuntime(options, editor, catalog, NoDbSandbox.Instance, provider,
            provider.GetService<TimeProvider>());
    }
}

/// <summary>Records request traces and serves the OUMV transports (spec §2.2-§2.4).</summary>
public sealed class OumvMiddleware
{
    private readonly RequestDelegate _next;
    private readonly OumvRuntime _runtime;

    public OumvMiddleware(RequestDelegate next, OumvRuntime runtime)
    {
        _next = next;
        _runtime = runtime;
    }

    public async Task InvokeAsync(HttpContext context)
    {
        var path = context.Request.Path.Value ?? "/";
        var options = _runtime.Options;
        var isRpc = path.Equals(options.RpcPath, StringComparison.OrdinalIgnoreCase);
        var isEvents = path.Equals(options.EventsPath, StringComparison.OrdinalIgnoreCase);

        // §12: the credential is checked before dispatch on every binding, so SSE
        // and WS cannot become the unguarded door; a tokenless non-loopback bind
        // is refused outright.
        if (isRpc || isEvents)
        {
            if (!IsAuthorized(context))
            {
                await RefuseAsync(context, StatusCodes.Status401Unauthorized,
                    "PERMISSION_DENIED: invalid OUMV session token", null);
                return;
            }
            if (string.IsNullOrEmpty(options.AuthToken) && !IsLoopbackConnection(context))
            {
                await RefuseAsync(context, StatusCodes.Status403Forbidden,
                    "PERMISSION_DENIED: a non-loopback bind requires AuthToken", "token_required_non_loopback");
                return;
            }
        }

        if (isRpc && context.WebSockets.IsWebSocketRequest)
        {
            await HandleWebSocketAsync(context);
            return;
        }
        if (isRpc && HttpMethods.IsPost(context.Request.Method))
        {
            await HandleRpcAsync(context);
            return;
        }
        if (isEvents && HttpMethods.IsGet(context.Request.Method))
        {
            await HandleSseAsync(context);
            return;
        }
        if (isRpc || isEvents)
        {
            context.Response.StatusCode = StatusCodes.Status405MethodNotAllowed;
            return;
        }

        var stopwatch = Stopwatch.StartNew();
        try
        {
            await _next(context);
        }
        finally
        {
            stopwatch.Stop();
            _runtime.RecordTrace(context.Request.Method, path, context.Response.StatusCode,
                stopwatch.Elapsed.TotalMilliseconds);
        }
    }

    /* -------------------------------------------------------------------- HTTP */

    /// <summary>True when the request carries the configured token (spec §12).</summary>
    private bool IsAuthorized(HttpContext context)
    {
        var expected = _runtime.Options.AuthToken;
        if (string.IsNullOrEmpty(expected)) return true;
        var provided = context.Request.Headers["X-OUMV-Session-Token"].ToString();
        if (string.IsNullOrEmpty(provided))
        {
            var authorization = context.Request.Headers.Authorization.ToString();
            if (authorization.StartsWith("Bearer ", StringComparison.OrdinalIgnoreCase))
                provided = authorization[7..].Trim();
        }
        if (string.IsNullOrEmpty(provided)) return false;
        return System.Security.Cryptography.CryptographicOperations.FixedTimeEquals(
            Encoding.UTF8.GetBytes(provided), Encoding.UTF8.GetBytes(expected));
    }

    /// <summary>Loopback (or an unbound/test address) may run tokenless in development.</summary>
    private static bool IsLoopbackConnection(HttpContext context)
    {
        var local = context.Connection.LocalIpAddress;
        return local is null || System.Net.IPAddress.IsLoopback(local);
    }

    private static async Task RefuseAsync(HttpContext context, int status, string message, string? reason)
    {
        context.Response.StatusCode = status;
        context.Response.ContentType = "application/json";
        var error = new JsonObject { ["code"] = OumvErrorCode.PermissionDenied, ["message"] = message };
        if (reason is not null) error["data"] = new JsonObject { ["reason"] = reason };
        await context.Response.WriteAsync(new JsonObject
        {
            ["jsonrpc"] = "2.0",
            ["id"] = null,
            ["error"] = error,
        }.ToJsonString());
    }

    private async Task HandleRpcAsync(HttpContext context)
    {
        context.Response.ContentType = "application/json";
        string body;
        using (var reader = new StreamReader(context.Request.Body, Encoding.UTF8))
            body = await reader.ReadToEndAsync(context.RequestAborted);

        JsonNode? payload;
        try
        {
            payload = JsonNode.Parse(body);
        }
        catch (JsonException error)
        {
            await WriteJsonAsync(context, new JsonObject
            {
                ["jsonrpc"] = "2.0",
                ["id"] = null,
                ["error"] = new JsonObject
                {
                    ["code"] = OumvErrorCode.ParseError,
                    ["message"] = $"PARSE_ERROR: {error.Message}",
                    ["data"] = new JsonObject(),
                },
            });
            return;
        }

        var response = await _runtime.HandleAsync(payload, context.RequestAborted);
        if (response is null)
        {
            context.Response.StatusCode = StatusCodes.Status202Accepted;
            return;
        }
        await WriteJsonAsync(context, response);
    }

    private static async Task WriteJsonAsync(HttpContext context, JsonNode payload)
    {
        await context.Response.WriteAsync(payload.ToJsonString(), context.RequestAborted);
    }

    /* --------------------------------------------------------------------- SSE */

    private async Task HandleSseAsync(HttpContext context)
    {
        context.Response.StatusCode = StatusCodes.Status200OK;
        context.Response.ContentType = "text/event-stream";
        context.Response.Headers.CacheControl = "no-cache";
        context.Response.Headers.Connection = "keep-alive";

        // Flush the response headers immediately: a client that opens the stream
        // before any event exists must not wait for the first heartbeat.
        await context.Response.WriteAsync(": connected\n\n", context.RequestAborted);
        await context.Response.Body.FlushAsync(context.RequestAborted);

        var lastId = ParseLastEventId(context);
        long sent = lastId;
        var channel = System.Threading.Channels.Channel.CreateUnbounded<(long Id, string Type, string Json)>(
            new System.Threading.Channels.UnboundedChannelOptions { SingleReader = true });
        try
        {
            foreach (var (id, type, json) in _runtime.Events.Since(lastId))
            {
                await WriteEventAsync(context, id, type, json);
                sent = id;
            }

            using var subscription = _runtime.Events.Subscribe((id, type, json) =>
            {
                if (id > sent) channel.Writer.TryWrite((id, type, json));
            });

            var cancellation = context.RequestAborted;
            while (!cancellation.IsCancellationRequested)
            {
                var wait = channel.Reader.WaitToReadAsync(cancellation).AsTask();
                var heartbeat = Task.Delay(TimeSpan.FromSeconds(15), cancellation);
                if (await Task.WhenAny(wait, heartbeat) != wait)
                {
                    await context.Response.WriteAsync(": heartbeat\n\n", cancellation);
                    await context.Response.Body.FlushAsync(cancellation);
                    continue;
                }
                if (!await wait) break;
                while (channel.Reader.TryRead(out var item))
                {
                    sent = item.Id;
                    await WriteEventAsync(context, item.Id, item.Type, item.Json);
                }
            }
        }
        catch (OperationCanceledException)
        {
            // the client went away; the subscription is disposed by `using`
        }
    }

    private static long ParseLastEventId(HttpContext context)
    {
        var raw = context.Request.Headers["Last-Event-ID"].ToString();
        if (string.IsNullOrEmpty(raw)) raw = context.Request.Query["lastEventId"].ToString();
        return long.TryParse(raw, out var value) ? value : 0;
    }

    private static async Task WriteEventAsync(HttpContext context, long id, string type, string json)
    {
        await context.Response.WriteAsync($"event: {type}\nid: {id}\ndata: {json}\n\n", context.RequestAborted);
        await context.Response.Body.FlushAsync(context.RequestAborted);
    }

    /* --------------------------------------------------------------- WebSocket */

    private async Task HandleWebSocketAsync(HttpContext context)
    {
        using var socket = await context.WebSockets.AcceptWebSocketAsync();
        using var writer = new SemaphoreSlim(1, 1);
        string? boundSession = null;

        async Task SendAsync(JsonNode payload)
        {
            await writer.WaitAsync(context.RequestAborted);
            try
            {
                var bytes = Encoding.UTF8.GetBytes(payload.ToJsonString());
                await socket.SendAsync(bytes.AsMemory(), WebSocketMessageType.Text, true, context.RequestAborted);
            }
            catch (Exception)
            {
                // A subscriber that cannot write must never fail the mutation.
            }
            finally
            {
                writer.Release();
            }
        }

        using var subscription = _runtime.Events.Subscribe((eventId, type, json) =>
        {
            JsonObject data;
            try { data = JsonNode.Parse(json) as JsonObject ?? new JsonObject(); }
            catch { return; }
            if (!string.IsNullOrEmpty(boundSession)) data["sessionId"] = boundSession;
            _ = SendAsync(new JsonObject
            {
                ["jsonrpc"] = "2.0",
                ["method"] = $"notifications/oumv.{type}",
                ["params"] = data,
            });
        });

        var buffer = new byte[16 * 1024];
        var pending = new MemoryStream();
        try
        {
            while (socket.State == WebSocketState.Open && !context.RequestAborted.IsCancellationRequested)
            {
                var received = await socket.ReceiveAsync(buffer.AsMemory(), context.RequestAborted);
                if (received.MessageType == WebSocketMessageType.Close)
                {
                    await socket.CloseAsync(WebSocketCloseStatus.NormalClosure, "closed", context.RequestAborted);
                    break;
                }
                pending.Write(buffer, 0, received.Count);
                if (!received.EndOfMessage) continue;

                var text = Encoding.UTF8.GetString(pending.ToArray());
                pending.SetLength(0);
                JsonNode? payload;
                try { payload = JsonNode.Parse(text); }
                catch (JsonException error)
                {
                    await SendAsync(new JsonObject
                    {
                        ["jsonrpc"] = "2.0", ["id"] = null,
                        ["error"] = new JsonObject { ["code"] = OumvErrorCode.ParseError, ["message"] = error.Message },
                    });
                    continue;
                }

                var response = await _runtime.HandleAsync(payload, context.RequestAborted);
                if (response is null) continue;
                boundSession = ExtractSession(response, boundSession);
                await SendAsync(response);
            }
        }
        catch (WebSocketException)
        {
            // peer vanished; nothing to close
        }
        finally
        {
            if (!string.IsNullOrEmpty(boundSession)) _runtime.Sessions.Delete(boundSession);
        }
    }

    private static string? ExtractSession(JsonNode response, string? bound)
    {
        if (response is not JsonObject single) return bound;
        if (single["result"] is JsonObject result && result["sessionId"] is JsonValue value &&
            value.TryGetValue<string>(out var sessionId) && sessionId.Length > 0)
            return sessionId;
        return bound;
    }
}
