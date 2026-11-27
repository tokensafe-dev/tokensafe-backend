package com.example.demo;

import com.example.demo.controller.ProductController;
import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.assertEquals;

/**
 * Static reverse-dependency evidence for `oumv/understand?query=impact`: this
 * file references {@link ProductController} and its {@code list()} handler.
 * The fixture is never compiled (no Maven/Gradle in this repository).
 */
class ProductControllerTest {

    @Test
    void listsProducts() {
        ProductController controller = new ProductController(null);
        assertEquals(0, controller.list().size());
    }
}
