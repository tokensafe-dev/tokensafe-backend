package com.example.demo.controller;

import org.springframework.stereotype.Controller;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RequestMethod;
import org.springframework.web.bind.annotation.ResponseBody;

import java.util.Map;

@Controller
@RequestMapping("/admin")
public class AdminController {

    @GetMapping("/stats")
    @ResponseBody
    public Map<String, Integer> stats() {
        return Map.of("routes", 17);
    }

    @RequestMapping(value = "/reload", method = RequestMethod.POST)
    @ResponseBody
    public Map<String, Boolean> reload() {
        return Map.of("reloaded", true);
    }
}
