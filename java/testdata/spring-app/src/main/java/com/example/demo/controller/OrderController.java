package com.example.demo.controller;

import com.example.demo.model.Order;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PatchMapping;
import org.springframework.web.bind.annotation.PathVariable;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RequestMethod;
import org.springframework.web.bind.annotation.RestController;

import java.util.ArrayList;
import java.util.List;

@RestController
@RequestMapping(value = "/api/v1/orders")
public class OrderController {

    private final List<Order> orders = new ArrayList<>();

    @GetMapping
    public List<Order> list() {
        return orders;
    }

    @PostMapping
    public Order create(@RequestBody Order order) {
        orders.add(order);
        return order;
    }

    @GetMapping("/{orderId}")
    public Order get(@PathVariable String orderId) {
        return orders.stream()
                .filter(order -> order.getId().equals(orderId))
                .findFirst()
                .orElseThrow(() -> new IllegalArgumentException("unknown order " + orderId));
    }

    @RequestMapping(value = "/{orderId}/cancel", method = RequestMethod.POST)
    public Order cancel(@PathVariable String orderId) {
        Order order = get(orderId);
        order.cancel();
        return order;
    }

    @PatchMapping("/{orderId}")
    public Order patch(@PathVariable String orderId, @RequestBody Order order) {
        return order;
    }

    @RequestMapping(path = "/{orderId}/tags", method = { RequestMethod.GET, RequestMethod.PUT })
    public List<String> tags(@PathVariable String orderId) {
        return List.of("priority-" + orderId);
    }
}
