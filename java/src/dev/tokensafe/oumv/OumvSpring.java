package dev.tokensafe.oumv;

/*
 * OumvSpring — a JDK-only OUMV backend adapter for Spring Boot workspaces.
 *
 * Design constraints (deliberate, not incidental):
 *   - ONE file, no Maven/Gradle dependency, no Spring jar on the classpath. It is
 *     launched with the JDK single-file source launcher:
 *         java OumvSpring.java --root <project> --stdio
 *         java OumvSpring.java --root <project> --port 7331
 *   - The routing surface is derived STATICALLY from Spring annotations
 *     (@RestController/@Controller, @RequestMapping, @GetMapping, ...). There is
 *     no live Spring ApplicationContext: this sidecar never boots the application.
 *   - Because no request ever flows through this process, L4 traces and runtime
 *     causality are NOT implemented and NOT advertised. understand/causality
 *     returns observed=false with an explicit note. Level 3 is not claimed.
 *   - verify runs the real configured command (default `javac {file}`) and reports
 *     its real exit status; it never claims an HTTP dispatch it did not perform.
 *
 * Wire contract: OUMV wire 1.5.0 (spec v1.8), backend profile. Frozen on purpose:
 * the wire 1.6.0 surface (§4.1 result versioning, §3 correlation/cancel, §14
 * extensions) is deliberately not implemented until there is demand.
 */

import com.sun.net.httpserver.Headers;
import com.sun.net.httpserver.HttpExchange;
import com.sun.net.httpserver.HttpServer;

import java.io.BufferedOutputStream;
import java.io.BufferedReader;
import java.io.ByteArrayOutputStream;
import java.io.FileDescriptor;
import java.io.FileOutputStream;
import java.io.IOException;
import java.io.InputStream;
import java.io.InputStreamReader;
import java.io.OutputStream;
import java.io.PrintStream;
import java.net.InetSocketAddress;
import java.nio.charset.StandardCharsets;
import java.nio.file.FileVisitResult;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.Paths;
import java.nio.file.SimpleFileVisitor;
import java.nio.file.StandardCopyOption;
import java.nio.file.attribute.BasicFileAttributes;
import java.nio.file.attribute.PosixFilePermissions;
import java.security.MessageDigest;
import java.security.SecureRandom;
import java.time.Instant;
import java.time.ZoneOffset;
import java.time.format.DateTimeFormatter;
import java.util.ArrayDeque;
import java.util.ArrayList;
import java.util.Arrays;
import java.util.Collections;
import java.util.Deque;
import java.util.HashSet;
import java.util.LinkedHashMap;
import java.util.LinkedHashSet;
import java.util.List;
import java.util.Locale;
import java.util.Map;
import java.util.Set;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.Executors;
import java.util.regex.Matcher;
import java.util.regex.Pattern;

public final class OumvSpring {

    static final String NAME = "oumv-java-spring";
    static final String VERSION = "0.2.0";
    static final String PROTOCOL_VERSION = "1.5.0";
    static final int SUPPORTED_MAJOR = 1;
    static final int SUPPORTED_MINOR_MAX = 6;
    static final String PROFILE = "backend";
    static final String FRAMEWORK = "spring";
    static final String[] OBSERVE_LEVELS = { "L0", "L1", "L2", "L3" };
    static final int DEFAULT_SESSION_TTL_SECONDS = 3600;
    static final int MAX_BODY_BYTES = 8 * 1024 * 1024;

    // ------------------------------------------------------------------ main

    public static void main(String[] args) {
        Options options;
        try {
            options = Options.parse(args);
        } catch (IllegalArgumentException err) {
            System.err.println("oumv-java-spring: " + err.getMessage());
            System.err.println(Options.USAGE);
            System.exit(2);
            return;
        }
        if (options.help) {
            System.out.println(Options.USAGE);
            return;
        }
        if (options.version) {
            System.out.println(NAME + " " + VERSION + " (OUMV " + PROTOCOL_VERSION + ")");
            return;
        }

        Engine engine;
        try {
            engine = new Engine(options);
        } catch (IOException err) {
            System.err.println("oumv-java-spring: cannot open workspace root: " + err.getMessage());
            System.exit(2);
            return;
        }

        if (options.scan) {
            try {
                Object dump = engine.scanDump();
                System.out.println(Json.stringify(dump));
            } catch (RuntimeException err) {
                System.err.println("oumv-java-spring: scan failed: " + err);
                System.exit(1);
            }
            return;
        }

        if (options.stdio) {
            engine.runStdio();
        } else {
            try {
                engine.runHttp();
            } catch (IOException err) {
                System.err.println("oumv-java-spring: cannot start HTTP server: " + err.getMessage());
                System.exit(2);
            }
        }
    }

    // --------------------------------------------------------------- options

    static final class Options {
        Path root = Paths.get(".");
        boolean stdio;
        boolean scan;
        boolean help;
        boolean version;
        boolean descriptor = true;
        boolean readOnly;
        String host = "127.0.0.1";
        int port = 7331;
        String token;
        String verifyCmd = "javac {file}";
        String verifyFile;
        long verifyTimeoutMs = 120_000L;

        static final String USAGE = String.join("\n",
                "oumv-java-spring — JDK-only OUMV backend adapter for Spring Boot workspaces",
                "",
                "Usage:",
                "  java OumvSpring.java --root <project> [--stdio | --port <n>] [options]",
                "",
                "Options:",
                "  --root <dir>              workspace root scanned for .java sources (default: .)",
                "  --stdio                   newline-delimited JSON-RPC on stdin/stdout (spec §2.5)",
                "  --port <n>                HTTP port for /__oumv/rpc (default 7331; 0 = ephemeral)",
                "  --host <addr>             HTTP bind address (default 127.0.0.1)",
                "  --token <token>           require X-OUMV-Session-Token / Bearer on HTTP",
                "  --verify-cmd <cmd>        verification command, {file} placeholder",
                "                            (default: javac {file})",
                "  --verify-file <path>      file the verification command compiles",
                "                            (default: resolved from the verify target)",
                "  --verify-timeout-ms <n>   verification timeout (default 120000)",
                "  --no-descriptor           do not write ~/.tokensafe/runtimes/<hash>.json",
                "  --read-only               do not register applyEdit/verify (spec §8.5)",
                "  --scan                    print the statically scanned route table and exit",
                "  --version                 print the adapter version",
                "  --help                    print this help",
                "",
                "Honest conformance claim: Level 2 (L0-L3 observe, static impact, OCC edits,",
                "verify). Live request traces (L4) and runtime causality are NOT implemented,",
                "because this sidecar never runs the Spring application.");

        static Options parse(String[] args) {
            Options o = new Options();
            for (int i = 0; i < args.length; i++) {
                String arg = args[i];
                switch (arg) {
                    case "--root":
                        o.root = Paths.get(requireValue(args, ++i, arg));
                        break;
                    case "--stdio":
                        o.stdio = true;
                        break;
                    case "--scan":
                        o.scan = true;
                        break;
                    case "--port":
                        o.port = Integer.parseInt(requireValue(args, ++i, arg));
                        break;
                    case "--host":
                        o.host = requireValue(args, ++i, arg);
                        break;
                    case "--token":
                        o.token = requireValue(args, ++i, arg);
                        break;
                    case "--verify-cmd":
                        o.verifyCmd = requireValue(args, ++i, arg);
                        break;
                    case "--verify-file":
                        o.verifyFile = requireValue(args, ++i, arg);
                        break;
                    case "--verify-timeout-ms":
                        o.verifyTimeoutMs = Long.parseLong(requireValue(args, ++i, arg));
                        break;
                    case "--no-descriptor":
                        o.descriptor = false;
                        break;
                    case "--read-only":
                        o.readOnly = true;
                        break;
                    case "--version":
                        o.version = true;
                        break;
                    case "--help":
                    case "-h":
                        o.help = true;
                        break;
                    default:
                        throw new IllegalArgumentException("unknown argument " + arg);
                }
            }
            return o;
        }

        static String requireValue(String[] args, int index, String flag) {
            if (index >= args.length) {
                throw new IllegalArgumentException("missing value for " + flag);
            }
            return args[index];
        }
    }

    // ------------------------------------------------------------ JSON codec

    static final class Json {
        static final class ParseException extends Exception {
            ParseException(String message) {
                super(message);
            }
        }

        static Object parse(String text) throws ParseException {
            Parser parser = new Parser(text);
            Object value = parser.readValue();
            parser.skipWs();
            if (!parser.done()) {
                throw new ParseException("trailing content at offset " + parser.pos);
            }
            return value;
        }

        static String stringify(Object value) {
            StringBuilder out = new StringBuilder();
            write(value, out);
            return out.toString();
        }

        @SuppressWarnings("unchecked")
        static void write(Object value, StringBuilder out) {
            if (value == null) {
                out.append("null");
            } else if (value instanceof String) {
                out.append(quote((String) value));
            } else if (value instanceof Boolean) {
                out.append(value.toString());
            } else if (value instanceof Number) {
                Number number = (Number) value;
                if (number instanceof Double || number instanceof Float) {
                    double d = number.doubleValue();
                    if (Double.isNaN(d) || Double.isInfinite(d)) {
                        out.append('0');
                    } else if (d == Math.rint(d) && Math.abs(d) < 1e15) {
                        out.append(Long.toString((long) d));
                    } else {
                        out.append(Double.toString(d));
                    }
                } else {
                    out.append(number.toString());
                }
            } else if (value instanceof Map) {
                out.append('{');
                boolean first = true;
                for (Map.Entry<?, ?> entry : ((Map<?, ?>) value).entrySet()) {
                    if (!first) {
                        out.append(',');
                    }
                    first = false;
                    out.append(quote(String.valueOf(entry.getKey())));
                    out.append(':');
                    write(entry.getValue(), out);
                }
                out.append('}');
            } else if (value instanceof Iterable) {
                out.append('[');
                boolean first = true;
                for (Object item : (Iterable<Object>) value) {
                    if (!first) {
                        out.append(',');
                    }
                    first = false;
                    write(item, out);
                }
                out.append(']');
            } else {
                out.append(quote(String.valueOf(value)));
            }
        }

        static String quote(String raw) {
            StringBuilder sb = new StringBuilder(raw.length() + 2);
            sb.append('"');
            for (int i = 0; i < raw.length(); i++) {
                char c = raw.charAt(i);
                switch (c) {
                    case '"':
                        sb.append("\\\"");
                        break;
                    case '\\':
                        sb.append("\\\\");
                        break;
                    case '\n':
                        sb.append("\\n");
                        break;
                    case '\r':
                        sb.append("\\r");
                        break;
                    case '\t':
                        sb.append("\\t");
                        break;
                    case '\b':
                        sb.append("\\b");
                        break;
                    case '\f':
                        sb.append("\\f");
                        break;
                    default:
                        if (c < 0x20) {
                            sb.append(String.format("\\u%04x", (int) c));
                        } else {
                            sb.append(c);
                        }
                }
            }
            sb.append('"');
            return sb.toString();
        }

        static final class Parser {
            final String text;
            int pos;

            Parser(String text) {
                this.text = text;
            }

            boolean done() {
                return pos >= text.length();
            }

            void skipWs() {
                while (pos < text.length()) {
                    char c = text.charAt(pos);
                    if (c == ' ' || c == '\t' || c == '\n' || c == '\r') {
                        pos++;
                    } else {
                        return;
                    }
                }
            }

            char peek() throws ParseException {
                if (done()) {
                    throw new ParseException("unexpected end of JSON");
                }
                return text.charAt(pos);
            }

            void expect(char wanted) throws ParseException {
                skipWs();
                if (done() || text.charAt(pos) != wanted) {
                    throw new ParseException("expected '" + wanted + "' at offset " + pos);
                }
                pos++;
            }

            Object readValue() throws ParseException {
                skipWs();
                char c = peek();
                switch (c) {
                    case '{':
                        return readObject();
                    case '[':
                        return readArray();
                    case '"':
                        return readString();
                    case 't':
                        readLiteral("true");
                        return Boolean.TRUE;
                    case 'f':
                        readLiteral("false");
                        return Boolean.FALSE;
                    case 'n':
                        readLiteral("null");
                        return null;
                    default:
                        return readNumber();
                }
            }

            Map<String, Object> readObject() throws ParseException {
                Map<String, Object> map = new LinkedHashMap<>();
                expect('{');
                skipWs();
                if (!done() && text.charAt(pos) == '}') {
                    pos++;
                    return map;
                }
                while (true) {
                    skipWs();
                    String key = readString();
                    expect(':');
                    map.put(key, readValue());
                    skipWs();
                    char c = peek();
                    if (c == ',') {
                        pos++;
                        continue;
                    }
                    if (c == '}') {
                        pos++;
                        return map;
                    }
                    throw new ParseException("expected ',' or '}' at offset " + pos);
                }
            }

            List<Object> readArray() throws ParseException {
                List<Object> list = new ArrayList<>();
                expect('[');
                skipWs();
                if (!done() && text.charAt(pos) == ']') {
                    pos++;
                    return list;
                }
                while (true) {
                    list.add(readValue());
                    skipWs();
                    char c = peek();
                    if (c == ',') {
                        pos++;
                        continue;
                    }
                    if (c == ']') {
                        pos++;
                        return list;
                    }
                    throw new ParseException("expected ',' or ']' at offset " + pos);
                }
            }

            String readString() throws ParseException {
                skipWs();
                if (done() || text.charAt(pos) != '"') {
                    throw new ParseException("expected string at offset " + pos);
                }
                pos++;
                StringBuilder sb = new StringBuilder();
                while (true) {
                    if (done()) {
                        throw new ParseException("unterminated string");
                    }
                    char c = text.charAt(pos++);
                    if (c == '"') {
                        return sb.toString();
                    }
                    if (c != '\\') {
                        sb.append(c);
                        continue;
                    }
                    if (done()) {
                        throw new ParseException("unterminated escape");
                    }
                    char esc = text.charAt(pos++);
                    switch (esc) {
                        case '"':
                            sb.append('"');
                            break;
                        case '\\':
                            sb.append('\\');
                            break;
                        case '/':
                            sb.append('/');
                            break;
                        case 'b':
                            sb.append('\b');
                            break;
                        case 'f':
                            sb.append('\f');
                            break;
                        case 'n':
                            sb.append('\n');
                            break;
                        case 'r':
                            sb.append('\r');
                            break;
                        case 't':
                            sb.append('\t');
                            break;
                        case 'u':
                            if (pos + 4 > text.length()) {
                                throw new ParseException("truncated \\u escape");
                            }
                            String hex = text.substring(pos, pos + 4);
                            pos += 4;
                            sb.append((char) Integer.parseInt(hex, 16));
                            break;
                        default:
                            throw new ParseException("bad escape \\" + esc);
                    }
                }
            }

            Number readNumber() throws ParseException {
                int start = pos;
                if (!done() && (text.charAt(pos) == '-' || text.charAt(pos) == '+')) {
                    pos++;
                }
                boolean decimal = false;
                while (!done()) {
                    char c = text.charAt(pos);
                    if (c >= '0' && c <= '9') {
                        pos++;
                    } else if (c == '.' || c == 'e' || c == 'E' || c == '-' || c == '+') {
                        decimal = decimal || c == '.' || c == 'e' || c == 'E';
                        pos++;
                    } else {
                        break;
                    }
                }
                String raw = text.substring(start, pos);
                if (raw.isEmpty()) {
                    throw new ParseException("expected value at offset " + start);
                }
                try {
                    return decimal ? (Number) Double.parseDouble(raw) : (Number) Long.parseLong(raw);
                } catch (NumberFormatException err) {
                    throw new ParseException("bad number " + raw);
                }
            }

