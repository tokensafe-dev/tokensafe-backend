package com.example.demo.controller;

import org.springframework.stereotype.Component;
import org.springframework.web.bind.annotation.GetMapping;

/**
 * Deliberately NOT a controller: the class carries {@code @Component}, so its
 * {@code @GetMapping} methods must never appear in the OUMV L1 surface. This
 * fixture exists to prove the scanner requires a controller stereotype.
 */
@Component
public class FakeController {

    @GetMapping("/fake/not-a-route")
    public String notARoute() {
        return "this class has no @RestController/@Controller";
    }
}
