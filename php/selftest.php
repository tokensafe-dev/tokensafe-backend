#!/usr/bin/env php
<?php

/**
 * Direct assertions for the OUMV PHP sidecar (no Composer, no test framework).
 *
 *   php php/selftest.php <workspace-copy> [adapter-path]
 *
 * The workspace is a throw-away copy of `testdata/laravel-app`; the script
 * mutates it on purpose (patches, OCC, delete, rename) and asserts on the wire
 * results plus the bytes left on disk. Run it through `selftest.sh`, which makes
 * the copy.
 */

declare(strict_types=1);

$root = $argv[1] ?? '';
$adapter = $argv[2] ?? (__DIR__ . '/bin/oumv-php');
$phpBinary = PHP_BINARY;
$failures = 0;
$checks = 0;

if ($root === '' || !is_dir($root)) {
    fwrite(STDERR, "usage: php selftest.php <workspace-copy> [adapter-path]\n");
    exit(2);
}
if (!is_file($adapter)) {
    fwrite(STDERR, "adapter not found: {$adapter}\n");
    exit(2);
}

$descriptors = [0 => ['pipe', 'r'], 1 => ['pipe', 'w'], 2 => ['pipe', 'w']];
$process = proc_open([$phpBinary, $adapter, '--root', $root, '--stdio'], $descriptors, $pipes);
if (!is_resource($process)) {
    fwrite(STDERR, "cannot start the adapter\n");
    exit(2);
}

$stderrPath = sys_get_temp_dir() . '/oumv-selftest-' . getmypid() . '.err';
$stderrSink = fopen($stderrPath, 'w');

function rpc(string $method, array $params = []): array
{
    global $pipes;
    static $id = 0;
    $request = ['jsonrpc' => '2.0', 'id' => ++$id, 'method' => $method, 'params' => $params];
    fwrite($pipes[0], json_encode($request, JSON_UNESCAPED_SLASHES) . "\n");
    fflush($pipes[0]);

    $read = [$pipes[1]];
    $write = null;
    $except = null;
    if (@stream_select($read, $write, $except, 20) !== 1) {
        return ['jsonrpc' => '2.0', 'id' => $id, 'error' => ['code' => 0, 'message' => 'timeout waiting for the adapter']];
    }
    $line = fgets($pipes[1]);
    if ($line === false) {
        return ['jsonrpc' => '2.0', 'id' => $id, 'error' => ['code' => 0, 'message' => 'adapter closed stdout']];
    }
    $decoded = json_decode(trim($line), true);
    return is_array($decoded) ? $decoded : ['jsonrpc' => '2.0', 'id' => $id, 'error' => ['code' => 0, 'message' => 'unparseable response: ' . $line]];
}

function check(string $name, bool $condition, string $detail = ''): void
{
    global $failures, $checks;
    $checks++;
    if ($condition) {
        fwrite(STDOUT, "  ok   {$name}\n");
        return;
    }
    $failures++;
    fwrite(STDOUT, "  FAIL {$name}" . ($detail !== '' ? ' — ' . $detail : '') . "\n");
}

function result(array $response): array
{
    return is_array($response['result'] ?? null) ? $response['result'] : [];
}

function errorCode(array $response): int
{
    return (int) ($response['error']['code'] ?? 0);
}

function errorData(array $response): array
{
    $data = $response['error']['data'] ?? [];
    return is_array($data) ? $data : [];
}

function occ(string $bytes): string
{
    return 'sha256:' . hash('sha256', $bytes);
}

function writeProbe(string $relative, string $content): void
{
    global $root;
    $path = $root . '/' . $relative;
    if (!is_dir(dirname($path))) {
        mkdir(dirname($path), 0755, true);
    }
    file_put_contents($path, $content);
}

function readProbe(string $relative): string
{
    global $root;
    $path = $root . '/' . $relative;
    return is_file($path) ? (string) file_get_contents($path) : '';
}

function finish(): void
{
    global $pipes, $process, $stderrSink, $checks, $failures, $stderrPath;
    fclose($pipes[0]);
    fclose($pipes[1]);
    fclose($stderrSink);
    $exitCode = proc_close($process);
    check('adapter exits cleanly after EOF', $exitCode === 0, (string) $exitCode);
    fwrite(STDOUT, "\n{$checks} checks, {$failures} failure(s)\n");
    if ($failures > 0) {
        fwrite(STDERR, 'adapter stderr: ' . (string) @file_get_contents($stderrPath) . "\n");
    }
    @unlink($stderrPath);
    exit($failures === 0 ? 0 : 1);
}