            void readLiteral(String literal) throws ParseException {
                if (!text.startsWith(literal, pos)) {
                    throw new ParseException("expected " + literal + " at offset " + pos);
                }
                pos += literal.length();
            }
        }
    }

    // ------------------------------------------------------------- map utils

    static Map<String, Object> map(Object... kv) {
        Map<String, Object> map = new LinkedHashMap<>();
        for (int i = 0; i + 1 < kv.length; i += 2) {
            map.put(String.valueOf(kv[i]), kv[i + 1]);
        }
        return map;
    }

    static List<Object> list(Object... values) {
        return new ArrayList<>(Arrays.asList(values));
    }

    static String str(Map<String, Object> params, String key) {
        Object value = params.get(key);
        return value instanceof String ? (String) value : null;
    }

    static String str(Map<String, Object> params, String key, String fallback) {
        String value = str(params, key);
        return value == null ? fallback : value;
    }

    static int intParam(Map<String, Object> params, String key, int fallback) {
        Object value = params.get(key);
        if (value instanceof Number) {
            return ((Number) value).intValue();
        }
        if (value instanceof String) {
            try {
                return Integer.parseInt((String) value);
            } catch (NumberFormatException ignored) {
                return fallback;
            }
        }
        return fallback;
    }

    static boolean boolParam(Map<String, Object> params, String key, boolean fallback) {
        Object value = params.get(key);
        if (value instanceof Boolean) {
            return (Boolean) value;
        }
        if (value instanceof String) {
            return Boolean.parseBoolean((String) value);
        }
        return fallback;
    }

    static boolean blank(String value) {
        return value == null || value.trim().isEmpty();
    }

    static String firstNonBlank(String first, String second) {
        return blank(first) ? second : first;
    }

    // --------------------------------------------------------------- errors

    static final class RpcException extends Exception {
        final int code;
        final Map<String, Object> data;

        RpcException(int code, String message, Map<String, Object> data) {
            super(message);
            this.code = code;
            this.data = data;
        }

        static RpcException invalidParams(String reason, String message) {
            return new RpcException(-32602, "INVALID_PARAMS: " + message, map("reason", reason));
        }

        static RpcException security(String reason, String message) {
            return new RpcException(-32003, "SECURITY_VIOLATION: " + message, map("reason", reason));
        }

        static RpcException conflict(String reason, String message, Map<String, Object> extra) {
            Map<String, Object> data = map("reason", reason);
            data.putAll(extra);
            return new RpcException(-32001, "CONFLICT_DETECTED: " + message, data);
        }

        static RpcException syntax(String message, String detail) {
            return new RpcException(-32002, "SYNTAX_ERROR: " + message,
                    map("reason", "syntax_error", "detail", detail == null ? "" : detail));
        }

        static RpcException unsupported(String reason, String message) {
            return new RpcException(-32006, "UNSUPPORTED_CAPABILITY: " + message, map("reason", reason));
        }

        static RpcException permission(String reason, String message) {
            return new RpcException(-32007, "PERMISSION_DENIED: " + message, map("reason", reason));
        }

        static RpcException resourceNotFound(String reason, String message) {
            return new RpcException(-32009, "RESOURCE_NOT_FOUND: " + message, map("reason", reason));
        }

        static RpcException validation(String reason, String message, Map<String, Object> extra) {
            Map<String, Object> data = map("reason", reason);
            if (extra != null) {
                data.putAll(extra);
            }
            return new RpcException(-32011, "VALIDATION_FAILED: " + message, data);
        }

        static RpcException runtimeUnavailable(String reason, String message) {
            return new RpcException(-32008, "RUNTIME_UNAVAILABLE: " + message, map("reason", reason));
        }

        static RpcException internal(String message) {
            return new RpcException(-32603, "INTERNAL_ERROR: " + message, map("reason", "internal_error"));
        }
    }

    // ------------------------------------------------------------- workspace

    static final class Workspace {
        final Path root;

        Workspace(Path root) throws IOException {
            this.root = root.toRealPath();
            if (!Files.isDirectory(this.root)) {
                throw new IOException("not a directory: " + this.root);
            }
        }

        /** Resolves a client-supplied path inside the jail or throws -32003/-32602. */
        Path resolve(String userPath) throws RpcException {
            if (blank(userPath)) {
                throw RpcException.invalidParams("path_required", "path is required");
            }
            Path candidate;
            try {
                Path raw = Paths.get(userPath);
                candidate = (raw.isAbsolute() ? raw : root.resolve(raw)).normalize().toAbsolutePath();
            } catch (RuntimeException err) {
                throw RpcException.security("invalid_path", "cannot parse path " + userPath);
            }
            Path real = realish(candidate);
            if (!real.startsWith(root)) {
                throw RpcException.security("path_traversal",
                        "path traversal blocked: " + userPath + " escapes the workspace boundary");
            }
            if (sensitive(real)) {
                throw RpcException.security("sensitive_path",
                        "access to a credential or VCS-internal path is refused: " + userPath);
            }
            return real;
        }

        /** Resolves through symlinks for the existing prefix, then appends the missing tail. */
        static Path realish(Path candidate) {
            Path existing = candidate;
            Deque<Path> tail = new ArrayDeque<>();
            while (existing != null && !Files.exists(existing)) {
                tail.addFirst(existing.getFileName());
                existing = existing.getParent();
            }
            Path base;
            try {
                base = existing == null ? candidate : existing.toRealPath();
            } catch (IOException err) {
                base = existing == null ? candidate : existing.toAbsolutePath().normalize();
            }
            Path out = base;
            for (Path segment : tail) {
                out = out.resolve(segment);
            }
            return out.normalize();
        }

        static boolean sensitive(Path path) {
            Path fileName = path.getFileName();
            String base = fileName == null ? "" : fileName.toString().toLowerCase(Locale.ROOT);
            if (base.startsWith(".env") || base.equals(".git")
                    || base.startsWith("id_rsa") || base.startsWith("id_ed25519")
                    || base.endsWith(".pem") || base.endsWith(".key")) {
                return true;
            }
            String unix = path.toString().replace('\\', '/');
            return unix.contains("/.git/") || unix.contains("/.tokensafe/keys");
        }

        String relative(Path file) {
            try {
                return root.relativize(file.toAbsolutePath().normalize()).toString().replace('\\', '/');
            } catch (RuntimeException err) {
                return file.toString();
            }
        }

        Path backup(Path file) throws IOException {
            byte[] previous = Files.readAllBytes(file);
            String rel = relative(file);
            String stamp = DateTimeFormatter.ofPattern("yyyyMMddHHmmssSSS")
                    .withZone(ZoneOffset.UTC).format(Instant.now());
            Path backup = root.resolve(".tokensafe").resolve("backups").resolve(rel + "." + stamp + ".bak");
            Files.createDirectories(backup.getParent());
            Files.write(backup, previous);
            return backup;
        }

        void atomicWrite(Path file, byte[] bytes) throws IOException {
            Path dir = file.getParent();
            if (dir != null) {
                Files.createDirectories(dir);
            }
            Path tmp = dir.resolve("." + file.getFileName() + ".oumv-" + randomHex(4) + ".tmp");
            Files.write(tmp, bytes);
            try {
                Files.move(tmp, file, StandardCopyOption.ATOMIC_MOVE, StandardCopyOption.REPLACE_EXISTING);
            } catch (IOException atomicFailed) {
                Files.move(tmp, file, StandardCopyOption.REPLACE_EXISTING);
            }
        }
    }

    static String sha256(byte[] bytes) {
        try {
            MessageDigest digest = MessageDigest.getInstance("SHA-256");
            return "sha256:" + toHex(digest.digest(bytes));
        } catch (Exception err) {
            throw new IllegalStateException("SHA-256 unavailable", err);
        }
    }

    static String toHex(byte[] bytes) {
        StringBuilder sb = new StringBuilder(bytes.length * 2);
        for (byte b : bytes) {
            sb.append(Character.forDigit((b >> 4) & 0xF, 16));
            sb.append(Character.forDigit(b & 0xF, 16));
        }
        return sb.toString();
    }

    static final SecureRandom RANDOM = new SecureRandom();

    static String randomHex(int bytes) {
        byte[] buf = new byte[bytes];
        RANDOM.nextBytes(buf);
        return toHex(buf);
    }

    // ------------------------------------------------------------- source text

    static final class SourceText {
        final String text;
        final int[] starts;

        SourceText(String text) {
            this.text = text;
            List<Integer> acc = new ArrayList<>();
            acc.add(0);
            for (int i = 0; i < text.length(); i++) {
                if (text.charAt(i) == '\n') {
                    acc.add(i + 1);
                }
            }
            starts = new int[acc.size()];
            for (int i = 0; i < acc.size(); i++) {
                starts[i] = acc.get(i);
            }
        }

        int lineCount() {
            return starts.length;
        }

        int lineOf(int offset) {
            int target = Math.max(0, Math.min(offset, text.length()));
            int lo = 0;
            int hi = starts.length - 1;
            while (lo < hi) {
                int mid = (lo + hi + 1) >>> 1;
                if (starts[mid] <= target) {
                    lo = mid;
                } else {
                    hi = mid - 1;
                }
            }
            return lo + 1;
        }

        int startOfLine(int offset) {
            return starts[lineOf(offset) - 1];
        }

        /** Offset of the newline that ends the line containing `offset`, or EOF. */
        int endOfLine(int offset) {
            int line = lineOf(offset);
            return line < starts.length ? starts[line] - 1 : text.length();
        }

        String slice(int startLine, int endLine) {
            int fromLine = Math.max(1, Math.min(startLine, lineCount()));
            int toLine = Math.max(fromLine, Math.min(endLine, lineCount()));
            int from = starts[fromLine - 1];
            int to = toLine >= lineCount() ? text.length() : starts[toLine] - 1;
            if (to < from) {
                to = from;
            }
            return text.substring(from, to);
        }
    }

    // ------------------------------------------------------- java scanning

    static final class Ann {
        final String name;
        final String rawArgs;
        final int start;

        Ann(String name, String rawArgs, int start) {
            this.name = name;
            this.rawArgs = rawArgs;
            this.start = start;
        }
    }

    static final class Member {
        final String kind; // "type" | "method"
        final String owner;
        final String name;
        final String rel;
        final int startOffset;
        final int endOffset;
        final int startLine;
        final int endLine;
        final int declStart;
        final int parenOpen;
        final int parenClose;
        final List<Ann> anns;

        Member(String kind, String owner, String name, String rel, int startOffset, int endOffset,
                int startLine, int endLine, int declStart, int parenOpen, int parenClose, List<Ann> anns) {
            this.kind = kind;
            this.owner = owner;
            this.name = name;
            this.rel = rel;
            this.startOffset = startOffset;
            this.endOffset = endOffset;
            this.startLine = startLine;
            this.endLine = endLine;
            this.declStart = declStart;
            this.parenOpen = parenOpen;
            this.parenClose = parenClose;
            this.anns = anns;
        }

        String symbol() {
            return owner == null || owner.isEmpty() ? name : owner + "#" + name;
        }
    }

    static final class FieldInfo {
        final String owner;
        final String name;
        final String type;

        FieldInfo(String owner, String name, String type) {
            this.owner = owner;
            this.name = name;
            this.type = type;
        }
    }

    static final class FileModel {
        final String rel;
        final Path path;
        final String text;
        final char[] mask;
        final SourceText source;
        final List<Member> types = new ArrayList<>();
        final List<Member> methods = new ArrayList<>();
        final List<FieldInfo> fields = new ArrayList<>();

        FileModel(String rel, Path path, String text) {
            this.rel = rel;
            this.path = path;
            this.text = text;
            this.mask = maskJava(text);
            this.source = new SourceText(text);
        }
    }

    static char[] maskJava(String text) {
        int n = text.length();
        char[] mask = text.toCharArray();
        int state = 0; // 0 code, 1 line comment, 2 block comment, 3 string, 4 char, 5 text block
        for (int i = 0; i < n; i++) {
            char c = text.charAt(i);
            switch (state) {
                case 0:
                    if (c == '/' && i + 1 < n && text.charAt(i + 1) == '/') {
                        mask[i] = ' ';
                        mask[i + 1] = ' ';
                        i++;
                        state = 1;
                    } else if (c == '/' && i + 1 < n && text.charAt(i + 1) == '*') {
                        mask[i] = ' ';
                        mask[i + 1] = ' ';
                        i++;
                        state = 2;
                    } else if (c == '"' && i + 2 < n && text.charAt(i + 1) == '"' && text.charAt(i + 2) == '"') {
                        mask[i] = ' ';
                        mask[i + 1] = ' ';
                        mask[i + 2] = ' ';
                        i += 2;
                        state = 5;
                    } else if (c == '"') {
                        mask[i] = ' ';
                        state = 3;
                    } else if (c == '\'') {
                        mask[i] = ' ';
                        state = 4;
                    }
                    break;
                case 1:
                    if (c == '\n') {
                        state = 0;
                    } else {
                        mask[i] = ' ';
                    }
                    break;
                case 2:
                    if (c == '*' && i + 1 < n && text.charAt(i + 1) == '/') {
                        mask[i] = ' ';
                        mask[i + 1] = ' ';
                        i++;
                        state = 0;
                    } else if (c != '\n') {
                        mask[i] = ' ';
                    }
                    break;
                case 3:
                case 4:
                    if (c == '\\') {
                        mask[i] = ' ';
                        if (i + 1 < n) {
                            if (text.charAt(i + 1) != '\n') {
                                mask[i + 1] = ' ';
                            }
                            i++;
                        }
                    } else if ((state == 3 && c == '"') || (state == 4 && c == '\'')) {
                        mask[i] = ' ';
                        state = 0;
                    } else if (c != '\n') {
                        mask[i] = ' ';
                    }
                    break;
                case 5:
                    if (c == '"' && i + 2 < n && text.charAt(i + 1) == '"' && text.charAt(i + 2) == '"') {
                        mask[i] = ' ';
                        mask[i + 1] = ' ';
                        mask[i + 2] = ' ';
                        i += 2;
                        state = 0;
                    } else if (c != '\n') {
                        mask[i] = ' ';
                    }
                    break;
                default:
                    break;
            }
        }
        return mask;
    }

    static int skipWs(String mask, int i, int end) {
        while (i < end && Character.isWhitespace(mask.charAt(i))) {
            i++;
        }
        return i;
    }

    static String readIdent(String mask, int i, int end) {
        if (i >= end || !Character.isJavaIdentifierStart(mask.charAt(i))) {
            return null;
        }
        int start = i;
        while (i < end && Character.isJavaIdentifierPart(mask.charAt(i))) {
            i++;
        }
        return mask.substring(start, i);
    }

    static String lastIdentBefore(String mask, int index, int floor) {
        int i = index - 1;
        while (i >= floor && Character.isWhitespace(mask.charAt(i))) {
            i--;
        }
        int end = i + 1;
        while (i >= floor && Character.isJavaIdentifierPart(mask.charAt(i))) {
            i--;
        }
        if (end <= i + 1) {
            return null;
        }
        String ident = mask.substring(i + 1, end);
        return ident.isEmpty() ? null : ident;
    }

    static int matchParen(String mask, int open, int end) {
        int depth = 0;
        for (int i = open; i < end; i++) {
            char c = mask.charAt(i);
            if (c == '(') {
                depth++;
            } else if (c == ')') {
                depth--;
                if (depth == 0) {
                    return i;
                }
            }
        }
        return -1;
    }

    static int matchBrace(String mask, int open, int end) {
        int depth = 0;
        for (int i = open; i < end; i++) {
            char c = mask.charAt(i);
            if (c == '{') {
                depth++;
            } else if (c == '}') {
                depth--;
                if (depth == 0) {
                    return i;
                }
            }
        }
        return -1;
    }

