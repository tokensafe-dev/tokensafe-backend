# OUMV adapter for Spring Boot — JDK-only sidecar

A **single-file, dependency-free** implementation of the OUMV backend profile for
Spring Boot workspaces. It speaks JSON-RPC 2.0 over **stdio** (§2.5) and **HTTP POST**
(§2.2), scans the project's Spring annotations to expose the routing surface, and
performs OCC-guarded source mutations with a workspace jail.

* **No Maven, no Gradle, no Spring jars.** The adapter is launched with the JDK
  single-file source launcher and uses only JDK classes
  (`com.sun.net.httpserver.HttpServer`, `java.nio`, `java.security`, ...).
* **No modification of the project.** It never boots the application and never writes
  into it except through `oumv/applyEdit` (plus `.tokensafe/backups/` and the
  out-of-repository discovery descriptor).
* **Honest conformance claim: Level 2** (fixtures v9 / wire `1.3.0`), certified report
  in [`../certification/java-spring.json`](../certification/java-spring.json).
  Level 3 is *not* claimed: L4 request traces and runtime causality require a live
  application, and this sidecar deliberately does not fake them.

```
java/src/dev/tokensafe/oumv/
  OumvSpring.java       the adapter (scan + wire + transports + descriptor)
  Selftest.java         JDK-only behavioural tests (no Node required)
testdata/
  spring-app/           Spring-style fixture project (sources only, never compiled)
  plain/Probe.java      compilable JDK-only file used by oumv/verify
  markers-spring.json   conformance markers for this fixture
certify.sh              reproducible certification (temp copy of the fixture)
selftest.sh             builds and runs Selftest against a temp copy
```

## Requirements

* JDK **17+** (developed and certified on OpenJDK 21/Temurin).
* Nothing else for the adapter. Node is only needed for the OUMV conformance CLI and
  for manifest linting.

## Quick start

```bash
# stdio (spec §2.5) — newline-delimited JSON-RPC on stdin/stdout
java java/src/dev/tokensafe/oumv/OumvSpring.java --root /path/to/spring-app --stdio

# HTTP sidecar (default 127.0.0.1:7331), writes the §9.2 discovery descriptor
java java/src/dev/tokensafe/oumv/OumvSpring.java --root /path/to/spring-app --port 7331

# inspect the statically scanned routing surface and exit
java java/src/dev/tokensafe/oumv/OumvSpring.java --root testdata/spring-app --scan
```

`--help` lists every flag. Notable ones:

| Flag | Meaning |
| :--- | :--- |
| `--root <dir>` | workspace root; every path is jailed to it |
| `--stdio` / `--port <n>` | transport selection (`--port 0` = ephemeral) |
| `--host <addr>` | HTTP bind address (default `127.0.0.1`) |
| `--token <s>` | require `X-OUMV-Session-Token` or `Authorization: Bearer` |
| `--verify-cmd <cmd>` | verification command with a `{file}` placeholder (default `javac {file}`) |
| `--verify-file <path>` | file the verification command runs on (overrides target resolution) |
| `--verify-timeout-ms <n>` | verification timeout (default 120000) |
| `--no-descriptor` | do not write `~/.tokensafe/runtimes/<hash>.json` |
| `--scan` | print the route table as JSON and exit |

### HTTP examples

```bash
java .../OumvSpring.java --root testdata/spring-app --port 7331 --token dev-token &
curl -s http://127.0.0.1:7331/__oumv/rpc \
  -H 'Content-Type: application/json' -H 'X-OUMV-Session-Token: dev-token' \
  -d '{"jsonrpc":"2.0","id":1,"method":"oumv/handshake","params":{"protocolVersion":"1.3.0"}}'
```

* `POST /__oumv/rpc` accepts one object or a batch array. Responses are arrays in any
  order; notifications (no `id`) get no entry (HTTP 204 when the whole body is a
  notification).
* HTTP **200** for every well-formed request, including JSON-RPC errors (the `error`
  object carries the code). `415` for a non-JSON content type, `405` for non-POST,
  `413` above 8 MiB, `401` (with `-32007`) when `--token` is set and the token is
  wrong or missing.
* `OPTIONS` answers `204` with `Allow: POST, OPTIONS`.
* `GET /__oumv/health` is a liveness probe (routes, levels, transports).
* **No SSE and no WebSocket**: they are not mounted and not advertised.

