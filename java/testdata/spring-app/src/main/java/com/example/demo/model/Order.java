package com.example.demo.model;

import java.util.List;

public class Order {

    private final String id;
    private final List<String> items;
    private String status = "NEW";

    public Order(String id, List<String> items) {
        this.id = id;
        this.items = items;
    }

    public String getId() {
        return id;
    }

    public List<String> getItems() {
        return items;
    }

    public String getStatus() {
        return status;
    }

    public void cancel() {
        this.status = "CANCELLED";
    }
}