    static final Pattern STRING_LITERAL = Pattern.compile("\"((?:[^\"\\\\]|\\\\.)*)\"");
    static final Pattern REQUEST_METHOD = Pattern.compile(
            "RequestMethod\\s*\\.\\s*(GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS|TRACE)");
    static final Pattern NAMED_STRING_ATTR = Pattern.compile("\\b(value|path)\\s*=\\s*");

    static Ann readAnnotation(String mask, String text, int at, int end) {
        int i = at + 1;
        StringBuilder sb = new StringBuilder();
        while (i < end) {
            char c = mask.charAt(i);
            if (Character.isJavaIdentifierPart(c) || c == '.') {
                sb.append(c);
                i++;
            } else {
                break;
            }
        }
        String qualified = sb.toString();
        String simple = qualified;
        int dot = qualified.lastIndexOf('.');
        if (dot >= 0) {
            simple = qualified.substring(dot + 1);
        }
        String rawArgs = null;
        int p = skipWs(mask, i, end);
        if (p < end && mask.charAt(p) == '(') {
            int close = matchParen(mask, p, end);
            if (close > 0) {
                rawArgs = text.substring(p + 1, close);
            }
        }
        return new Ann(simple, rawArgs, at);
    }

    static String unescapeJavaString(String raw) {
        StringBuilder sb = new StringBuilder(raw.length());
        for (int i = 0; i < raw.length(); i++) {
            char c = raw.charAt(i);
            if (c != '\\' || i + 1 >= raw.length()) {
                sb.append(c);
                continue;
            }
            char next = raw.charAt(++i);
            switch (next) {
                case 'n':
                    sb.append('\n');
                    break;
                case 't':
                    sb.append('\t');
                    break;
                case 'r':
                    sb.append('\r');
                    break;
                case '"':
                    sb.append('"');
                    break;
                case '\'':
                    sb.append('\'');
                    break;
                case '\\':
                    sb.append('\\');
                    break;
                case 'u':
                    if (i + 4 < raw.length()) {
                        sb.append((char) Integer.parseInt(raw.substring(i + 1, i + 5), 16));
                        i += 4;
                    }
                    break;
                default:
                    sb.append(next);
            }
        }
        return sb.toString();
    }

    static String firstStringAfter(String raw, int from) {
        Matcher matcher = STRING_LITERAL.matcher(raw);
        matcher.region(Math.min(from, raw.length()), raw.length());
        if (matcher.find()) {
            return unescapeJavaString(matcher.group(1));
        }
        return "";
    }

    /** Path of a mapping annotation; prefers the named `value`/`path` attribute. */
    static String annPath(Ann ann) {
        if (ann == null || ann.rawArgs == null) {
            return "";
        }
        String raw = ann.rawArgs;
        Matcher named = NAMED_STRING_ATTR.matcher(raw);
        if (named.find()) {
            return firstStringAfter(raw, named.end());
        }
        int eq = raw.indexOf('=');
        if (eq >= 0) {
            int firstQuote = raw.indexOf('"');
            if (firstQuote < 0 || firstQuote > eq) {
                return "";
            }
        }
        return firstStringAfter(raw, 0);
    }

    static List<String> annVerbs(Ann ann) {
        List<String> verbs = new ArrayList<>();
        if (ann == null || ann.rawArgs == null) {
            return verbs;
        }
        Matcher matcher = REQUEST_METHOD.matcher(ann.rawArgs);
        while (matcher.find()) {
            verbs.add(matcher.group(1));
        }
        return verbs;
    }

    static Ann findAnn(List<Ann> anns, String name) {
        for (Ann ann : anns) {
            if (ann.name.equals(name)) {
                return ann;
            }
        }
        return null;
    }

    static Ann findAnnAny(List<Ann> anns, String... names) {
        for (Ann ann : anns) {
            for (String name : names) {
                if (ann.name.equals(name)) {
                    return ann;
                }
            }
        }
        return null;
    }

    static boolean hasAnn(List<Ann> anns, String... names) {
        for (Ann ann : anns) {
            for (String name : names) {
                if (ann.name.equals(name)) {
                    return true;
                }
            }
        }
        return false;
    }

    static final Set<String> TYPE_KEYWORDS = new HashSet<>(Arrays.asList("class", "interface", "enum", "record"));
    static final Set<String> MODIFIERS = new HashSet<>(Arrays.asList(
            "public", "protected", "private", "static", "final", "abstract", "sealed", "non-sealed",
            "strictfp", "default", "native", "synchronized", "transient", "volatile"));

    static boolean isTypeKeywordAt(String mask, int i, int end) {
        String ident = readIdent(mask, i, end);
        return ident != null && TYPE_KEYWORDS.contains(ident);
    }

    /** Index of the `class`/`interface`/`enum`/`record` keyword after modifiers, or -1. */
    static int findTypeKeyword(String mask, int from, int to) {
        int i = from;
        while (true) {
            i = skipWs(mask, i, to);
            String ident = readIdent(mask, i, to);
            if (ident == null) {
                return -1;
            }
            if (TYPE_KEYWORDS.contains(ident)) {
                return i;
            }
            if (!MODIFIERS.contains(ident)) {
                return -1;
            }
            i += ident.length();
        }
    }

    /**
     * Scans one brace-delimited body. Annotations seen since the previous member
     * belong to the next type/method, which is how Spring ties @GetMapping to a
     * handler without a real parser.
     */
    static void scanBody(FileModel fm, int from, int to, int depth, String owner) {
        String mask = new String(fm.mask);
        SourceText st = fm.source;
        int i = from;
        List<Ann> pending = new ArrayList<>();
        while (i < to) {
            char c = mask.charAt(i);
            if (Character.isWhitespace(c)) {
                i++;
                continue;
            }
            if (c == '@') {
                Ann ann = readAnnotation(mask, fm.text, i, to);
                pending.add(ann);
                int p = skipWs(mask, ann.start + 1, to);
                // advance past the identifier and optional argument list
                int j = ann.start + 1;
                while (j < to && (Character.isJavaIdentifierPart(mask.charAt(j)) || mask.charAt(j) == '.')) {
                    j++;
                }
                j = skipWs(mask, j, to);
                if (j < to && mask.charAt(j) == '(') {
                    int close = matchParen(mask, j, to);
                    if (close > 0) {
                        j = close + 1;
                    }
                }
                i = Math.max(j, ann.start + 1);
                continue;
            }
            if (c == ';') {
                pending.clear();
                i++;
                continue;
            }
            if (c == '}') {
                pending.clear();
                i++;
                continue;
            }
            if (c == '{') {
                int close = matchBrace(mask, i, to);
                pending.clear();
                i = close > i ? close + 1 : to;
                continue;
            }

            int declStart = i;
            int kw = findTypeKeyword(mask, i, to);
            if (kw >= 0) {
                String keyword = readIdent(mask, kw, to);
                int nameStart = skipWs(mask, kw + keyword.length(), to);
                String typeName = readIdent(mask, nameStart, to);
                if (typeName != null) {
                    int bodyOpen = findTypeBodyOpen(mask, nameStart + typeName.length(), to);
                    if (bodyOpen > 0) {
                        int bodyClose = matchBrace(mask, bodyOpen, to);
                        if (bodyClose > 0) {
                            int annStart = pending.isEmpty() ? declStart : pending.get(0).start;
                            int startOffset = st.startOfLine(annStart);
                            int endOffset = st.endOfLine(bodyClose);
                            List<Ann> typeAnns = new ArrayList<>(pending);
                            Member type = new Member("type", typeName, typeName, fm.rel, startOffset, endOffset,
                                    st.lineOf(startOffset), st.lineOf(endOffset), declStart, -1, -1, typeAnns);
                            fm.types.add(type);
                            scanBody(fm, bodyOpen + 1, bodyClose, depth + 1, typeName);
                            pending.clear();
                            i = bodyClose + 1;
                            continue;
                        }
                    }
                }
            }

            // method or field
            int paren = findMemberParen(mask, i, to);
            if (paren > 0) {
                String name = lastIdentBefore(mask, paren, i);
                int parenClose = matchParen(mask, paren, to);
                if (name != null && parenClose > 0) {
                    int after = skipWs(mask, parenClose + 1, to);
                    int bodyOpen = -1;
                    int bodyClose = -1;
                    if (after < to && mask.charAt(after) == '{') {
                        bodyOpen = after;
                        bodyClose = matchBrace(mask, after, to);
                    } else if (after < to && mask.charAt(after) == ';') {
                        bodyClose = after;
                    }
                    if (bodyClose > 0) {
                        int annStart = pending.isEmpty() ? declStart : pending.get(0).start;
                        int startOffset = st.startOfLine(annStart);
                        int endOffset = st.endOfLine(bodyClose);
                        List<Ann> anns = new ArrayList<>(pending);
                        Member method = new Member("method", owner, name, fm.rel, startOffset, endOffset,
                                st.lineOf(startOffset), st.lineOf(endOffset), declStart, paren, parenClose, anns);
                        fm.methods.add(method);
                        pending.clear();
                        i = bodyClose + 1;
                        continue;
                    }
                }
            }

            // field / import / package: skip to the terminating ';' or block
            int terminator = skipToTerminator(mask, i, to);
            if (terminator < 0) {
                break;
            }
            if (mask.charAt(terminator) == ';' && owner != null) {
                addField(fm, declStart, terminator, owner);
            }
            pending.clear();
            i = terminator + 1;
        }
    }

    static int findTypeBodyOpen(String mask, int from, int to) {
        for (int i = from; i < to; i++) {
            char c = mask.charAt(i);
            if (c == '{') {
                return i;
            }
            if (c == ';') {
                return -1;
            }
        }
        return -1;
    }

    static int findMemberParen(String mask, int from, int to) {
        int paren = 0;
        int bracket = 0;
        for (int i = from; i < to; i++) {
            char c = mask.charAt(i);
            if (c == '@') {
                int j = i + 1;
                while (j < to && (Character.isJavaIdentifierPart(mask.charAt(j)) || mask.charAt(j) == '.')) {
                    j++;
                }
                j = skipWs(mask, j, to);
                if (j < to && mask.charAt(j) == '(') {
                    int close = matchParen(mask, j, to);
                    i = close > 0 ? close : to;
                } else {
                    i = j - 1;
                }
                continue;
            }
            if (c == '=' && paren == 0 && bracket == 0) {
                return -1;
            }
            if (c == '(') {
                if (paren == 0 && bracket == 0) {
                    return i;
                }
                paren++;
            } else if (c == ')') {
                paren--;
            } else if (c == '[') {
                bracket++;
            } else if (c == ']') {
                bracket--;
            } else if (c == '{' || c == '}' || c == ';') {
                if (paren == 0 && bracket == 0) {
                    return -1;
                }
            }
        }
        return -1;
    }

    static int skipToTerminator(String mask, int from, int to) {
        int paren = 0;
        int bracket = 0;
        for (int i = from; i < to; i++) {
            char c = mask.charAt(i);
            if (c == '(') {
                paren++;
            } else if (c == ')') {
                paren--;
            } else if (c == '[') {
                bracket++;
            } else if (c == ']') {
                bracket--;
            } else if (paren == 0 && bracket == 0) {
                if (c == ';') {
                    return i;
                }
                if (c == '{') {
                    int close = matchBrace(mask, i, to);
                    return close < 0 ? -1 : close;
                }
                if (c == '}') {
                    return i;
                }
            }
        }
        return -1;
    }

    static void addField(FileModel fm, int declStart, int semi, String owner) {
        String segment = fm.text.substring(declStart, semi);
        int eq = segment.indexOf('=');
        String head = (eq >= 0 ? segment.substring(0, eq) : segment).trim();
        if (head.isEmpty()) {
            return;
        }
        int nameStart = head.length();
        while (nameStart > 0 && Character.isJavaIdentifierPart(head.charAt(nameStart - 1))) {
            nameStart--;
        }
        String name = head.substring(nameStart);
        String type = head.substring(0, nameStart).trim();
        if (name.isEmpty() || type.isEmpty()) {
            return;
        }
        fm.fields.add(new FieldInfo(owner, name, type));
    }

    // ------------------------------------------------------- java syntax

    static final class SyntaxResult {
        final String status;
        final String tool;
        final String error;

        SyntaxResult(String status, String tool, String error) {
            this.status = status;
            this.tool = tool;
            this.error = error;
        }

        Map<String, Object> toMap() {
            Map<String, Object> map = map("status", status, "tool", tool);
            if (error != null && !error.isEmpty()) {
                map.put("error", error);
            }
            return map;
        }
    }

    /**
     * Structural Java pre-flight: brace/paren/bracket balance plus string, char,
     * line-comment, block-comment and text-block termination. This is deliberately
     * NOT a full Java parser (no Spring jars, no compiler in the loop) and says so
     * in the `tool` field. It rejects the conformance broken content while accepting
     * a partially-rewritten file that still balances.
     */
    static SyntaxResult javaSyntaxCheck(String text) {
        Deque<Character> stack = new ArrayDeque<>();
        Deque<Integer> lines = new ArrayDeque<>();
        int line = 1;
        int state = 0; // 0 code, 1 line comment, 2 block comment, 3 string, 4 char, 5 text block
        for (int i = 0; i < text.length(); i++) {
            char c = text.charAt(i);
            if (c == '\n') {
                line++;
            }
            switch (state) {
                case 0:
                    if (c == '/' && i + 1 < text.length() && text.charAt(i + 1) == '/') {
                        i++;
                        state = 1;
                    } else if (c == '/' && i + 1 < text.length() && text.charAt(i + 1) == '*') {
                        i++;
                        state = 2;
                    } else if (c == '"' && i + 2 < text.length() && text.charAt(i + 1) == '"'
                            && text.charAt(i + 2) == '"') {
                        i += 2;
                        state = 5;
                    } else if (c == '"') {
                        state = 3;
                    } else if (c == '\'') {
                        state = 4;
                    } else if (c == '{' || c == '(' || c == '[') {
                        stack.push(c == '{' ? '}' : (c == '(' ? ')' : ']'));
                        lines.push(line);
                    } else if (c == '}' || c == ')' || c == ']') {
                        if (stack.isEmpty()) {
                            return new SyntaxResult("failed", "jdk/brace-balance",
                                    "unexpected '" + c + "' at line " + line);
                        }
                        char expected = stack.pop();
                        lines.pop();
                        if (expected != c) {
                            return new SyntaxResult("failed", "jdk/brace-balance",
                                    "mismatched '" + c + "' at line " + line + " (expected '" + expected + "')");
                        }
                    }
                    break;
                case 1:
                    if (c == '\n') {
                        state = 0;
                    }
                    break;
                case 2:
                    if (c == '*' && i + 1 < text.length() && text.charAt(i + 1) == '/') {
                        i++;
                        state = 0;
                    }
                    break;
                case 3:
                case 4:
                    if (c == '\\') {
                        i++;
                    } else if ((state == 3 && c == '"') || (state == 4 && c == '\'')) {
                        state = 0;
                    }
                    break;
                case 5:
                    if (c == '"' && i + 2 < text.length() && text.charAt(i + 1) == '"'
                            && text.charAt(i + 2) == '"') {
                        i += 2;
                        state = 0;
                    }
                    break;
                default:
                    break;
            }
        }
        if (state == 2) {
            return new SyntaxResult("failed", "jdk/brace-balance", "unterminated block comment");
        }
        if (state == 3) {
            return new SyntaxResult("failed", "jdk/brace-balance", "unterminated string literal");
        }
        if (state == 4) {
            return new SyntaxResult("failed", "jdk/brace-balance", "unterminated char literal");
        }
        if (state == 5) {
            return new SyntaxResult("failed", "jdk/brace-balance", "unterminated text block");
        }
        if (!stack.isEmpty()) {
            return new SyntaxResult("failed", "jdk/brace-balance",
                    "unclosed '" + stack.peek() + "' opened at line " + lines.peek());
        }
        return new SyntaxResult("passed", "jdk/brace-balance", "");
    }