// --- Symfony attribute routing (scan-only mode) ---------------------------

if (($argv[3] ?? '') === '--symfony') {
    $hs = result(rpc('oumv/handshake', ['protocolVersion' => '1.3.0']));
    check('symfony: handshake serves the backend profile', ($hs['profile'] ?? '') === 'backend');
    $l0 = result(rpc('oumv/observe', ['level' => 'L0']));
    check('symfony: framework detected from composer.json', ($l0['data']['framework'] ?? '') === 'symfony', (string) ($l0['data']['framework'] ?? ''));
    $l1 = result(rpc('oumv/observe', ['level' => 'L1']));
    $routes = [];
    foreach (($l1['data']['routes'] ?? []) as $route) {
        $routes[$route['method'] . ' ' . $route['path']] = $route['handlerName'];
    }
    check('symfony: 9 attribute routes discovered', count($routes) === 9, (string) count($routes));
    $expected = [
        'GET /blog' => 'BlogController#index',
        'GET /blog/{slug}' => 'BlogController#show',
        'POST /blog' => 'BlogController#create',
        'PUT /blog/{id}' => 'BlogController#update',
        'PATCH /blog/{id}' => 'BlogController#update',
        'DELETE /blog/{id}' => 'BlogController#delete',
        'GET /health' => 'HealthController#show',
        'GET /health/deep' => 'HealthController#deep',
        'HEAD /health/deep' => 'HealthController#deep',
    ];
    foreach ($expected as $key => $handler) {
        check('symfony: ' . $key . ' -> ' . $handler, ($routes[$key] ?? 'missing') === $handler, (string) ($routes[$key] ?? 'missing'));
    }
    $l2 = result(rpc('oumv/observe', ['level' => 'L2', 'target' => 'GET /blog/{slug}']));
    check('symfony: L2 carries the attribute route name', ($l2['data']['routeName'] ?? '') === 'blog_show', (string) ($l2['data']['routeName'] ?? ''));
    check('symfony: L2 reports static evidence', ($l2['data']['evidence']['provenance'] ?? '') === 'static');
    $l3 = result(rpc('oumv/observe', ['level' => 'L3', 'target' => 'GET /blog']));
    check('symfony: L3 resolves class+method source', ($l3['data']['symbol'] ?? '') === 'BlogController#index' && str_contains((string) ($l3['data']['source'] ?? ''), 'public function index'));
    check('symfony: L3 keeps the whole-file OCC version', preg_match('/^sha256:[0-9a-f]{64}$/', (string) ($l3['data']['version'] ?? '')) === 1);
    finish();
}

// --- handshake -------------------------------------------------------------

$handshake = rpc('oumv/handshake', ['protocolVersion' => '1.0.0']);
$hs = result($handshake);
check('handshake returns result', isset($handshake['result']));
check('handshake protocolVersion is 1.3.0', ($hs['protocolVersion'] ?? '') === '1.3.0', (string) ($hs['protocolVersion'] ?? ''));
check('handshake profile is backend', ($hs['profile'] ?? '') === 'backend');
check('handshake names runtimeInfo', ($hs['runtimeInfo']['name'] ?? '') === 'oumv-php' && ($hs['runtimeInfo']['version'] ?? '') !== '');
check('handshake issues sessionId', is_string($hs['sessionId'] ?? null) && str_starts_with((string) $hs['sessionId'], 'sess-'));
check('handshake advertises L0-L3 only', ($hs['capabilities']['observe']['levels'] ?? []) === ['L0', 'L1', 'L2', 'L3']);
check('handshake does not advertise causality', ($hs['capabilities']['understand']['causality'] ?? true) === false);
check('handshake advertises OCC + syntaxGuard', ($hs['capabilities']['mutate']['occ'] ?? false) === true && ($hs['capabilities']['mutate']['syntaxGuard'] ?? false) === true);

$badVersion = rpc('oumv/handshake', ['protocolVersion' => '9.9.9']);
check('handshake rejects MAJOR 9 with -32006', errorCode($badVersion) === -32006, (string) errorCode($badVersion));
$narrowed = result(rpc('oumv/handshake', ['protocolVersion' => '1.1.0', 'requestedCapabilities' => ['observe' => ['levels' => ['L1', 'L2'], 'formats' => ['json']]]]));
check('handshake narrows requested observe levels', ($narrowed['capabilities']['observe']['levels'] ?? []) === ['L1', 'L2']);

