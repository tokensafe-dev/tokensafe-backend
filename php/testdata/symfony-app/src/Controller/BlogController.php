<?php

namespace App\Controller;

use Symfony\Bundle\FrameworkBundle\Controller\AbstractController;
use Symfony\Component\HttpFoundation\JsonResponse;
use Symfony\Component\HttpFoundation\Request;
use Symfony\Component\Routing\Attribute\Route;

/**
 * Attribute-routed Symfony controller.
 *
 * A class-level #[Route] is the prefix every method-level route inherits; the
 * OUMV PHP sidecar joins the two exactly like Symfony's attribute loader does.
 */
#[Route('/blog')]
class BlogController extends AbstractController
{
    #[Route('/', name: 'blog_index', methods: ['GET'])]
    public function index(): JsonResponse
    {
        return $this->json(['data' => []]);
    }

    #[Route('/{slug}', name: 'blog_show', methods: ['GET'])]
    public function show(string $slug): JsonResponse
    {
        return $this->json(['slug' => $slug]);
    }

    #[Route('', name: 'blog_create', methods: ['POST'])]
    public function create(Request $request): JsonResponse
    {
        return $this->json(['created' => true], 201);
    }

    #[Route('/{id}', name: 'blog_update', methods: ['PUT', 'PATCH'])]
    public function update(Request $request, string $id): JsonResponse
    {
        return $this->json(['updated' => true]);
    }

    #[Route('/{id}', name: 'blog_delete', methods: ['DELETE'])]
    public function delete(string $id): JsonResponse
    {
        return $this->json(['deleted' => true]);
    }
}