    // ------------------------------------------------------------- routes

    static final class Route {
        final String method;
        final String path;
        final String handler;
        final String rel;
        final int startLine;
        final int endLine;
        final String className;

        Route(String method, String path, String handler, String rel, int startLine, int endLine, String className) {
            this.method = method;
            this.path = path;
            this.handler = handler;
            this.rel = rel;
            this.startLine = startLine;
            this.endLine = endLine;
            this.className = className;
        }

        Map<String, Object> toMap() {
            return map(
                    "method", method,
                    "path", path,
                    "handlerName", handler,
                    "file", rel,
                    "className", className,
                    "startLine", startLine,
                    "endLine", endLine);
        }
    }

    static final Map<String, String> MAPPING_VERBS = new LinkedHashMap<>();

    static {
        MAPPING_VERBS.put("GetMapping", "GET");
        MAPPING_VERBS.put("PostMapping", "POST");
        MAPPING_VERBS.put("PutMapping", "PUT");
        MAPPING_VERBS.put("PatchMapping", "PATCH");
        MAPPING_VERBS.put("DeleteMapping", "DELETE");
    }

    static String joinPaths(String base, String sub) {
        String left = base == null ? "" : base.trim();
        String right = sub == null ? "" : sub.trim();
        if (left.isEmpty() && right.isEmpty()) {
            return "/";
        }
        if (left.isEmpty()) {
            return right.startsWith("/") ? right : "/" + right;
        }
        if (right.isEmpty()) {
            return left.startsWith("/") ? left : "/" + left;
        }
        if (left.endsWith("/") && right.startsWith("/")) {
            return left + right.substring(1);
        }
        if (!left.endsWith("/") && !right.startsWith("/")) {
            return left + "/" + right;
        }
        return left + right;
    }

    static List<Route> routesForFile(FileModel fm) {
        List<Route> routes = new ArrayList<>();
        for (Member type : fm.types) {
            if (!hasAnn(type.anns, "RestController", "Controller")) {
                continue;
            }
            Ann baseAnn = findAnn(type.anns, "RequestMapping");
            String basePath = annPath(baseAnn);
            List<String> classVerbs = annVerbs(baseAnn);
            for (Member method : fm.methods) {
                if (!type.name.equals(method.owner)) {
                    continue;
                }
                Ann mapping = findAnnAny(method.anns, "GetMapping", "PostMapping", "PutMapping",
                        "PatchMapping", "DeleteMapping", "RequestMapping");
                if (mapping == null) {
                    continue;
                }
                String subPath = annPath(mapping);
                List<String> verbs = new ArrayList<>();
                if (MAPPING_VERBS.containsKey(mapping.name)) {
                    verbs.add(MAPPING_VERBS.get(mapping.name));
                } else {
                    verbs.addAll(annVerbs(mapping));
                    if (verbs.isEmpty()) {
                        verbs.addAll(classVerbs);
                    }
                    if (verbs.isEmpty()) {
                        verbs.add("ANY");
                    }
                }
                String path = joinPaths(basePath, subPath);
                for (String verb : new LinkedHashSet<>(verbs)) {
                    routes.add(new Route(verb, path, method.owner + "#" + method.name, fm.rel,
                            method.startLine, method.endLine, type.name));
                }
            }
        }
        return routes;
    }

    // ------------------------------------------------------- member lookup

    static Member resolveSymbol(FileModel fm, String symbol) {
        String s = symbol == null ? "" : symbol.trim();
        if (s.isEmpty()) {
            return null;
        }
        String owner = null;
        String name = s;
        int hash = s.lastIndexOf('#');
        if (hash > 0 && hash < s.length() - 1) {
            owner = s.substring(0, hash);
            name = s.substring(hash + 1);
        } else {
            int dot = s.lastIndexOf('.');
            if (dot > 0 && dot < s.length() - 1 && Character.isUpperCase(s.charAt(dot + 1))) {
                owner = s.substring(0, dot);
                name = s.substring(dot + 1);
            }
        }
        if (owner != null) {
            int dot = owner.lastIndexOf('.');
            String simpleOwner = dot >= 0 ? owner.substring(dot + 1) : owner;
            for (Member type : fm.types) {
                if (type.name.equals(simpleOwner) && name.equals(type.name)) {
                    return type;
                }
            }
            for (Member method : fm.methods) {
                if (name.equals(method.name) && simpleOwner.equals(method.owner)) {
                    return method;
                }
            }
            // "Owner#symbol" where symbol is a field, or an unqualified type in a package
            return null;
        }
        for (Member type : fm.types) {
            if (type.name.equals(name)) {
                return type;
            }
        }
        Member unique = null;
        for (Member method : fm.methods) {
            if (method.name.equals(name)) {
                if (unique != null) {
                    return null; // ambiguous: never silently pick one overload
                }
                unique = method;
            }
        }
        return unique;
    }

    static List<Map<String, Object>> parseParams(FileModel fm, Member method) {
        List<Map<String, Object>> params = new ArrayList<>();
        if (method.parenOpen < 0 || method.parenClose <= method.parenOpen) {
            return params;
        }
        String raw = fm.text.substring(method.parenOpen + 1, method.parenClose);
        for (String chunk : splitTopLevel(raw, ',')) {
            String param = chunk.trim();
            if (param.isEmpty()) {
                continue;
            }
            List<String> annotations = new ArrayList<>();
            String where = null;
            String declaredName = null;
            Matcher matcher = Pattern.compile("@\\s*([A-Za-z_][A-Za-z0-9_.]*)").matcher(param);
            while (matcher.find()) {
                String simple = matcher.group(1);
                int dot = simple.lastIndexOf('.');
                if (dot >= 0) {
                    simple = simple.substring(dot + 1);
                }
                annotations.add("@" + simple);
                if (simple.equals("PathVariable")) {
                    where = "path";
                } else if (simple.equals("RequestParam") || simple.equals("RequestHeader")) {
                    where = simple.equals("RequestParam") ? "query" : "header";
                } else if (simple.equals("RequestBody")) {
                    where = "body";
                }
                String value = firstStringAfter(param.substring(matcher.start()), 0);
                if (!value.isEmpty()) {
                    declaredName = value;
                }
            }
            String head = stripAnnotations(param);
            int eq = head.indexOf('=');
            if (eq >= 0) {
                head = head.substring(0, eq);
            }
            head = head.trim();
            int nameStart = head.length();
            while (nameStart > 0 && Character.isJavaIdentifierPart(head.charAt(nameStart - 1))) {
                nameStart--;
            }
            String inferred = head.substring(nameStart).trim();
            String type = head.substring(0, nameStart);
            Matcher types = Pattern.compile("([A-Za-z_$][A-Za-z0-9_$.<>\\[\\], ?]*)$").matcher(type.trim());
            String typeText = types.find() ? types.group(1).trim() : type.trim();
            String name = declaredName != null ? declaredName : inferred;
            if (name.isEmpty()) {
                continue;
            }
            Map<String, Object> entry = map("name", name, "type", typeText);
            if (where != null) {
                entry.put("in", where);
            }
            if (!annotations.isEmpty()) {
                entry.put("annotations", annotations);
            }
            params.add(entry);
        }
        return params;
    }

    /** Removes @Annotation and @Annotation(...) prefixes so a parameter's type/name survive. */
    static String stripAnnotations(String text) {
        StringBuilder out = new StringBuilder(text.length());
        int i = 0;
        while (i < text.length()) {
            char c = text.charAt(i);
            if (c != '@') {
                out.append(c);
                i++;
                continue;
            }
            i++;
            while (i < text.length()
                    && (Character.isJavaIdentifierPart(text.charAt(i)) || text.charAt(i) == '.')) {
                i++;
            }
            int probe = i;
            while (probe < text.length() && Character.isWhitespace(text.charAt(probe))) {
                probe++;
            }
            if (probe < text.length() && text.charAt(probe) == '(') {
                int depth = 0;
                i = probe;
                while (i < text.length()) {
                    char d = text.charAt(i);
                    if (d == '(') {
                        depth++;
                    } else if (d == ')') {
                        depth--;
                        if (depth == 0) {
                            i++;
                            break;
                        }
                    }
                    i++;
                }
            }
            out.append(' ');
        }
        return out.toString();
    }

    static List<String> splitTopLevel(String text, char separator) {        List<String> parts = new ArrayList<>();
        int angle = 0;
        int paren = 0;
        int bracket = 0;
        int start = 0;
        for (int i = 0; i < text.length(); i++) {
            char c = text.charAt(i);
            if (c == '<') {
                angle++;
            } else if (c == '>') {
                angle = Math.max(0, angle - 1);
            } else if (c == '(') {
                paren++;
            } else if (c == ')') {
                paren = Math.max(0, paren - 1);
            } else if (c == '[') {
                bracket++;
            } else if (c == ']') {
                bracket = Math.max(0, bracket - 1);
            } else if (c == separator && angle == 0 && paren == 0 && bracket == 0) {
                parts.add(text.substring(start, i));
                start = i + 1;
            }
        }
        parts.add(text.substring(start));
        return parts;
    }

    // ------------------------------------------------------------- sessions

    static final class Sessions {
        final Map<String, Long> expiries = new ConcurrentHashMap<>();
        final long ttlSeconds;

        Sessions(long ttlSeconds) {
            this.ttlSeconds = ttlSeconds;
        }

        String create() {
            String id = "sess-" + randomHex(8);
            expiries.put(id, Instant.now().getEpochSecond() + ttlSeconds);
            return id;
        }

        boolean validate(String id) {
            if (blank(id)) {
                return true;
            }
            Long expiry = expiries.get(id);
            if (expiry == null) {
                return false;
            }
            if (Instant.now().getEpochSecond() > expiry) {
                expiries.remove(id);
                return false;
            }
            return true;
        }

        boolean delete(String id) {
            if (blank(id)) {
                return false;
            }
            return expiries.remove(id) != null;
        }
    }

    // --------------------------------------------------------------- engine

    static final class Engine {
        final Options opts;
        final Workspace ws;
        final Sessions sessions = new Sessions(DEFAULT_SESSION_TTL_SECONDS);
        final Instant startedAt = Instant.now();
        final Deque<Long> internalErrors = new ArrayDeque<>();
        final Object mutateLock = new Object();
        final Object indexLock = new Object();
        volatile ScanIndex index;
        volatile String descriptorFile;

        Engine(Options options) throws IOException {
            this.opts = options;
            this.ws = new Workspace(options.root);
        }

        // ---- index

        static final class ScanIndex {
            final List<Route> routes;
            final List<FileModel> files;

            ScanIndex(List<Route> routes, List<FileModel> files) {
                this.routes = routes;
                this.files = files;
            }
        }

        ScanIndex index() {
            ScanIndex current = index;
            if (current != null) {
                return current;
            }
            synchronized (indexLock) {
                if (index != null) {
                    return index;
                }
                List<FileModel> files = new ArrayList<>();
                List<Route> routes = new ArrayList<>();
                try {
                    List<Path> targets = new ArrayList<>();
                    Files.walkFileTree(ws.root, new SimpleFileVisitor<>() {
                        @Override
                        public FileVisitResult preVisitDirectory(Path dir, BasicFileAttributes attrs) {
                            String name = dir.getFileName() == null ? "" : dir.getFileName().toString();
                            if (!dir.equals(ws.root) && (name.equals("target") || name.equals("build")
                                    || name.equals(".git") || name.equals("node_modules")
                                    || name.equals(".tokensafe") || name.equals(".gradle")
                                    || name.equals(".idea") || name.equals("out"))) {
                                return FileVisitResult.SKIP_SUBTREE;
                            }
                            return FileVisitResult.CONTINUE;
                        }

                        @Override
                        public FileVisitResult visitFile(Path file, BasicFileAttributes attrs) {
                            if (!attrs.isRegularFile()) {
                                return FileVisitResult.CONTINUE;
                            }
                            String name = file.getFileName().toString();
                            if (name.endsWith(".java")) {
                                targets.add(file);
                            }
                            return FileVisitResult.CONTINUE;
                        }
                    });
                    targets.sort((a, b) -> ws.relative(a).compareTo(ws.relative(b)));
                    for (Path file : targets) {
                        try {
                            String text = new String(Files.readAllBytes(file), StandardCharsets.UTF_8);
                            FileModel fm = new FileModel(ws.relative(file), file, text);
                            scanBody(fm, 0, fm.text.length(), 0, null);
                            files.add(fm);
                            routes.addAll(routesForFile(fm));
                        } catch (IOException ignored) {
                            // an unreadable source file must not fail the whole surface
                        }
                    }
                    routes.sort((a, b) -> {
                        int byPath = a.path.compareTo(b.path);
                        return byPath != 0 ? byPath : a.method.compareTo(b.method);
                    });
                } catch (IOException err) {
                    System.err.println("[oumv-java-spring] scan error: " + err.getMessage());
                }
                index = new ScanIndex(Collections.unmodifiableList(routes), Collections.unmodifiableList(files));
                return index;
            }
        }

        Object scanDump() {
            ScanIndex idx = index();
            List<Object> routeMaps = new ArrayList<>();
            for (Route route : idx.routes) {
                routeMaps.add(route.toMap());
            }
            List<Object> controllers = new ArrayList<>();
            for (FileModel fm : idx.files) {
                for (Member type : fm.types) {
                    if (hasAnn(type.anns, "RestController", "Controller")) {
                        controllers.add(map("class", type.name, "file", fm.rel, "startLine", type.startLine,
                                "endLine", type.endLine));
                    }
                }
            }
            return map(
                    "root", ws.root.toString(),
                    "framework", FRAMEWORK,
                    "routeCount", idx.routes.size(),
                    "routes", routeMaps,
                    "controllers", controllers,
                    "javaFiles", idx.files.size());
        }

        // ---- helpers

        void noteInternalError() {
            long now = System.currentTimeMillis();
            synchronized (internalErrors) {
                internalErrors.addLast(now);
                while (!internalErrors.isEmpty() && now - internalErrors.peekFirst() > 60_000L) {
                    internalErrors.pollFirst();
                }
            }
        }

        int errorCount1m() {
            long now = System.currentTimeMillis();
            synchronized (internalErrors) {
                while (!internalErrors.isEmpty() && now - internalErrors.peekFirst() > 60_000L) {
                    internalErrors.pollFirst();
                }
                return internalErrors.size();
            }
        }

        void requireSession(Map<String, Object> params) throws RpcException {
            String sessionId = str(params, "sessionId");
            if (!blank(sessionId) && !sessions.validate(sessionId)) {
                throw RpcException.validation("session_expired",
                        "unknown or expired sessionId " + sessionId + "; re-run oumv/handshake",
                        map("sessionId", sessionId));
            }
        }

        // ---- handshake