// --- guards and error codes ------------------------------------------------

check('unknown session is -32011', errorCode(rpc('oumv/observe', ['level' => 'L1', 'sessionId' => 'sess-unknown'])) === -32011);
check('unknown method is -32601', errorCode(rpc('oumv/nonexistentMethod')) === -32601);
check('unknown observe level is -32006', errorCode(rpc('oumv/observe', ['level' => 'L9'])) === -32006);
check('L2 without target is -32602', errorCode(rpc('oumv/observe', ['level' => 'L2'])) === -32602);
check('L3 without target is -32602', errorCode(rpc('oumv/observe', ['level' => 'L3'])) === -32602);
check('L2 unknown target is -32009', errorCode(rpc('oumv/observe', ['level' => 'L2', 'target' => 'GET /nope'])) === -32009);
check('unknown understand query is -32006', errorCode(rpc('oumv/understand', ['query' => 'vibes', 'target' => 'GET /users'])) === -32006);

// --- observe L0/L1 ---------------------------------------------------------

$l0 = result(rpc('oumv/observe', ['level' => 'L0']));
$l0data = $l0['data'] ?? [];
check('L0 carries the required pulse fields', isset($l0data['status'], $l0data['uptimeSeconds'], $l0data['activeRoutes'], $l0data['errorCount1m'], $l0data['framework']));
check('L0 detects the laravel framework', ($l0data['framework'] ?? '') === 'laravel', (string) ($l0data['framework'] ?? ''));
$routeCount = (int) ($l0data['activeRoutes'] ?? 0);
check('L0 counts 26 detected routes', $routeCount === 26, (string) $routeCount);

$l1 = result(rpc('oumv/observe', ['level' => 'L1']));
$routes = $l1['data']['routes'] ?? [];
$byKey = [];
foreach ($routes as $route) {
    $byKey[$route['method'] . ' ' . $route['path']] = $route['handlerName'];
}
check('L1 exposes 26 routes', count($routes) === 26, (string) count($routes));
check('L1 resolves the group prefix /admin/stats', ($byKey['GET /admin/stats'] ?? '') === 'UserController#stats');
check('L1 resolves controller-group strings /admin/profile', ($byKey['GET /admin/profile'] ?? '') === 'UserController#profile');
check('L1 expands apiResource /orders', ($byKey['GET /orders'] ?? '') === 'OrderController#index' && ($byKey['DELETE /orders/{order}'] ?? '') === 'OrderController#destroy');
check('L1 expands resource /photos', ($byKey['GET /photos/create'] ?? '') === 'PhotoController#create' && ($byKey['GET /photos/{photo}/edit'] ?? '') === 'PhotoController#edit');
check('L1 normalizes the optional parameter /users/{id}', ($byKey['GET /users/{id}'] ?? '') === 'UserController#show');
check('L1 keeps the closure route', ($byKey['GET /'] ?? '') === '{closure}');
check('L1 reports middlewares as an array', is_array($routes[0]['middlewares'] ?? null));

$page1 = result(rpc('oumv/observe', ['level' => 'L1', 'limit' => 5]));
check('L1 limit sets hasMore', ($page1['data']['hasMore'] ?? null) === true);
check('L1 limit sets nextCursor', is_string($page1['data']['nextCursor'] ?? null));
$page2 = result(rpc('oumv/observe', ['level' => 'L1', 'limit' => 5, 'cursor' => (string) ($page1['data']['nextCursor'] ?? '')]));
check('L1 cursor advances the page', ($page2['data']['routes'][0]['path'] ?? '') !== ($page1['data']['routes'][0]['path'] ?? ''));

// --- observe L2/L3 ---------------------------------------------------------

$l2 = result(rpc('oumv/observe', ['level' => 'L2', 'target' => 'GET /health']));
check('L2 echoes the target', ($l2['data']['target'] ?? '') === 'GET /health');
check('L2 reports method/path/handler', ($l2['data']['method'] ?? '') === 'GET' && ($l2['data']['path'] ?? '') === '/health' && ($l2['data']['handlerName'] ?? '') === 'HealthController#show');
check('L2 carries static evidence', ($l2['data']['evidence']['provenance'] ?? '') === 'static');
check('L2 resolves the handler file', ($l2['data']['sourceLocation']['filePath'] ?? '') === 'app/Http/Controllers/HealthController.php');

