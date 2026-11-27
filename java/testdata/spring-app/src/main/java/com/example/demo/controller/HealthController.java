package com.example.demo.controller;

import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.RestController;

import java.time.Instant;
import java.util.Map;

@RestController
public class HealthController {

    @GetMapping("/health")
    public Map<String, Object> health() {
        return Map.of("status", "UP", "checkedAt", Instant.now().toString());
    }

    @GetMapping("/metrics")
    public Map<String, Long> metrics() {
        return Map.of("uptimeSeconds", 42L);
    }
}
