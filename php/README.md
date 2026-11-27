# OUMV PHP sidecar (`oumv-php`)

A **dependency-free** PHP implementation of the OUMV backend profile (wire
`protocolVersion` **1.3.0**, spec `agent-oumv/spec/SPEC.md`). One file, no
Composer, no framework boot:

```
php/bin/oumv-php
```

It scans a Laravel or Symfony project statically, serves the OUMV wire over
**stdio** (newline-delimited JSON-RPC, spec §2.5) and **HTTP POST `/__oumv/rpc`**
(PHP built-in server router, spec §2.2), and mutates source under mandatory OCC,
a workspace jail, a `php -l` pre-flight and automatic backups.

## Honest conformance claim: **Level 2**

| Level | Result | Cases |
| :--- | :--- | :--- |
| 1 | passed | 9/9 |
| 2 | passed | 31/31 |
| 3 | **failed by design** | 0/1 — `observe-l4-returns-recorded-traces` |

Report: [`certification/php-laravel.json`](../certification/php-laravel.json)
(fixtures v7, spec 1.3.0, 32/33 cases).

Level 3 requires L4 request traces and runtime causality derived from real
executions. This sidecar has **no live PHP application** attached: `verify` runs
a static check, not a request dispatch. It therefore answers L4 with an empty
collection plus a note, and `understand(causality)` with `observed: false` and a
note — it never fabricates traces, root causes or latency. `understand` does not
advertise `causality` in the handshake and the adapter manifest does not list
`observe.traces` or `understand.causality`.

## Requirements

* PHP 8.1+ CLI (`php -v`); PHP 8.6.0 was used for the committed certification.
* `proc_open` enabled (used for `php -l` and the configured verify command).
* Nothing else. Composer, Laravel and Symfony are **not** installed or booted.

## Usage

### stdio (the certification transport)

```bash
php bin/oumv-php --root /path/to/app --stdio
```

One JSON-RPC object per line on stdin, one response line per request on stdout,
diagnostics on stderr.

### HTTP (PHP built-in server as router)

```bash
# explicit root through the environment (reliable)
OUMV_ROOT=/path/to/app php -S 127.0.0.1:8787 bin/oumv-php

# or: run from the project root and let cwd be the workspace
cd /path/to/app && php -S 127.0.0.1:8787 /path/to/bin/oumv-php

# the documented form also works: the flags are recovered from the server process
php -S 127.0.0.1:8787 bin/oumv-php --root /path/to/app
```

`php -S` does not populate `$argv` for router scripts, so `oumv-php` reads its
own server command line (`ps -o command= -p <pid>`) to recover `--root`,
`--token`, `--verify-cmd`, `--read-only` and `--register`. The `OUMV_*`
environment variables take precedence and avoid that extra process.

Endpoints: `POST /__oumv/rpc` (also `/oumv/rpc`), `GET /__oumv/health`,
`OPTIONS` (CORS preflight, 204). The router handles every request and never
returns `false`, so the built-in server never falls back to serving static files
from the docroot.

| Binding | Behaviour |
| :--- | :--- |
| `Content-Type` not `application/json` | HTTP 415 + `-32600` |
| wrong method (GET/PUT/…) | HTTP 405 + `-32600` |
| body > 8 MiB | HTTP 413 |
| malformed JSON | HTTP 200 + `-32700` |
| unknown path | HTTP 404 + `-32600` |
| `--token` set, token missing/wrong | HTTP 401 + `-32007` (`reason: unauthorized`) |
| non-loopback bind without `--token` | HTTP 403 + `-32007` (`reason: token_required_non_loopback`) |
| batch array | array response, notifications omitted |
| JSON-RPC errors | HTTP 200, `error` field carries the code |

### Flags

| Flag | Meaning |
| :--- | :--- |
| `--root <dir>` | workspace to scan (default: current directory) |
| `--stdio` | serve newline-delimited JSON-RPC on stdin/stdout |
| `--token <token>` | require `X-OUMV-Session-Token` or `Authorization: Bearer` |
| `--verify-cmd <cmd>` | command run by `oumv/verify`; placeholders `{file}` `{rel}` `{path}` `{root}` `{target}` (default: `<php binary> -l {file}`) |
| `--read-only` | refuse `oumv/applyEdit` with `-32007` |
| `--register` | write the spec §9.2 registry on handshake (HTTP mode) |
| `--print-descriptor` | print the discovery descriptor and exit |
| `--verbose`, `--help` | diagnostics / usage |