$l3 = result(rpc('oumv/observe', ['level' => 'L3', 'target' => 'GET /health']));
$healthSource = readProbe('app/Http/Controllers/HealthController.php');
check('L3 symbol names the handler', ($l3['data']['symbol'] ?? '') === 'HealthController#show');
check('L3 source is the handler body', str_contains((string) ($l3['data']['source'] ?? ''), 'return response()->json(['));
check('L3 version hashes the whole file', ($l3['data']['version'] ?? '') === occ($healthSource));
check('L3 startLine/endLine are handler lines', ($l3['data']['startLine'] ?? 0) > 0 && ($l3['data']['endLine'] ?? 0) >= ($l3['data']['startLine'] ?? 0));

$l3file = result(rpc('oumv/observe', ['level' => 'L3', 'target' => 'app/Http/Controllers/UserController.php#index']));
check('L3 file#symbol returns that method', str_contains((string) ($l3file['data']['source'] ?? ''), 'public function index()'));
check('L3 file#symbol reports the symbol', ($l3file['data']['symbol'] ?? '') === 'index');

// --- getSource -------------------------------------------------------------

$probePath = 'conformance/edit-target.php';
$probeOriginal = readProbe($probePath);
$source = result(rpc('oumv/getSource', ['path' => $probePath]));
check('getSource returns source', ($source['source'] ?? '') === $probeOriginal, json_encode($source['source'] ?? null));
check('getSource returns the whole-file sha256', ($source['version'] ?? '') === occ($probeOriginal));
check('getSource startLine is 1-based inclusive', ($source['startLine'] ?? 0) === 1 && ($source['endLine'] ?? 0) === 4);

$window = result(rpc('oumv/getSource', ['path' => $probePath, 'startLine' => 3, 'endLine' => 3]));
check('getSource window returns the requested lines', ($window['source'] ?? '') === '$value = 41;', json_encode($window['source'] ?? null));
check('getSource window keeps the whole-file version', ($window['version'] ?? '') === occ($probeOriginal));
$clamped = result(rpc('oumv/getSource', ['path' => $probePath, 'startLine' => 2, 'endLine' => 999]));
check('getSource clamps an endLine past EOF', ($clamped['endLine'] ?? 0) === 4);

$dedup = result(rpc('oumv/getSource', ['path' => $probePath, 'ifVersion' => occ($probeOriginal)]));
check('getSource ifVersion answers unchanged', ($dedup['unchanged'] ?? false) === true);
check('getSource ifVersion sends no source', !array_key_exists('source', $dedup));

check('getSource unknown symbol is -32009', errorCode(rpc('oumv/getSource', ['path' => $probePath, 'symbol' => 'NoSuchSymbol'])) === -32009);
check('getSource missing file is -32009', errorCode(rpc('oumv/getSource', ['path' => 'conformance/missing.php'])) === -32009);
check('getSource traversal is -32003', errorCode(rpc('oumv/getSource', ['path' => '../outside.php'])) === -32003);

// --- applyEdit: guards -----------------------------------------------------

check('applyEdit missing file is -32009', errorCode(rpc('oumv/applyEdit', ['path' => 'conformance/missing.php', 'replacement' => "<?php\n", 'expectedVersion' => occ('')])) === -32009);
check('applyEdit traversal is -32003', errorCode(rpc('oumv/applyEdit', ['path' => '../outside.php', 'replacement' => "<?php\n"])) === -32003);
check('applyEdit .env is -32003', errorCode(rpc('oumv/applyEdit', ['path' => '.env', 'replacement' => 'x'])) === -32003);
check('applyEdit without expectedVersion is -32011', errorCode(rpc('oumv/applyEdit', ['path' => $probePath, 'replacement' => "<?php\n\n\$value = 1;\n"])) === -32011);
check('applyEdit stale version is -32001', errorCode(rpc('oumv/applyEdit', ['path' => $probePath, 'replacement' => "<?php\n\n\$value = 1;\n", 'expectedVersion' => 'sha256:' . str_repeat('0', 64)])) === -32001);
check('applyEdit broken code is -32002', errorCode(rpc('oumv/applyEdit', ['path' => $probePath, 'replacement' => "<?php\n\nfunction broken( {\n", 'expectedVersion' => occ($probeOriginal)])) === -32002);
check('applyEdit broken code wrote no bytes', readProbe($probePath) === $probeOriginal);
check('applyEdit unknown mode is -32006', errorCode(rpc('oumv/applyEdit', ['path' => $probePath, 'replacement' => "<?php\n", 'expectedVersion' => occ($probeOriginal), 'mode' => 'splice'])) === -32006);

