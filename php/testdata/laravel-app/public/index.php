<?php

/*
 * Front controller for the certification fixture app: the real entry point
 * `php -S 127.0.0.1:<port> -t public` serves, so oumv/verify dispatches to a
 * live Laravel application rather than a stub.
 */

use Illuminate\Http\Request;

define('LARAVEL_START', microtime(true));

// The fixture runs on whatever PHP the developer has; deprecations from a newer
// PHP than the framework officially supports must not corrupt JSON responses.
ini_set('display_errors', '0');
error_reporting(E_ALL & ~E_DEPRECATED & ~E_USER_DEPRECATED);

require __DIR__ . '/../vendor/autoload.php';

$app = require_once __DIR__ . '/../bootstrap/app.php';

$app->handleRequest(Request::capture());