Env overrides for HTTP mode: `OUMV_ROOT`, `OUMV_TOKEN`, `OUMV_VERIFY_CMD`,
`OUMV_READ_ONLY=1`, `OUMV_REGISTER=1`.

## What is scanned

Recursive scan of `*.php` under `--root`, skipping `vendor/`, `node_modules/`,
`storage/`, `.git/`, `.tokensafe/`, `var/cache/` and files above 2 MiB. Files are
tokenized with `token_get_all()`; nothing is executed.

Framework detection: `composer.json` (`laravel/framework`, `symfony/framework-bundle`
or `symfony/routing`), then layout fallbacks (`routes/api.php`, `app/Http/Controllers`,
`src/Controller`, `config/routes.yaml`). Reported in L0 as `framework:
"laravel" | "symfony" | "plain"`.

### Laravel shapes

```php
Route::get('/users', [UserController::class, 'index']);
Route::post('/users', 'UserController@store');            // legacy @ syntax
Route::match(['get', 'post'], '/search', [C::class, 'search']);  // one entry per method
Route::any('/hook', [C::class, 'hook']);                  // method "ANY"
Route::delete('/users/{user}', [UserController::class, 'destroy']);

Route::prefix('admin')->group(function () { ... });        // nested prefixes compose
Route::group(['prefix' => 'admin'], function () { ... });  // array form
Route::controller(UserController::class)->group(function () {
    Route::get('/profile', 'profile');                     // handler UserController#profile
});
Route::middleware(['auth'])->group(function () { ... });   // middlewares ignored, []; prefix kept

Route::apiResource('orders', OrderController::class);      // index/store/show/update(PUT,PATCH)/destroy
Route::resource('photos', PhotoController::class);         // + create, edit
Route::resource('photos', PhotoController::class)->only(['index', 'show']);
Route::resource('photos', PhotoController::class)->except(['destroy']);

Route::get('/', function () { ... });                      // closure handler, sliced at its own range
```

Handlers are reported as `Class#method` (FQCNs are shortened). Closures are
reported as `{closure}` and resolve to the closure's own line range for L3.

### Symfony shapes

```php
#[Route('/blog')]                       // class-level prefix
class BlogController
{
    #[Route('/', name: 'blog_index', methods: ['GET'])]
    #[Route(path: '/{slug}', methods: ['GET', 'POST'])]
    #[Get('/health')]                   // Symfony 7 shortcut attributes
    public function index(): Response {}
}
```

Class-level `methods` act as the default for methods without their own list;
`#[Route]` without `methods` on both levels becomes method `ANY`.

### Path normalization

`{id}`, `{id?}`, `{id<\d+>}`, `{post:slug}` all normalize to `{id}`/`{post}`.
Prefixes and paths are joined with a single slash; a trailing slash is dropped
except for `/`. Route lists are ordered deterministically by path, method and
handler, which makes `limit`/`cursor` pagination stable.

## Wire surface