        Map<String, Object> handshake(Map<String, Object> params) throws RpcException {
            String clientVersion = str(params, "protocolVersion");
            if (!blank(clientVersion)) {
                int[] parsed = parseProtocolVersion(clientVersion);
                if (parsed == null || parsed[0] != SUPPORTED_MAJOR || parsed[1] > SUPPORTED_MINOR_MAX) {
                    throw RpcException.unsupported("unsupported_protocol_version",
                            "protocol version " + clientVersion + " (this runtime serves " + PROTOCOL_VERSION + ")");
                }
            }
            List<String> levels = new ArrayList<>(Arrays.asList(OBSERVE_LEVELS));
            Object requested = params.get("requestedCapabilities");
            if (requested instanceof Map) {
                Object observe = ((Map<?, ?>) requested).get("observe");
                if (observe instanceof Map) {
                    Object wanted = ((Map<?, ?>) observe).get("levels");
                    if (wanted instanceof List && !((List<?>) wanted).isEmpty()) {
                        List<String> narrowed = new ArrayList<>();
                        for (Object level : (List<?>) wanted) {
                            if (level instanceof String && levels.contains(level)) {
                                narrowed.add((String) level);
                            }
                        }
                        levels = narrowed;
                    }
                }
            }
            String sessionId = sessions.create();
            Map<String, Object> capabilities = map(
                    "observe", map("levels", levels, "formats", list("json")),
                    "understand", map("causality", false, "impactAnalysis", true));
            if (opts.readOnly) {
                // §8.5: no writing pillar is advertised because none is registered.
                capabilities.put("readOnly", true);
            } else {
                capabilities.put("mutate", map("occ", true, "syntaxGuard", true));
                capabilities.put("verify", map("multiMetric", true));
            }
            return map(
                    "protocolVersion", PROTOCOL_VERSION,
                    "profile", PROFILE,
                    "sessionId", sessionId,
                    "ttlSeconds", DEFAULT_SESSION_TTL_SECONDS,
                    "runtimeInfo", map("name", NAME, "version", VERSION, "framework", FRAMEWORK),
                    "capabilities", capabilities);
        }

        Map<String, Object> disconnect(Map<String, Object> params) throws RpcException {
            String sessionId = str(params, "sessionId");
            if (blank(sessionId)) {
                throw RpcException.validation("session_required",
                        "sessionId is required for oumv/disconnect", map("sessionId", ""));
            }
            boolean existed = sessions.delete(sessionId);
            if (!existed) {
                throw RpcException.validation("session_expired",
                        "unknown or expired sessionId " + sessionId, map("sessionId", sessionId));
            }
            return map("disconnected", true, "sessionId", sessionId);
        }

        static int[] parseProtocolVersion(String raw) {
            String[] parts = raw.trim().split("\\.");
            if (parts.length < 2) {
                return null;
            }
            try {
                return new int[] { Integer.parseInt(parts[0]), Integer.parseInt(parts[1]) };
            } catch (NumberFormatException err) {
                return null;
            }
        }

        // ---- observe

        Map<String, Object> observe(Map<String, Object> params) throws RpcException {
            String level = str(params, "level", "L0").trim().toUpperCase(Locale.ROOT);
            if (!Arrays.asList(OBSERVE_LEVELS).contains(level)) {
                if (level.equals("L4")) {
                    throw RpcException.unsupported("level_not_implemented",
                            "observe L4 requires live request traces; this JDK-only sidecar does not run the "
                                    + "Spring application, so it records none");
                }
                throw RpcException.unsupported("unsupported_level", "observe level " + level);
            }
            switch (level) {
                case "L0":
                    return observeL0();
                case "L1":
                    return observeL1(params);
                case "L2":
                    return observeL2(params);
                case "L3":
                    return observeL3(params);
                default:
                    throw RpcException.unsupported("unsupported_level", "observe level " + level);
            }
        }

        Map<String, Object> observeL0() {
            int errors = errorCount1m();
            String status = errors > 0 ? "DEGRADED" : "HEALTHY";
            Map<String, Object> data = map(
                    "status", status,
                    "uptimeSeconds", Math.max(0, Instant.now().getEpochSecond() - startedAt.getEpochSecond()),
                    "activeRoutes", index().routes.size(),
                    "errorCount1m", errors,
                    "framework", FRAMEWORK);
            return envelope("L0", data, null);
        }

        Map<String, Object> observeL1(Map<String, Object> params) {
            List<Route> routes = index().routes;
            int limit = intParam(params, "limit", 0);
            int start = 0;
            String cursor = str(params, "cursor");
            if (!blank(cursor)) {
                try {
                    start = Math.max(0, Integer.parseInt(cursor.trim()));
                } catch (NumberFormatException ignored) {
                    start = 0;
                }
            }
            if (start > routes.size()) {
                start = routes.size();
            }
            int end = routes.size();
            boolean hasMore = false;
            String nextCursor = null;
            if (limit > 0 && start + limit < routes.size()) {
                end = start + limit;
                hasMore = true;
                nextCursor = Integer.toString(end);
            }
            List<Object> page = new ArrayList<>();
            for (Route route : routes.subList(start, end)) {
                page.add(route.toMap());
            }
            Map<String, Object> data = map(
                    "routes", page,
                    // spec §4.1 names the collection `endpoints` for the backend
                    // profile; both keys carry the identical array (unknown fields
                    // are ignored by conforming clients).
                    "endpoints", page,
                    "total", routes.size());
            if (limit > 0) {
                data.put("hasMore", hasMore);
                if (nextCursor != null) {
                    data.put("nextCursor", nextCursor);
                }
            }
            if (routes.isEmpty()) {
                data.put("note", "no @RestController/@Controller handler routes found under "
                        + ws.root + " (scanned " + index().files.size() + " .java files)");
            }
            return envelope("L1", data, null);
        }

        Map<String, Object> observeL2(Map<String, Object> params) throws RpcException {
            String target = str(params, "target");
            if (blank(target)) {
                throw RpcException.invalidParams("target_required", "target is required for observe L2");
            }
            Route route = findRoute(target);
            if (route != null) {
                FileModel fm = fileFor(route.rel);
                Member method = null;
                if (fm != null) {
                    for (Member candidate : fm.methods) {
                        if (candidate.symbol().equals(route.handler)) {
                            method = candidate;
                            break;
                        }
                    }
                }
                Map<String, Object> data = map(
                        "target", target,
                        "kind", "endpoint",
                        "method", route.method,
                        "path", route.path,
                        "handler", route.handler,
                        "handlerName", route.handler,
                        "file", route.rel,
                        "startLine", route.startLine,
                        "endLine", route.endLine,
                        "note", "contract derived statically from Spring annotations in " + route.rel
                                + "; no live ApplicationContext exists in this sidecar, so no DTO reflection is claimed");
                if (method != null) {
                    List<Map<String, Object>> paramsList = parseParams(fm, method);
                    if (!paramsList.isEmpty()) {
                        data.put("params", new ArrayList<Object>(paramsList));
                    }
                    String declaredReturn = declaredReturnType(fm, method);
                    if (!blank(declaredReturn)) {
                        data.put("responseType", declaredReturn);
                        List<Object> responses = new ArrayList<>();
                        responses.add(map("declaredType", declaredReturn,
                                "note", "declared Java return type; no HTTP status mapping is inferred "
                                        + "without a live Spring context"));
                        data.put("responses", responses);
                    }
                }
                data.put("evidence", map("provenance", "static", "sourceAdapter", NAME, "confidence", "probable"));
                return envelope("L2", data, evidenceStatic());
            }
            FileModel model = findType(target);
            if (model != null) {
                Member type = null;
                String simple = simpleName(target);
                for (Member candidate : model.types) {
                    if (candidate.name.equals(simple)) {
                        type = candidate;
                        break;
                    }
                }
                if (type != null) {
                    List<Object> fields = new ArrayList<>();
                    for (FieldInfo field : model.fields) {
                        if (type.name.equals(field.owner)) {
                            fields.add(map("name", field.name, "type", field.type));
                        }
                    }
                    Map<String, Object> data = map(
                            "target", target,
                            "kind", "model",
                            "model", type.name,
                            "file", model.rel,
                            "startLine", type.startLine,
                            "endLine", type.endLine,
                            "fields", fields,
                            "note", "model shape derived statically from the declaration; annotations are not resolved "
                                    + "to a schema (no Spring context and no reflection on the classpath)");
                    data.put("evidence", map("provenance", "static", "sourceAdapter", NAME, "confidence", "probable"));
                    return envelope("L2", data, evidenceStatic());
                }
            }
            throw RpcException.resourceNotFound("target_not_found",
                    "no Spring route or model under " + ws.root + " matches target " + target);
        }

        Map<String, Object> observeL3(Map<String, Object> params) throws RpcException {
            String target = str(params, "target");
            if (blank(target)) {
                throw RpcException.invalidParams("target_required", "target is required for observe L3");
            }
            if (!target.contains("#") && endpointOf(target) != null) {
                Route route = findRoute(target);
                if (route == null) {
                    throw RpcException.resourceNotFound("target_not_found",
                            "no Spring route matches target " + target);
                }
                Path file = ws.root.resolve(route.rel);
                if (!Files.isRegularFile(file)) {
                    throw RpcException.resourceNotFound("file_not_found",
                            "route " + route.handler + " points at a missing file " + route.rel);
                }
                byte[] bytes;
                try {
                    bytes = Files.readAllBytes(file);
                } catch (IOException err) {
                    throw RpcException.resourceNotFound("file_not_found", "cannot read " + route.rel);
                }
                String text = new String(bytes, StandardCharsets.UTF_8);
                SourceText source = new SourceText(text);
                Map<String, Object> data = map(
                        "symbol", route.handler,
                        "path", route.rel,
                        "source", source.slice(route.startLine, route.endLine),
                        "version", sha256(bytes),
                        "startLine", route.startLine,
                        "endLine", route.endLine);
                return envelope("L3", data,
                        map("provenance", "static", "sourceAdapter", NAME, "confidence", "confirmed"));
            }
            String path = target;
            String symbol = null;
            int hash = target.lastIndexOf('#');
            if (hash > 0 && hash < target.length() - 1 && !target.substring(hash + 1).contains("/")) {
                path = target.substring(0, hash);
                symbol = target.substring(hash + 1);
            }
            Map<String, Object> sourceResult = getSource(map("path", path, "symbol", symbol));
            Map<String, Object> data = map(
                    "symbol", sourceResult.getOrDefault("symbol", symbol == null ? "" : symbol),
                    "path", sourceResult.get("path"),
                    "source", sourceResult.get("source"),
                    "version", sourceResult.get("version"),
                    "startLine", sourceResult.get("startLine"),
                    "endLine", sourceResult.get("endLine"));
            return envelope("L3", data,
                    map("provenance", "static", "sourceAdapter", NAME, "confidence", "confirmed"));
        }

        static Map<String, Object> envelope(String level, Map<String, Object> data, Map<String, Object> evidence) {
            Map<String, Object> result = map("level", level, "format", "json", "data", data);
            if (evidence != null) {
                result.put("evidence", evidence);
            }
            return result;
        }

        static Map<String, Object> evidenceStatic() {
            return map("provenance", "static", "sourceAdapter", NAME, "confidence", "probable");
        }

        String declaredReturnType(FileModel fm, Member method) {
            String head = fm.text.substring(method.declStart, method.parenOpen).trim();
            int nameStart = head.length();
            while (nameStart > 0 && Character.isJavaIdentifierPart(head.charAt(nameStart - 1))) {
                nameStart--;
            }
            String type = head.substring(0, nameStart).trim();
            type = type.replaceAll("^(public|protected|private|static|final|synchronized|abstract|default)\\s+", "");
            return type;
        }

        FileModel fileFor(String rel) {
            for (FileModel fm : index().files) {
                if (fm.rel.equals(rel)) {
                    return fm;
                }
            }
            return null;
        }

        FileModel findType(String target) {
            String simple = simpleName(target);
            for (FileModel fm : index().files) {
                for (Member type : fm.types) {
                    if (type.name.equals(simple)) {
                        return fm;
                    }
                }
            }
            return null;
        }

        static String simpleName(String target) {
            String s = target.trim();
            int hash = s.indexOf('#');
            if (hash > 0) {
                s = s.substring(0, hash);
            }
            int dot = s.lastIndexOf('.');
            if (dot >= 0) {
                s = s.substring(dot + 1);
            }
            return s;
        }

        // ---- route matching

        static String[] endpointOf(String target) {
            String t = target == null ? "" : target.trim();
            if (t.isEmpty()) {
                return null;
            }
            if (t.startsWith("/")) {
                return new String[] { "", t };
            }
            int space = t.indexOf(' ');
            if (space > 0) {
                String method = t.substring(0, space).trim().toUpperCase(Locale.ROOT);
                String path = t.substring(space + 1).trim();
                if (path.startsWith("/") && method.matches("[A-Z]+")) {
                    return new String[] { method, path };
                }
            }
            return null;
        }

        Route findRoute(String target) {
            String[] endpoint = endpointOf(target);
            String method = null;
            String path = null;
            if (endpoint != null) {
                method = endpoint[0].isEmpty() ? null : endpoint[0];
                path = endpoint[1];
            }
            Route bySymbol = null;
            for (Route route : index().routes) {
                if (route.handler.equals(target.trim())) {
                    return route;
                }
            }
            for (Route route : index().routes) {
                if (path == null) {
                    continue;
                }
                boolean methodOk = method == null || route.method.equalsIgnoreCase(method) || route.method.equals("ANY");
                if (!methodOk) {
                    continue;
                }
                if (route.path.equals(path)) {
                    return route;
                }
                if (bySymbol == null && templateMatches(route.path, path)) {
                    bySymbol = route;
                }
            }
            return bySymbol;
        }

        static boolean templateMatches(String pattern, String concrete) {
            String[] left = pattern.split("/", -1);
            String[] right = concrete.split("/", -1);
            if (left.length != right.length) {
                return false;
            }
            for (int i = 0; i < left.length; i++) {
                String segment = left[i];
                if (segment.equals(right[i])) {
                    continue;
                }
                if (segment.startsWith("{") && segment.endsWith("}") && !right[i].isEmpty()) {
                    continue;
                }
                return false;
            }
            return true;
        }

        // ---- getSource

        Map<String, Object> getSource(Map<String, Object> params) throws RpcException {
            String requestedPath = firstNonBlank(str(params, "path"), str(params, "filePath"));
            Path file = ws.resolve(requestedPath);
            if (!Files.isRegularFile(file)) {
                throw RpcException.resourceNotFound("file_not_found", "file does not exist: " + requestedPath);
            }
            byte[] bytes;
            try {
                bytes = Files.readAllBytes(file);
            } catch (IOException err) {
                throw RpcException.resourceNotFound("file_not_found", "cannot read " + requestedPath);
            }
            String version = sha256(bytes);
            String symbol = str(params, "symbol");
            String ifVersion = str(params, "ifVersion");
            if (!blank(ifVersion) && ifVersion.equals(version)) {
                Map<String, Object> result = map("path", requestedPath, "version", version, "unchanged", true);
                if (!blank(symbol)) {
                    result.put("symbol", symbol);
                }
                return result;
            }
            String text = new String(bytes, StandardCharsets.UTF_8);
            SourceText source = new SourceText(text);
            int startLine = 1;
            int endLine = source.lineCount();
            if (!blank(symbol)) {
                FileModel fm = new FileModel(ws.relative(file), file, text);
                scanBody(fm, 0, text.length(), 0, null);
                Member member = resolveSymbol(fm, symbol);
                if (member == null) {
                    throw RpcException.resourceNotFound("symbol_not_found",
                            "symbol " + symbol + " does not exist in " + requestedPath);
                }
                startLine = member.startLine;
                endLine = member.endLine;
            }
            int requestedStart = intParam(params, "startLine", 0);
            int requestedEnd = intParam(params, "endLine", 0);
            if (requestedStart > 0) {
                startLine = requestedStart;
            }
            if (requestedEnd > 0) {
                endLine = requestedEnd;
            }
            if (startLine < 1) {
                startLine = 1;
            }
            if (startLine > source.lineCount()) {
                startLine = source.lineCount();
            }
            if (endLine > source.lineCount()) {
                endLine = source.lineCount();
            }
            if (endLine < startLine) {
                endLine = startLine;
            }
            Map<String, Object> result = map(
                    "path", requestedPath,
                    "source", source.slice(startLine, endLine),
                    "version", version,
                    "startLine", startLine,
                    "endLine", endLine);
            if (!blank(symbol)) {
                result.put("symbol", symbol);
            }
            return result;
        }

