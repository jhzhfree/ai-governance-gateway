import { test } from 'node:test';
import assert from 'node:assert/strict';
import { validateConfig } from '../src/config.ts';

const base = { host: '127.0.0.1', port: 8090, database: ':memory:', bootstrapToken: 'a'.repeat(40),
  tenant: 'a', higressUrl: 'http://127.0.0.1:8080', higressToken: 'private',
  models: { chat: { upstreamModel: 'm', inputRate: 1, outputRate: 2, maxOutput: 100, rateVersion: 'v1' } },
  blockedTerms: ['secret'], timeoutMs: 1000 };
test('nonlocal cleartext upstream and URL credentials are refused', () => {
  assert.throws(() => validateConfig({ ...base, higressUrl: 'http://example.com' }), /HTTP allowed only/);
  assert.throws(() => validateConfig({ ...base, higressUrl: 'https://user:pass@example.com' }), /invalid Higress endpoint/);
  assert.throws(() => validateConfig({ ...base, higressUrl: 'file:///tmp/model' }), /invalid Higress endpoint/);
});
test('floating or negative money rates are refused', () => {
  for (const rate of [0.1, -1, NaN, Number.MAX_SAFE_INTEGER]) {
    assert.throws(() => validateConfig({ ...base, models: { chat: { ...base.models.chat, inputRate: rate } } }), /invalid price rate/);
  }
});
test('missing credentials and unconstrained output cannot start', () => {
  assert.throws(() => validateConfig({ ...base, higressToken: '' }), /credentials required/);
  assert.throws(() => validateConfig({ ...base, models: { chat: { ...base.models.chat, maxOutput: 0 } } }), /invalid model settings/);
});
