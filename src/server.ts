import { createServer, type IncomingMessage, type ServerResponse } from 'node:http';
import { createHash } from 'node:crypto';
import { Fault, integer, identifier } from './errors.ts';
import { Store, type Actor } from './store.ts';
import type { Config } from './config.ts';

function reply(res: ServerResponse, status: number, value: unknown) {
  res.writeHead(status, {
    'content-type': 'application/json; charset=utf-8', 'cache-control': 'no-store',
    'x-content-type-options': 'nosniff',
  });
  res.end(JSON.stringify(value));
}
async function body(req: IncomingMessage): Promise<any> {
  if (!(req.headers['content-type'] || '').startsWith('application/json')) throw new Fault(415, 'json_required');
  let size = 0; const parts: Buffer[] = [];
  for await (const chunk of req) {
    size += chunk.length;
    if (size > 65_536) throw new Fault(413, 'request_too_large');
    parts.push(chunk);
  }
  try {
    const value = JSON.parse(Buffer.concat(parts).toString('utf8'));
    if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error();
    return value;
  } catch { throw new Fault(400, 'invalid_json'); }
}
function actor(req: IncomingMessage, store: Store): Actor {
  const value = req.headers.authorization || '';
  if (!value.startsWith('Bearer ')) throw new Fault(401, 'bearer_required');
  return store.authenticate(value.slice(7));
}
function only(value: any, fields: string[]) {
  if (Object.keys(value).some(k => !fields.includes(k))) throw new Fault(400, 'unsupported_field');
}
async function upstreamJson(response: Response) {
  if (!response.body) throw new Fault(502, 'empty_upstream_response');
  let size = 0; const parts: Uint8Array[] = [];
  for await (const part of response.body) {
    size += part.length;
    if (size > 2_097_152) throw new Fault(502, 'upstream_response_too_large');
    parts.push(part);
  }
  try { return JSON.parse(Buffer.concat(parts).toString('utf8')); }
  catch { throw new Fault(502, 'invalid_upstream_response'); }
}

