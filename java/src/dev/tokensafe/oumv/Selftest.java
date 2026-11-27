package dev.tokensafe.oumv;

import java.io.BufferedReader;
import java.io.BufferedWriter;
import java.io.Closeable;
import java.io.IOException;
import java.io.InputStreamReader;
import java.io.OutputStreamWriter;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.Paths;
import java.security.MessageDigest;
import java.util.List;
import java.util.Map;

/**
 * JDK-only self-test for the OumvSpring adapter. It spawns the adapter exactly
 * as shipped (single-file source launcher, stdio transport) and asserts the
 * wire behaviour the TypeScript conformance suite covers, without requiring
 * Node or the OUMV packages:
 * handshake, L0-L3 observe, getSource, applyEdit patch/OCC/guards, verify,
 * understand and session lifecycle.
 *
 * Usage (normally through selftest.sh):
 *   java -cp <classes> dev.tokensafe.oumv.Selftest <fixtureRoot> <verifyFile> <OumvSpring.java>
 */
public final class Selftest {

    static int checks;
    static int failures;

    public static void main(String[] args) throws Exception {
        if (args.length < 3) {
            System.err.println("usage: Selftest <fixtureRoot> <verifyFile> <OumvSpring.java>");
            System.exit(2);
            return;
        }
        Path root = Paths.get(args[0]).toAbsolutePath().normalize();
        Path verifyFile = Paths.get(args[1]).toAbsolutePath().normalize();
        Path adapter = Paths.get(args[2]).toAbsolutePath().normalize();
        Path classes = root.getParent().resolve("classes");
        Files.createDirectories(classes);

        System.out.println("oumv-java-spring selftest");
        System.out.println("  root=" + root);
        System.out.println("  adapter=" + adapter);
        System.out.println("  verify=" + verifyFile);

        try (Client client = new Client(
                "java", adapter.toString(),
                "--root", root.toString(),
                "--stdio",
                "--verify-file", verifyFile.toString(),
                "--verify-cmd", "javac -d " + quote(classes.toString()) + " {file}")) {
            run(client, root);
        }

        System.out.println();
        System.out.println(failures == 0
                ? "selftest PASSED (" + checks + " checks)"
                : "selftest FAILED (" + failures + " of " + checks + " checks failed)");
        System.exit(failures == 0 ? 0 : 1);
    }

