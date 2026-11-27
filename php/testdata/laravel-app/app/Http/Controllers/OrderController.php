<?php

namespace App\Http\Controllers;

use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;

class OrderController extends Controller
{
    public function index(): JsonResponse
    {
        return response()->json(['data' => []]);
    }

    public function store(Request $request): JsonResponse
    {
        return response()->json(['created' => true], 201);
    }

    public function show(string $order): JsonResponse
    {
        return response()->json(['data' => ['id' => $order]]);
    }

    public function update(Request $request, string $order): JsonResponse
    {
        return response()->json(['updated' => true]);
    }

    public function destroy(string $order): JsonResponse
    {
        return response()->json(['deleted' => true]);
    }
}
