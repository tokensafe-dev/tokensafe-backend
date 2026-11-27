<?php

use App\Http\Controllers\OrderController;
use App\Http\Controllers\PhotoController;
use App\Http\Controllers\UserController;
use Illuminate\Support\Facades\Route;

/*
 * Application API surface.
 *
 * Real Laravel registers this file from bootstrap/app.php; the OUMV PHP sidecar
 * never boots the framework, it scans the route registration statically. Every
 * shape exercised here (verb calls, array handlers, string handlers inside a
 * Route::controller group, match(), prefixes, apiResource/resource) is what the
 * scanner must resolve to a controller file and method range.
 */

Route::get('/users', [UserController::class, 'index']);
Route::get('/users/{id}', [UserController::class, 'show'])->where('id', '[0-9]+');
Route::post('/users', [UserController::class, 'store']);
Route::match(['get', 'post'], '/users/search', [UserController::class, 'search']);
Route::delete('/users/{user}', [UserController::class, 'destroy']);

Route::prefix('admin')->group(function () {
    Route::get('/stats', [UserController::class, 'stats']);

    Route::controller(UserController::class)->group(function () {
        Route::get('/profile', 'profile');
        Route::put('/profile', 'updateProfile');
    });
});

Route::apiResource('orders', OrderController::class);
Route::resource('photos', PhotoController::class);
