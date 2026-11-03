import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { Store } from '../src/store.ts';

function fixture(t: any) {
  const dir = mkdtempSync(join(tmpdir(), 'agw-'));
  const path = join(dir, 'test.db'); const store = new Store(path);
  const token = 'a'.repeat(40); store.bootstrap(token, 'tenant-a');
  const admin = store.authenticate(token);
  store.budget(admin, 'project', 100);
  const cred = store.provision(admin, 'service', 'project', 3600);
  const actor = store.authenticate(cred.token);
  t.after(() => { store.close(); rmSync(dir, { recursive: true, force: true }); });
  return { store, admin, cred, actor, path };
}
test('reservation, settlement and replay do not double charge', t => {
  const { store, actor, admin } = fixture(t);
  const id = store.reserve(actor, 'key-12345', 'fp', 'model', 50, 'v1');
  assert.throws(() => store.reserve(actor, 'key-12345', 'fp', 'model', 50, 'v1'), /request_already_recorded/);
  store.settle(id, 20, 10, 5); store.settle(id, 20, 10, 5);
  assert.throws(() => store.settle(id, 21, 10, 5), /settlement_conflict/);
  assert.deepEqual({ ...store.snapshot(admin).budgets[0] }, { project: 'project', cap: 100, spent: 20, held: 0 });
});
test('uncertain upstream keeps hold and requires audited reconciliation', t => {
  const { store, actor, admin } = fixture(t);
  const id = store.reserve(actor, 'key-12345', 'fp', 'model', 60, 'v1'); store.pending(id);
  assert.equal((store.snapshot(admin).budgets[0] as any).held, 60);
  store.reconcile(admin, id, 10, 4, 3);
  assert.equal((store.snapshot(admin).budgets[0] as any).held, 0);
  assert.equal((store.snapshot(admin).audit[0] as any).action, 'usage.reconciled');
});
test('two database connections cannot reserve more than the remaining cap', t => {
  const { store, actor, path, admin } = fixture(t); const second = new Store(path); t.after(() => second.close());
  let accepted = 0;
  for (let i = 0; i < 20; i++) {
    try { (i % 2 ? store : second).reserve(actor, `key-${i}`, `fp-${i}`, 'm', 10, 'v'); accepted++; }
    catch (e: any) { assert.equal(e.code, 'budget_exhausted'); }
  }
  assert.equal(accepted, 10);
  assert.equal((store.snapshot(admin).budgets[0] as any).held, 100);
});
test('revoked credential is checked inside reserve', t => {
  const { store, actor, admin, cred } = fixture(t); store.revoke(admin, cred.id);
  assert.throws(() => store.authenticate(cred.token), /invalid_credential/);
  assert.throws(() => store.reserve(actor, 'key-12345', 'fp', 'm', 1, 'v'), /invalid_credential/);
});
test('tenant isolation covers reads, revocation and reconciliation', t => {
  const { store, actor, admin, cred } = fixture(t);
  const other = { ...admin, tenant: 'tenant-b' };
  store.budget(other, 'project', 999);
  const id = store.reserve(actor, 'key-12345', 'fp', 'm', 20, 'v'); store.pending(id);
  assert.throws(() => store.reconcile(other, id, 0, 0, 0), /request_not_found/);
  assert.throws(() => store.revoke(other, cred.id), /credential_not_found/);
  assert.equal(store.snapshot(other).requests.length, 0);
  assert.equal((store.snapshot(admin).budgets[0] as any).cap, 100);
});
test('restart converts interrupted reservations to pending without refund', t => {
  const { store, actor, admin } = fixture(t);
  store.reserve(actor, 'key-12345', 'fp', 'm', 20, 'v'); store.recover();
  assert.equal((store.snapshot(admin).requests[0] as any).state, 'PENDING_RECONCILIATION');
  assert.equal((store.snapshot(admin).budgets[0] as any).held, 20);
});
test('actual charges exceeding estimate are recorded and stop new requests', t => {
  const { store, actor, admin } = fixture(t);
  const id = store.reserve(actor, 'key-12345', 'fp', 'm', 20, 'v'); store.settle(id, 120, 100, 10);
  assert.equal((store.snapshot(admin).budgets[0] as any).spent, 120);
  assert.throws(() => store.reserve(actor, 'key-67890', 'fp', 'm', 1, 'v'), /budget_exhausted/);
});
test('budget cap cannot be reduced below held funds', t => {
  const { store, actor, admin } = fixture(t);
  store.reserve(actor, 'key-12345', 'fp', 'm', 20, 'v');
  assert.throws(() => store.budget(admin, 'project', 19), /cap_below_committed/);
  assert.throws(() => store.budget(admin, 'project', 1.1), /invalid_integer/);
  assert.throws(() => store.provision(actor, 'user', 'project', 3600), /admin_required/);
});
test('idempotency conflict does not mutate budget', t => {
  const { store, actor, admin } = fixture(t);
  store.reserve(actor, 'key-12345', 'fp', 'm', 20, 'v');
  assert.throws(() => store.reserve(actor, 'key-12345', 'changed', 'm', 20, 'v'), /idempotency_conflict/);
  assert.equal((store.snapshot(admin).budgets[0] as any).held, 20);
});
test('credential plaintext never persists', t => {
  const { store, cred } = fixture(t);
  const rows = store.db.prepare('SELECT * FROM credentials').all();
  assert.equal(JSON.stringify(rows).includes(cred.token), false);
});
test('bootstrap cannot create another administrator using a different token', t => {
  const { store } = fixture(t);
  assert.throws(() => store.bootstrap('b'.repeat(40), 'other'), /bootstrap already completed/);
});
