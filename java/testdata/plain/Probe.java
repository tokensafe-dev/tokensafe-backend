package probe;

/**
 * Plain JDK-only probe compiled by {@code oumv/verify} (default command
 * {@code javac {file}}). It deliberately has no Spring imports: verification
 * must exercise the real javac toolchain, not a mock.
 */
public final class Probe {

    private Probe() {
    }

    /** Returns a greeting; exists so javac has a real method body to type-check. */
    public static String greet(String name) {
        if (name == null || name.isEmpty()) {
            return "hello, anonymous";
        }
        return "hello, " + name;
    }

    public static void main(String[] args) {
        System.out.println(greet(args.length > 0 ? args[0] : "oumv"));
    }
}