    static void run(Client client, Path root) throws Exception {
        // ---- handshake -------------------------------------------------
        Map<String, Object> handshake = client.call("oumv/handshake", OumvSpring.map("protocolVersion", "1.0.0"));
        Map<String, Object> handshakeResult = result(handshake, "handshake");
        check("1.4.0".equals(handshakeResult.get("protocolVersion")), "handshake serves protocol 1.4.0");
        check("backend".equals(handshakeResult.get("profile")), "handshake profile is backend");
        check(handshakeResult.get("sessionId") instanceof String, "handshake issues a sessionId");
        String sessionId = String.valueOf(handshakeResult.get("sessionId"));
        checkEquals(deep(handshakeResult, "capabilities", "observe", "levels"), List.of("L0", "L1", "L2", "L3"),
                "handshake advertises L0-L3 (no L4)");
        checkEquals(deep(handshakeResult, "capabilities", "understand", "causality"), Boolean.FALSE,
                "handshake does not claim causality");

        // ---- observe L0/L1 ---------------------------------------------
        Map<String, Object> l0 = result(client.call("oumv/observe", OumvSpring.map("level", "L0")), "observe L0");
        check("L0".equals(l0.get("level")), "observe L0 level echo");
        check("json".equals(l0.get("format")), "observe L0 format is json");
        checkEquals(deep(l0, "data", "framework"), "spring", "observe L0 framework=spring");
        int activeRoutes = ((Number) deep(l0, "data", "activeRoutes")).intValue();
        checkEquals(activeRoutes, 17, "observe L0 activeRoutes is the scanned route count (17)");

        Map<String, Object> l1 = result(client.call("oumv/observe", OumvSpring.map("level", "L1")), "observe L1");
        Object routes = deep(l1, "data", "routes");
        check(routes instanceof List && ((List<?>) routes).size() == 17, "observe L1 returns 17 routes");
        check(routes instanceof List && ((List<?>) routes).size() == ((List<?>) deep(l1, "data", "endpoints")).size(),
                "observe L1 routes and endpoints arrays agree");
        check(hasRoute((List<?>) routes, "GET", "/api/products/{id}", "ProductController#get"),
                "observe L1 resolves a {id} template route to Class#method");
        check(hasRoute((List<?>) routes, "POST", "/api/v1/orders/{orderId}/cancel", "OrderController#cancel"),
                "observe L1 resolves a multi-segment base path plus @RequestMapping(method=...)");
        check(!hasRoute((List<?>) routes, "GET", "/fake/not-a-route", null),
                "observe L1 ignores @GetMapping on a non-controller class");
        check(!hasRoute((List<?>) routes, "GET", "/commented-out", null),
                "observe L1 ignores annotations inside comments");

        Map<String, Object> page = result(client.call("oumv/observe", OumvSpring.map("level", "L1", "limit", 1)),
                "observe L1 paginated");
        checkEquals(deep(page, "data", "hasMore"), Boolean.TRUE, "observe L1 limit=1 reports hasMore=true");
        check(deep(page, "data", "nextCursor") != null, "observe L1 limit=1 returns nextCursor");

        // ---- observe L2/L3 ---------------------------------------------
        Map<String, Object> l2 = result(client.call("oumv/observe",
                OumvSpring.map("level", "L2", "target", "GET /api/products/{id}")), "observe L2");
        checkEquals(deep(l2, "data", "target"), "GET /api/products/{id}", "observe L2 echoes data.target");
        checkEquals(deep(l2, "data", "handler"), "ProductController#get", "observe L2 resolves the handler");
        checkEquals(deep(l2, "data", "evidence", "provenance"), "static", "observe L2 carries static evidence");

        Map<String, Object> l3 = result(client.call("oumv/observe",
                OumvSpring.map("level", "L3", "target", "GET /api/products/{id}")), "observe L3");
        String l3Version = String.valueOf(deep(l3, "data", "version"));
        check(l3Version.matches("sha256:[0-9a-f]{64}"), "observe L3 version is sha256 of the whole file");
        check(String.valueOf(deep(l3, "data", "source")).contains("@GetMapping"),
                "observe L3 source includes the handler annotation");

        // ---- getSource --------------------------------------------------
        Map<String, Object> source = result(client.call("oumv/getSource",
                OumvSpring.map("path", "sandbox/SymbolTarget.java")), "getSource");
        check(String.valueOf(source.get("source")).contains("return 41"), "getSource returns file bytes");
        String symbolVersion = String.valueOf(source.get("version"));
        check(symbolVersion.equals(l3Version) || symbolVersion.matches("sha256:[0-9a-f]{64}"),
                "getSource version is a sha256 token");

        Map<String, Object> unchanged = result(client.call("oumv/getSource",
                OumvSpring.map("path", "sandbox/SymbolTarget.java", "ifVersion", symbolVersion)), "getSource ifVersion");
        checkEquals(unchanged.get("unchanged"), Boolean.TRUE, "getSource ifVersion returns unchanged=true");
        check(!unchanged.containsKey("source"), "getSource ifVersion omits source bytes");

        expectError(client.call("oumv/getSource",
                OumvSpring.map("path", "sandbox/SymbolTarget.java", "symbol", "NoSuchSymbol")), -32009,
                "getSource unknown symbol is -32009");
        expectError(client.call("oumv/getSource", OumvSpring.map("path", "../../etc/passwd")), -32003,
                "getSource path traversal is -32003");
        expectError(client.call("oumv/getSource", OumvSpring.map("path", "sandbox/Missing.java")), -32009,
                "getSource missing file is -32009");

        Map<String, Object> window = result(client.call("oumv/getSource",
                OumvSpring.map("path", "sandbox/SymbolTarget.java", "startLine", 2, "endLine", 2)), "getSource window");
        checkEquals(window.get("source"), "    int value() {", "getSource startLine/endLine window is 1-based inclusive");
        checkEquals(window.get("version"), source.get("version"), "windowed getSource keeps the whole-file version");

        // ---- applyEdit: create + patch ---------------------------------
        Map<String, Object> created = result(client.call("oumv/applyEdit",
                OumvSpring.map("path", "sandbox/SelftestCreated.java", "replacement", "package x;\n", "mode", "create")),
                "applyEdit create");
        checkEquals(created.get("created"), Boolean.TRUE, "applyEdit create reports created=true");
        checkEquals(created.get("newVersion"), occ("package x;\n"), "applyEdit create newVersion matches bytes");

        expectError(client.call("oumv/applyEdit",
                OumvSpring.map("path", "sandbox/SelftestCreated.java", "replacement", "package y;\n", "mode", "create")),
                -32001, "applyEdit create over an existing file is -32001");
        expectError(client.call("oumv/applyEdit",
                OumvSpring.map("path", "sandbox/SelftestVersioned.java", "replacement", "package y;\n",
                        "expectedVersion", occ("package x;\n"), "mode", "create")),
                -32011, "applyEdit create with expectedVersion is -32011");

        Map<String, Object> patched = result(client.call("oumv/applyEdit",
                OumvSpring.map("path", "sandbox/SelftestCreated.java", "expectedVersion", occ("package x;\n"),
                        "mode", "patch",
                        "hunks", List.of(OumvSpring.map("target", "package x;", "replacement", "package z;")))),
                "applyEdit patch");
        checkEquals(patched.get("hunksApplied"), 1L, "applyEdit patch reports hunksApplied=1");
        checkEquals(patched.get("newVersion"), occ("package z;\n"), "applyEdit patch newVersion matches spliced bytes");

        expectError(client.call("oumv/applyEdit",
                OumvSpring.map("path", "sandbox/SelftestCreated.java", "expectedVersion", occ("package z;\n"),
                        "mode", "patch",
                        "hunks", List.of(OumvSpring.map("target", "does-not-exist", "replacement", "x")))),
                -32009, "applyEdit patch target missing is -32009");

        result(client.call("oumv/applyEdit",
                OumvSpring.map("path", "sandbox/SelftestDup.java", "replacement", "same\nsame\n", "mode", "create")),
                "create duplicate target file");
        Map<String, Object> ambiguous = client.call("oumv/applyEdit",
                OumvSpring.map("path", "sandbox/SelftestDup.java", "expectedVersion", occ("same\nsame\n"),
                        "mode", "patch",
                        "hunks", List.of(OumvSpring.map("target", "same", "replacement", "other"))));
        expectError(ambiguous, -32011, "applyEdit patch ambiguous target is -32011");
        checkEquals(deep(ambiguous, "error", "data", "reason"), "target_not_unique",
                "ambiguous patch error reason is target_not_unique");
        checkEquals(deep(ambiguous, "error", "data", "count"), 2L, "ambiguous patch reports count=2");
        checkEquals(deep(ambiguous, "error", "data", "lines"), List.of(1L, 2L),
                "ambiguous patch reports occurrence lines [1, 2]");

        // ---- applyEdit: replace guards ---------------------------------
        Map<String, Object> editSource = result(client.call("oumv/getSource",
                OumvSpring.map("path", "sandbox/EditTarget.java")), "getSource edit target");
        String editVersion = String.valueOf(editSource.get("version"));

        Map<String, Object> applied = result(client.call("oumv/applyEdit",
                OumvSpring.map("path", "sandbox/EditTarget.java", "replacement", "value = 3\n",
                        "expectedVersion", editVersion)),
                "applyEdit replace");
        checkEquals(applied.get("status"), "APPLIED", "applyEdit replace APPLIED");
        checkEquals(applied.get("newVersion"), occ("value = 3\n"), "applyEdit replace newVersion matches content");
        check(String.valueOf(applied.get("backupPath")).contains("/.tokensafe/backups/"),
                "applyEdit writes a backup under .tokensafe/backups");
        check(Files.isRegularFile(Paths.get(String.valueOf(applied.get("backupPath")))),
                "backup file exists on disk");

        Map<String, Object> conflict = client.call("oumv/applyEdit",
                OumvSpring.map("path", "sandbox/EditTarget.java", "replacement", "value = 4\n",
                        "expectedVersion", editVersion));
        expectError(conflict, -32001, "stale expectedVersion is -32001");
        checkEquals(deep(conflict, "error", "data", "expectedVersion"), editVersion,
                "conflict carries data.expectedVersion");
        checkEquals(deep(conflict, "error", "data", "actualVersion"), occ("value = 3\n"),
                "conflict carries data.actualVersion");

        expectError(client.call("oumv/applyEdit",
                OumvSpring.map("path", "sandbox/EditTarget.java", "replacement", "value = 4\n")),
                -32011, "missing expectedVersion is -32011");
        expectError(client.call("oumv/applyEdit",
                OumvSpring.map("path", "sandbox/EditTarget.java", "replacement", "def broken(:\n  pass\n",
                        "expectedVersion", occ("value = 3\n"))),
                -32002, "unbalanced replacement is -32002");
        expectError(client.call("oumv/applyEdit",
                OumvSpring.map("path", "../outside.txt", "replacement", "x", "expectedVersion", editVersion)),
                -32003, "applyEdit path traversal is -32003");
        expectError(client.call("oumv/applyEdit",
                OumvSpring.map("path", "sandbox/Missing.java", "replacement", "x", "expectedVersion", editVersion)),
                -32009, "applyEdit missing file is -32009");
        expectError(client.call("oumv/applyEdit",
                OumvSpring.map("path", "sandbox/EditTarget.java", "replacement", "x",
                        "expectedVersion", editVersion, "mode", "splice")),
                -32006, "applyEdit unknown mode is -32006");

        // ---- applyEdit: symbol / delete / rename ----------------------
        String symbolFileVersion = String.valueOf(result(client.call("oumv/getSource",
                OumvSpring.map("path", "sandbox/SymbolTarget.java")), "symbol file version").get("version"));
        Map<String, Object> symbolEdit = result(client.call("oumv/applyEdit",
                OumvSpring.map("path", "sandbox/SymbolTarget.java", "symbol", "value",
                        "replacement", "    int value() {\n        return 42;\n    }",
                        "expectedVersion", symbolFileVersion, "mode", "symbol")),
                "applyEdit symbol");
        checkEquals(symbolEdit.get("symbol"), "value", "symbol edit echoes the symbol");
        String symbolFile = Files.readString(Paths.get(root.resolve("sandbox/SymbolTarget.java").toString()));
        checkEquals(symbolFile, "class SymbolTarget {\n    int value() {\n        return 42;\n    }\n}\n",
                "symbol edit preserves the rest of the file byte for byte");
        checkEquals(symbolEdit.get("newVersion"), occ(symbolFile), "symbol edit newVersion matches the file");

        expectError(client.call("oumv/applyEdit",
                OumvSpring.map("path", "sandbox/DeleteTarget.java", "expectedVersion", occ("value = 1\n"),
                        "replacement", "x", "mode", "delete")),
                -32011, "delete with replacement is -32011");
        Map<String, Object> deleted = result(client.call("oumv/applyEdit",
                OumvSpring.map("path", "sandbox/DeleteTarget.java", "expectedVersion", occ("value = 1\n"),
                        "mode", "delete")),
                "delete");
        checkEquals(deleted.get("deleted"), Boolean.TRUE, "delete reports deleted=true");
        check(!Files.exists(root.resolve("sandbox/DeleteTarget.java")), "delete removed the file");

        Map<String, Object> renamed = result(client.call("oumv/applyEdit",
                OumvSpring.map("path", "sandbox/RenameSource.java", "renameTo", "sandbox/RenameTarget.java",
                        "expectedVersion", occ("value = 2\n"), "mode", "rename")),
                "rename");
        checkEquals(renamed.get("newPath"), "sandbox/RenameTarget.java", "rename reports newPath");
        check(Files.isRegularFile(root.resolve("sandbox/RenameTarget.java")), "rename moved the file");

        // ---- verify -----------------------------------------------------
        Map<String, Object> verify = result(client.call("oumv/verify",
                OumvSpring.map("target", "GET /api/products", "action", "conformance-probe")), "verify");
        checkEquals(verify.get("status"), "passed", "verify passes on a compilable probe file");
        check(deep(verify, "timings", "t_total_ms") instanceof Number, "verify reports real timings");
        check(deep(verify, "scope", "checked") instanceof List
                && !((List<?>) deep(verify, "scope", "checked")).isEmpty(), "verify states scope.checked");
        check(deep(verify, "scope", "notExecuted") instanceof List
                && !((List<?>) deep(verify, "scope", "notExecuted")).isEmpty(), "verify states scope.notExecuted");
        checkEquals(deep(verify, "diff", "dispatch"), "none", "verify never claims a dispatch it did not perform");

        Map<String, Object> steps = result(client.call("oumv/verify",
                OumvSpring.map("target", "GET /api/products",
                        "steps", List.of(
                                OumvSpring.map("target", "GET /api/products", "action", "conformance-probe"),
                                OumvSpring.map("target", "GET /api/products", "action", "conformance-probe")))),
                "verify steps");
        check(steps.get("steps") instanceof List && ((List<?>) steps.get("steps")).size() == 2,
                "verify multi-step returns one entry per step");

        // ---- understand -------------------------------------------------
        Map<String, Object> impact = result(client.call("oumv/understand",
                OumvSpring.map("query", "impact", "target", "GET /api/products")), "understand impact");
        checkEquals(deep(impact, "impact", "target"), "GET /api/products", "impact echoes the target");
        checkEquals(deep(impact, "impact", "evidence", "provenance"), "static", "impact evidence is static");
        check(String.valueOf(deep(impact, "impact", "affectedSymbols")).contains("ProductController#list"),
                "impact lists the handler and its static referrers");

        Map<String, Object> causality = result(client.call("oumv/understand",
                OumvSpring.map("query", "causality", "target", "GET /api/products")), "understand causality");
        checkEquals(deep(causality, "causality", "observed"), Boolean.FALSE,
                "causality is observed=false (no live traces)");
        check(deep(causality, "causality", "note") instanceof String, "causality explains what evidence is missing");
        expectError(client.call("oumv/understand", OumvSpring.map("query", "signals", "target", "x")), -32006,
                "unknown understand query is -32006");

        // ---- guards / sessions / unsupported ---------------------------
        expectError(client.call("oumv/observe", OumvSpring.map("level", "L9")), -32006,
                "observe unknown level is -32006");
        expectError(client.call("oumv/observe", OumvSpring.map("level", "L4")), -32006,
                "observe L4 is honestly unsupported");
        expectError(client.call("oumv/handshake", OumvSpring.map("protocolVersion", "9.9.9")), -32006,
                "handshake rejects MAJOR 9");
        Map<String, Object> futureMinor = result(client.call("oumv/handshake",
                OumvSpring.map("protocolVersion", "1.2.0")), "handshake 1.2.0");
        check(futureMinor.get("sessionId") instanceof String, "handshake accepts an earlier MINOR (1.2.0)");
        Map<String, Object> badSession = client.call("oumv/observe",
                OumvSpring.map("level", "L1", "sessionId", "sess-unknown"));
        expectError(badSession, -32011, "unknown session is -32011");
        checkEquals(deep(badSession, "error", "data", "reason"), "session_expired",
                "unknown session reason is session_expired");
        expectError(client.call("oumv/nonexistentMethod", OumvSpring.map()), -32601,
                "unknown method is -32601");

        Map<String, Object> disconnect = result(client.call("oumv/disconnect",
                OumvSpring.map("sessionId", sessionId)), "disconnect");
        checkEquals(disconnect.get("disconnected"), Boolean.TRUE, "disconnect closes the session");
        expectError(client.call("oumv/observe", OumvSpring.map("level", "L0", "sessionId", sessionId)), -32011,
                "a disconnected session is rejected");

        // a live session keeps working
        Map<String, Object> fresh = result(client.call("oumv/handshake",
                OumvSpring.map("protocolVersion", "1.4.0")), "handshake 1.4.0");
        String freshSession = String.valueOf(fresh.get("sessionId"));
        Map<String, Object> withSession = result(client.call("oumv/observe",
                OumvSpring.map("level", "L0", "sessionId", freshSession)), "observe with session");
        checkEquals(withSession.get("level"), "L0", "a live sessionId is accepted");
    }

