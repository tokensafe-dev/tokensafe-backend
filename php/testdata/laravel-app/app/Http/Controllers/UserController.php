<?php

namespace App\Http\Controllers;

use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;

class UserController extends Controller
{
    public function index(): JsonResponse
    {
        return response()->json(['data' => []]);
    }

    public function show(string $id): JsonResponse
    {
        return response()->json(['data' => ['id' => $id]]);
    }

    public function store(Request $request): JsonResponse
    {
        return response()->json(['created' => true], 201);
    }

    public function search(Request $request): JsonResponse
    {
        return response()->json(['query' => $request->query('q')]);
    }

    public function destroy(string $id): JsonResponse
    {
        return response()->json(['deleted' => true]);
    }

    public function stats(): JsonResponse
    {
        return response()->json(['users' => 0]);
    }

    public function profile(): JsonResponse
    {
        return response()->json(['profile' => null]);
    }

    public function updateProfile(Request $request): JsonResponse
    {
        return response()->json(['updated' => true]);
    }
}
