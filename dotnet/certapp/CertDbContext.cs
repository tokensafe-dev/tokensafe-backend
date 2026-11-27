using Microsoft.EntityFrameworkCore;

namespace TokenSafe.Oumv.CertApp;

/// <summary>The EF Core context the certification exercises (and rolls back).</summary>
public sealed class CertDbContext(DbContextOptions<CertDbContext> options) : DbContext(options)
{
    public DbSet<Order> Orders => Set<Order>();
}

public sealed class Order
{
    public int Id { get; set; }

    public string Customer { get; set; } = string.Empty;

    public int Total { get; set; }
}

public sealed record OrderDto(string Customer, int Total);
