import { test, expect } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';

test('embedded model editor publishes input-only pricing and shows durable evidence', async ({
  page,
}) => {
  await page.goto('/');
  await page.getByRole('link', { name: /使用组织账号登录/ }).click();
  await page.getByRole('button', { name: '供应商配置', exact: true }).click();
  await expect(page.getByLabel('接口能力 1', { exact: true })).toBeVisible();
  await expect(page.getByLabel('审核渠道 1', { exact: true })).toHaveCount(0);
  await page.getByLabel('接口能力 1', { exact: true }).selectOption('embeddings');
  await expect(page.getByLabel('最大输出 token 1', { exact: true })).toHaveValue('0');
  await expect(page.getByLabel('输出单价（微元/百万 token） 1', { exact: true })).toHaveValue('0');
  await page.getByLabel('价格版本 1', { exact: true }).fill('embedded-vector-v1');
  const published = page.waitForResponse(
    (r) => r.url().endsWith('/api/v1/providers') && r.request().method() === 'PUT',
  );
  await page.getByRole('button', { name: '发布供应商配置', exact: true }).click();
  expect((await published).ok()).toBeTruthy();
  await page.reload();
  await expect(page.getByLabel('接口能力 1', { exact: true })).toHaveValue('embeddings');
  await expect(page.getByRole('region', { name: '已发布模型配置表' })).toContainText('文本向量');
  const a11y = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa']).analyze();
  expect(a11y.violations.map((v) => ({ id: v.id, nodes: v.nodes.map((n) => n.target) }))).toEqual(
    [],
  );
  const response = await page.request.post('/v1/embeddings', {
    headers: { Authorization: `Bearer ${process.env.AIGW_E2E_EMBEDDING_KEY}` },
    data: { model: 'aliyun/fixture', input: 'synthetic', encoding_format: 'float' },
  });
  expect(response.status()).toBe(200);
  const requestID = response.headers()['x-request-id'];
  const organizations = await (await page.request.get('/api/v1/organizations')).json();
  const org = organizations.data[0].id;
  const projects = await (await page.request.get(`/api/v1/organizations/${org}/projects`)).json();
  const project = projects.data[0].id;
  await page.goto(`/?org=${org}&project=${project}&view=requests&request=${requestID}`);
  const evidence = page.getByRole('region', { name: '供应商完成证据' });
  await expect(evidence).toContainText('文本向量 · 仅输入计费');
  await expect(evidence.getByText('3 / 0', { exact: true })).toBeVisible();
  await expect(evidence.getByText('0.000006 元', { exact: true })).toBeVisible();
  await expect(evidence).toContainText('已生成结算回执');
  await evidence.screenshot({ path: '/tmp/aigw-embedding-evidence-desktop.png' });
  await page.setViewportSize({ width: 390, height: 844 });
  expect(
    await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1),
  ).toBeTruthy();
  await evidence.screenshot({ path: '/tmp/aigw-embedding-evidence-mobile.png' });
});
