<?php

namespace App\Http\Controllers;

use Illuminate\Http\JsonResponse;

class HealthController extends Controller
{
    /**
     * Liveness probe. The OUMV certification target `GET /health` resolves here.
     */
    public function show(): JsonResponse
    {
        return response()->json([
            'status' => 'ok',
            'service' => 'fixture-laravel-app',
        ]);
    }

    public function ping(): JsonResponse
    {
        return response()->json(['pong' => true]);
    }
}
