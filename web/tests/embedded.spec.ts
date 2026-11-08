import { test, expect } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';

test('embedded model editor publishes reviewed pricing and shows durable evidence', async ({
  page,
}) => {
  const streaming = process.env.AIGW_E2E_EMBEDDED_PROTOCOL === 'messages-stream';
  const native = streaming || process.env.AIGW_E2E_EMBEDDED_PROTOCOL === 'messages';
  const cached = native || process.env.AIGW_E2E_EMBEDDED_PROTOCOL === 'cache';
  const label = streaming
    ? 'messages-stream'
    : native
      ? 'messages'
      : cached
        ? 'cache'
        : 'embedding';
  await page.goto('/');
  await page.getByRole('link', { name: /使用组织账号登录/ }).click();
  await page.getByRole('button', { name: '供应商配置', exact: true }).click();
  await expect(page.getByLabel('接口能力 1', { exact: true })).toBeVisible();
  await expect(page.getByLabel('审核渠道 1', { exact: true })).toHaveCount(0);
  if (cached) {
    if (native) await page.getByLabel('接口能力 1', { exact: true }).selectOption('messages');
    else await page.getByLabel('启用 DeepSeek 缓存命中计费').check();
    await expect(page.getByLabel('缓存命中单价（微元/百万 Token）', { exact: true })).toHaveValue(
      '2000000',
    );
    await page.getByLabel('缓存命中单价（微元/百万 Token）', { exact: true }).fill('500000');
    await page.getByLabel('价格版本 1', { exact: true }).fill('embedded-cache-v1');
    if (streaming) await page.getByLabel('审核原生 Messages 实时流 1', { exact: true }).check();
  } else {
    await page.getByLabel('接口能力 1', { exact: true }).selectOption('embeddings');
    await expect(page.getByLabel('最大输出 token 1', { exact: true })).toHaveValue('0');
    await expect(page.getByLabel('输出单价（微元/百万 token） 1', { exact: true })).toHaveValue(
      '0',
    );
    await page.getByLabel('价格版本 1', { exact: true }).fill('embedded-vector-v1');
  }
  const published = page.waitForResponse(
    (r) => r.url().endsWith('/api/v1/providers') && r.request().method() === 'PUT',
  );
  await page.getByRole('button', { name: '发布供应商配置', exact: true }).click();
  expect((await published).ok()).toBeTruthy();
  await page.reload();
  await expect(page.getByLabel('接口能力 1', { exact: true })).toHaveValue(
    native ? 'messages' : cached ? '' : 'embeddings',
  );
  await expect(page.getByRole('region', { name: '已发布模型配置表' })).toContainText(
    cached ? '缓存命中 500000' : '文本向量',
  );
  const a11y = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa']).analyze();
  expect(a11y.violations.map((v) => ({ id: v.id, nodes: v.nodes.map((n) => n.target) }))).toEqual(
    [],
  );
  if (native)
    await expect(page.getByLabel('审核原生 Messages 实时流 1', { exact: true })).toBeChecked({
      checked: streaming,
    });
  const response = await page.request.post(
    native ? '/v1/messages' : cached ? '/v1/chat/completions' : '/v1/embeddings',
    {
      headers: { Authorization: `Bearer ${process.env.AIGW_E2E_EMBEDDING_KEY}` },
      data: cached
        ? {
            model: 'deepseek/fixture',
            max_tokens: 32,
            stream: streaming,
            messages: [{ role: 'user', content: 'synthetic' }],
          }
        : { model: 'aliyun/fixture', input: 'synthetic', encoding_format: 'float' },
    },
  );
  expect(response.status()).toBe(200);
  if (streaming) {
    const text = await response.text();
    expect(text).toContain('event: message_stop');
    expect(text).not.toContain('[DONE]');
  }
  const requestID = response.headers()['x-request-id'];
  const organizations = await (await page.request.get('/api/v1/organizations')).json();
  const org = organizations.data[0].id;
  const projects = await (await page.request.get(`/api/v1/organizations/${org}/projects`)).json();
  const project = projects.data[0].id;
  await page.goto(`/?org=${org}&project=${project}&view=requests&request=${requestID}`);
  const evidence = page.getByRole('region', { name: '供应商完成证据' });
  await expect(evidence).toContainText(
    native
      ? '原生 Messages · 缓存命中计费'
      : cached
        ? 'DeepSeek · 缓存命中计费'
        : '文本向量 · 仅输入计费',
  );
  await expect(evidence.getByText(cached ? '3 / 2' : '3 / 0', { exact: true })).toBeVisible();
  await expect(
    evidence.getByText(cached ? '0.000011 元' : '0.000006 元', { exact: true }),
  ).toBeVisible();
  await expect(evidence).toContainText('已生成结算回执');
  if (cached) await expect(evidence.getByText('2 / 1', { exact: true })).toBeVisible();
  if (native) await expect(evidence).toContainText('原始缓存写入');
  await evidence.screenshot({
    path: `/tmp/aigw-${label}-evidence-desktop.png`,
  });
  await page.setViewportSize({ width: 390, height: 844 });
  expect(
    await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1),
  ).toBeTruthy();
  await evidence.screenshot({
    path: `/tmp/aigw-${label}-evidence-mobile.png`,
  });
  if (cached) {
    await page.setViewportSize({ width: 1280, height: 900 });
    await page.getByRole('button', { name: '成本报表', exact: true }).click();
    const report = page.getByRole('region', { name: '成本明细表' });
    await expect(report).toContainText('缓存计量覆盖');
    await expect(report).toContainText('2 / 1');
    await expect(report.getByRole('cell', { name: '1 / 1', exact: true })).toHaveCount(2);
  }
});
