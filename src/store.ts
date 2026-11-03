import { DatabaseSync } from 'node:sqlite';
import { createHash, randomBytes, randomUUID } from 'node:crypto';
import { Fault, identifier, integer } from './errors.ts';

export type Actor = { id: string; tenant: string; subject: string; role: string; project: string };
const hash = (value: string) => createHash('sha256').update(value).digest('hex');

// SQLite is the executable single-node milestone. BEGIN IMMEDIATE serializes
// writes across local connections; this is NOT a distributed HA budget store.
export class Store {
  db: DatabaseSync;
  constructor(path: string) {
    this.db = new DatabaseSync(path);
    this.db.exec(`
      PRAGMA journal_mode=WAL;
      PRAGMA foreign_keys=ON;
      PRAGMA busy_timeout=5000;
      CREATE TABLE IF NOT EXISTS credentials (
        id TEXT PRIMARY KEY, tenant TEXT NOT NULL, subject TEXT NOT NULL,
        role TEXT NOT NULL CHECK(role IN ('admin','service')),
        project TEXT NOT NULL, digest TEXT NOT NULL UNIQUE,
        expires INTEGER NOT NULL, revoked INTEGER NOT NULL DEFAULT 0
      );
      CREATE TABLE IF NOT EXISTS budgets (
        tenant TEXT NOT NULL, project TEXT NOT NULL,
        cap INTEGER NOT NULL CHECK(cap>=0), spent INTEGER NOT NULL DEFAULT 0,
        held INTEGER NOT NULL DEFAULT 0, PRIMARY KEY(tenant, project),
        CHECK(spent>=0 AND held>=0)
      );
      CREATE TABLE IF NOT EXISTS requests (
        id TEXT PRIMARY KEY, tenant TEXT NOT NULL, project TEXT NOT NULL,
        subject TEXT NOT NULL, credential TEXT NOT NULL, client_key TEXT NOT NULL,
        fingerprint TEXT NOT NULL, model TEXT NOT NULL, rate_version TEXT NOT NULL,
        reserved INTEGER NOT NULL, actual INTEGER, state TEXT NOT NULL,
        input_tokens INTEGER, output_tokens INTEGER,
        created INTEGER NOT NULL, updated INTEGER NOT NULL,
        UNIQUE(tenant, credential, client_key),
        FOREIGN KEY(tenant, project) REFERENCES budgets(tenant, project)
      );
      CREATE TABLE IF NOT EXISTS ledger (
        seq INTEGER PRIMARY KEY AUTOINCREMENT, request_id TEXT NOT NULL,
        tenant TEXT NOT NULL, kind TEXT NOT NULL, amount INTEGER NOT NULL,
        created INTEGER NOT NULL, FOREIGN KEY(request_id) REFERENCES requests(id)
      );
      CREATE TABLE IF NOT EXISTS audit (
        seq INTEGER PRIMARY KEY AUTOINCREMENT, tenant TEXT NOT NULL,
        subject TEXT NOT NULL, action TEXT NOT NULL, resource TEXT NOT NULL,
        created INTEGER NOT NULL
      );
      CREATE INDEX IF NOT EXISTS audit_tenant_seq ON audit(tenant,seq);
      CREATE INDEX IF NOT EXISTS requests_tenant_created ON requests(tenant,created);
      PRAGMA user_version=1;
    `);
  }
  close() { this.db.close(); }
  transaction<T>(fn: () => T): T {
    this.db.exec('BEGIN IMMEDIATE');
    try { const value = fn(); this.db.exec('COMMIT'); return value; }
    catch (e) { this.db.exec('ROLLBACK'); throw e; }
  }
  audit(actor: Actor, action: string, resource: string) {
    this.db.prepare('INSERT INTO audit(tenant,subject,action,resource,created) VALUES(?,?,?,?,?)')
      .run(actor.tenant, actor.subject, action, resource, Date.now());
  }
  bootstrap(token: string, tenant: string) {
    if (token.length < 32) throw new Error('bootstrap token must contain at least 32 characters');
    identifier(tenant);
    const existing = this.db.prepare('SELECT id FROM credentials WHERE digest=?').get(hash(token));
    if (existing) return;
    if (this.db.prepare("SELECT id FROM credentials WHERE role='admin' LIMIT 1").get()) {
      throw new Error('bootstrap already completed; refusing an unrecognized bootstrap token');
    }
    this.db.prepare('INSERT INTO credentials VALUES(?,?,?,?,?,?,?,0)').run(
      randomUUID(), tenant, 'bootstrap-admin', 'admin', '_admin', hash(token), Date.now() + 86400_000);
  }
  authenticate(token: string): Actor {
    if (!token || token.length > 512) throw new Fault(401, 'invalid_credential');
    const row = this.db.prepare('SELECT * FROM credentials WHERE digest=? AND revoked=0 AND expires>?')
      .get(hash(token), Date.now()) as any;
    if (!row) throw new Fault(401, 'invalid_credential');
    return { id: row.id, tenant: row.tenant, subject: row.subject, role: row.role, project: row.project };
  }
  admin(actor: Actor) { if (actor.role !== 'admin') throw new Fault(403, 'admin_required'); }
  provision(actor: Actor, subject: unknown, project: unknown, ttl: unknown) {
    this.admin(actor); identifier(subject); identifier(project); integer(ttl, 86400);
    if ((ttl as number) < 60) throw new Fault(400, 'ttl_too_short');
    if (!this.db.prepare('SELECT 1 FROM budgets WHERE tenant=? AND project=?').get(actor.tenant, project as string)) {
      throw new Fault(404, 'budget_not_found');
    }
    const token = 'agw_' + randomBytes(32).toString('base64url');
    const id = randomUUID();
    this.transaction(() => {
      this.db.prepare('INSERT INTO credentials VALUES(?,?,?,?,?,?,?,0)').run(
        id, actor.tenant, subject as string, 'service', project as string, hash(token), Date.now() + (ttl as number) * 1000);
      this.audit(actor, 'credential.created', id);
    });
    return { id, token };
  }
  revoke(actor: Actor, id: string) {
    this.admin(actor);
    this.transaction(() => {
      const result = this.db.prepare("UPDATE credentials SET revoked=1 WHERE tenant=? AND id=? AND role='service'")
        .run(actor.tenant, id);
      if (!result.changes) throw new Fault(404, 'credential_not_found');
      this.audit(actor, 'credential.revoked', id);
    });
  }
  budget(actor: Actor, project: unknown, cap: unknown) {
    this.admin(actor); identifier(project); integer(cap);
    this.transaction(() => {
      const row = this.db.prepare('SELECT * FROM budgets WHERE tenant=? AND project=?').get(actor.tenant, project as string) as any;
      if (row && (cap as number) < row.spent + row.held) throw new Fault(409, 'cap_below_committed');
      this.db.prepare(`INSERT INTO budgets(tenant,project,cap) VALUES(?,?,?)
        ON CONFLICT(tenant,project) DO UPDATE SET cap=excluded.cap`).run(actor.tenant, project as string, cap as number);
      this.audit(actor, 'budget.updated', project as string);
    });
  }
  reserve(actor: Actor, key: string, fingerprint: string, model: string, amount: number, rate: string) {
    integer(amount);
    return this.transaction(() => {
      // Recheck revocation within the budget transaction, not only at HTTP entry.
      if (!this.db.prepare('SELECT 1 FROM credentials WHERE id=? AND revoked=0 AND expires>?')
        .get(actor.id, Date.now())) throw new Fault(401, 'invalid_credential');
      const old = this.db.prepare('SELECT fingerprint FROM requests WHERE tenant=? AND credential=? AND client_key=?')
        .get(actor.tenant, actor.id, key) as any;
      if (old) throw new Fault(409, old.fingerprint === fingerprint ? 'request_already_recorded' : 'idempotency_conflict');
      const result = this.db.prepare(`UPDATE budgets SET held=held+?
        WHERE tenant=? AND project=? AND spent+held+?<=cap`).run(amount, actor.tenant, actor.project, amount);
      if (!result.changes) throw new Fault(402, 'budget_exhausted');
      const id = randomUUID(); const now = Date.now();
      this.db.prepare(`INSERT INTO requests(id,tenant,project,subject,credential,client_key,fingerprint,model,
        rate_version,reserved,state,created,updated) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`)
        .run(id, actor.tenant, actor.project, actor.subject, actor.id, key, fingerprint, model, rate, amount, 'RESERVED', now, now);
      this.db.prepare('INSERT INTO ledger(request_id,tenant,kind,amount,created) VALUES(?,?,?,?,?)')
        .run(id, actor.tenant, 'RESERVE', amount, now);
      return id;
    });
  }
  settle(id: string, cost: number, input: number, output: number, reconciler?: Actor) {
    integer(cost); integer(input); integer(output);
    this.transaction(() => {
      const r = this.db.prepare('SELECT * FROM requests WHERE id=?').get(id) as any;
      if (!r) throw new Fault(404, 'request_not_found');
      if (r.state === 'SETTLED') {
        if (r.actual !== cost || r.input_tokens !== input || r.output_tokens !== output) throw new Fault(409, 'settlement_conflict');
        return;
      }
      if (!['RESERVED', 'PENDING_RECONCILIATION'].includes(r.state)) throw new Fault(409, 'invalid_state');
      // Real upstream charges may exceed a reservation; record the debt truthfully.
      this.db.prepare('UPDATE budgets SET held=held-?,spent=spent+? WHERE tenant=? AND project=?')
        .run(r.reserved, cost, r.tenant, r.project);
      this.db.prepare("UPDATE requests SET actual=?,input_tokens=?,output_tokens=?,state='SETTLED',updated=? WHERE id=?")
        .run(cost, input, output, Date.now(), id);
      this.db.prepare('INSERT INTO ledger(request_id,tenant,kind,amount,created) VALUES(?,?,?,?,?)')
        .run(id, r.tenant, 'SETTLE', cost, Date.now());
      this.db.prepare('INSERT INTO ledger(request_id,tenant,kind,amount,created) VALUES(?,?,?,?,?)')
        .run(id, r.tenant, 'RELEASE', r.reserved, Date.now());
      if (reconciler) this.audit(reconciler, 'usage.reconciled', id);
    });
  }
  pending(id: string) {
    this.db.prepare("UPDATE requests SET state='PENDING_RECONCILIATION',updated=? WHERE id=? AND state='RESERVED'")
      .run(Date.now(), id);
  }
  recover() {
    // Never release an uncertain upstream charge automatically after a crash.
    this.db.prepare("UPDATE requests SET state='PENDING_RECONCILIATION',updated=? WHERE state='RESERVED'").run(Date.now());
  }
  reconcile(actor: Actor, id: string, cost: number, input: number, output: number) {
    this.admin(actor);
    const r = this.db.prepare('SELECT state FROM requests WHERE id=? AND tenant=?').get(id, actor.tenant) as any;
    if (!r) throw new Fault(404, 'request_not_found');
    if (r.state !== 'PENDING_RECONCILIATION') throw new Fault(409, 'not_pending');
    this.settle(id, cost, input, output, actor);
  }
  snapshot(actor: Actor, limit = 50, before = Number.MAX_SAFE_INTEGER) {
    this.admin(actor); integer(limit, 100); integer(before, Number.MAX_SAFE_INTEGER);
    return {
      budgets: this.db.prepare('SELECT project,cap,spent,held FROM budgets WHERE tenant=? ORDER BY project LIMIT 100').all(actor.tenant),
      requests: this.db.prepare(`SELECT id,project,subject,model,rate_version,reserved,actual,state,input_tokens,output_tokens,created
        FROM requests WHERE tenant=? ORDER BY created DESC LIMIT ?`).all(actor.tenant, limit),
      audit: this.db.prepare('SELECT seq,subject,action,resource,created FROM audit WHERE tenant=? AND seq<? ORDER BY seq DESC LIMIT ?')
        .all(actor.tenant, before, limit),
    };
  }
}
