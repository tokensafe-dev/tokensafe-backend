using Microsoft.EntityFrameworkCore;
using TokenSafe.Oumv;
using TokenSafe.Oumv.CertApp;

// The certification harness points TOKENSAFE_CERT_WORKSPACE at a throwaway copy,
// so the fixture files the suite edits never touch the repository.
var workspace = Environment.GetEnvironmentVariable("TOKENSAFE_CERT_WORKSPACE")
    ?? Directory.GetCurrentDirectory();
Directory.CreateDirectory(workspace);
// The sqlite file lives OUTSIDE the workspace: a database journal inside it
// would rewrite its mtime on every query and make the §4.1 digest flap.
var databasePath = Path.Combine(Path.GetTempPath(), $"oumv-cert-{Environment.ProcessId}.sqlite");

var builder = WebApplication.CreateBuilder(args);
builder.Services.AddDbContext<CertDbContext>(options => options.UseSqlite($"Data Source={databasePath}"));
builder.Services.AddControllers();
// One builder line: the adapter discovers the DbContext and enables the rollback sandbox.
builder.Services.AddTokensafe(options =>
{
    options.WorkspaceRoot = workspace;
    // §12: the harness starts the app with a token, so the auth refusal cases are
    // certified against this binding instead of being asserted in prose.
    options.AuthToken = Environment.GetEnvironmentVariable("TOKENSAFE_OUMV_AUTH_TOKEN");
});

var app = builder.Build();
app.UseWebSockets();
app.UseTokensafe();

app.MapGet("/health", () => Results.Json(new { ok = true }));

app.MapGet("/api/v1/orders", (CertDbContext db) =>
    Results.Json(new { source = "minimal-api", orders = db.Orders.OrderBy(order => order.Id).ToArray() }));

app.MapPost("/api/v1/orders", (CertDbContext db, OrderDto dto) =>
{
    var order = new Order { Customer = dto.Customer, Total = dto.Total };
    db.Orders.Add(order);
    db.SaveChanges();
    return Results.Created($"/api/v1/orders/{order.Id}", new { order.Id, order.Customer, order.Total });
});

// The sandbox capability case dispatches here: the write happens inside the
// verify transaction and must be rolled back, so a dry run leaves no rows.
app.MapPost("/__oumv_sandbox_probe", (HttpContext context, CertDbContext db) =>
{
    if (context.Request.Headers["X-TokenSafe-Dry-Run"] != "true")
        return Results.BadRequest(new { error = "dry-run header required" });
    db.Orders.Add(new Order { Customer = "sandbox-probe", Total = 1 });
    db.SaveChanges();
    return Results.Json(new { ok = true, rows = db.Orders.Count() });
});

app.MapControllers();

using (var scope = app.Services.CreateScope())
{
    var db = scope.ServiceProvider.GetRequiredService<CertDbContext>();
    db.Database.EnsureCreated();
}

app.Run();
