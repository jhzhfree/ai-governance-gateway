import { randomBytes } from 'node:crypto';
import { mkdirSync, writeFileSync, existsSync } from 'node:fs';
mkdirSync('var', { recursive: true, mode: 0o700 });
if (existsSync('.env')) throw new Error('.env exists; refusing to replace credentials');
writeFileSync('.env', [
  'BOOTSTRAP_ADMIN_TOKEN=' + randomBytes(32).toString('base64url'),
  'HIGRESS_ACCESS_TOKEN=' + randomBytes(32).toString('base64url'),
  'BOOTSTRAP_TENANT=demo', 'DATABASE_PATH=var/governance.db',
  'HIGRESS_URL=http://127.0.0.1:8080',
].join('\n') + '\n', { mode: 0o600 });
process.stdout.write('Created .env with random development credentials. Keep it private.\n');
