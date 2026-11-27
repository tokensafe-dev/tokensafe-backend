<?php

namespace App\Controller;

use Symfony\Bundle\FrameworkBundle\Controller\AbstractController;
use Symfony\Component\HttpFoundation\JsonResponse;
use Symfony\Component\Routing\Attribute\Route;

class HealthController extends AbstractController
{
    #[Route('/health', name: 'health', methods: ['GET'])]
    public function show(): JsonResponse
    {
        return $this->json(['status' => 'ok']);
    }

    #[Route(path: '/health/deep', name: 'health_deep', methods: ['GET', 'HEAD'])]
    public function deep(): JsonResponse
    {
        return $this->json(['status' => 'ok', 'checks' => []]);
    }
}