    // ------------------------------------------------------------ helpers

    static boolean hasRoute(List<?> routes, String method, String path, String handler) {
        for (Object raw : routes) {
            if (!(raw instanceof Map)) {
                continue;
            }
            Map<?, ?> route = (Map<?, ?>) raw;
            boolean matches = method.equals(route.get("method")) && path.equals(route.get("path"));
            if (handler != null) {
                matches = matches && handler.equals(route.get("handlerName"));
            }
            if (matches) {
                return true;
            }
        }
        return false;
    }

    static Map<String, Object> result(Map<String, Object> response, String label) {
        if (response.get("error") != null) {
            failures++;
            checks++;
            System.out.println("  ✗ " + label + " returned error " + OumvSpring.Json.stringify(response.get("error")));
            throw new IllegalStateException(label + " failed");
        }
        Object result = response.get("result");
        if (!(result instanceof Map)) {
            failures++;
            checks++;
            System.out.println("  ✗ " + label + " returned no object result");
            throw new IllegalStateException(label + " failed");
        }
        @SuppressWarnings("unchecked")
        Map<String, Object> map = (Map<String, Object>) result;
        return map;
    }

    static void expectError(Map<String, Object> response, int code, String label) {
        Map<String, Object> error = asObject(response.get("error"));
        boolean ok = error != null && ((Number) error.get("code")).intValue() == code;
        check(ok, label + (ok ? "" : " (got " + OumvSpring.Json.stringify(response) + ")"));
    }

