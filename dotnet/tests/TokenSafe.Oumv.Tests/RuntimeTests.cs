using System.Text.Json.Nodes;
using Microsoft.AspNetCore.Http;
using Microsoft.AspNetCore.Routing;
using Microsoft.EntityFrameworkCore;
using Microsoft.Extensions.DependencyInjection;
using Microsoft.Extensions.Primitives;
using TokenSafe.Oumv;
using Xunit;

namespace TokenSafe.Oumv.Tests;

/// <summary>
/// Direct contract tests for the pieces certification alone cannot isolate:
/// guard order, atomic staging, the jail, session expiry and EF discovery.
/// </summary>
public sealed class RuntimeTests : IDisposable
{
    private readonly string _workspace = Directory.CreateTempSubdirectory("oumv-dotnet-tests-").FullName;
    private readonly FakeTime _time = new();
    private readonly OumvRuntime _runtime;

    public RuntimeTests()
    {
        var services = new ServiceCollection().BuildServiceProvider();
        _runtime = new OumvRuntime(
            new OumvOptions { WorkspaceRoot = _workspace, Version = "test" },
            new SourceEditor(_workspace),
            new EndpointCatalog(new EmptyEndpointDataSource()),
            NoDbSandbox.Instance,
            services,
            _time);
    }

    public void Dispose()
    {
        try { Directory.Delete(_workspace, recursive: true); } catch { /* best effort */ }
    }

    private string Write(string name, string content)
    {
        var path = Path.Combine(_workspace, name);
        File.WriteAllText(path, content);
        return path;
    }

    private async Task<JsonNode?> Call(string method, JsonObject parameters)
    {
        var request = new JsonObject
        {
            ["jsonrpc"] = "2.0",
            ["id"] = 1,
            ["method"] = method,
            ["params"] = parameters,
        };
        var response = await _runtime.HandleAsync(request);
        return response;
    }

    private static int ErrorCode(JsonNode? response) =>
        response?["error"]?["code"]?.GetValue<int>() ?? 0;

    [Fact]
    public async Task Observe_and_understand_honour_the_ifVersion_freshness_handshake()
    {
        Write("app.cs", "class A {}\n");
        var handshake = await Call("oumv/handshake", new JsonObject { ["protocolVersion"] = "1.6.0" });
        var sessionId = handshake!["result"]!["sessionId"]!.GetValue<string>();

        var observed = await Call("oumv/observe", new JsonObject { ["level"] = "L0", ["sessionId"] = sessionId });
        var digest = observed!["result"]!["workspaceVersion"]!.GetValue<string>();
        Assert.StartsWith("sha256:", digest);
        Assert.Equal(71, digest.Length);

        // Match: the cached copy is still fresh, so the answer is the short envelope.
        var shortAnswer = await Call("oumv/observe",
            new JsonObject { ["level"] = "L1", ["sessionId"] = sessionId, ["ifVersion"] = digest });
        Assert.True(shortAnswer!["result"]!["unchanged"]!.GetValue<bool>());
        Assert.Equal(digest, shortAnswer["result"]!["workspaceVersion"]!.GetValue<string>());
        Assert.Null(shortAnswer["result"]!["data"]);

        // Any touched file invalidates the caller's token with -32010.
        Write("touched.cs", "class B {}\n");
        var fresh = await Call("oumv/observe", new JsonObject { ["level"] = "L0", ["sessionId"] = sessionId });
        var freshDigest = fresh!["result"]!["workspaceVersion"]!.GetValue<string>();
        Assert.NotEqual(digest, freshDigest);

        var stale = await Call("oumv/observe",
            new JsonObject { ["level"] = "L0", ["sessionId"] = sessionId, ["ifVersion"] = digest });
        Assert.Equal(-32010, ErrorCode(stale));
        Assert.Equal("stale_version", stale!["error"]!["data"]!["reason"]!.GetValue<string>());
        Assert.Equal(freshDigest, stale["error"]!["data"]!["current"]!.GetValue<string>());

        var understood = await Call("oumv/understand",
            new JsonObject { ["query"] = "causality", ["target"] = "GET /x", ["sessionId"] = sessionId });
        Assert.Equal(freshDigest, understood!["result"]!["workspaceVersion"]!.GetValue<string>());
        var understoodShort = await Call("oumv/understand",
            new JsonObject { ["query"] = "causality", ["sessionId"] = sessionId, ["ifVersion"] = freshDigest });
        Assert.True(understoodShort!["result"]!["unchanged"]!.GetValue<bool>());
    }

