import { defineConfig } from 'tsup';

export default defineConfig({
  entry: ['src/index.ts', 'src/stdio.ts', 'src/readonly.ts'],
  format: ['esm', 'cjs'],
  dts: true,
  clean: true,
  sourcemap: true,
});