        // ---- applyEdit

        static final Set<String> MODES = new LinkedHashSet<>(
                Arrays.asList("replace", "create", "symbol", "delete", "rename", "patch"));

        Map<String, Object> applyEdit(Map<String, Object> params) throws RpcException {
            String requestedPath = firstNonBlank(str(params, "path"), str(params, "filePath"));
            String mode = str(params, "mode", "replace").trim().toLowerCase(Locale.ROOT);
            if (mode.isEmpty()) {
                mode = "replace";
            }
            String replacement = firstNonBlank(str(params, "replacement"), str(params, "source"));
            String expectedVersion = str(params, "expectedVersion");
            synchronized (mutateLock) {
                Path file = ws.resolve(requestedPath); // guard 1: boundary (-32003)
                if (!MODES.contains(mode)) {
                    throw RpcException.unsupported("unknown_mode",
                            "unknown applyEdit mode " + mode + " (supported: replace, create, symbol, delete, rename, patch)");
                }
                switch (mode) {
                    case "create":
                        return createFile(requestedPath, file, replacement, expectedVersion);
                    case "patch":
                        return patchFile(params, requestedPath, file, replacement, expectedVersion);
                    case "delete":
                        return deleteFile(requestedPath, file, replacement, expectedVersion);
                    case "rename":
                        return renameFile(params, requestedPath, file, replacement, expectedVersion);
                    case "symbol":
                        return symbolReplace(requestedPath, file, replacement, expectedVersion, str(params, "symbol"));
                    default:
                        return replaceFile(requestedPath, file, replacement, expectedVersion, str(params, "symbol"));
                }
            }
        }

        Map<String, Object> replaceFile(String requestedPath, Path file, String replacement,
                String expectedVersion, String symbol) throws RpcException {
            if (!Files.isRegularFile(file)) {
                throw RpcException.resourceNotFound("file_not_found", "file does not exist: " + requestedPath);
            }
            byte[] current = readBytes(file, requestedPath);
            String currentVersion = sha256(current);
            if (blank(replacement)) {
                throw RpcException.validation("replacement_required",
                        "replacement is required for mode=replace", map("path", requestedPath));
            }
            SyntaxResult syntax = javaSyntaxCheck(replacement);
            if (!syntax.status.equals("passed")) {
                throw RpcException.syntax("pre-flight syntax check failed for " + requestedPath, syntax.error);
            }
            requireVersion(expectedVersion, currentVersion, requestedPath);
            Path backup = backupOrFail(file);
            byte[] next = replacement.getBytes(StandardCharsets.UTF_8);
            writeOrFail(file, next);
            Map<String, Object> result = map(
                    "status", "APPLIED",
                    "path", requestedPath,
                    "newVersion", sha256(next),
                    "syntaxCheck", syntax.toMap(),
                    "backupPath", backup.toString());
            if (!blank(symbol)) {
                result.put("symbol", symbol);
            }
            return result;
        }

        Map<String, Object> createFile(String requestedPath, Path file, String replacement,
                String expectedVersion) throws RpcException {
            if (Files.exists(file)) {
                throw RpcException.conflict("file_exists",
                        "file already exists; use mode=replace with its version", map("path", requestedPath));
            }
            if (blank(replacement)) {
                throw RpcException.validation("replacement_required",
                        "replacement is required for mode=create", map("path", requestedPath));
            }
            SyntaxResult syntax = javaSyntaxCheck(replacement);
            if (!syntax.status.equals("passed")) {
                throw RpcException.syntax("pre-flight syntax check failed for " + requestedPath, syntax.error);
            }
            if (!blank(expectedVersion)) {
                throw RpcException.validation("create_forbids_version",
                        "expectedVersion must be omitted for mode=create (a create never overwrites)",
                        map("path", requestedPath, "expectedVersion", expectedVersion));
            }
            byte[] next = replacement.getBytes(StandardCharsets.UTF_8);
            writeOrFail(file, next);
            return map(
                    "status", "APPLIED",
                    "path", requestedPath,
                    "newVersion", sha256(next),
                    "syntaxCheck", syntax.toMap(),
                    "created", true);
        }

        Map<String, Object> patchFile(Map<String, Object> params, String requestedPath, Path file,
                String replacement, String expectedVersion) throws RpcException {
            if (!Files.isRegularFile(file)) {
                throw RpcException.resourceNotFound("file_not_found", "file does not exist: " + requestedPath);
            }
            Object rawHunks = params.get("hunks");
            List<?> hunks = rawHunks instanceof List ? (List<?>) rawHunks : Collections.emptyList();
            if (hunks.isEmpty()) {
                throw RpcException.validation("hunks_required",
                        "hunks is required for mode=patch", map("path", requestedPath));
            }
            byte[] currentBytes = readBytes(file, requestedPath);
            String currentText = new String(currentBytes, StandardCharsets.UTF_8);
            String currentVersion = sha256(currentBytes);
            requireVersion(expectedVersion, currentVersion, requestedPath);
            String patched = currentText;
            for (int index = 0; index < hunks.size(); index++) {
                Object raw = hunks.get(index);
                if (!(raw instanceof Map)) {
                    throw RpcException.validation("hunk_invalid", "hunk " + index + " is not an object",
                            map("hunk", index));
                }
                Map<?, ?> hunk = (Map<?, ?>) raw;
                Object targetValue = hunk.get("target");
                Object replacementValue = hunk.get("replacement");
                String target = targetValue instanceof String ? (String) targetValue : null;
                String hunkReplacement = replacementValue instanceof String ? (String) replacementValue : "";
                if (blank(target)) {
                    throw RpcException.validation("target_required", "hunk " + index + " has an empty target",
                            map("hunk", index));
                }
                patched = applyHunk(patched, index, target, hunkReplacement,
                        intOf(hunk.get("startLine")), intOf(hunk.get("endLine")),
                        Boolean.TRUE.equals(hunk.get("replaceAll")));
            }
            SyntaxResult syntax = javaSyntaxCheck(patched);
            if (!syntax.status.equals("passed")) {
                throw RpcException.syntax("pre-flight syntax check failed for the patched " + requestedPath,
                        syntax.error);
            }
            Path backup = backupOrFail(file);
            byte[] next = patched.getBytes(StandardCharsets.UTF_8);
            writeOrFail(file, next);
            return map(
                    "status", "APPLIED",
                    "path", requestedPath,
                    "newVersion", sha256(next),
                    "syntaxCheck", syntax.toMap(),
                    "hunksApplied", hunks.size(),
                    "backupPath", backup.toString());
        }

        static int intOf(Object value) {
            if (value instanceof Number) {
                return ((Number) value).intValue();
            }
            if (value instanceof String) {
                try {
                    return Integer.parseInt((String) value);
                } catch (NumberFormatException ignored) {
                    return 0;
                }
            }
            return 0;
        }

        /** Exact-snippet splice with the spec §4.3 uniqueness rules. */
        static String applyHunk(String text, int index, String target, String replacement,
                int startLine, int endLine, boolean replaceAll) throws RpcException {
            int from = 0;
            int to = text.length();
            if (startLine > 0 || endLine > 0) {
                SourceText source = new SourceText(text);
                int start = startLine > 0 ? startLine : 1;
                int end = endLine > 0 ? endLine : source.lineCount();
                if (start > source.lineCount()) {
                    start = source.lineCount();
                }
                if (end > source.lineCount()) {
                    end = source.lineCount();
                }
                if (end < start) {
                    throw RpcException.validation("inverted_line_range",
                            "hunk " + index + " endLine precedes startLine", map("hunk", index));
                }
                from = source.starts[start - 1];
                to = end >= source.lineCount() ? text.length() : source.starts[end] - 1;
            }
            if (to < from) {
                to = from;
            }
            String region = text.substring(from, to);
            int count = countOccurrences(region, target);
            if (count == 0) {
                throw RpcException.resourceNotFound("target_not_found",
                        "hunk " + index + " target does not occur in the file");
            }
            if (count > 1 && !replaceAll) {
                Map<String, Object> data = map("hunk", index, "count", count,
                        "lines", occurrenceLines(region, from, text, target));
                throw RpcException.validation("target_not_unique",
                        "hunk " + index + " target occurs " + count
                                + " times; narrow it with startLine/endLine or set replaceAll",
                        data);
            }
            String spliced = replaceAll
                    ? region.replace(target, replacement)
                    : replaceFirst(region, target, replacement);
            return text.substring(0, from) + spliced + text.substring(to);
        }

        static int countOccurrences(String region, String target) {
            int count = 0;
            int index = 0;
            while (true) {
                int found = region.indexOf(target, index);
                if (found < 0) {
                    return count;
                }
                count++;
                index = found + target.length();
            }
        }

        static String replaceFirst(String region, String target, String replacement) {
            int index = region.indexOf(target);
            if (index < 0) {
                return region;
            }
            return region.substring(0, index) + replacement + region.substring(index + target.length());
        }

        static List<Object> occurrenceLines(String region, int from, String whole, String target) {
            List<Object> lines = new ArrayList<>();
            SourceText source = new SourceText(whole);
            int offset = 0;
            while (lines.size() < 5) {
                int found = region.indexOf(target, offset);
                if (found < 0) {
                    break;
                }
                lines.add(source.lineOf(from + found));
                offset = found + target.length();
            }
            return lines;
        }

        Map<String, Object> deleteFile(String requestedPath, Path file, String replacement,
                String expectedVersion) throws RpcException {
            if (!Files.isRegularFile(file)) {
                throw RpcException.resourceNotFound("file_not_found", "file does not exist: " + requestedPath);
            }
            if (!blank(replacement)) {
                throw RpcException.validation("replacement_forbidden",
                        "replacement must be omitted for mode=delete", map("path", requestedPath));
            }
            byte[] current = readBytes(file, requestedPath);
            String currentVersion = sha256(current);
            requireVersion(expectedVersion, currentVersion, requestedPath);
            Path backup = backupOrFail(file);
            try {
                Files.delete(file);
            } catch (IOException err) {
                throw RpcException.internal("failed to delete " + requestedPath + ": " + err.getMessage());
            }
            return map(
                    "status", "APPLIED",
                    "path", requestedPath,
                    "deleted", true,
                    "syntaxCheck", map("status", "skipped"),
                    "backupPath", backup.toString());
        }

        Map<String, Object> renameFile(Map<String, Object> params, String requestedPath, Path file,
                String replacement, String expectedVersion) throws RpcException {
            String renameTo = str(params, "renameTo");
            if (blank(renameTo)) {
                throw RpcException.validation("rename_to_required",
                        "renameTo is required for mode=rename", map("path", requestedPath));
            }
            Path target = ws.resolve(renameTo); // guard 1 also covers renameTo
            if (!Files.isRegularFile(file)) {
                throw RpcException.resourceNotFound("file_not_found", "file does not exist: " + requestedPath);
            }
            if (Files.exists(target)) {
                throw RpcException.conflict("target_exists",
                        "rename target already exists: " + renameTo, map("renameTo", renameTo));
            }
            if (!blank(replacement)) {
                throw RpcException.validation("replacement_forbidden",
                        "replacement must be omitted for mode=rename", map("path", requestedPath));
            }
            byte[] current = readBytes(file, requestedPath);
            requireVersion(expectedVersion, sha256(current), requestedPath);
            try {
                if (target.getParent() != null) {
                    Files.createDirectories(target.getParent());
                }
                Files.move(file, target);
            } catch (IOException err) {
                throw RpcException.internal("failed to rename " + requestedPath + ": " + err.getMessage());
            }
            return map(
                    "status", "APPLIED",
                    "path", requestedPath,
                    "newPath", renameTo,
                    "syntaxCheck", map("status", "skipped"));
        }

        Map<String, Object> symbolReplace(String requestedPath, Path file, String replacement,
                String expectedVersion, String symbol) throws RpcException {
            if (!Files.isRegularFile(file)) {
                throw RpcException.resourceNotFound("file_not_found", "file does not exist: " + requestedPath);
            }
            if (blank(symbol)) {
                throw RpcException.validation("symbol_required",
                        "symbol is required for mode=symbol", map("path", requestedPath));
            }
            byte[] currentBytes = readBytes(file, requestedPath);
            String text = new String(currentBytes, StandardCharsets.UTF_8);
            FileModel fm = new FileModel(ws.relative(file), file, text);
            scanBody(fm, 0, text.length(), 0, null);
            Member member = resolveSymbol(fm, symbol);
            if (member == null) {
                throw RpcException.resourceNotFound("symbol_not_found",
                        "symbol " + symbol + " does not exist in " + requestedPath);
            }
            String spliced = text.substring(0, member.startOffset)
                    + (replacement == null ? "" : replacement)
                    + text.substring(member.endOffset);
            SyntaxResult syntax = javaSyntaxCheck(spliced);
            if (!syntax.status.equals("passed")) {
                throw RpcException.syntax("pre-flight syntax check failed for the symbol edit in " + requestedPath,
                        syntax.error);
            }
            requireVersion(expectedVersion, sha256(currentBytes), requestedPath);
            Path backup = backupOrFail(file);
            byte[] next = spliced.getBytes(StandardCharsets.UTF_8);
            writeOrFail(file, next);
            return map(
                    "status", "APPLIED",
                    "path", requestedPath,
                    "symbol", symbol,
                    "newVersion", sha256(next),
                    "syntaxCheck", syntax.toMap(),
                    "backupPath", backup.toString());
        }

        static void requireVersion(String expectedVersion, String currentVersion, String path)
                throws RpcException {
            if (blank(expectedVersion)) {
                throw RpcException.validation("expected_version_required",
                        "expectedVersion is required to edit an existing file — call oumv/getSource "
                                + "(or observe L3) and pass its version",
                        map("path", path, "actualVersion", currentVersion));
            }
            if (!expectedVersion.equals(currentVersion)) {
                throw RpcException.conflict("version_mismatch",
                        "version mismatch: expected " + expectedVersion + ", current is " + currentVersion,
                        map("expectedVersion", expectedVersion, "actualVersion", currentVersion));
            }
        }

        byte[] readBytes(Path file, String requestedPath) throws RpcException {
            try {
                return Files.readAllBytes(file);
            } catch (IOException err) {
                throw RpcException.resourceNotFound("file_not_found", "cannot read " + requestedPath);
            }
        }

        Path backupOrFail(Path file) throws RpcException {
            try {
                return ws.backup(file);
            } catch (IOException err) {
                throw RpcException.internal("failed to write backup for " + ws.relative(file) + ": "
                        + err.getMessage());
            }
        }

        void writeOrFail(Path file, byte[] bytes) throws RpcException {
            try {
                ws.atomicWrite(file, bytes);
            } catch (IOException err) {
                throw RpcException.internal("failed to persist " + ws.relative(file) + ": " + err.getMessage());
            }
        }

        // ---- understand

        Map<String, Object> understand(Map<String, Object> params) throws RpcException {
            String query = str(params, "query", "").trim().toLowerCase(Locale.ROOT);
            String target = str(params, "target", "").trim();
            if (query.isEmpty()) {
                throw RpcException.invalidParams("query_required", "query is required");
            }
            if (query.equals("impact")) {
                return understandImpact(target);
            }
            if (query.equals("causality") || query.equals("why_state")) {
                return understandCausality(query, target);
            }
            throw RpcException.unsupported("unsupported_query", "understand query " + query);
        }

