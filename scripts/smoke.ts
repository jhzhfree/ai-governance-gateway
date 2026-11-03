import { randomUUID } from 'node:crypto';
import assert from 'node:assert/strict';

const base = process.env.GOVERNANCE_URL || 'http://127.0.0.1:8090';
const admin = process.env.BOOTSTRAP_ADMIN_TOKEN;
if (!admin) throw new Error('BOOTSTRAP_ADMIN_TOKEN required');
const project = 'smoke-' + randomUUID().slice(0, 8);
async function post(path: string, data: unknown, token: string, extra = {}) {
  return fetch(base + path, { method: 'POST', headers: { 'content-type': 'application/json',
    authorization: 'Bearer ' + token, ...extra }, body: JSON.stringify(data) });
}
assert.equal((await post('/api/v1/budgets', { project, cap: 1_000_000 }, admin)).status, 201);
const provision = await post('/api/v1/credentials', { project, subject: 'smoke-service', ttlSeconds: 600 }, admin);
assert.equal(provision.status, 201); const credential = await provision.json() as any;
const completion = await post('/v1/chat/completions', { model: 'approved-chat', messages: [{ role: 'user', content: '你好' }],
  max_tokens: 32 }, credential.token, { 'idempotency-key': randomUUID() });
assert.equal(completion.status, 200); const result = await completion.json() as any;
assert.ok(result.id); assert.equal(result.governance.accounting, 'SETTLED');
assert.equal((await post(`/api/v1/credentials/${credential.id}/revoke`, {}, admin)).status, 200);
assert.equal((await post('/v1/chat/completions', {}, credential.token)).status, 401);
const overview = await fetch(base + '/api/v1/overview', { headers: { authorization: 'Bearer ' + admin } });
assert.equal(overview.status, 200);
process.stdout.write(JSON.stringify({ result: 'passed', project, requestId: result.id,
  checks: ['provision', 'reserve', 'forward', 'settle', 'revoke', 'audit'] }) + '\n');