    [Fact]
    public async Task Handshake_echoes_granted_ttl_and_rejects_a_foreign_major()
    {
        var ok = await Call("oumv/handshake", new JsonObject { ["protocolVersion"] = "1.5.0", ["sessionTtlSeconds"] = 1 });
        Assert.Equal(1, ok!["result"]!["ttlSeconds"]!.GetValue<int>());
        Assert.StartsWith("sess-", ok["result"]!["sessionId"]!.GetValue<string>());

        var bad = await Call("oumv/handshake", new JsonObject { ["protocolVersion"] = "9.9.9" });
        Assert.Equal(OumvErrorCode.UnsupportedCapability, ErrorCode(bad));
    }

    [Fact]
    public async Task Session_expires_after_the_granted_ttl()
    {
        var handshake = await Call("oumv/handshake", new JsonObject { ["sessionTtlSeconds"] = 1 });
        var sessionId = handshake!["result"]!["sessionId"]!.GetValue<string>();

        var live = await Call("oumv/observe", new JsonObject { ["level"] = "L0", ["sessionId"] = sessionId });
        Assert.Null(live!["error"]);

        _time.Advance(TimeSpan.FromSeconds(2));
        var expired = await Call("oumv/observe", new JsonObject { ["level"] = "L0", ["sessionId"] = sessionId });
        Assert.Equal(OumvErrorCode.ValidationFailed, ErrorCode(expired));
        Assert.Equal("session_expired", expired!["error"]!["data"]!["reason"]!.GetValue<string>());
    }

    [Fact]
    public async Task Path_jail_rejects_traversal_and_outside_roots()
    {
        Write("inside.cs", "public static class Inside { }\n");
        var traversal = await Call("oumv/applyEdit", new JsonObject
        {
            ["path"] = "../outside.cs",
            ["replacement"] = "x",
            ["expectedVersion"] = "sha256:" + new string('0', 64),
        });
        Assert.Equal(OumvErrorCode.SecurityViolation, ErrorCode(traversal));

        var absolute = await Call("oumv/getSource", new JsonObject { ["path"] = "/etc/hosts" });
        Assert.Equal(OumvErrorCode.SecurityViolation, ErrorCode(absolute));
    }

    [Fact]
    public async Task Replace_checks_occ_before_syntax()
    {
        Write("target.cs", "public static class Value { public static int Number => 41; }\n");
        var stale = await Call("oumv/applyEdit", new JsonObject
        {
            ["path"] = "target.cs",
            ["replacement"] = "not valid csharp (",
            ["expectedVersion"] = "sha256:" + new string('0', 64),
        });
        Assert.Equal(OumvErrorCode.ConflictDetected, ErrorCode(stale));

        var missing = await Call("oumv/applyEdit", new JsonObject
        {
            ["path"] = "target.cs",
            ["replacement"] = "public static class Value { }\n",
        });
        Assert.Equal(OumvErrorCode.ValidationFailed, ErrorCode(missing));
    }

    [Fact]
    public async Task Create_refuses_an_existing_file_and_forbids_a_version()
    {
        Write("existing.cs", "public static class Existing { }\n");
        var exists = await Call("oumv/applyEdit", new JsonObject
        {
            ["path"] = "existing.cs",
            ["mode"] = "create",
            ["replacement"] = "public static class Other { }\n",
        });
        Assert.Equal(OumvErrorCode.ConflictDetected, ErrorCode(exists));

        var withVersion = await Call("oumv/applyEdit", new JsonObject
        {
            ["path"] = "new.cs",
            ["mode"] = "create",
            ["replacement"] = "public static class New { }\n",
            ["expectedVersion"] = "sha256:" + new string('0', 64),
        });
        Assert.Equal(OumvErrorCode.ValidationFailed, ErrorCode(withVersion));
        Assert.False(File.Exists(Path.Combine(_workspace, "new.cs")));
    }

