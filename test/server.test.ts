import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { once } from 'node:events';
import { server } from '../src/server.ts';
import { Store } from '../src/store.ts';
import { validateConfig, type Config } from '../src/config.ts';

async function setup(t: any, mode = 'ok') {
  let calls = 0; let auth = ''; let seen: any;
  const upstream = createServer(async (req, res) => {
    calls++; auth = req.headers.authorization || '';
    let raw = ''; for await (const chunk of req) raw += chunk;
    seen = JSON.parse(raw);
    if (mode === 'error') { res.writeHead(500).end('secret-upstream-key'); return; }
    const data: any = { choices: [{ message: { content: mode === 'blocked' ? 'secret-word' : 'hello' } }], private_key: 'never-forward' };
    if (mode !== 'missing') data.usage = { prompt_tokens: 10, completion_tokens: 5 };
    res.writeHead(200, { 'content-type': 'application/json' }).end(JSON.stringify(data));
  });
  upstream.listen(0, '127.0.0.1'); await once(upstream, 'listening');
  const port = (upstream.address() as any).port;
  const store = new Store(':memory:'); const adminToken = 'a'.repeat(40);
  store.bootstrap(adminToken, 'tenant'); const admin = store.authenticate(adminToken);
  store.budget(admin, 'p', 1_000_000); const cred = store.provision(admin, 'service-a', 'p', 3600);
  const config: Config = validateConfig({ host: '127.0.0.1', port: 0, database: ':memory:',
    bootstrapToken: adminToken, tenant: 'tenant', higressUrl: `http://127.0.0.1:${port}`,
    higressToken: 'higress-private-key', models: { chat: { upstreamModel: 'approved-chat', inputRate: 1, outputRate: 2, maxOutput: 100, rateVersion: 'v1' } },
    blockedTerms: ['secret-word'], timeoutMs: 1000 });
  const app = server(config, store); app.listen(0, '127.0.0.1'); await once(app, 'listening');
  const base = `http://127.0.0.1:${(app.address() as any).port}`;
  t.after(async () => {
    app.closeAllConnections(); upstream.closeAllConnections();
    await Promise.all([new Promise<void>(r => app.close(() => r())), new Promise<void>(r => upstream.close(() => r()))]);
    store.close();
  });
  const payload = { model: 'chat', messages: [{ role: 'user', content: 'hello' }], max_tokens: 20 };
  const call = (data: any = payload, token = cred.token, key = 'request-12345') => fetch(base + '/v1/chat/completions', {
    method: 'POST', headers: { authorization: 'Bearer ' + token, 'content-type': 'application/json',
      'idempotency-key': key, 'x-agw-tenant': 'forged', 'x-agw-subject': 'forged' }, body: JSON.stringify(data) });
  return { store, admin, adminToken, cred, base, call, payload, inspect: () => ({ calls, auth, seen }) };
}
test('authenticated request forwards only server credential and records trusted attribution', async t => {
  const f = await setup(t); const response = await f.call(); assert.equal(response.status, 200);
  const data = await response.json() as any;
  assert.equal(data.usage.total_tokens, 15); assert.equal(data.private_key, undefined);
  assert.equal(f.inspect().auth, 'Bearer higress-private-key');
  assert.equal(f.inspect().seen.model, 'approved-chat');
  const record = f.store.snapshot(f.admin).requests[0] as any;
  assert.equal(record.subject, 'service-a'); assert.equal(record.actual, 20);
});
test('missing or invalid identity is rejected before forwarding', async t => {
  const f = await setup(t); assert.equal((await f.call(undefined, 'invalid')).status, 401);
  assert.equal(f.inspect().calls, 0);
});
test('admin credentials cannot invoke model APIs', async t => {
  const f = await setup(t); assert.equal((await f.call(undefined, f.adminToken)).status, 403);
});
test('duplicate HTTP call does not invoke provider again', async t => {
  const f = await setup(t); assert.equal((await f.call()).status, 200);
  assert.equal((await f.call()).status, 409); assert.equal(f.inspect().calls, 1);
});
test('input keyword, stream, unknown model and unsupported tools reject before spending', async t => {
  const f = await setup(t);
  for (const data of [{ ...f.payload, messages: [{ role: 'user', content: 'secret-word' }] },
    { ...f.payload, stream: true }, { ...f.payload, model: 'other' }, { ...f.payload, tools: [] }]) {
    assert.ok([400, 403, 422].includes((await f.call(data)).status));
  }
  assert.equal(f.inspect().calls, 0); assert.equal(f.store.snapshot(f.admin).requests.length, 0);
});
test('output blocked after usage is settled', async t => {
  const f = await setup(t, 'blocked'); assert.equal((await f.call()).status, 403);
  assert.equal((f.store.snapshot(f.admin).requests[0] as any).state, 'SETTLED');
});
test('missing usage stays pending and holds funds', async t => {
  const f = await setup(t, 'missing'); const result = await f.call(); assert.equal(result.status, 200);
  assert.equal((f.store.snapshot(f.admin).requests[0] as any).state, 'PENDING_RECONCILIATION');
  assert.ok((f.store.snapshot(f.admin).budgets[0] as any).held > 0);
});
test('upstream failure is sanitized and does not refund uncertain charges', async t => {
  const f = await setup(t, 'error'); const result = await f.call(); assert.equal(result.status, 502);
  assert.equal((await result.text()).includes('secret-upstream-key'), false);
  assert.equal((f.store.snapshot(f.admin).requests[0] as any).state, 'PENDING_RECONCILIATION');
});
test('revoke endpoint stops subsequent request and ext-auth', async t => {
  const f = await setup(t);
  const denied = await fetch(f.base + '/api/v1/overview', { headers: { authorization: 'Bearer ' + f.cred.token } });
  assert.equal(denied.status, 403);
  await fetch(f.base + `/api/v1/credentials/${f.cred.id}/revoke`, { method: 'POST', headers: { authorization: 'Bearer ' + f.adminToken } });
  assert.equal((await f.call()).status, 401);
  assert.equal((await fetch(f.base + '/internal/admission/check', { headers: { authorization: 'Bearer ' + f.cred.token } })).status, 401);
});
test('concurrent HTTP requests share a single budget cap', async t => {
  const f = await setup(t); f.store.budget(f.admin, 'p', 6000);
  const results = await Promise.all(Array.from({ length: 12 }, (_, i) => f.call(f.payload, f.cred.token, `concurrent-${i}`)));
  assert.ok(results.some(r => r.status === 402));
  const budget = f.store.snapshot(f.admin).budgets[0] as any;
  assert.ok(budget.spent + budget.held <= budget.cap);
});
