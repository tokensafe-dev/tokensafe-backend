using Microsoft.AspNetCore.Mvc;

namespace TokenSafe.Oumv.CertApp.Controllers;

/// <summary>
/// A classic MVC controller next to the minimal APIs: the adapter must see both
/// through the same EndpointDataSource and dispatch both in-process.
/// </summary>
[ApiController]
[Route("api/v1/legacy-orders")]
public sealed class OrdersController(CertDbContext db) : ControllerBase
{
    [HttpGet]
    public IActionResult List() =>
        Ok(new { source = "controller", orders = db.Orders.OrderBy(order => order.Id).ToArray() });

    [HttpPost]
    public IActionResult Create([FromBody] OrderDto dto)
    {
        var order = new Order { Customer = dto.Customer, Total = dto.Total };
        db.Orders.Add(order);
        db.SaveChanges();
        return Created($"/api/v1/legacy-orders/{order.Id}", new { order.Id, order.Customer, order.Total });
    }
}
