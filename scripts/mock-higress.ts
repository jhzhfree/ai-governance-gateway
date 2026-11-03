import { createServer } from 'node:http';
// Protocol fixture only. This does not implement or validate Higress.
const key = process.env.HIGRESS_ACCESS_TOKEN;
if (!key) throw new Error('HIGRESS_ACCESS_TOKEN required');
createServer(async (req, res) => {
  if (req.headers.authorization !== 'Bearer ' + key || req.url !== '/v1/chat/completions' || req.method !== 'POST') {
    res.writeHead(401).end(); return;
  }
  let size = 0;
  for await (const chunk of req) { size += chunk.length; if (size > 65_536) { res.writeHead(413).end(); return; } }
  res.writeHead(200, { 'content-type': 'application/json' });
  res.end(JSON.stringify({ choices: [{ message: { role: 'assistant', content: '模拟模型已响应。' }, finish_reason: 'stop' }],
    usage: { prompt_tokens: 12, completion_tokens: 8 } }));
}).listen(8080, '127.0.0.1', () => process.stdout.write('Protocol fixture listening on 127.0.0.1:8080\n'));