### Descriptor (spec §9.2)

In HTTP mode the sidecar writes `~/.tokensafe/runtimes/<sha256(realpath(root))[:16]>.json`
with mode `0600` (`TOKENSAFE_HOME` overrides the root). The file is a registry keyed by
profile: the writer preserves a `frontend` entry, and shutdown removes only its own
`backend` entry (deleting the file when nothing is left). `capabilities.transports` is
`["http"]`; `causality` is `false`. stdio mode writes no descriptor (it has no URL).

## Routing scan (the core value)

`--root` is walked recursively for `.java` files, skipping `target/`, `build/`,
`.git/`, `node_modules/`, `.gradle/`, `.idea/`, `out/` and `.tokensafe/`. The scanner
masks comments, string literals, char literals and text blocks before parsing, so a
`@GetMapping` inside a comment or a string is never a route.

Recognised:

* class level `@RestController` / `@Controller` (`@Component` is **not** a controller);
* class level `@RequestMapping("/base")`, `@RequestMapping(value = "/base")`,
  `@RequestMapping(path = "/base")`, multi-segment bases like `/api/v1/orders`;
* method level `@GetMapping`, `@PostMapping`, `@PutMapping`, `@PatchMapping`,
  `@DeleteMapping`, `@RequestMapping(method = RequestMethod.X)` and
  `method = {RequestMethod.GET, RequestMethod.PUT}` (one route per verb).
  A verbless `@RequestMapping` yields `method: "ANY"`;
* path templates `{id}` and `{id:[0-9]+}` (kept verbatim in L1; a concrete path also
  resolves against them);
* handler identity `Class#method`, with the file and 1-based `startLine`/`endLine`
  covering the annotation through the closing brace;
* nested types are scanned, but a nested non-controller's methods are not routes;
  only methods of the annotated type itself become routes.

The fixture (`testdata/spring-app`) is designed to prove the negative cases: it has
`FakeController` (`@Component` with a `@GetMapping`), annotations inside line/block
comments and inside a string literal, and nested helper types. The scan yields
**17 routes across 4 controllers in 14 `.java` files**:

```bash
$ java .../OumvSpring.java --root testdata/spring-app --scan | jq '.routeCount'
17
$ ... | jq -r '.routes[] | "\(.method) \(.path) \(.handlerName)"'
GET    /admin/stats                          AdminController#stats
POST   /admin/reload                         AdminController#reload
GET    /api/products                         ProductController#list
POST   /api/products                         ProductController#create
GET    /api/products/{id}                    ProductController#get
PUT    /api/products/{id}                    ProductController#update
DELETE /api/products/{id}                    ProductController#delete
GET    /api/products/{id:[0-9]+}/reviews     ProductController#reviews
GET    /api/v1/orders                        OrderController#list
POST   /api/v1/orders                        OrderController#create
GET    /api/v1/orders/{orderId}              OrderController#get
PATCH  /api/v1/orders/{orderId}              OrderController#patch
POST   /api/v1/orders/{orderId}/cancel       OrderController#cancel
GET    /api/v1/orders/{orderId}/tags         OrderController#tags
PUT    /api/v1/orders/{orderId}/tags         OrderController#tags
GET    /health                               HealthController#health
GET    /metrics                              HealthController#metrics
```

The scan is cached per process (the sidecar is a dev tool, not a file watcher);
restart it after moving routes.

## Wire behaviour

Session lifecycle: `oumv/handshake` validates `protocolVersion` (MAJOR 1, MINOR ≤ 3;
anything else is `-32006`), issues `sessionId` + `ttlSeconds: 3600` and closes with
`oumv/disconnect`. A **supplied but unknown/expired** `sessionId` is `-32011` with
`data.reason: "session_expired"`; an **absent** one is accepted (stateless mode).

### observe

| Level | `data` |
| :--- | :--- |
| L0 | `{status, uptimeSeconds, activeRoutes, errorCount1m, framework: "spring"}` |
| L1 | `{routes: [{method, path, handlerName, file, className, startLine, endLine}], endpoints: [same], total, hasMore?, nextCursor?}` — `limit`/`cursor` paginate; an empty surface is `[]` + `note` |
| L2 | `{target, kind, method, path, handler, handlerName, file, startLine, endLine, params?, responseType?, responses?, note, evidence}` for a route, or `{target, kind:"model", model, file, fields, note, evidence}` for a class |
| L3 | `{symbol, path, source, version, startLine, endLine}` where `version` is `sha256` of the **whole file** |

