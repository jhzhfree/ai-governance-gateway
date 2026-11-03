import { spawn } from 'node:child_process';
import { mkdtempSync, rmSync } from 'node:fs';
import { join } from 'node:path';
import { tmpdir } from 'node:os';
import { once } from 'node:events';
import { randomBytes } from 'node:crypto';

// Launch all processes together so isolated executors share the same network.
// Uses a fresh temporary database and ephemeral test credentials, not .env.
const directory = mkdtempSync(join(tmpdir(), 'agw-smoke-'));
const env = { ...process.env, BOOTSTRAP_ADMIN_TOKEN: randomBytes(32).toString('base64url'),
  HIGRESS_ACCESS_TOKEN: randomBytes(32).toString('base64url'),
  DATABASE_PATH: join(directory, 'test.db'), HOST: '127.0.0.1', PORT: '8090',
  HIGRESS_URL: 'http://127.0.0.1:8080', BOOTSTRAP_TENANT: 'demo' };
const children = [spawn(process.execPath, ['scripts/mock-higress.ts'], { env, stdio: 'inherit' }),
  spawn(process.execPath, ['src/main.ts'], { env, stdio: 'inherit' })];
try {
  let ready = false;
  for (let i = 0; i < 50; i++) {
    if (children.some(p => p.exitCode !== null)) throw new Error('demo process exited during startup');
    try { ready = (await fetch('http://127.0.0.1:8090/healthz')).ok; } catch { /* startup */ }
    if (ready) break;
    await new Promise(r => setTimeout(r, 100));
  }
  if (!ready) throw new Error('governance did not start');
  const smoke = spawn(process.execPath, ['scripts/smoke.ts'], { env, stdio: 'inherit' });
  const [code] = await once(smoke, 'exit');
  if (code !== 0) throw new Error('smoke failed');
} finally {
  for (const child of children) child.kill('SIGTERM');
  await Promise.all(children.map(p => p.exitCode === null ? once(p, 'exit') : Promise.resolve()));
  rmSync(directory, { recursive: true, force: true });
}