// --- applyEdit: patch ------------------------------------------------------

writeProbe('conformance/patch-unique.php', "<?php\n\n\$status = 'draft';\n");
$patchOriginal = readProbe('conformance/patch-unique.php');
$patched = result(rpc('oumv/applyEdit', [
    'path' => 'conformance/patch-unique.php',
    'mode' => 'patch',
    'expectedVersion' => occ($patchOriginal),
    'hunks' => [['target' => "'draft'", 'replacement' => "'published'"]],
]));
check('patch applies a unique hunk', ($patched['status'] ?? '') === 'APPLIED', json_encode($patched));
check('patch reports hunksApplied', ($patched['hunksApplied'] ?? 0) === 1);
$patchedBytes = readProbe('conformance/patch-unique.php');
check('patch wrote the spliced bytes', $patchedBytes === str_replace("'draft'", "'published'", $patchOriginal));
check('patch newVersion matches the bytes on disk', ($patched['newVersion'] ?? '') === occ($patchedBytes));
check('patch syntaxCheck passed', ($patched['syntaxCheck']['status'] ?? '') === 'passed');
check('patch keeps a backup outside the source directory', is_string($patched['backupPath'] ?? null) && str_contains((string) $patched['backupPath'], '/.tokensafe/backups/') && dirname((string) $patched['backupPath']) !== dirname($root . '/conformance/patch-unique.php'));
check('patch backup holds the previous bytes', (string) @file_get_contents((string) ($patched['backupPath'] ?? '')) === $patchOriginal);
check('patch leaves no temporary files', !glob($root . '/conformance/.patch-unique.php.oumv-*.tmp'));

writeProbe('conformance/patch-ambiguous.php', "<?php\n\n\$a = 'same';\n\$b = 'same';\n");
$ambiguousOriginal = readProbe('conformance/patch-ambiguous.php');
$ambiguous = rpc('oumv/applyEdit', [
    'path' => 'conformance/patch-ambiguous.php',
    'mode' => 'patch',
    'expectedVersion' => occ($ambiguousOriginal),
    'hunks' => [['target' => "'same'", 'replacement' => "'other'"]],
]);
check('patch ambiguous target is -32011', errorCode($ambiguous) === -32011, (string) errorCode($ambiguous));
check('patch ambiguous reason is target_not_unique', (errorData($ambiguous)['reason'] ?? '') === 'target_not_unique');
check('patch ambiguous reports the occurrence count', (errorData($ambiguous)['count'] ?? 0) === 2);
check('patch ambiguous reports the occurrence lines', (errorData($ambiguous)['lines'] ?? []) === [3, 4], json_encode(errorData($ambiguous)['lines'] ?? null));
check('patch ambiguous wrote no bytes', readProbe('conformance/patch-ambiguous.php') === $ambiguousOriginal);

$narrowedPatch = result(rpc('oumv/applyEdit', [
    'path' => 'conformance/patch-ambiguous.php',
    'mode' => 'patch',
    'expectedVersion' => occ($ambiguousOriginal),
    'hunks' => [['target' => "'same'", 'replacement' => "'other'", 'startLine' => 4, 'endLine' => 4]],
]));
check('patch startLine/endLine narrows the search', ($narrowedPatch['status'] ?? '') === 'APPLIED');
check('patch narrowed only the second occurrence', readProbe('conformance/patch-ambiguous.php') === str_replace("\$b = 'same';", "\$b = 'other';", $ambiguousOriginal));

writeProbe('conformance/patch-replace-all.php', "<?php\n\n\$a = 'x';\n\$b = 'x';\n");
$replaceAllOriginal = readProbe('conformance/patch-replace-all.php');
$replaceAll = result(rpc('oumv/applyEdit', [
    'path' => 'conformance/patch-replace-all.php',
    'mode' => 'patch',
    'expectedVersion' => occ($replaceAllOriginal),
    'hunks' => [['target' => "'x'", 'replacement' => "'y'", 'replaceAll' => true]],
]));
check('patch replaceAll rewrites every occurrence', ($replaceAll['status'] ?? '') === 'APPLIED' && substr_count(readProbe('conformance/patch-replace-all.php'), "'y'") === 2);