L2/L3 targets accept `"METHOD /path"`, `"/path"`, `"path#symbol"` and a type name
(L2 only). An unresolvable target is `-32009`; a missing target is `-32602`.
`tokenEstimate` is **omitted** on purpose: spec §4.1 says to omit it rather than guess
when the adapter has no calibrated tokenizer.

The emitted `evidence` is `{provenance: "static", confidence: "probable"}` — the
routing surface comes from source annotations, not from a live router.

### getSource

`{path, symbol?, startLine?, endLine?, ifVersion?}` → `{path, symbol?, source?,
unchanged?, version, startLine, endLine}`. Windows are 1-based inclusive with `endLine`
clamped at EOF; `version` is always the whole-file hash. `ifVersion` equal to the
current version returns `{path, version, unchanged: true}` **without** `source`.
Unknown symbol → `-32009`; missing file → `-32009`; escape/sensitive path → `-32003`.

### applyEdit

Modes: `replace` (default), `create`, `symbol`, `delete`, `rename`, `patch`.
Guard order is security (`-32003`) → unknown mode (`-32006`) → resource existence
(`-32009`, or `-32001` for an existing create/rename target) → syntax pre-flight
(`-32002`) → field validation (`-32011`) → OCC (`-32001`) → write.

* `expectedVersion` is mandatory for every mutation of an existing file; `create`
  forbids it (`-32011 reason=create_forbids_version`).
* Version mismatch is `-32001` with `data.expectedVersion` and `data.actualVersion`.
* `patch` matches hunk targets **literally** against the evolving text; 0 occurrences
  is `-32009 target_not_found`, more than one is `-32011 target_not_unique` with
  `count` and 1-based `lines`. `startLine`/`endLine` narrow the search. A failing hunk
  writes no bytes.
* `symbol` replaces exactly the symbol's source range and preserves the rest of the
  file byte for byte; the syntax pre-flight runs on the resulting file.
* The syntax pre-flight is a structural Java check: brace/paren/bracket balance plus
  unterminated string/char/comment/text-block detection, `tool: "jdk/brace-balance"`.
  It is **not** a full parser (no Spring jars, no compiler), and the result says so.
* Backups are written to `<root>/.tokensafe/backups/<relative path>.<timestamp>.bak`
  and reported in `backupPath`; writes go through a temp file + atomic rename.
* Deprecated aliases are accepted on input (`filePath`, `source`) and never emitted.

### verify

`{target, action?, payload?, headers?, steps?}`. There is no in-process handler in a
static sidecar, so verification runs the **real** configured command:

```bash
--verify-cmd "javac -d /tmp/classes {file}"     # default: javac {file}
```

The file is chosen by `--verify-file` when given, otherwise from `path#symbol`/a path
target, otherwise from the route's handler file. `status` is `passed` iff the command
exits 0; `statusCode` is `0` (no HTTP); `timings` are real wall-clock measurements;
`diff` carries `verifiedFile`, `verifyCommand`, `exitCode`, `output` and
`dispatch: "none"`. `scope` always states what was **not** executed: HTTP dispatch of
the target, Spring context startup, real network, persistent state — and for
`steps` it states that each step is an independent static check, not shared
application state. No rollback is ever claimed.

### understand

* `impact`: a static reverse-dependency scan over the scanned sources. It returns the
  handler plus the enclosing members that reference the handler's class or method
  (`references` with file/line), `breakingRisk` (`LOW` with no referrers, `MEDIUM` with
  referrers, `UNKNOWN` when unresolved), `observed: false`, and
  `evidence.provenance: "static"`. Unresolvable targets are not an error.
* `causality` / `why_state`: `{observed: false, traceCount: 0, rootCause: "unknown",
  note}` with an explicit explanation that no live trace exists and Level 3 is not
  implemented.

## Certification

Report: [`../certification/java-spring.json`](../certification/java-spring.json) —
**attainedLevel 2**, **32/33** fixtures passed. The single failure is the Level 3 case
`observe-l4-returns-recorded-traces`, which answers `-32006` by design:

