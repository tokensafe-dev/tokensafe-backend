import type { OumvRuntimeOptions, VerifySandboxHook } from './runtime.js';

/**
 * DB-sandbox autodetect (the .NET `AddDbContext` discovery is the template).
 *
 * Node has no DI container, so the honest equivalent is what an application
 * already exposes: an object with `run(fn)` in `app.locals` / a Fastify
 * decorator, or a Prisma client with `$transaction`. Anything else keeps the
 * runtime without a sandbox capability instead of guessing at an ORM.
 */

export interface DetectedSandboxSource {
  /** Where the hook was found (`app.locals.db`, `fastify.decorator:prisma`). */
  source: string;
  hook: VerifySandboxHook;
}

/** Wraps a Prisma-like client: the transaction is always rolled back. */
export function prismaSandbox(client: { $transaction: (...args: any[]) => Promise<unknown> }): VerifySandboxHook {
  const ROLLBACK = Symbol('oumv-prisma-rollback');
  return {
    db: {
      async run<T>(fn: () => Promise<T> | T) {
        let value: T | undefined;
        let rolledBack = false;
        try {
          await client.$transaction(async () => {
            value = await fn();
            // Throwing is the only way to make Prisma roll back a transaction.
            throw ROLLBACK;
          });
        } catch (error) {
          if (error !== ROLLBACK) throw error;
          rolledBack = true;
        }
        return { result: value as T, rolledBack };
      },
    },
  };
}

function toHook(candidate: any): VerifySandboxHook | undefined {
  if (!candidate || typeof candidate !== 'object') return undefined;
  if (typeof candidate.run === 'function') {
    // The documented adopter interface: it owns the transaction and the rollback.
    return {
      db: {
        run: candidate.run.bind(candidate),
        ...(typeof candidate.query === 'function' ? { query: candidate.query.bind(candidate) } : {}),
      },
    };
  }
  if (typeof candidate.$transaction === 'function') {
    return prismaSandbox(candidate as { $transaction: (...args: any[]) => Promise<unknown> });
  }
  return undefined;
}

/**
 * Finds a sandbox hook an Express or Fastify app already holds. Explicit
 * `options.sandbox` always wins; this only runs when the adopter passed none.
 */
export function detectSandbox(app: any): DetectedSandboxSource | undefined {
  const candidates: Array<{ source: string; value: any }> = [];
  const locals = app?.locals;
  if (locals && typeof locals === 'object') {
    for (const name of ['db', 'database', 'prisma', 'knex']) {
      if (locals[name] !== undefined) candidates.push({ source: `app.locals.${name}`, value: locals[name] });
    }
  }
  // Fastify decorations are named sources; check them before generic properties.
  if (app && typeof app.hasDecorator === 'function') {
    for (const name of ['db', 'database', 'prisma']) {
      try {
        if (app.hasDecorator(name)) candidates.push({ source: `fastify.decorator:${name}`, value: app[name] });
      } catch {
        // A framework without decorations simply has none.
      }
    }
  }
  for (const name of ['db', 'database', 'prisma']) {
    if (app?.[name] !== undefined) candidates.push({ source: `app.${name}`, value: app[name] });
  }

  for (const candidate of candidates) {
    const hook = toHook(candidate.value);
    if (hook) return { source: candidate.source, hook };
  }
  return undefined;
}

/** Runtime options with a detected sandbox merged in (explicit options win). */
export function withDetectedSandbox<T extends OumvRuntimeOptions>(app: any, options: T): T {
  if (options.sandbox) return options;
  const detected = detectSandbox(app);
  return detected ? { ...options, sandbox: detected.hook } : options;
}