writeProbe('conformance/patch-atomic.php', "<?php\n\n\$value = 1;\n");
$atomicOriginal = readProbe('conformance/patch-atomic.php');
$atomic = rpc('oumv/applyEdit', [
    'path' => 'conformance/patch-atomic.php',
    'mode' => 'patch',
    'expectedVersion' => occ($atomicOriginal),
    'hunks' => [
        ['target' => '$value = 1;', 'replacement' => '$value = 2;'],
        ['target' => 'this-snippet-does-not-exist', 'replacement' => 'x'],
    ],
]);
check('patch fails atomically with -32009', errorCode($atomic) === -32009);
check('patch atomic failure wrote no bytes', readProbe('conformance/patch-atomic.php') === $atomicOriginal);

$stale = rpc('oumv/applyEdit', [
    'path' => 'conformance/patch-atomic.php',
    'mode' => 'patch',
    'expectedVersion' => 'sha256:' . str_repeat('0', 64),
    'hunks' => [['target' => '$value = 1;', 'replacement' => '$value = 2;']],
]);
check('patch rejects a stale version with -32001', errorCode($stale) === -32001);
check('patch without hunks is -32011', errorCode(rpc('oumv/applyEdit', ['path' => 'conformance/patch-atomic.php', 'mode' => 'patch', 'expectedVersion' => occ($atomicOriginal)])) === -32011);

// --- applyEdit: replace / symbol / create / delete / rename ----------------

$replaceTarget = 'conformance/replace-target.php';
writeProbe($replaceTarget, "<?php\n\n\$value = 41;\n");
$replaceOriginal = readProbe($replaceTarget);
$replaced = result(rpc('oumv/applyEdit', ['path' => $replaceTarget, 'replacement' => "<?php\n\n\$value = 42;\n", 'expectedVersion' => occ($replaceOriginal)]));
check('replace applies with a matching version', ($replaced['status'] ?? '') === 'APPLIED');
check('replace returns newVersion for the written bytes', ($replaced['newVersion'] ?? '') === occ("<?php\n\n\$value = 42;\n"));
check('replace keeps a backup', is_string($replaced['backupPath'] ?? null) && is_file((string) $replaced['backupPath']));

$symbolTarget = 'conformance/symbol-probe.php';
writeProbe($symbolTarget, "<?php\n\nfunction value(): int\n{\n    return 41;\n}\n");
$symbolOriginal = readProbe($symbolTarget);
$symbolEdit = result(rpc('oumv/applyEdit', [
    'path' => $symbolTarget,
    'mode' => 'symbol',
    'symbol' => 'value',
    'replacement' => "function value(): int\n{\n    return 42;\n}",
    'expectedVersion' => occ($symbolOriginal),
]));
check('symbol edit replaces only the symbol', readProbe($symbolTarget) === "<?php\n\nfunction value(): int\n{\n    return 42;\n}\n");
check('symbol edit keeps the file prefix and suffix', str_starts_with(readProbe($symbolTarget), '<?php'));
check('symbol edit returns the new version', ($symbolEdit['newVersion'] ?? '') === occ(readProbe($symbolTarget)));
check('symbol edit without symbol is -32011', errorCode(rpc('oumv/applyEdit', ['path' => $symbolTarget, 'mode' => 'symbol', 'replacement' => 'x', 'expectedVersion' => occ(readProbe($symbolTarget))])) === -32011);
check('symbol edit unknown symbol is -32009', errorCode(rpc('oumv/applyEdit', ['path' => $symbolTarget, 'mode' => 'symbol', 'symbol' => 'nope', 'replacement' => 'x', 'expectedVersion' => occ(readProbe($symbolTarget))])) === -32009);