```
observe L4 requires live request traces; this JDK-only sidecar does not run the
Spring application, so it records none
```

`observe-l4` is not advertised in the handshake (`levels: ["L0","L1","L2","L3"]`) and
`understand.causality` is `false`, so the adapter does not over-claim.

### Re-certify

```bash
./java/certify.sh            # stdio; rewrites certification/java-spring.json
./java/certify.sh --http     # additionally proves the same suite over HTTP (temp report)
```

`certify.sh` copies `testdata/spring-app` to a temp directory (the worktree stays
clean), runs the conformance CLI, validates the report against
`agent-oumv/schemas/conformance-report.json` with ajv, and fails unless the claimed
level holds and every failure is the documented L4 gap.

The underlying command (as run by the script):

```bash
node /path/to/agent-oumv/packages/conformance/dist/cli/certify.js \
  --adapter java-spring --framework spring --profile backend \
  --stdio "java /abs/java/src/dev/tokensafe/oumv/OumvSpring.java \
           --root /tmp/oumv-cert/app --stdio \
           --verify-file /abs/java/testdata/plain/Probe.java \
           --verify-cmd \"javac -d /tmp/oumv-cert/classes {file}\"" \
  --fixtures /path/to/agent-oumv/fixtures/conformance-cases.json \
  --markers @/abs/java/testdata/markers-spring.json \
  --report /abs/tokensafe-backend/certification/java-spring.json \
  --min-level 2
```

`oumv-certify` exits `1` when any case fails, which is expected here because of the
honest L4 gap; the script judges the report instead of the raw exit code.

### Self-test without the TypeScript suite

```bash
./java/selftest.sh
```

It compiles `OumvSpring.java` + `Selftest.java` with `javac`, copies the fixtures to a
temp directory, spawns the adapter through the same source-launcher command used in
production, and runs 84 assertions over handshake, L0–L3, getSource, applyEdit
(create/patch/symbol/delete/rename, OCC conflict, syntax/traversal/missing guards),
verify, understand and session lifecycle.

## Deliberate non-goals and resolved ambiguities

* **No L4 / causality.** Request traces cannot exist without running the application.
  `observe` L4 answers `-32006`; `causality` answers `observed: false` with a note.
  Level 3 is not claimed.
* **No SSE/WebSocket** binding; the descriptor and health endpoint list only
  `http` (+ `stdio` in health).
* **No token estimate**: omitted rather than guessed (spec §4.1).
* **No DTO reflection**: without a Spring context the adapter cannot reflect request
  and response types; L2 says so in `note` and only reports what annotations prove.
* **`routes` vs `endpoints`**: spec §4.1 names the backend L1 collection `endpoints`;
  the adapter emits both keys with the identical array so either client convention
  works (unknown fields must be ignored).
* **Unknown verify `action`**: accepted and echoed in `diff.action`; it is not a
  dispatch name because nothing is dispatched. The scope makes this explicit.
* **`disconnect` with an unknown session** answers `-32011 session_expired` for
  consistency with every other method; a live session answers `{disconnected: true}`.
* **`create` with an empty replacement** is `-32011 replacement_required` (validation),
  not `-32602`.
* **`delete` keeps a backup** (the spec calls it optional) so a mistaken delete is
  recoverable; the path is reported.
* **Files are read as UTF-8**; OCC hashes the raw bytes, so non-UTF-8 sources would
  round-trip lossily. Spring projects are UTF-8 in practice; this is documented rather
  than guessed per file.
* **`startLine`/`endLine` outside the file**: clamped (never an error), matching the
  spec's window semantics.

## Fixture layout

```
testdata/spring-app/
  src/main/java/com/example/demo/
    DemoApplication.java
    controller/{ProductController,OrderController,HealthController,AdminController,FakeController}.java
    model/{Product,Order}.java
    service/ProductService.java
  src/test/java/com/example/demo/ProductControllerTest.java   # static impact evidence
  sandbox/                        # applyEdit targets (create/patch/symbol/delete/rename)
testdata/plain/Probe.java         # javac target for oumv/verify
```

The fixture is never compiled (there are no Spring jars); it exists to exercise the
scanner and the mutation guards. `verify` compiles `testdata/plain/Probe.java`, which
is plain Java.
