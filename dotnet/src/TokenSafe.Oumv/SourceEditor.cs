using System.Security.Cryptography;
using System.Text;
using System.Text.Json.Nodes;
using Microsoft.CodeAnalysis;
using Microsoft.CodeAnalysis.CSharp;
using Microsoft.CodeAnalysis.CSharp.Syntax;

namespace TokenSafe.Oumv;

/// <summary>Pre-flight syntax guard result reported on the wire (spec §4.3).</summary>
public sealed record SyntaxCheck(string Status, string? Tool = null, string? Error = null);

/// <summary>
/// Workspace-jailed source access: OCC tokens, Roslyn syntax pre-flight, symbol
/// replacement, exact-snippet patches and backups. No policy decisions here — the
/// runtime owns guard order.
/// </summary>
public sealed class SourceEditor
{
    private static readonly CSharpParseOptions ParseOptions = new(LanguageVersion.Preview);

    public SourceEditor(string workspaceRoot)
    {
        Root = Path.GetFullPath(workspaceRoot);
    }

    public string Root { get; }

    /// <summary>Resolves a wire path inside the workspace or throws -32003.</summary>
    public string Resolve(string path)
    {
        if (string.IsNullOrWhiteSpace(path))
            throw new OumvException(OumvErrorCode.InvalidParams, "INVALID_PARAMS: path is required");

        var candidate = Path.IsPathRooted(path)
            ? Path.GetFullPath(path)
            : Path.GetFullPath(Path.Combine(Root, path));

        var comparison = OperatingSystem.IsWindows() ? StringComparison.OrdinalIgnoreCase : StringComparison.Ordinal;
        if (!candidate.Equals(Root, comparison) && !candidate.StartsWith(Root + Path.DirectorySeparatorChar, comparison))
            throw OumvException.Security($"SECURITY_VIOLATION: path escapes the workspace root: {path}");

        return candidate;
    }

    /// <summary>Wire-relative path with forward slashes (what the result reports).</summary>
    public string Relative(string absolute) =>
        Path.GetRelativePath(Root, absolute).Replace(Path.DirectorySeparatorChar, '/');

    public static string Version(byte[] content) => OumvJson.Sha256(content);

    public string ReadText(string absolute) => File.ReadAllText(absolute, Encoding.UTF8);

    /// <summary>Roslyn parse over the staged content; non-C# files are skipped, not claimed.</summary>
    public SyntaxCheck Check(string absolute, string content)
    {
        if (!absolute.EndsWith(".cs", StringComparison.OrdinalIgnoreCase))
            return new SyntaxCheck("skipped");

        var tree = CSharpSyntaxTree.ParseText(content, ParseOptions, path: absolute);
        var error = tree.GetDiagnostics().FirstOrDefault(d => d.Severity == DiagnosticSeverity.Error);
        return error is null
            ? new SyntaxCheck("passed", "roslyn")
            : new SyntaxCheck("failed", "roslyn", error.ToString());
    }

    /// <summary>Replaces one type/member declaration with the given text (mode=symbol).</summary>
    public string ReplaceSymbol(string absolute, string source, string symbol, string replacement)
    {
        if (string.IsNullOrWhiteSpace(symbol))
            throw OumvException.Validation("VALIDATION_FAILED: symbol is required for mode=symbol",
                new Dictionary<string, object?> { ["reason"] = "symbol_required" });

        var root = CSharpSyntaxTree.ParseText(source, ParseOptions, path: absolute).GetRoot();
        var target = root.DescendantNodes()
            .OfType<MemberDeclarationSyntax>()
            .FirstOrDefault(member => Identifier(member) == symbol);

        if (target is null)
            throw OumvException.NotFound($"RESOURCE_NOT_FOUND: symbol {symbol} not found in {absolute}");

        return string.Concat(source.AsSpan(0, target.SpanStart), replacement, source.AsSpan(target.Span.End));
    }

    /// <summary>Returns the exact source span of one type/member declaration.</summary>
    public string ExtractSymbol(string absolute, string source, string symbol)
    {
        var root = CSharpSyntaxTree.ParseText(source, ParseOptions, path: absolute).GetRoot();
        var target = root.DescendantNodes()
            .OfType<MemberDeclarationSyntax>()
            .FirstOrDefault(member => Identifier(member) == symbol);
        if (target is null)
            throw OumvException.NotFound($"RESOURCE_NOT_FOUND: symbol {symbol} not found in {absolute}");
        return source[target.SpanStart..target.Span.End];
    }

    private static string? Identifier(MemberDeclarationSyntax member) => member switch
    {
        BaseTypeDeclarationSyntax type => type.Identifier.ValueText,
        MethodDeclarationSyntax method => method.Identifier.ValueText,
        PropertyDeclarationSyntax property => property.Identifier.ValueText,
        ConstructorDeclarationSyntax constructor => constructor.Identifier.ValueText,
        DelegateDeclarationSyntax @delegate => @delegate.Identifier.ValueText,
        _ => null,
    };