    @SuppressWarnings("unchecked")
    static Map<String, Object> asObject(Object value) {
        return value instanceof Map ? (Map<String, Object>) value : null;
    }

    static Object deep(Object node, String... path) {
        Object current = node;
        for (String key : path) {
            if (!(current instanceof Map)) {
                return null;
            }
            current = ((Map<?, ?>) current).get(key);
        }
        return current;
    }

    static void check(boolean ok, String label) {
        checks++;
        if (ok) {
            System.out.println("  ✓ " + label);
        } else {
            failures++;
            System.out.println("  ✗ " + label);
        }
    }

    static void checkEquals(Object got, Object want, String label) {
        boolean ok = got == null ? want == null : got.equals(want);
        check(ok, label + (ok ? "" : " (got " + OumvSpring.Json.stringify(got) + ", want "
                + OumvSpring.Json.stringify(want) + ")"));
    }

    static String occ(String content) {
        try {
            MessageDigest digest = MessageDigest.getInstance("SHA-256");
            byte[] hash = digest.digest(content.getBytes(StandardCharsets.UTF_8));
            StringBuilder sb = new StringBuilder("sha256:");
            for (byte b : hash) {
                sb.append(Character.forDigit((b >> 4) & 0xF, 16));
                sb.append(Character.forDigit(b & 0xF, 16));
            }
            return sb.toString();
        } catch (Exception err) {
            throw new IllegalStateException(err);
        }
    }