        Map<String, Object> understandImpact(String target) {
            List<String> affected = new ArrayList<>();
            String resolvedSymbol = null;
            String resolvedFile = null;
            String methodName = null;
            String className = null;
            Route route = blank(target) ? null : findRoute(target);
            if (route != null) {
                resolvedSymbol = route.handler;
                resolvedFile = route.rel;
                methodName = route.handler.substring(route.handler.indexOf('#') + 1);
                className = route.className;
            } else if (!blank(target) && target.contains("#")) {
                int hash = target.indexOf('#');
                String pathPart = target.substring(0, hash);
                String symbolPart = target.substring(hash + 1);
                try {
                    Path file = ws.resolve(pathPart);
                    if (Files.isRegularFile(file)) {
                        String text = new String(Files.readAllBytes(file), StandardCharsets.UTF_8);
                        FileModel fm = new FileModel(ws.relative(file), file, text);
                        scanBody(fm, 0, text.length(), 0, null);
                        Member member = resolveSymbol(fm, symbolPart);
                        if (member != null) {
                            resolvedSymbol = member.symbol();
                            resolvedFile = fm.rel;
                            methodName = member.name;
                            className = member.owner;
                        }
                    }
                } catch (RpcException | IOException ignored) {
                    // unresolvable is a legitimate answer (spec §4.2)
                }
            } else if (!blank(target)) {
                FileModel fm = findType(target);
                if (fm != null) {
                    String simple = simpleName(target);
                    for (Member type : fm.types) {
                        if (type.name.equals(simple)) {
                            resolvedSymbol = type.name;
                            resolvedFile = fm.rel;
                            className = type.name;
                            break;
                        }
                    }
                }
            }

            List<Object> references = new ArrayList<>();
            if (resolvedSymbol != null) {
                affected.add(resolvedSymbol);
                references = staticReferences(resolvedFile, className, methodName);
                for (Object reference : references) {
                    String symbol = String.valueOf(((Map<?, ?>) reference).get("symbol"));
                    if (!affected.contains(symbol)) {
                        affected.add(symbol);
                    }
                }
            }

            boolean resolved = resolvedSymbol != null;
            String risk = !resolved ? "UNKNOWN" : (references.isEmpty() ? "LOW" : "MEDIUM");
            String summary;
            if (!resolved) {
                summary = "Target \"" + target + "\" could not be resolved to a Spring route, handler or type in "
                        + ws.root + "; no impact evidence is claimed.";
            } else {
                summary = "Static reverse-dependency scan of " + index().files.size() + " Java files: "
                        + references.size() + " referencing site(s) found for " + resolvedSymbol
                        + " (" + resolvedFile + "). Provenance is static; no runtime traces exist in this sidecar.";
            }
            Map<String, Object> impact = map(
                    "target", target,
                    "affectedSymbols", new ArrayList<Object>(affected),
                    "references", references,
                    "breakingRisk", risk,
                    "summary", summary,
                    "observed", false,
                    "evidence", map("provenance", "static", "sourceAdapter", NAME,
                            "confidence", resolved ? "probable" : "unknown"));
            return map("query", "impact", "target", target, "impact", impact);
        }

        List<Object> staticReferences(String declaringFile, String className, String methodName) {
            List<Object> references = new ArrayList<>();
            if (className == null && methodName == null) {
                return references;
            }
            Set<String> seen = new LinkedHashSet<>();
            for (FileModel fm : index().files) {
                if (fm.rel.equals(declaringFile)) {
                    continue;
                }
                String mask = new String(fm.mask);
                int firstTypeStart = fm.types.isEmpty() ? Integer.MAX_VALUE : fm.types.get(0).startOffset;
                for (int i = 0; i < mask.length(); i++) {
                    if (!Character.isJavaIdentifierStart(mask.charAt(i))) {
                        continue;
                    }
                    int start = i;
                    while (i < mask.length() && Character.isJavaIdentifierPart(mask.charAt(i))) {
                        i++;
                    }
                    String ident = mask.substring(start, i);
                    boolean match;
                    if (className != null && ident.equals(className)) {
                        match = true;
                    } else if (methodName != null && ident.equals(methodName)) {
                        int after = i;
                        while (after < mask.length() && Character.isWhitespace(mask.charAt(after))) {
                            after++;
                        }
                        match = after < mask.length() && mask.charAt(after) == '(';
                    } else {
                        match = false;
                    }
                    if (!match || start < firstTypeStart) {
                        continue;
                    }
                    if (isDeclarationName(fm, start, ident)) {
                        continue; // a same-named handler declared elsewhere is not a reference
                    }
                    String symbol = enclosingSymbol(fm, start);
                    if (symbol == null || symbol.equals(declaringFile + "#" + methodName)) {
                        continue;
                    }
                    if (seen.add(symbol)) {
                        Map<String, Object> reference = map("symbol", symbol, "file", fm.rel,
                                "line", fm.source.lineOf(start));
                        references.add(reference);
                    }
                }
            }
            return references;
        }

        static boolean isDeclarationName(FileModel fm, int offset, String ident) {
            for (Member method : fm.methods) {
                if (!method.name.equals(ident) || method.parenOpen < 0) {
                    continue;
                }
                int nameAt = Math.max(method.declStart, method.parenOpen - method.name.length());
                if (offset >= nameAt && offset < method.parenOpen) {
                    return true;
                }
            }
            return false;
        }

        static String enclosingSymbol(FileModel fm, int offset) {            Member best = null;
            for (Member method : fm.methods) {
                if (method.startOffset <= offset && offset <= method.endOffset) {
                    if (best == null || (method.endOffset - method.startOffset) < (best.endOffset - best.startOffset)) {
                        best = method;
                    }
                }
            }
            if (best != null) {
                return best.symbol();
            }
            for (Member type : fm.types) {
                if (type.startOffset <= offset && offset <= type.endOffset) {
                    if (best == null || (type.endOffset - type.startOffset) < (best.endOffset - best.startOffset)) {
                        best = type;
                    }
                }
            }
            return best == null ? null : best.symbol();
        }

        Map<String, Object> understandCausality(String query, String target) {
            String normalized = query.equals("why_state") ? "why_state" : "causality";
            Map<String, Object> causality = map(
                    "target", target,
                    "observed", false,
                    "traceCount", 0,
                    "traceChain", new ArrayList<>(),
                    "rootCause", "unknown",
                    "note", "No live request trace exists: this JDK-only sidecar scans Spring sources statically "
                            + "and never boots the application, so oumv/verify compiles a file instead of "
                            + "dispatching HTTP. Level 3 (L4 traces / runtime causality) is not implemented; run a "
                            + "trace-capable adapter attached to the running application for causality evidence.",
                    "evidence", map("provenance", "runtime", "sourceAdapter", NAME, "confidence", "unknown"));
            return map("query", normalized, "target", target, "causality", causality);
        }

        // ---- verify

        Map<String, Object> verify(Map<String, Object> params) throws RpcException {
            String target = str(params, "target");
            if (blank(target)) {
                throw RpcException.invalidParams("target_required", "target is required for oumv/verify");
            }
            Object rawSteps = params.get("steps");
            if (rawSteps instanceof List && !((List<?>) rawSteps).isEmpty()) {
                List<?> steps = (List<?>) rawSteps;
                List<Object> results = new ArrayList<>();
                double total = 0;
                boolean allPassed = true;
                for (int index = 0; index < steps.size(); index++) {
                    Object raw = steps.get(index);
                    Map<?, ?> step = raw instanceof Map ? (Map<?, ?>) raw : Collections.emptyMap();
                    String stepTarget = step.get("target") instanceof String ? (String) step.get("target") : target;
                    String action = step.get("action") instanceof String ? (String) step.get("action") : "";
                    VerifyRun run = runVerify(stepTarget, action);
                    total += run.durationMs;
                    allPassed = allPassed && run.passed;
                    results.add(map(
                            "target", stepTarget,
                            "action", action,
                            "statusCode", 0,
                            "status", run.passed ? "passed" : "failed",
                            "responseBody", run.output));
                }
                Map<String, Object> timings = map(
                        "t_action_ms", total, "t_schema_ms", 0.0, "t_propagation_ms", 0.0,
                        "t_dom_ms", 0.0, "t_total_ms", total);
                return map(
                        "status", allPassed ? "passed" : "failed",
                        "target", target,
                        "statusCode", 0,
                        "steps", results,
                        "diff", map("stepCount", results.size(), "dispatch", "none",
                                "note", "each step ran the static verification command independently; "
                                        + "no application instance was shared because none exists"),
                        "timings", timings,
                        "durationMs", total,
                        "scope", verifyScope(results.size()));
            }
            String action = str(params, "action", "");
            VerifyRun run = runVerify(target, action);
            Map<String, Object> diff = map(
                    "verifiedFile", run.file == null ? "" : run.file,
                    "verifyCommand", run.command,
                    "exitCode", run.exitCode,
                    "output", run.output,
                    "dispatch", "none");
            if (params.containsKey("payload")) {
                diff.put("payloadIgnored", true);
            }
            Map<String, Object> timings = map(
                    "t_action_ms", run.durationMs, "t_schema_ms", 0.0, "t_propagation_ms", 0.0,
                    "t_dom_ms", 0.0, "t_total_ms", run.durationMs);
            return map(
                    "status", run.passed ? "passed" : "failed",
                    "target", target,
                    "action", action,
                    "statusCode", 0,
                    "diff", diff,
                    "timings", timings,
                    "durationMs", run.durationMs,
                    "scope", verifyScope(1));
        }

        Map<String, Object> verifyScope(int steps) {
            List<Object> checked = new ArrayList<>();
            checked.add("static verification command executed with the JDK (default: javac {file})");
            if (steps > 1) {
                checked.add("ordered multi-step run of the static verification command (" + steps + " steps)");
            }
            List<Object> notExecuted = new ArrayList<>(Arrays.asList(
                    "HTTP dispatch of the requested route target",
                    "Spring ApplicationContext startup",
                    "real network calls",
                    "persistent database state",
                    "shared state between multi-step calls (no application instance exists)"));
            List<Object> notSupported = new ArrayList<>(Arrays.asList(
                    "browser/DOM assertions",
                    "in-process handler execution",
                    "rollback"));
            return map("checked", checked, "notExecuted", notExecuted, "notSupported", notSupported);
        }

        static final class VerifyRun {
            boolean passed;
            int exitCode;
            double durationMs;
            String output = "";
            String file;
            String command = "";
        }

        VerifyRun runVerify(String target, String action) throws RpcException {
            long started = System.nanoTime();
            Path file = resolveVerifyFile(target);
            String command = opts.verifyCmd.replace("{file}", shellQuote(file.toString()));
            VerifyRun run = new VerifyRun();
            run.file = file.toString();
            run.command = command;
            Process process = null;
            try {
                ProcessBuilder builder = new ProcessBuilder("/bin/sh", "-c", command);
                builder.directory(ws.root.toFile());
                builder.redirectErrorStream(true);
                process = builder.start();
                ByteArrayOutputStream buffer = new ByteArrayOutputStream();
                InputStream stream = process.getInputStream();
                byte[] chunk = new byte[8192];
                long deadline = System.currentTimeMillis() + Math.max(1000L, opts.verifyTimeoutMs);
                boolean timedOut = false;
                while (true) {
                    while (stream.available() > 0) {
                        int read = stream.read(chunk);
                        if (read < 0) {
                            break;
                        }
                        if (buffer.size() < 200_000) {
                            buffer.write(chunk, 0, read);
                        }
                    }
                    if (process.isAlive()) {
                        if (System.currentTimeMillis() > deadline) {
                            timedOut = true;
                            process.destroyForcibly();
                            break;
                        }
                        Thread.sleep(10);
                    } else {
                        while (stream.available() > 0) {
                            int read = stream.read(chunk);
                            if (read < 0) {
                                break;
                            }
                            if (buffer.size() < 200_000) {
                                buffer.write(chunk, 0, read);
                            }
                        }
                        break;
                    }
                }
                if (timedOut) {
                    run.passed = false;
                    run.exitCode = -1;
                    run.output = "verification timed out after " + opts.verifyTimeoutMs + " ms";
                } else {
                    run.exitCode = process.exitValue();
                    run.passed = run.exitCode == 0;
                    run.output = truncate(buffer.toString(StandardCharsets.UTF_8), 4000);
                }
            } catch (IOException err) {
                throw RpcException.runtimeUnavailable("verify_command_failed",
                        "cannot execute verification command: " + err.getMessage());
            } catch (InterruptedException err) {
                Thread.currentThread().interrupt();
                throw RpcException.internal("verification interrupted");
            } finally {
                if (process != null && process.isAlive()) {
                    process.destroyForcibly();
                }
            }
            run.durationMs = (System.nanoTime() - started) / 1_000_000.0;
            return run;
        }

        static String truncate(String text, int max) {
            return text.length() <= max ? text : text.substring(0, max) + "\n… [truncated]";
        }

        static String shellQuote(String value) {
            return "'" + value.replace("'", "'\\''") + "'";
        }

        Path resolveVerifyFile(String target) throws RpcException {
            if (!blank(opts.verifyFile)) {
                Path configured = Paths.get(opts.verifyFile);
                if (!configured.isAbsolute()) {
                    configured = ws.root.resolve(configured);
                }
                configured = configured.normalize();
                if (!Files.isRegularFile(configured)) {
                    throw RpcException.runtimeUnavailable("verify_file_missing",
                            "configured --verify-file does not exist: " + configured);
                }
                return configured;
            }
            String t = target.trim();
            String candidate = null;
            if (t.contains("#") && !t.substring(t.indexOf('#') + 1).contains("/")) {
                candidate = t.substring(0, t.indexOf('#'));
            } else {
                String[] endpoint = endpointOf(t);
                if (endpoint != null) {
                    Route route = findRoute(t);
                    if (route != null) {
                        return ws.root.resolve(route.rel);
                    }
                    throw RpcException.runtimeUnavailable("verify_target_unresolved",
                            "no Spring route matches verify target " + t
                                    + "; pass --verify-file to name the file to check");
                }
                candidate = t;
            }
            Path path = Paths.get(candidate);
            if (!path.isAbsolute()) {
                path = ws.root.resolve(path);
            }
            if (Files.isRegularFile(path.normalize())) {
                return path.normalize();
            }
            throw RpcException.runtimeUnavailable("verify_target_unresolved",
                    "cannot resolve verify target " + target + " to a file; pass --verify-file");
        }

        // ---- dispatch

        Map<String, Object> dispatch(String method, Map<String, Object> params) throws RpcException {
            // §8.5 (revision v1.11): a read-only deployment has no handler for the
            // writing surface at all, so the answer is METHOD_NOT_FOUND whatever the
            // session state is — never a policy refusal of a handler that exists.
            if (opts.readOnly && ("oumv/applyEdit".equals(method) || "oumv/verify".equals(method))) {
                throw new RpcException(-32601, "METHOD_NOT_FOUND: unknown method " + method,
                        map("reason", "method_not_found", "method", method));
            }
            switch (method) {
                case "oumv/handshake":
                    return handshake(params);
                case "oumv/disconnect":
                    return disconnect(params);
                default:
                    break;
            }
            requireSession(params);
            switch (method) {
                case "oumv/observe":
                    return observe(params);
                case "oumv/getSource":
                    return getSource(params);
                case "oumv/applyEdit":
                    return applyEdit(params);
                case "oumv/verify":
                    return verify(params);
                case "oumv/understand":
                    return understand(params);
                default:
                    throw new RpcException(-32601, "METHOD_NOT_FOUND: unknown method " + method,
                            map("reason", "method_not_found", "method", method));
            }
        }

        // ---- JSON-RPC

        static Map<String, Object> errorResponse(Object id, int code, String message, Map<String, Object> data) {
            Map<String, Object> error = map("code", code, "message", message);
            if (data != null) {
                error.put("data", data);
            }
            return map("jsonrpc", "2.0", "id", id, "error", error);
        }

