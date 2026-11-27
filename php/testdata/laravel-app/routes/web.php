<?php

use App\Http\Controllers\HealthController;
use Illuminate\Support\Facades\Route;

Route::get('/', function () {
    return response()->json(['service' => 'fixture-laravel-app']);
});

Route::get('/health', [HealthController::class, 'show']);

Route::prefix('api/v1')->group(function () {
    Route::get('/ping', [HealthController::class, 'ping']);
});