    static String quote(String value) {
        return "'" + value.replace("'", "'\\''") + "'";
    }

    /** Newline-delimited JSON-RPC client over the adapter's stdio transport. */
    static final class Client implements Closeable {
        final Process process;
        final BufferedWriter writer;
        final BufferedReader reader;
        long nextId = 1;

        Client(String... command) throws IOException {
            ProcessBuilder builder = new ProcessBuilder(command);
            builder.redirectError(ProcessBuilder.Redirect.INHERIT);
            this.process = builder.start();
            this.writer = new BufferedWriter(
                    new OutputStreamWriter(process.getOutputStream(), StandardCharsets.UTF_8));
            this.reader = new BufferedReader(
                    new InputStreamReader(process.getInputStream(), StandardCharsets.UTF_8));
        }

        Map<String, Object> call(String method, Map<String, Object> params) throws IOException {
            Map<String, Object> request = OumvSpring.map(
                    "jsonrpc", "2.0", "id", nextId++, "method", method, "params", params);
            writer.write(OumvSpring.Json.stringify(request));
            writer.newLine();
            writer.flush();
            String line = reader.readLine();
            if (line == null) {
                throw new IOException("adapter closed stdout before answering " + method);
            }
            try {
                return asObject(OumvSpring.Json.parse(line));
            } catch (OumvSpring.Json.ParseException err) {
                throw new IOException("adapter returned non-JSON: " + line, err);
            }
        }

        @Override
        public void close() throws IOException {
            try {
                writer.close();
            } catch (IOException ignored) {
                // the process may have exited already
            }
            process.destroy();
            try {
                process.waitFor();
            } catch (InterruptedException err) {
                Thread.currentThread().interrupt();
            }
        }
    }
}
