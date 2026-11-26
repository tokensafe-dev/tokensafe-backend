import fs from 'node:fs';
import path from 'node:path';
import { createRequire } from 'node:module';
import { describe, expect, it } from 'vitest';
import { createWorkspace, hasConformanceRunner } from './helpers/workspace.js';

const require = createRequire(import.meta.url);
const AGENT_OUMV = process.env.OUMV_REPO
  ? path.resolve(process.env.OUMV_REPO)
  : path.resolve(__dirname, '../../../agent-oumv');
const CONFORMANCE_CJS = path.join(AGENT_OUMV, 'packages/conformance/dist/index.cjs');
const FIXTURES = path.join(AGENT_OUMV, 'fixtures/conformance-cases.json');
const STDIO_BIN = path.resolve(__dirname, '../dist/stdio.cjs');

// The stdio entry is built by `pnpm build`; a source-only checkout (or a local
// run before a build) skips instead of pretending to certify.
const ready = hasConformanceRunner && fs.existsSync(STDIO_BIN);

describe('stdio transport certification (§2.5)', () => {
  it.runIf(ready)(
    'passes the language-neutral fixtures at level 3 over stdio',
    async () => {
      const workspace = createWorkspace();
      const { loadFixtures, runFixtureSuite, stdioTransport } = require(CONFORMANCE_CJS);
      const transport = stdioTransport({
        command: process.execPath,
        args: [
          STDIO_BIN,
          '--workspace',
          workspace.dir,
          '--app',
          path.resolve(__dirname, 'fixtures/express-app.cjs'),
        ],
        cwd: workspace.dir,
        timeoutMs: 30_000,
      });
      try {
        const report = await runFixtureSuite(transport, loadFixtures(FIXTURES), workspace.markers, {
          adapterName: 'node-stdio',
          framework: 'node',
          environment: { transport: 'stdio', node: process.version },
        });
        const outDir = process.env.CERT_OUT_DIR;
        if (outDir) {
          const transports = path.join(outDir, 'transports');
          fs.mkdirSync(transports, { recursive: true });
          fs.writeFileSync(path.join(transports, 'node-stdio.json'), JSON.stringify(report, null, 2) + '\n');
        }
        if (report.failed > 0) {
          console.log(report.cases.filter((c: any) => c.status !== 'passed'));
        }
        expect(report.passed).toBe(report.total);
        expect(report.attainedLevel).toBe(3);
      } finally {
        transport.close?.();
        workspace.cleanup();
      }
    },
    60_000
  );
});