    [Fact]
    public async Task Patch_reports_ambiguity_with_lines_and_aborts_without_bytes()
    {
        var content = "public static class A\n{\n    // dup\n    // dup\n}\n";
        Write("patch.cs", content);
        var version = OumvJson.Sha256(content);
        var ambiguous = await Call("oumv/applyEdit", new JsonObject
        {
            ["path"] = "patch.cs",
            ["mode"] = "patch",
            ["expectedVersion"] = version,
            ["hunks"] = new JsonArray(new JsonObject { ["target"] = "// dup", ["replacement"] = "// one" }),
        });
        Assert.Equal(OumvErrorCode.ValidationFailed, ErrorCode(ambiguous));
        Assert.Equal("target_not_unique", ambiguous!["error"]!["data"]!["reason"]!.GetValue<string>());
        Assert.Equal(new[] { 3, 4 }, ambiguous["error"]!["data"]!["lines"]!.AsArray().Select(node => node!.GetValue<int>()));
        Assert.Equal(content, File.ReadAllText(Path.Combine(_workspace, "patch.cs")));
    }

    [Fact]
    public async Task Patch_replace_all_splices_every_occurrence()
    {
        var content = "// dup\n// dup\n";
        Write("patch.cs", content);
        var patched = await Call("oumv/applyEdit", new JsonObject
        {
            ["path"] = "patch.cs",
            ["mode"] = "patch",
            ["expectedVersion"] = OumvJson.Sha256(content),
            ["hunks"] = new JsonArray(new JsonObject
            {
                ["target"] = "// dup", ["replacement"] = "// done", ["replaceAll"] = true,
            }),
        });
        Assert.Null(patched!["error"]);
        Assert.Equal("// done\n// done\n", File.ReadAllText(Path.Combine(_workspace, "patch.cs")));
        // hunksApplied counts hunks, not occurrences (spec §4.3).
        Assert.Equal(1, patched["result"]!["hunksApplied"]!.GetValue<int>());
    }

    [Fact]
    public async Task Symbol_replaces_one_member_and_keeps_the_rest()
    {
        var before = "namespace Cert;\n\npublic static class SymbolTarget\n{\n    public static int Symbol() { return 41; }\n}\n";
        var after = before.Replace("return 41", "return 42");
        Write("symbol.cs", before);

        var response = await Call("oumv/applyEdit", new JsonObject
        {
            ["path"] = "symbol.cs",
            ["mode"] = "symbol",
            ["symbol"] = "Symbol",
            ["replacement"] = "public static int Symbol() { return 42; }",
            ["expectedVersion"] = OumvJson.Sha256(before),
        });

        Assert.Null(response!["error"]);
        Assert.Equal(after, File.ReadAllText(Path.Combine(_workspace, "symbol.cs")));
        Assert.Equal(OumvJson.Sha256(after), response["result"]!["newVersion"]!.GetValue<string>());
    }

    [Fact]
    public async Task Unknown_symbol_is_resource_not_found()
    {
        Write("symbol.cs", "public static class Holder { public static int Value => 1; }\n");
        var response = await Call("oumv/getSource", new JsonObject { ["path"] = "symbol.cs", ["symbol"] = "Nope" });
        Assert.Equal(OumvErrorCode.ResourceNotFound, ErrorCode(response));
    }