export function server(config: Config, store: Store) {
  const http = createServer(async (req, res) => {
    try {
      const url = new URL(req.url || '/', 'http://local');
      if (req.method === 'GET' && url.pathname === '/healthz') return reply(res, 200, { status: 'ok', version: '0.1.0-alpha.1' });
      const who = actor(req, store);
      if (req.method === 'GET' && url.pathname === '/internal/admission/check') {
        if (who.role !== 'service') throw new Fault(403, 'service_required');
        // Derived from authenticated identity; incoming identity headers ignored.
        res.setHeader('x-agw-tenant', who.tenant);
        res.setHeader('x-agw-subject', who.subject);
        return reply(res, 200, { allowed: true });
      }
      if (url.pathname.startsWith('/api/v1/')) {
        store.admin(who);
        if (req.method === 'GET' && url.pathname === '/api/v1/overview') {
          const limit = url.searchParams.has('limit') ? Number(url.searchParams.get('limit')) : 50;
          const before = url.searchParams.has('before') ? Number(url.searchParams.get('before')) : Number.MAX_SAFE_INTEGER;
          return reply(res, 200, store.snapshot(who, limit, before));
        }
        if (req.method === 'POST' && url.pathname === '/api/v1/budgets') {
          const data = await body(req); only(data, ['project', 'cap']);
          store.budget(who, data.project, data.cap); return reply(res, 201, { updated: true });
        }
        if (req.method === 'POST' && url.pathname === '/api/v1/credentials') {
          const data = await body(req); only(data, ['subject', 'project', 'ttlSeconds']);
          return reply(res, 201, store.provision(who, data.subject, data.project, data.ttlSeconds));
        }
        const revoke = url.pathname.match(/^\/api\/v1\/credentials\/([a-f0-9-]{36})\/revoke$/);
        if (req.method === 'POST' && revoke) {
          store.revoke(who, revoke[1]); return reply(res, 200, { revoked: true });
        }
        const reconcile = url.pathname.match(/^\/api\/v1\/requests\/([a-f0-9-]{36})\/reconcile$/);
        if (req.method === 'POST' && reconcile) {
          const data = await body(req); only(data, ['cost', 'inputTokens', 'outputTokens']);
          integer(data.cost); integer(data.inputTokens); integer(data.outputTokens);
          store.reconcile(who, reconcile[1], data.cost, data.inputTokens, data.outputTokens);
          return reply(res, 200, { reconciled: true });
        }
        throw new Fault(404, 'not_found');
      }
      if (req.method !== 'POST' || url.pathname !== '/v1/chat/completions') throw new Fault(404, 'not_found');
      if (who.role !== 'service') throw new Fault(403, 'service_required');
      const data = await body(req);
      only(data, ['model', 'messages', 'max_tokens', 'stream']);
      if (data.stream !== undefined && data.stream !== false) throw new Fault(422, 'stream_not_available_in_alpha');
      identifier(data.model);
      const model = Object.hasOwn(config.models, data.model) ? config.models[data.model] : undefined;
      if (!model) throw new Fault(403, 'model_not_allowed');
      if (!Array.isArray(data.messages) || data.messages.length < 1 || data.messages.length > 50) throw new Fault(400, 'invalid_messages');
      for (const message of data.messages) {
        if (!message || typeof message !== 'object' || Array.isArray(message)) throw new Fault(400, 'invalid_message');
        only(message, ['role', 'content']);
        if (!['system', 'user', 'assistant'].includes(message.role) || typeof message.content !== 'string') throw new Fault(400, 'text_messages_only');
        if (config.blockedTerms.some(term => message.content.includes(term))) throw new Fault(403, 'input_rule_blocked');
      }
      const outputLimit = data.max_tokens === undefined ? Math.min(512, model.maxOutput) : integer(data.max_tokens, model.maxOutput);
      if (!outputLimit) throw new Fault(400, 'output_limit_required');
      const key = req.headers['idempotency-key'];
      if (typeof key !== 'string' || !/^[a-zA-Z0-9_-]{8,100}$/.test(key)) throw new Fault(400, 'idempotency_key_required');
      const payload = JSON.stringify({ model: model.upstreamModel, messages: data.messages, max_tokens: outputLimit, stream: false });
      // Byte-based input estimate is intentionally not called an exact tokenizer.
      // Provider-specific hidden overhead/caching/reasoning require later adapters.
      const estimateInput = Buffer.byteLength(payload) + 4096;
      const reserved = estimateInput * model.inputRate + outputLimit * model.outputRate;
      const fingerprint = createHash('sha256').update(payload).digest('hex');
      const requestId = store.reserve(who, key, fingerprint, data.model, reserved, model.rateVersion);
      res.setHeader('x-agw-request-id', requestId);
      try {
        const response = await fetch(config.higressUrl.replace(/\/$/, '') + '/v1/chat/completions', {
          method: 'POST', redirect: 'error', signal: AbortSignal.timeout(config.timeoutMs),
          headers: { 'content-type': 'application/json', authorization: 'Bearer ' + config.higressToken,
            'x-agw-request-id': requestId }, body: payload,
        });
        if (!response.ok) {
          await response.body?.cancel();
          throw new Fault(502, 'upstream_failed');
        }
        const result = await upstreamJson(response);
        // Never pass arbitrary provider headers/body through to the client.
        if (!Array.isArray(result?.choices) || !result.choices.length) throw new Fault(502, 'invalid_upstream_response');
        const usage = result.usage;
        let usageKnown = true;
        try {
          integer(usage?.prompt_tokens, 1_000_000); integer(usage?.completion_tokens, 1_000_000);
        } catch { usageKnown = false; }
        if (usageKnown) {
          store.settle(requestId, usage.prompt_tokens * model.inputRate + usage.completion_tokens * model.outputRate,
            usage.prompt_tokens, usage.completion_tokens);
        } else store.pending(requestId);
        const choices = result.choices.map((choice: any, index: number) => {
          if (typeof choice?.message?.content !== 'string' || choice.message.tool_calls || choice.message.function_call) {
            throw new Fault(502, 'unsupported_upstream_content');
          }
          if (config.blockedTerms.some(term => choice.message.content.includes(term))) throw new Fault(403, 'output_rule_blocked');
          return { index, message: { role: 'assistant', content: choice.message.content }, finish_reason: choice.finish_reason || 'stop' };
        });
        return reply(res, 200, { id: requestId, object: 'chat.completion', model: data.model, choices,
          usage: usageKnown ? { prompt_tokens: usage.prompt_tokens, completion_tokens: usage.completion_tokens,
            total_tokens: usage.prompt_tokens + usage.completion_tokens } : undefined,
          governance: { accounting: usageKnown ? 'SETTLED' : 'PENDING_RECONCILIATION' } });
      } catch (error) {
        store.pending(requestId);
        if (error instanceof Fault) throw error;
        throw new Fault(502, 'upstream_unavailable');
      }
    } catch (error) {
      const fault = error instanceof Fault ? error : new Fault(503, 'service_unavailable');
      // No raw upstream errors, prompts, credentials, or database details in output.
      if (!res.headersSent) reply(res, fault.status, { error: { code: fault.code } });
      else res.end();
    }
  });
  http.requestTimeout = 15_000;
  http.headersTimeout = 10_000;
  http.maxHeadersCount = 40;
  return http;
}
