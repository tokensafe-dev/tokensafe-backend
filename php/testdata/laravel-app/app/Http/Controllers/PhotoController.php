<?php

namespace App\Http\Controllers;

use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;

class PhotoController extends Controller
{
    public function index(): JsonResponse
    {
        return response()->json(['data' => []]);
    }

    public function create(): JsonResponse
    {
        return response()->json(['form' => 'create']);
    }

    public function store(Request $request): JsonResponse
    {
        return response()->json(['created' => true], 201);
    }

    public function show(string $photo): JsonResponse
    {
        return response()->json(['data' => ['id' => $photo]]);
    }

    public function edit(string $photo): JsonResponse
    {
        return response()->json(['form' => 'edit', 'id' => $photo]);
    }

    public function update(Request $request, string $photo): JsonResponse
    {
        return response()->json(['updated' => true]);
    }

    public function destroy(string $photo): JsonResponse
    {
        return response()->json(['deleted' => true]);
    }
}