    /// <summary>
    /// Splices exact snippets in order into the evolving text. A target occurring
    /// zero times is -32009; more than once without replaceAll is -32011 with the
    /// occurrence lines, so the agent can narrow instead of guessing.
    /// </summary>
    public string ApplyHunks(string text, JsonArray? hunks)
    {
        if (hunks is null || hunks.Count == 0)
            throw OumvException.Validation("VALIDATION_FAILED: hunks is required for mode=patch",
                new Dictionary<string, object?> { ["reason"] = "hunks_required" });

        var current = text;
        for (var index = 0; index < hunks.Count; index++)
        {
            var hunk = hunks[index] as JsonObject
                ?? throw OumvException.Validation($"VALIDATION_FAILED: hunk {index} must be an object");

            var target = OumvJson.Str(hunk["target"]) ?? string.Empty;
            var replacement = OumvJson.Str(hunk["replacement"]) ?? string.Empty;
            if (target.Length == 0)
                throw OumvException.Validation($"VALIDATION_FAILED: hunk {index} has an empty target",
                    new Dictionary<string, object?> { ["reason"] = "target_required", ["hunk"] = index });

            var (start, end) = Window(current, hunk, index);
            var region = current[start..end];
            var count = CountOccurrences(region, target);
            if (count == 0)
                throw OumvException.NotFound($"RESOURCE_NOT_FOUND: hunk {index} target does not occur in the file")
                    .With("reason", "target_not_found").With("hunk", index);

            if (count > 1 && !OumvJson.Bool(hunk["replaceAll"]))
            {
                var lines = new List<int>();
                var position = 0;
                while (lines.Count < 5)
                {
                    var found = region.IndexOf(target, position, StringComparison.Ordinal);
                    if (found < 0) break;
                    lines.Add(LineNumberAt(current, start + found));
                    position = found + target.Length;
                }
                throw OumvException.Validation(
                        $"VALIDATION_FAILED: hunk {index} target occurs {count} times; narrow it with startLine/endLine or set replaceAll",
                        new Dictionary<string, object?>
                        {
                            ["reason"] = "target_not_unique", ["hunk"] = index, ["count"] = count, ["lines"] = lines,
                        });
            }

            var spliced = OumvJson.Bool(hunk["replaceAll"])
                ? region.Replace(target, replacement, StringComparison.Ordinal)
                : ReplaceFirst(region, target, replacement);
            current = string.Concat(current.AsSpan(0, start), spliced, current.AsSpan(end));
        }

        return current;
    }

    private static (int Start, int End) Window(string text, JsonObject hunk, int index)
    {
        var startLine = OumvJson.Int(hunk["startLine"]);
        var endLine = OumvJson.Int(hunk["endLine"]);
        if (startLine is null && endLine is null) return (0, text.Length);

        var lines = text.Split('\n');
        var start = Math.Max(1, startLine ?? 1);
        var end = endLine ?? lines.Length;
        if (start > lines.Length) start = lines.Length;
        if (end > lines.Length) end = lines.Length;
        if (end < start)
            throw OumvException.Validation("VALIDATION_FAILED: endLine precedes startLine",
                new Dictionary<string, object?> { ["reason"] = "inverted_line_range", ["hunk"] = index });

        var offsets = new int[lines.Length];
        var running = 0;
        for (var i = 0; i < lines.Length; i++)
        {
            offsets[i] = running;
            running += lines[i].Length + 1;
        }
        return (offsets[start - 1], offsets[end - 1] + lines[end - 1].Length);
    }

    private static int LineNumberAt(string text, int offset)
    {
        var line = 1;
        for (var i = 0; i < offset && i < text.Length; i++)
            if (text[i] == '\n') line++;
        return line;
    }

    private static int CountOccurrences(string haystack, string needle)
    {
        var count = 0;
        var position = 0;
        while ((position = haystack.IndexOf(needle, position, StringComparison.Ordinal)) >= 0)
        {
            count++;
            position += needle.Length;
        }
        return count;
    }

    private static string ReplaceFirst(string text, string target, string replacement)
    {
        var index = text.IndexOf(target, StringComparison.Ordinal);
        return index < 0 ? text : string.Concat(text.AsSpan(0, index), replacement, text.AsSpan(index + target.Length));
    }

    /// <summary>Writes bytes, creating the parent directory (create/rename targets).</summary>
    public void Write(string absolute, string content)
    {
        var directory = Path.GetDirectoryName(absolute);
        if (!string.IsNullOrEmpty(directory)) Directory.CreateDirectory(directory);
        File.WriteAllText(absolute, content, new UTF8Encoding(false));
    }

    /// <summary>Keeps the previous bytes under .tokensafe/backups and returns the relative path.</summary>
    public string? Backup(string absolute, byte[] previous)
    {
        try
        {
            var directory = Path.Combine(Root, ".tokensafe", "backups");
            Directory.CreateDirectory(directory);
            var name = $"{Convert.ToHexString(SHA256.HashData(previous))[..8].ToLowerInvariant()}_{Path.GetFileName(absolute)}.bak";
            var path = Path.Combine(directory, name);
            File.WriteAllBytes(path, previous);
            return Relative(path);
        }
        catch
        {
            return null; // a backup is advisory; OCC still protects the write
        }
    }
}
