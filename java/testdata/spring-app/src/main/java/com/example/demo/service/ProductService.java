package com.example.demo.service;

import com.example.demo.model.Product;
import org.springframework.stereotype.Service;

import java.math.BigDecimal;
import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Optional;

@Service
public class ProductService {

    private final Map<String, Product> products = new LinkedHashMap<>();

    public ProductService() {
        products.put("1", new Product("1", "Keyboard", new BigDecimal("79.00")));
        products.put("2", new Product("2", "Mouse", new BigDecimal("29.00")));
    }

    public List<Product> findAll() {
        return new ArrayList<>(products.values());
    }

    public Optional<Product> findById(String id) {
        return Optional.ofNullable(products.get(id));
    }

    public Product save(Product product) {
        products.put(product.getId(), product);
        return product;
    }

    public boolean delete(String id) {
        return products.remove(id) != null;
    }
}
