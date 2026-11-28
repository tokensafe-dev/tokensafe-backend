<?php

namespace App\Http\Middleware;

use Closure;
use Illuminate\Http\Request;
use Symfony\Component\HttpFoundation\Response;

/**
 * Keeps the certification probe JSON stable on a PHP newer than the framework
 * officially supports: Laravel turns any E_WARNING into a 500, and the session
 * interfaces declare one such warning on PHP 8.6. The fixture serves stateless
 * JSON and has no session, database or views, so masking warnings and
 * deprecations here changes nothing else.
 */
class QuietDeprecations
{
    public function handle(Request $request, Closure $next): Response
    {
        error_reporting(E_ALL & ~E_WARNING & ~E_DEPRECATED & ~E_USER_DEPRECATED);

        return $next($request);
    }
}
