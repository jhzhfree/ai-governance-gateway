import { test, expect } from '@playwright/test';

test('unified channels, write-only credentials, explicit publication and governed request', async ({
  page,
  baseURL,
}) => {
  await page.goto('/');
  await page.getByRole('link', { name: /使用组织账号登录/ }).click();
  await page.getByRole('button', { name: '供应商配置', exact: true }).click();
  await expect(page.getByRole('heading', { name: '供应商渠道', exact: true })).toBeVisible();
  const session = (await (await page.request.get('/auth/session')).json()) as {
    csrf_token: string;
  };
  const denied = await page.request.post('/api/v1/newapi/channels', { data: {} });
  expect(denied.status()).toBe(403);
  await page.getByRole('button', { name: '新增渠道', exact: true }).click();
  await page.getByLabel('渠道名称', { exact: true }).fill('浏览器审核渠道');
  await page.getByLabel('渠道协议', { exact: true }).selectOption('1');
  await page
    .getByLabel('渠道 Base URL', { exact: true })
    .fill(process.env.AIGW_E2E_NEWAPI_UPSTREAM!);
  await page.getByLabel('渠道模型列表', { exact: true }).fill('governance-model');
  await page.getByLabel('渠道供应商密钥', { exact: true }).fill('synthetic-browser-vendor');
  await page.getByLabel('启用渠道', { exact: true }).check();
  await page.getByRole('button', { name: '保存渠道', exact: true }).click();
  await expect(page.getByRole('status').filter({ hasText: '已保存' })).toBeVisible();
  await expect(page.getByLabel('渠道供应商密钥', { exact: true })).toHaveValue('');
  expect(await page.locator('body').innerText()).not.toContain('synthetic-browser-vendor');
  const listing = await page.request.get('/api/v1/newapi/channels');
  expect(listing.status()).toBe(200);
  expect(await listing.text()).not.toContain('synthetic-browser-vendor');
  await page.getByRole('button', { name: '关闭编辑', exact: true }).click();
  await page.getByLabel('部署证据引用', { exact: true }).fill('synthetic-browser-review');
  await page.getByLabel('部署证据有效期', { exact: true }).fill('2099-01-01T00:00');
  await page.getByRole('button', { name: '添加模型路由', exact: true }).click();
  await page.getByLabel('供应商 1', { exact: true }).fill('reviewed');
  await page.getByLabel('网关模型别名 1', { exact: true }).fill('reviewed/team-chat');
  await page.getByLabel('审核渠道 1', { exact: true }).selectOption({ label: '#2 浏览器审核渠道' });
  await page.getByLabel('价格版本 1', { exact: true }).fill('synthetic-v1');
  for (const [label, value] of [
    ['输入单价（微元/百万 token）', '2000000'],
    ['输出单价（微元/百万 token）', '4000000'],
    ['最大输入 token', '128'],
    ['最大输出 token', '8'],
  ])
    await page.getByLabel(`${label} 1`, { exact: true }).fill(value);
  await page.getByLabel('模型驻留证据 1', { exact: true }).fill('synthetic-model-review');
  await page.getByLabel('模型证据有效期 1', { exact: true }).fill('2099-01-01T00:00');
  await page.getByLabel('确认渠道与价格审核', { exact: true }).check();
  const publication = page.waitForResponse(
    (r) => r.url().endsWith('/api/v1/providers') && r.request().method() === 'PUT',
  );
  await page.getByRole('button', { name: '发布供应商配置', exact: true }).click();
  expect((await publication).status()).toBe(200);
  const headers = { Origin: baseURL!, 'X-CSRF-Token': session.csrf_token };
  await expect
    .poll(
      async () => {
        const response = await page.request.get('/api/v1/models');
        return (await response.text()).includes('reviewed/team-chat');
      },
      { timeout: 10000 },
    )
    .toBe(true);
  const org = await page.request.post('/api/v1/organizations', {
    headers,
    data: { name: '渠道测试组织', currency: 'CNY_MICRO', period: '2026-11', limit_amount: 100000 },
  });
  expect(org.status()).toBe(201);
  const o = (await org.json()) as { id: string };
  const project = await page.request.post(`/api/v1/organizations/${o.id}/projects`, {
    headers,
    data: { name: '渠道测试项目', limit_amount: 100000 },
  });
  expect(project.status()).toBe(201);
  const p = (await project.json()) as { id: string };
  const root = `/api/v1/organizations/${o.id}/projects/${p.id}`;
  expect(
    (
      await page.request.put(`${root}/models`, {
        headers,
        data: { models: ['reviewed/team-chat'] },
      })
    ).status(),
  ).toBe(204);
  const keyResponse = await page.request.post(`${root}/keys`, {
    headers,
    data: { name: '合成项目密钥', models: ['reviewed/team-chat'], ttl_seconds: 3600 },
  });
  expect(keyResponse.status()).toBe(201);
  const key = (await keyResponse.json()) as { secret: string };
  const result = await page.request.post('/v1/chat/completions', {
    headers: { Authorization: `Bearer ${key.secret}` },
    data: {
      model: 'reviewed/team-chat',
      messages: [{ role: 'user', content: 'synthetic' }],
      max_tokens: 8,
    },
  });
  expect(result.status(), await result.text()).toBe(200);
  const row = page.getByRole('row').filter({ hasText: '浏览器审核渠道' });
  await row.getByRole('button', { name: '编辑渠道' }).click();
  await page.getByLabel('启用渠道', { exact: true }).uncheck();
  await page.getByRole('button', { name: '保存渠道', exact: true }).click();
  await expect(page.getByRole('status').filter({ hasText: '已保存' })).toBeVisible();
  await expect(row).toContainText('须重新审核');
  expect(await page.evaluate(() => Object.keys(localStorage).length)).toBe(0);
  expect(await page.evaluate(() => Object.keys(sessionStorage).length)).toBe(0);
});