| Method | Implemented |
| :--- | :--- |
| `oumv/handshake` | `protocolVersion` 1.3.0, `profile: "backend"`, `runtimeInfo`, `capabilities`, `sessionId`, `ttlSeconds`; MAJOR≠1 or MINOR>3 → `-32006`; `requestedCapabilities.observe.{levels,formats}` narrows the advertised set |
| `oumv/observe` L0 | `{status, uptimeSeconds, activeRoutes, errorCount1m, framework, scannedFiles, note}` |
| `oumv/observe` L1 | `{routes, endpoints, total, framework, hasMore?, nextCursor?}` with `limit`/`cursor` |
| `oumv/observe` L2 | `{target, method, path, handlerName, middlewares, registration, sourceLocation, handlerSymbol, note, evidence}` |
| `oumv/observe` L3 | `{symbol, path, source, version: "sha256:…", startLine, endLine}`; endpoint targets resolve through the route → handler; `path#symbol` also works |
| `oumv/observe` L4 | `{traces: [], traceCount: 0, note, evidence}` — never invented |
| `oumv/getSource` | `{path, symbol?, source, version, startLine, endLine}`; 1-based inclusive window, `endLine` clamped; `ifVersion` → `{path, version, unchanged: true}` with no source; unknown symbol → `-32009` |
| `oumv/applyEdit` | modes `replace`, `create`, `symbol`, `patch`, `delete`, `rename` |
| `oumv/verify` | `{status, target, diff, timings{t_*}, durationMs, scope{checked,notExecuted,notSupported}, note}`; runs `--verify-cmd`; `steps` supported |
| `oumv/understand` | `impact` → `{query, target, impact{…, evidence}}`; `causality`/`why_state` → `{observed: false, note, …}`; `dependencies`/`signals` → `-32006` |
| `oumv/disconnect` | `{closed, sessionId}` |
| `oumv/descriptor` | spec §9.2 discovery record for this workspace |
| `oumv/sessions` | session policy (`mode: "stateless"`, `ttlSeconds`) |
| anything else | `-32601` |

Sessions are **stateless signed tokens** (`sess-<issued>-<nonce>-<sig>`): the same
semantics over stdio (one process) and HTTP (one process per request). An empty
`sessionId` is accepted (spec §3); an unknown/expired one is `-32011`
(`reason: session_expired`).

## Mutation guards

Order of evaluation (first failure wins):

1. `-32003` workspace boundary / sensitive path (source **and** `renameTo`;
   `.env*`, `.git`, `id_rsa*`, `id_ed25519*`, `*.pem`, `*.key`, `credentials.json`,
   `.tokensafe`); symlinks are resolved and re-checked.
2. `-32009` missing resource (except `create`, where an existing file is
   `-32001 reason=file_exists`).
3. `-32011` structural validation (`replacement_required`,
   `expected_version_required`, `create_forbids_version`, `replacement_forbidden`,
   `symbol_required`, `hunks_required`, `rename_to_required`).
4. `-32002` syntax pre-flight of the **resulting file** (never a fragment):
   `php -l` runs on a temp copy in the system temp dir, so broken bytes never
   reach the workspace.
5. `-32001` OCC mismatch on the current whole-file sha256.
6. write.

`mode=patch` follows spec §4.3 exactly: hunks apply in order to the evolving
text, `target` is a literal match, exactly one occurrence unless `replaceAll` or
`startLine`/`endLine` narrows it, `0` matches → `-32009 target_not_found`, `>1` →
`-32011 target_not_unique` with `data.count` and `data.lines`, and the first
failing hunk aborts with **no bytes written**.

Writes are atomic (temp file in the same directory + `rename`). Backups are
previous bytes under `<root>/.tokensafe/backups/`, mode 0600 — never next to the
source file — and reported as `backupPath`. `create` has no previous bytes, so no
backup; `delete` keeps one; `rename` changes no bytes.

## Verification semantics (important)

`oumv/verify` runs the operator-supplied check (`php -l {file}` by default) on
the handler file resolved from the target (`GET /path`, `/path`, `handler`, or a
workspace `.php` path). It does **not** dispatch HTTP requests: there is no live
application. `status` reflects the check's exit code, `timings` are real
wall-clock measurements, and `scope.notExecuted` names runtime dispatch, real
network and persistent state. A target that matches no route and no file returns
`status: "failed"` with `diff.error` (never a silent pass). `steps` runs one check
per step, in order; the steps do not share application state because nothing is
dispatched.

## Re-certification and tests

```bash
cd tokensafe-backend/php

# Level 1+2 certification report (writes ../certification/php-laravel.json)
./certify.sh

# Equivalent manual run (the fixtures path is needed outside the agent-oumv tree)
node ../../agent-oumv/packages/conformance/dist/cli/certify.js \
  --adapter php-laravel --framework laravel --profile backend \
  --fixtures ../../agent-oumv/fixtures/conformance-cases.json \
  --stdio "/opt/homebrew/bin/php $PWD/bin/oumv-php --root <tmpapp> --stdio" \
  --markers @$PWD/testdata/markers-php.json \
  --report ../certification/php-laravel.json --min-level 2

# Direct assertions: 126 stdio checks + 17 Symfony scan checks + 9 HTTP checks
./selftest.sh

# Adapter manifest lint
node ../../agent-oumv/packages/conformance/dist/cli/lint-adapter.js \
  ../../agent-oumv/adapters/php-laravel.json
```