$created = result(rpc('oumv/applyEdit', ['path' => 'conformance/created.php', 'mode' => 'create', 'replacement' => "<?php\n\n\$new = true;\n"]));
check('create writes a new file', ($created['created'] ?? false) === true && is_file($root . '/conformance/created.php'));
check('create returns the content version', ($created['newVersion'] ?? '') === occ("<?php\n\n\$new = true;\n"));
check('create refuses an existing file with -32001', errorCode(rpc('oumv/applyEdit', ['path' => 'conformance/created.php', 'mode' => 'create', 'replacement' => "<?php\n"])) === -32001);
check('create forbids expectedVersion with -32011', errorCode(rpc('oumv/applyEdit', ['path' => 'conformance/created2.php', 'mode' => 'create', 'replacement' => "<?php\n", 'expectedVersion' => occ('x')])) === -32011);

writeProbe('conformance/delete-probe.php', "<?php\n\n\$gone = true;\n");
$deleteVersion = occ(readProbe('conformance/delete-probe.php'));
check('delete forbids replacement with -32011', errorCode(rpc('oumv/applyEdit', ['path' => 'conformance/delete-probe.php', 'mode' => 'delete', 'replacement' => 'x', 'expectedVersion' => $deleteVersion])) === -32011);
$deleted = result(rpc('oumv/applyEdit', ['path' => 'conformance/delete-probe.php', 'mode' => 'delete', 'expectedVersion' => $deleteVersion]));
check('delete removes the file', ($deleted['deleted'] ?? false) === true && !is_file($root . '/conformance/delete-probe.php'));
check('delete reports a skipped syntax check', ($deleted['syntaxCheck']['status'] ?? '') === 'skipped');

writeProbe('conformance/rename-probe.php', "<?php\n\n\$moved = true;\n");
$renameVersion = occ(readProbe('conformance/rename-probe.php'));
$renamed = result(rpc('oumv/applyEdit', ['path' => 'conformance/rename-probe.php', 'mode' => 'rename', 'renameTo' => 'conformance/renamed-probe.php', 'expectedVersion' => $renameVersion]));
check('rename moves the file', ($renamed['newPath'] ?? '') === 'conformance/renamed-probe.php' && is_file($root . '/conformance/renamed-probe.php'));
check('rename without renameTo is -32011', errorCode(rpc('oumv/applyEdit', ['path' => 'conformance/renamed-probe.php', 'mode' => 'rename', 'expectedVersion' => occ(readProbe('conformance/renamed-probe.php'))])) === -32011);
check('rename onto an existing file is -32001', errorCode(rpc('oumv/applyEdit', ['path' => 'conformance/renamed-probe.php', 'mode' => 'rename', 'renameTo' => 'conformance/created.php', 'expectedVersion' => occ(readProbe('conformance/renamed-probe.php'))])) === -32001);
check('rename to a traversal path is -32003', errorCode(rpc('oumv/applyEdit', ['path' => 'conformance/renamed-probe.php', 'mode' => 'rename', 'renameTo' => '../escape.php', 'expectedVersion' => occ(readProbe('conformance/renamed-probe.php'))])) === -32003);

// --- route catalog: array-form groups, resource filters, invalidation ------

$extraRoutes = <<<'PHP'
<?php

use App\Http\Controllers\UserController;
use Illuminate\Support\Facades\Route;

Route::group(['prefix' => 'v2'], function () {
    Route::get('/users', [UserController::class, 'index']);
});

Route::resource('widgets', UserController::class)->only(['index', 'show']);
PHP;

$createdRoutes = result(rpc('oumv/applyEdit', ['path' => 'routes/extra.php', 'mode' => 'create', 'replacement' => $extraRoutes]));
check('create accepts a new route file', ($createdRoutes['created'] ?? false) === true, json_encode($createdRoutes));
$l1After = result(rpc('oumv/observe', ['level' => 'L1']));
$after = [];
foreach (($l1After['data']['routes'] ?? []) as $route) {
    $after[$route['method'] . ' ' . $route['path']] = $route['handlerName'];
}
check('array-form group prefix is applied', ($after['GET /v2/users'] ?? '') === 'UserController#index', json_encode(array_keys($after)));
check('resource only() filters the actions', isset($after['GET /widgets'], $after['GET /widgets/{widget}']) && !isset($after['GET /widgets/create']) && !isset($after['DELETE /widgets/{widget}']));
check('catalog invalidates after a mutation', ($l1After['data']['total'] ?? 0) === 29, (string) ($l1After['data']['total'] ?? 0));

// --- verify ---------------------------------------------------------------

