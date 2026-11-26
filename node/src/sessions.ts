import crypto from 'node:crypto';

/** A live handshake session (spec §3). Absent ids mean stateless callers. */
export class SessionRegistry {
  private readonly sessions = new Map<string, number>();
  readonly ttlSeconds: number;

  constructor(ttlSeconds = 3600) {
    this.ttlSeconds = Math.max(Math.floor(ttlSeconds), 1);
  }

  create(): string {
    const id = `sess-${crypto.randomBytes(8).toString('hex')}`;
    this.sessions.set(id, Date.now() + this.ttlSeconds * 1000);
    return id;
  }

  /** Unknown or expired ids are invalid; expiry is enforced on read. */
  validate(id?: string | null): boolean {
    if (!id) return true;
    const expiresAt = this.sessions.get(id);
    if (expiresAt === undefined) return false;
    if (Date.now() > expiresAt) {
      this.sessions.delete(id);
      return false;
    }
    return true;
  }

  delete(id?: string | null): boolean {
    if (!id) return false;
    return this.sessions.delete(id);
  }

  count(): number {
    return this.sessions.size;
  }
}