    [Fact]
    public async Task Batch_stages_everything_before_writing_anything()
    {
        var response = await Call("oumv/applyEdit", new JsonObject
        {
            ["edits"] = new JsonArray(
                new JsonObject
                {
                    ["path"] = "batch_a.cs", ["mode"] = "create",
                    ["replacement"] = "public static class BatchA { }\n",
                },
                new JsonObject
                {
                    ["path"] = "batch_b.cs", ["mode"] = "create",
                    ["replacement"] = "public static class Broken {",
                }),
        });

        Assert.Equal(OumvErrorCode.SyntaxError, ErrorCode(response));
        Assert.Equal(1, response!["error"]!["data"]!["editIndex"]!.GetValue<int>());
        Assert.False(File.Exists(Path.Combine(_workspace, "batch_a.cs")));
        Assert.False(File.Exists(Path.Combine(_workspace, "batch_b.cs")));
    }

    [Fact]
    public async Task Batch_applies_all_items_in_order()
    {
        var response = await Call("oumv/applyEdit", new JsonObject
        {
            ["edits"] = new JsonArray(
                new JsonObject
                {
                    ["path"] = "batch_a.cs", ["mode"] = "create",
                    ["replacement"] = "public static class BatchA { }\n",
                },
                new JsonObject
                {
                    ["path"] = "batch_b.cs", ["mode"] = "create",
                    ["replacement"] = "public static class BatchB { }\n",
                }),
        });

        Assert.Null(response!["error"]);
        Assert.Equal(2, response["result"]!["edits"]!.AsArray().Count);
        Assert.Equal("APPLIED", response["result"]!["status"]!.GetValue<string>());
        Assert.True(File.Exists(Path.Combine(_workspace, "batch_a.cs")));
        Assert.True(File.Exists(Path.Combine(_workspace, "batch_b.cs")));
    }

    [Fact]
    public async Task Batch_rejects_duplicate_paths()
    {
        var response = await Call("oumv/applyEdit", new JsonObject
        {
            ["edits"] = new JsonArray(
                new JsonObject
                {
                    ["path"] = "same.cs", ["mode"] = "create",
                    ["replacement"] = "public static class Same { }\n",
                },
                new JsonObject
                {
                    ["path"] = "./same.cs", ["mode"] = "create",
                    ["replacement"] = "public static class Again { }\n",
                }),
        });
        Assert.Equal(OumvErrorCode.ValidationFailed, ErrorCode(response));
        Assert.Equal("duplicate_path", response!["error"]!["data"]!["reason"]!.GetValue<string>());
    }

    [Fact]
    public async Task Get_source_if_version_is_a_dedup_pointer()
    {
        var content = "public static class Pointer { }\n";
        Write("pointer.cs", content);
        var response = await Call("oumv/getSource", new JsonObject
        {
            ["path"] = "pointer.cs",
            ["ifVersion"] = OumvJson.Sha256(content),
        });
        Assert.Null(response!["result"]!["source"]);
        Assert.True(response["result"]!["unchanged"]!.GetValue<bool>());
    }

    [Fact]
    public void Ef_sandbox_discovers_add_dbcontext_registrations()
    {
        var services = new ServiceCollection();
        services.AddDbContext<ProbeContext>(options => options.UseQueryTrackingBehavior(
            Microsoft.EntityFrameworkCore.QueryTrackingBehavior.NoTracking));
        var discovered = EfCoreSandbox.Discover(services);
        Assert.Contains(typeof(ProbeContext), discovered);
        Assert.True(new EfCoreSandbox(discovered).HasEngine);
        Assert.False(NoDbSandbox.Instance.HasEngine);
    }

    private sealed class ProbeContext(DbContextOptions<ProbeContext> options) : DbContext(options);

    private sealed class EmptyEndpointDataSource : EndpointDataSource
    {
        public override IReadOnlyList<Endpoint> Endpoints => Array.Empty<Endpoint>();

        public override IChangeToken GetChangeToken() => new CancellationChangeToken(CancellationToken.None);
    }

    private sealed class FakeTime : TimeProvider
    {
        private DateTimeOffset _now = new(2026, 11, 27, 12, 0, 0, TimeSpan.Zero);

        public override DateTimeOffset GetUtcNow() => _now;

        public void Advance(TimeSpan delta) => _now = _now.Add(delta);
    }
}
