import { mkdirSync } from 'node:fs';
import { dirname } from 'node:path';
import { loadConfig } from './config.ts';
import { Store } from './store.ts';
import { server } from './server.ts';

const config = loadConfig();
mkdirSync(dirname(config.database), { recursive: true, mode: 0o700 });
const store = new Store(config.database);
store.bootstrap(config.bootstrapToken, config.tenant);
store.recover();
const app = server(config, store);
app.listen(config.port, config.host, () => process.stdout.write(`AI governance alpha listening on ${config.host}:${config.port}\n`));
let closing = false;
for (const signal of ['SIGINT', 'SIGTERM']) process.on(signal, () => {
  if (closing) return;
  closing = true;
  app.close(() => { store.close(); process.exit(0); });
  setTimeout(() => process.exit(1), 35_000).unref();
});