        /** Handles one JSON body (object or batch) and returns the response payload. */
        Object handleBody(String body) {
            Object parsed;
            try {
                parsed = Json.parse(body);
            } catch (Json.ParseException err) {
                return errorResponse(null, -32700, "PARSE_ERROR: " + err.getMessage(),
                        map("reason", "parse_error"));
            }
            if (parsed instanceof List) {
                List<?> batch = (List<?>) parsed;
                if (batch.isEmpty()) {
                    return errorResponse(null, -32600, "INVALID_REQUEST: empty batch", map("reason", "empty_batch"));
                }
                List<Object> responses = new ArrayList<>();
                for (Object item : batch) {
                    if (!(item instanceof Map)) {
                        responses.add(errorResponse(null, -32600, "INVALID_REQUEST: batch entry is not an object",
                                map("reason", "invalid_request")));
                        continue;
                    }
                    Map<String, Object> response = handleOne(castMap(item));
                    if (response != null) {
                        responses.add(response);
                    }
                }
                return responses.isEmpty() ? null : responses;
            }
            if (parsed instanceof Map) {
                return handleOne(castMap(parsed));
            }
            return errorResponse(null, -32600, "INVALID_REQUEST: body must be an object or array",
                    map("reason", "invalid_request"));
        }

        @SuppressWarnings("unchecked")
        static Map<String, Object> castMap(Object value) {
            return (Map<String, Object>) value;
        }

        @SuppressWarnings("unchecked")
        Map<String, Object> handleOne(Map<String, Object> request) {
            boolean notification = !request.containsKey("id");
            Object id = request.get("id");
            Object jsonrpc = request.get("jsonrpc");
            if (jsonrpc != null && !"2.0".equals(jsonrpc)) {
                return notification ? null : errorResponse(id, -32600,
                        "INVALID_REQUEST: jsonrpc must be \"2.0\"", map("reason", "invalid_request"));
            }
            Object rawMethod = request.get("method");
            if (!(rawMethod instanceof String) || blank((String) rawMethod)) {
                return notification ? null : errorResponse(id, -32600,
                        "INVALID_REQUEST: method is required", map("reason", "invalid_request"));
            }
            String method = (String) rawMethod;
            Object rawParams = request.get("params");
            Map<String, Object> params = new LinkedHashMap<>();
            if (rawParams instanceof Map) {
                for (Map.Entry<String, Object> entry : ((Map<String, Object>) rawParams).entrySet()) {
                    params.put(entry.getKey(), entry.getValue());
                }
            } else if (rawParams != null) {
                return notification ? null : errorResponse(id, -32602,
                        "INVALID_PARAMS: params must be an object", map("reason", "invalid_params"));
            }
            try {
                Map<String, Object> result = dispatch(method, params);
                return notification ? null : map("jsonrpc", "2.0", "id", id, "result", result);
            } catch (RpcException err) {
                return notification ? null : errorResponse(id, err.code, err.getMessage(), err.data);
            } catch (StackOverflowError err) {
                noteInternalError();
                return notification ? null : errorResponse(id, -32603,
                        "INTERNAL_ERROR: stack overflow while handling " + method, map("reason", "internal_error"));
            } catch (RuntimeException err) {
                noteInternalError();
                System.err.println("[oumv-java-spring] internal error in " + method + ": " + err);
                err.printStackTrace(System.err);
                return notification ? null : errorResponse(id, -32603,
                        "INTERNAL_ERROR: " + err.getClass().getSimpleName() + ": " + err.getMessage(),
                        map("reason", "internal_error"));
            }
        }

        // ---- transports

        void runStdio() {
            System.err.println("[" + NAME + "] " + VERSION + " stdio root=" + ws.root
                    + " levels=" + String.join(",", OBSERVE_LEVELS) + " (no descriptor in stdio mode)");
            BufferedReader reader = new BufferedReader(
                    new InputStreamReader(System.in, StandardCharsets.UTF_8));
            PrintStream out = new PrintStream(
                    new BufferedOutputStream(new FileOutputStream(FileDescriptor.out), 1 << 16),
                    false, StandardCharsets.UTF_8);
            try {
                String line;
                while ((line = reader.readLine()) != null) {
                    if (line.trim().isEmpty()) {
                        continue;
                    }
                    Object response = handleBody(line);
                    if (response != null) {
                        out.println(Json.stringify(response));
                        out.flush();
                    }
                }
            } catch (IOException err) {
                System.err.println("[" + NAME + "] stdio read error: " + err.getMessage());
            } finally {
                out.flush();
            }
        }

        void runHttp() throws IOException {
            HttpServer server = HttpServer.create(new InetSocketAddress(opts.host, opts.port), 0);
            server.createContext("/__oumv/rpc", this::handleHttpRpc);
            server.createContext("/__oumv/health", this::handleHttpHealth);
            server.createContext("/", exchange -> {
                byte[] body = Json.stringify(errorResponse(null, -32600, "INVALID_REQUEST: unknown path",
                        map("reason", "unknown_path"))).getBytes(StandardCharsets.UTF_8);
                send(exchange, 404, body);
            });
            server.setExecutor(Executors.newFixedThreadPool(4));
            server.start();
            int port = server.getAddress().getPort();
            String publicHost = opts.host.equals("0.0.0.0") || opts.host.equals("::") ? "127.0.0.1" : opts.host;
            String url = "http://" + publicHost + ":" + port + "/__oumv/rpc";
            System.err.println("[" + NAME + "] " + VERSION + " root=" + ws.root + " url=" + url
                    + " levels=" + String.join(",", OBSERVE_LEVELS));
            if (opts.descriptor) {
                try {
                    descriptorFile = writeDescriptor(url);
                    System.err.println("[" + NAME + "] descriptor=" + descriptorFile);
                } catch (IOException err) {
                    System.err.println("[" + NAME + "] descriptor write failed: " + err.getMessage());
                }
            }
            Runtime.getRuntime().addShutdownHook(new Thread(() -> {
                server.stop(0);
                deleteDescriptor();
            }, NAME + "-shutdown"));
        }

        void handleHttpRpc(HttpExchange exchange) throws IOException {
            try {
                String path = exchange.getRequestURI().getPath();
                if (!"/__oumv/rpc".equals(path)) {
                    send(exchange, 404, Json.stringify(errorResponse(null, -32600,
                            "INVALID_REQUEST: unknown path", map("reason", "unknown_path")))
                            .getBytes(StandardCharsets.UTF_8));
                    return;
                }
                String httpMethod = exchange.getRequestMethod();
                if ("OPTIONS".equalsIgnoreCase(httpMethod)) {
                    Headers headers = exchange.getResponseHeaders();
                    headers.set("Allow", "POST, OPTIONS");
                    headers.set("Access-Control-Allow-Origin", "*");
                    headers.set("Access-Control-Allow-Headers", "Content-Type, X-OUMV-Session-Token, Authorization");
                    headers.set("Access-Control-Allow-Methods", "POST, OPTIONS");
                    exchange.sendResponseHeaders(204, -1);
                    return;
                }
                if (!"POST".equalsIgnoreCase(httpMethod)) {
                    Headers headers = exchange.getResponseHeaders();
                    headers.set("Allow", "POST, OPTIONS");
                    send(exchange, 405, Json.stringify(errorResponse(null, -32600,
                            "INVALID_REQUEST: only POST is supported", map("reason", "method_not_allowed")))
                            .getBytes(StandardCharsets.UTF_8));
                    return;
                }
                String contentType = exchange.getRequestHeaders().getFirst("Content-Type");
                if (contentType == null || !contentType.toLowerCase(Locale.ROOT).contains("application/json")) {
                    send(exchange, 415, Json.stringify(errorResponse(null, -32600,
                            "INVALID_REQUEST: Content-Type must be application/json",
                            map("reason", "unsupported_content_type"))).getBytes(StandardCharsets.UTF_8));
                    return;
                }
                if (opts.token != null && !authorized(exchange)) {
                    send(exchange, 401, Json.stringify(errorResponse(null, -32007,
                            "PERMISSION_DENIED: missing or invalid session token",
                            map("reason", "unauthorized"))).getBytes(StandardCharsets.UTF_8));
                    return;
                }
                if ((opts.token == null || opts.token.isEmpty()) && !isLoopbackHost(opts.host)) {
                    // §12: a non-loopback bind with no token configured is refused
                    // before dispatch, exactly like the PHP sidecar.
                    send(exchange, 403, Json.stringify(errorResponse(null, -32007,
                            "PERMISSION_DENIED: a non-loopback bind requires --token",
                            map("reason", "token_required_non_loopback"))).getBytes(StandardCharsets.UTF_8));
                    return;
                }
                byte[] body = readLimited(exchange.getRequestBody(), MAX_BODY_BYTES);
                if (body == null) {
                    send(exchange, 413, Json.stringify(errorResponse(null, -32600,
                            "INVALID_REQUEST: body exceeds " + MAX_BODY_BYTES + " bytes",
                            map("reason", "body_too_large"))).getBytes(StandardCharsets.UTF_8));
                    return;
                }
                Object response = handleBody(new String(body, StandardCharsets.UTF_8));
                if (response == null) {
                    exchange.sendResponseHeaders(204, -1);
                    return;
                }
                send(exchange, 200, Json.stringify(response).getBytes(StandardCharsets.UTF_8));
            } finally {
                exchange.close();
            }
        }

        /** True for the bind addresses that need no token (§12 loopback allowance). */
        static boolean isLoopbackHost(String host) {
            String h = host == null ? "" : host.trim().toLowerCase(Locale.ROOT);
            return h.equals("localhost") || h.equals("127.0.0.1") || h.equals("::1") || h.equals("0:0:0:0:0:0:0:1");
        }

        boolean authorized(HttpExchange exchange) {            String provided = exchange.getRequestHeaders().getFirst("X-OUMV-Session-Token");
            if (provided == null) {
                String authorization = exchange.getRequestHeaders().getFirst("Authorization");
                if (authorization != null && authorization.toLowerCase(Locale.ROOT).startsWith("bearer ")) {
                    provided = authorization.substring(7).trim();
                }
            }
            if (provided == null) {
                return false;
            }
            return MessageDigest.isEqual(provided.getBytes(StandardCharsets.UTF_8),
                    opts.token.getBytes(StandardCharsets.UTF_8));
        }

        void handleHttpHealth(HttpExchange exchange) throws IOException {
            if (!"GET".equalsIgnoreCase(exchange.getRequestMethod())) {
                send(exchange, 405, Json.stringify(errorResponse(null, -32600,
                        "INVALID_REQUEST: health is GET-only", map("reason", "method_not_allowed")))
                        .getBytes(StandardCharsets.UTF_8));
                return;
            }
            Map<String, Object> health = map(
                    "name", NAME,
                    "version", VERSION,
                    "profile", PROFILE,
                    "protocolVersion", PROTOCOL_VERSION,
                    "framework", FRAMEWORK,
                    "observeLevels", Arrays.asList(OBSERVE_LEVELS),
                    "transports", list("http", "stdio"),
                    "routes", index().routes.size(),
                    "uptimeSeconds", Math.max(0, Instant.now().getEpochSecond() - startedAt.getEpochSecond()));
            send(exchange, 200, Json.stringify(health).getBytes(StandardCharsets.UTF_8));
        }

        static void send(HttpExchange exchange, int status, byte[] body) throws IOException {
            Headers headers = exchange.getResponseHeaders();
            headers.set("Content-Type", "application/json; charset=utf-8");
            headers.set("Cache-Control", "no-store");
            exchange.sendResponseHeaders(status, body.length == 0 ? -1 : body.length);
            if (body.length > 0) {
                try (OutputStream out = exchange.getResponseBody()) {
                    out.write(body);
                }
            }
        }

        static byte[] readLimited(InputStream in, int limit) throws IOException {
            ByteArrayOutputStream buffer = new ByteArrayOutputStream();
            byte[] chunk = new byte[8192];
            int total = 0;
            while (true) {
                int read = in.read(chunk);
                if (read < 0) {
                    return buffer.toByteArray();
                }
                total += read;
                if (total > limit) {
                    return null;
                }
                buffer.write(chunk, 0, read);
            }
        }

        // ---- descriptor (spec §9.2)

        String writeDescriptor(String url) throws IOException {
            Path path = descriptorPath(ws.root);
            Files.createDirectories(path.getParent());
            Map<String, Object> registry = readRegistry(path);
            Map<String, Object> descriptor = map(
                    "type", PROFILE,
                    "name", NAME,
                    "version", VERSION,
                    "url", url,
                    "workspace", ws.root.toString(),
                    "capabilities", map(
                            "observeLevels", Arrays.asList(OBSERVE_LEVELS),
                            "transports", list("http"),
                            "causality", false,
                            "occ", !opts.readOnly,
                            "verify", !opts.readOnly,
                            "readOnly", opts.readOnly),
                    "startedAt", DateTimeFormatter.ISO_INSTANT.format(Instant.now()));
            if (opts.token != null) {
                descriptor.put("sessionToken", opts.token);
            }
            registry.put("backend", descriptor);
            registry.put("workspaceRoot", ws.root.toString());
            registry.put("pid", ProcessHandle.current().pid());
            registry.put("updatedAt", System.currentTimeMillis());
            writeRegistry(path, registry);
            return path.toString();
        }

        void deleteDescriptor() {
            if (descriptorFile == null) {
                return;
            }
            try {
                Path path = Paths.get(descriptorFile);
                Map<String, Object> registry = readRegistry(path);
                registry.remove("backend");
                if (!registry.containsKey("frontend")) {
                    Files.deleteIfExists(path);
                } else {
                    writeRegistry(path, registry);
                }
            } catch (IOException err) {
                System.err.println("[" + NAME + "] descriptor cleanup failed: " + err.getMessage());
            }
        }

        static Path descriptorPath(Path root) throws IOException {
            String home = System.getenv("TOKENSAFE_HOME");
            Path base = (home != null && !home.isBlank())
                    ? Paths.get(home)
                    : Paths.get(System.getProperty("user.home"), ".tokensafe");
            Path real = root.toRealPath();
            String digest = sha256(real.toString().getBytes(StandardCharsets.UTF_8)).substring("sha256:".length());
            return base.resolve("runtimes").resolve(digest.substring(0, 16) + ".json");
        }

        static Map<String, Object> readRegistry(Path path) {
            Map<String, Object> registry = new LinkedHashMap<>();
            if (!Files.isRegularFile(path)) {
                return registry;
            }
            try {
                String raw = new String(Files.readAllBytes(path), StandardCharsets.UTF_8);
                Object parsed = Json.parse(raw);
                if (parsed instanceof Map) {
                    for (Map.Entry<String, Object> entry : castMap(parsed).entrySet()) {
                        registry.put(entry.getKey(), entry.getValue());
                    }
                }
            } catch (IOException | Json.ParseException ignored) {
                // a corrupt registry must not block startup
            }
            return registry;
        }

        static void writeRegistry(Path path, Map<String, Object> registry) throws IOException {
            Files.createDirectories(path.getParent());
            Path temp = path.resolveSibling(path.getFileName() + ".tmp");
            Files.write(temp, (Json.stringify(registry) + "\n").getBytes(StandardCharsets.UTF_8));
            try {
                Files.setPosixFilePermissions(temp, PosixFilePermissions.fromString("rw-------"));
            } catch (UnsupportedOperationException ignored) {
                // non-POSIX filesystem
            }
            try {
                Files.move(temp, path, StandardCopyOption.ATOMIC_MOVE, StandardCopyOption.REPLACE_EXISTING);
            } catch (IOException err) {
                Files.move(temp, path, StandardCopyOption.REPLACE_EXISTING);
            }
        }
    }
}
