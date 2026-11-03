import { mkdtemp, readFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { spawnSync } from 'node:child_process';

const directory = await mkdtemp(join(tmpdir(), 'aigw-api-'));
try {
  const output = join(directory, 'api.ts');
  const result = spawnSync(
    process.execPath,
    ['node_modules/openapi-typescript/bin/cli.js', '../api/openapi/management.json', '-o', output],
    { stdio: 'inherit' },
  );
  if (result.status !== 0) process.exitCode = 1;
  else if (!(await readFile(output)).equals(await readFile('src/generated/api.ts'))) {
    console.error('API types differ from the contract. Run npm run api:generate.');
    process.exitCode = 1;
  }
} finally {
  await rm(directory, { recursive: true, force: true });
}
