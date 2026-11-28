import { defineConfig } from 'tsup';

export default defineConfig({
  entry: ['src/index.ts', 'src/stdio.ts', 'src/readonly.ts', 'src/elysia.ts', 'src/next-bridge.ts'],
  format: ['esm', 'cjs'],
  dts: true,
  clean: true,
  sourcemap: true,
  // Peer dependency: the host Elysia app provides it; never bundle a second copy.
  external: ['elysia'],
});
