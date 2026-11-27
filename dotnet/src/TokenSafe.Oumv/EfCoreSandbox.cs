using System.Reflection;
using Microsoft.Extensions.DependencyInjection;

namespace TokenSafe.Oumv;

/// <summary>
/// Database sandbox hook for `verify.sandbox: {mode:"rollback"}` (spec §4).
/// The core package holds no EF Core reference: discovery and transaction control
/// go through the types the app already has loaded, so a non-EF app carries no
/// dependency and still reports `not_applicable` honestly.
/// </summary>
public interface IOumvDbSandbox
{
    /// <summary>True when at least one DbContext was discovered in DI.</summary>
    bool HasEngine { get; }

    /// <summary>Driver name reported in dbDiff (e.g. "efcore").</summary>
    string Driver { get; }

    /// <summary>
    /// Runs <paramref name="body"/> inside one transaction per discovered DbContext
    /// and rolls every transaction back afterwards, reporting whether the rollback
    /// was actually observed.
    /// </summary>
    Task<(SandboxOutcome Outcome, T? Result)> RunAsync<T>(
        IServiceProvider rootServices,
        Func<IServiceProvider, Task<T>> body,
        CancellationToken cancellationToken);
}

/// <summary>Evidence for the sandboxed verify: what was observed, never assumed.</summary>
public sealed record SandboxOutcome(bool HasEngine, bool RolledBack, IReadOnlyList<string> Errors)
{
    public string RollbackStatus => !HasEngine ? "not_applicable" : RolledBack ? "verified" : "unverified";
}

/// <summary>No database layer wired: the capability is not advertised.</summary>
public sealed class NoDbSandbox : IOumvDbSandbox
{
    public static readonly NoDbSandbox Instance = new();

    public bool HasEngine => false;

    public string Driver => "none";

    public async Task<(SandboxOutcome Outcome, T? Result)> RunAsync<T>(
        IServiceProvider rootServices,
        Func<IServiceProvider, Task<T>> body,
        CancellationToken cancellationToken)
    {
        // Without an engine the verify still runs; the outcome says not_applicable.
        var result = await body(rootServices);
        return (new SandboxOutcome(false, false, Array.Empty<string>()), result);
    }
}

/// <summary>EF Core sandbox: auto-detected DbContexts, one transaction each, always rolled back.</summary>
public sealed class EfCoreSandbox : IOumvDbSandbox
{
    private static readonly Type? OptionsType =
        Type.GetType("Microsoft.EntityFrameworkCore.DbContextOptions`1, Microsoft.EntityFrameworkCore");

    private static readonly Type? TransactionInterface =
        Type.GetType("Microsoft.EntityFrameworkCore.Storage.IDbContextTransaction, Microsoft.EntityFrameworkCore");

    private readonly IReadOnlyList<Type> _contextTypes;

    public EfCoreSandbox(IReadOnlyList<Type> contextTypes) => _contextTypes = contextTypes;

    public bool HasEngine => _contextTypes.Count > 0;

    public string Driver => "efcore";

    /// <summary>Scans the service collection for `AddDbContext&lt;T&gt;()` registrations.</summary>
    public static IReadOnlyList<Type> Discover(IServiceCollection services)
    {
        if (OptionsType is null) return Array.Empty<Type>();
        return services
            .Where(descriptor => descriptor.ServiceType.IsGenericType &&
                                 descriptor.ServiceType.GetGenericTypeDefinition() == OptionsType)
            .Select(descriptor => descriptor.ServiceType.GetGenericArguments()[0])
            .Distinct()
            .ToArray();
    }

    public async Task<(SandboxOutcome Outcome, T? Result)> RunAsync<T>(
        IServiceProvider rootServices,
        Func<IServiceProvider, Task<T>> body,
        CancellationToken cancellationToken)
    {
        if (!HasEngine)
        {
            var plain = await body(rootServices);
            return (new SandboxOutcome(false, false, Array.Empty<string>()), plain);
        }

        var transactions = new List<object>();
        var errors = new List<string>();
        using var scope = rootServices.CreateScope();

        foreach (var contextType in _contextTypes)
        {
            try
            {
                var context = scope.ServiceProvider.GetService(contextType);
                if (context is null) continue;
                var transaction = BeginTransaction(context);
                if (transaction is not null) transactions.Add(transaction);
            }
            catch (Exception ex)
            {
                errors.Add($"{contextType.Name}: {ex.GetType().Name}: {ex.Message}");
            }
        }

        T? result = default;
        try
        {
            result = await body(scope.ServiceProvider);
        }
        finally
        {
            foreach (var transaction in transactions)
            {
                try { Rollback(transaction); }
                catch (Exception ex) { errors.Add($"rollback: {ex.GetType().Name}: {ex.Message}"); }
            }
        }

        // Evidence rule: `verified` only when at least one transaction was opened
        // and every rollback call succeeded — never inferred from configuration.
        var rolledBack = transactions.Count > 0 && errors.Count == 0;
        return (new SandboxOutcome(true, rolledBack, errors), result);
    }

    private static object? BeginTransaction(object context)
    {
        var database = context.GetType().GetProperty("Database")?.GetValue(context)
            ?? throw new InvalidOperationException("DbContext.Database is not readable");
        var begin = database.GetType().GetMethod("BeginTransaction", Type.EmptyTypes)
            ?? throw new InvalidOperationException("Database.BeginTransaction() not found");
        return begin.Invoke(database, null);
    }

    private static void Rollback(object transaction)
    {
        var method = TransactionInterface?.GetMethod("Rollback", Type.EmptyTypes)
            ?? transaction.GetType().GetMethod("Rollback", Type.EmptyTypes);
        if (method is null) throw new InvalidOperationException("Rollback() not found");
        method.Invoke(transaction, null);
    }
}
