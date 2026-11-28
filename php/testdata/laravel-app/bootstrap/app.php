<?php

/*
 * Minimal Laravel 11 bootstrap for the certification fixture.
 *
 * The OUMV sidecar scans this project statically and never boots it; this file
 * exists so `php/certify.sh` can start a REAL Laravel application and let
 * oumv/verify dispatch GET /health to it. The response that comes back is what
 * makes the L4 trace evidence honest instead of fabricated.
 */

use Illuminate\Foundation\Application;
use Illuminate\Foundation\Configuration\Exceptions;
use Illuminate\Foundation\Configuration\Middleware;

return Application::configure(basePath: dirname(__DIR__))
    ->withRouting(
        web: __DIR__ . '/../routes/web.php',
        api: __DIR__ . '/../routes/api.php',
        health: '/up',
    )
    ->withMiddleware(function (Middleware $middleware): void {
        // A newer PHP than the framework officially supports emits E_WARNING when
        // the session interfaces are declared, and Laravel turns any warning into a
        // 500. The fixture serves stateless JSON, so the middleware masks warnings
        // and deprecations for this certification app only.
        $middleware->prepend(\App\Http\Middleware\QuietDeprecations::class);
    })
    ->withExceptions(function (Exceptions $exceptions): void {
        //
    })
    ->create();
