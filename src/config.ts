import { readFileSync } from 'node:fs';

export type Model = { upstreamModel: string; inputRate: number; outputRate: number; maxOutput: number; rateVersion: string };
export type Config = {
  host: string; port: number; database: string; bootstrapToken: string; tenant: string;
  higressUrl: string; higressToken: string; models: Record<string, Model>;
  blockedTerms: string[]; timeoutMs: number;
};

export function validateConfig(config: Config): Config {
  const url = new URL(config.higressUrl);
  if (!['https:', 'http:'].includes(url.protocol) || url.username || url.password || url.search || url.hash) {
    throw new Error('invalid Higress endpoint');
  }
  if (url.protocol === 'http:' && !['localhost', '127.0.0.1', 'higress', 'mock-higress'].includes(url.hostname)) {
    throw new Error('HTTP allowed only for an explicit local Higress service');
  }
  if (!config.higressToken || config.bootstrapToken.length < 32) throw new Error('credentials required');
  if (!Object.keys(config.models).length) throw new Error('model allowlist required');
  for (const m of Object.values(config.models)) {
    if (!m.upstreamModel || !m.rateVersion || !Number.isSafeInteger(m.maxOutput) || m.maxOutput < 1 || m.maxOutput > 8192) {
      throw new Error('invalid model settings');
    }
    for (const rate of [m.inputRate, m.outputRate]) {
      if (!Number.isSafeInteger(rate) || rate < 0 || rate > 1_000_000) throw new Error('invalid price rate');
    }
  }
  if (!Array.isArray(config.blockedTerms) || config.blockedTerms.some(x => typeof x !== 'string' || !x || x.length > 128)) {
    throw new Error('invalid blocked terms');
  }
  return config;
}

export function loadConfig(): Config {
  const policy = JSON.parse(readFileSync(process.env.POLICY_FILE || 'config/policy.example.json', 'utf8'));
  return validateConfig({
    host: process.env.HOST || '127.0.0.1', port: Number(process.env.PORT || '8090'),
    database: process.env.DATABASE_PATH || 'var/governance.db',
    bootstrapToken: process.env.BOOTSTRAP_ADMIN_TOKEN || '', tenant: process.env.BOOTSTRAP_TENANT || 'demo',
    higressUrl: process.env.HIGRESS_URL || 'http://127.0.0.1:8080',
    higressToken: process.env.HIGRESS_ACCESS_TOKEN || '',
    models: policy.models, blockedTerms: policy.blockedTerms || [], timeoutMs: 30_000,
  });
}
