<?php

/*
 * Fixture configuration. The APP_KEY is a fixed development key for this
 * certification app only — it encrypts the session cookie the web middleware
 * sets, and is committed on purpose so the harness is reproducible. It must
 * never be reused by a real application.
 */

return [
    'name' => 'OUMV Laravel fixture',
    'env' => env('APP_ENV', 'local'),
    'debug' => (bool) env('APP_DEBUG', false),
    'url' => env('APP_URL', 'http://127.0.0.1'),
    'key' => env('APP_KEY', 'base64:' . base64_encode(str_repeat('oumv-fixture-key', 2))),
    'cipher' => 'AES-256-CBC',
    'timezone' => 'UTC',
    'locale' => 'en',
    'fallback_locale' => 'en',
];