`certify.sh` copies `testdata/laravel-app` to a temp dir (the cases create,
patch, delete and rename files), runs the certifier, validates the report
against `agent-oumv/schemas/conformance-report.json` with ajv, and exits 0 only
when Level 1 and Level 2 fully pass.

Fixture projects (sources only, never installed):

| Fixture | Routes discovered |
| :--- | :--- |
| `testdata/laravel-app` | **26** (groups, controller group, `match`, `apiResource`, `resource`, closure) |
| `testdata/symfony-app` | **9** (`#[Route]` class prefix + method routes, named `path:`, `GET`+`HEAD`) |

## Known limits

* **L4/runtime causality is not implemented** (no live app, no traces). Level 3
  is not claimed.
* Route discovery is **static**: computed prefixes (`Route::prefix($var)`),
  route files loaded via `include`, `Route::group` with non-literal attributes,
  Symfony YAML/XML/PHP route configs, and `Route::resource` bindings other than
  `only`/`except` are not resolved.
* Middleware is intentionally not reported (`middlewares: []`); middleware
  aliases live in framework config that is not parsed.
* DTOs (`requestDTO`/`responseDTO`) are omitted — they cannot be reflected
  without booting the framework; L2 carries a `note` saying so.
* `verify` does not dispatch requests, so it asserts no HTTP status code.
* Symbol edits/reads cover functions, methods, classes, interfaces, traits and
  enums by name (`Class#method` or a bare name). PHP 8 attributes are included in
  the symbol range only when they sit on the declaration line block preceding it
  — the range starts at the `function`/`class` keyword line.
* Path parameters are template-normalized, not matched: `GET /users/42` does not
  resolve to `/users/{id}`.
* One workspace per process; the route catalog is memoized per process and
  invalidated after a successful mutation.

## Spec ambiguities and how they were resolved

1. **L1 collection name.** Spec §4.1 names the backend L1 field `endpoints`; the
   adapter contract in this task names it `routes`. The response carries **both**
   keys with identical entries (`data.routes` and `data.endpoints`).
2. **Guard order.** Spec §4.3's table lists the syntax pre-flight before the
   version rules; this adapter follows the order stated for this deliverable
   (`-32003 → -32009 → -32002 → -32011 → -32001`) and documents it above.
3. **Backup location.** Spec §8 requires a backup before edits, and the task
   requires `<root>/.tokensafe/backups/` while also forbidding backups "inside
   the source tree". Resolution: backups live in the tool-owned
   `<root>/.tokensafe/backups/` directory and are never written next to the file
   they protect.
4. **`verify` for an endpoint without a live app.** The spec's status is defined
   for dispatched requests; here it reflects the static check, and `scope`
   declares dispatch as `notExecuted`.
5. **HTTP `--root`.** PHP's `cli-server` SAPI drops `argv`; recovered from the
   server process command line (see usage) with `OUMV_ROOT` as the explicit
   override.
6. **Session store.** Spec §3 allows stateless runtimes; signed self-describing
   session ids give the required `session_expired` behaviour on both transports
   without writing a state file.

## Security notes

* Every path is lexically resolved and realpath-checked (including symlinks)
  against the workspace root; sensitive credential files are refused even inside
  the workspace.
* No shell interpolation of agent input: `php -l` runs via `proc_open` with an
  argument array; only the operator-supplied `--verify-cmd` is run through a
  shell, with each substituted path `escapeshellarg`-quoted.
* `--read-only` forces read-only mode; a non-loopback HTTP bind requires
  `--token` and fails closed otherwise.
* The discovery registry (`--register`) is written to
  `~/.tokensafe/runtimes/<sha256(realpath(root))[:16]>.json` (mode 0600, atomic
  rename, other profile entries preserved) — never into the repository.