$verify = result(rpc('oumv/verify', ['target' => 'GET /health', 'action' => 'conformance-probe']));
check('verify returns a status', in_array($verify['status'] ?? '', ['passed', 'failed'], true));
check('verify reports real timings', is_numeric($verify['timings']['t_total_ms'] ?? null) && ($verify['timings']['t_total_ms'] ?? -1) >= 0);
check('verify states what it checked', is_array($verify['scope']['checked'] ?? null) && ($verify['scope']['checked'] ?? []) !== []);
check('verify states what it did not execute', in_array('runtime dispatch of the request (no live PHP application attached)', $verify['scope']['notExecuted'] ?? [], true));
check('verify resolves a route target to its handler file', ($verify['diff']['checkedFile'] ?? '') === 'app/Http/Controllers/HealthController.php', json_encode($verify['diff'] ?? []));
check('verify passes on a valid handler', ($verify['status'] ?? '') === 'passed', json_encode($verify['diff'] ?? []));

$verifyFile = result(rpc('oumv/verify', ['target' => 'app/Http/Controllers/UserController.php']));
check('verify accepts a plain workspace file', ($verifyFile['status'] ?? '') === 'passed' && ($verifyFile['diff']['checkedFile'] ?? '') === 'app/Http/Controllers/UserController.php');

$multi = result(rpc('oumv/verify', ['target' => 'GET /health', 'steps' => [
    ['target' => 'GET /health'],
    ['target' => 'GET /users'],
]]));
check('verify multi-step returns one entry per step', count($multi['steps'] ?? []) === 2);
check('verify multi-step reports a status', in_array($multi['status'] ?? '', ['passed', 'failed'], true));

$verifyMissing = result(rpc('oumv/verify', ['target' => 'GET /no-such-route']));
check('verify of an unknown target fails instead of pretending', ($verifyMissing['status'] ?? '') === 'failed' && str_contains((string) ($verifyMissing['diff']['error'] ?? ''), 'matches no route'), json_encode($verifyMissing));

// --- understand / L4 ------------------------------------------------------

$impact = result(rpc('oumv/understand', ['query' => 'impact', 'target' => 'GET /users']));
check('impact echoes the target', ($impact['impact']['target'] ?? '') === 'GET /users');
check('impact lists the affected handler', in_array('UserController#index', $impact['impact']['affectedSymbols'] ?? [], true));
check('impact is honest about evidence', in_array($impact['impact']['evidence']['provenance'] ?? '', ['static', 'runtime', 'contract', 'inferred', 'external'], true));

$causality = result(rpc('oumv/understand', ['query' => 'causality', 'target' => 'GET /health']));
check('causality is not observed without traces', ($causality['causality']['observed'] ?? true) === false);
check('causality explains the missing evidence', strlen((string) ($causality['causality']['note'] ?? '')) > 20);
check('causality never fabricates a root cause', ($causality['causality']['rootCause'] ?? '') === 'unknown');

$l4 = result(rpc('oumv/observe', ['level' => 'L4']));
check('L4 returns an empty trace collection, never invented traces', ($l4['data']['traces'] ?? null) === [] && ($l4['data']['traceCount'] ?? -1) === 0);
check('L4 explains why there are no traces', str_contains((string) ($l4['data']['note'] ?? ''), 'no request traces'));

// --- descriptor / notifications ------------------------------------------

$descriptor = result(rpc('oumv/descriptor'));
check('descriptor is a backend discovery record', ($descriptor['type'] ?? '') === 'backend' && ($descriptor['capabilities']['transports'] ?? []) === ['stdio']);
check('descriptor never carries a session token', !array_key_exists('sessionToken', $descriptor));

$before = $checks;
fwrite($pipes[0], '{"jsonrpc":"2.0","method":"oumv/observe","params":{"level":"L0"}}' . "\n");
fflush($pipes[0]);
$read = [$pipes[1]];
$write = null;
$except = null;
$ready = @stream_select($read, $write, $except, 1);
check('a notification gets no response line', $ready === 0);

fwrite($pipes[0], "{not json\n");
fflush($pipes[0]);
$read = [$pipes[1]];
$ready = @stream_select($read, $write, $except, 5);
$parseLine = $ready === 1 ? json_decode(trim((string) fgets($pipes[1])), true) : null;
check('malformed JSON is -32700', (int) ($parseLine['error']['code'] ?? 0) === -32700);

// --- shutdown -------------------------------------------------------------

finish();
