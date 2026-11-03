import { test, expect } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';

test('login, organization, project, one-time key and revocation', async ({ page }) => {
  const errors: string[] = [];
  page.on('pageerror', (error) => errors.push(error.name));
  await page.goto('/');
  await page.getByRole('link', { name: /使用组织账号登录/ }).click();
  await expect(page.getByRole('heading', { name: '项目概览.' })).toBeVisible();
  await page.getByRole('button', { name: /新建组织/ }).click();
  await page.getByLabel('组织名称', { exact: true }).fill('研发团队');
  await page.getByRole('button', { name: '确认创建' }).click();
  await expect(page.getByRole('status').filter({ hasText: '组织已创建' })).toBeVisible();
  await page.getByRole('button', { name: /新建项目/ }).click();
  await page.getByLabel('项目名称', { exact: true }).fill('内容服务');
  await page.getByRole('button', { name: '确认创建' }).click();
  await expect(page.getByRole('heading', { name: '内容服务' })).toBeVisible();
  await page.getByRole('button', { name: /创建密钥/ }).click();
  await page.getByLabel('密钥名称', { exact: true }).fill('SDK 主密钥');
  await page.getByRole('button', { name: '确认创建' }).click();
  await expect(page.getByLabel('新密钥', { exact: true })).toBeVisible();
  // Never print or persist the secret, including in test reports.
  expect(
    (await page.getByLabel('新密钥', { exact: true }).inputValue()).startsWith('aigw_pk_'),
  ).toBeTruthy();
  await page.getByRole('button', { name: '我已保存，关闭' }).click();
  await expect(page.getByLabel('新密钥', { exact: true })).toHaveCount(0);
  await expect(page.getByRole('cell', { name: /^SDK 主密钥 / })).toBeVisible();
  await expect(page.getByRole('progressbar', { name: '预算占用比例' })).toBeVisible();
  await page.screenshot({ path: '/tmp/aigw-console-desktop.png', fullPage: true });
  const accessibility = await new AxeBuilder({ page })
    .withTags(['wcag2a', 'wcag2aa', 'wcag21aa'])
    .analyze();
  expect(
    accessibility.violations.map((issue) => ({
      id: issue.id,
      targets: issue.nodes.map((node) => node.target),
    })),
  ).toEqual([]);
  await page.getByRole('button', { name: '撤销 SDK 主密钥' }).click();
  await page.getByRole('button', { name: '确认撤销' }).click();
  await expect(page.getByText('已撤销', { exact: true })).toBeVisible();
  await page.reload();
  await expect(page.getByLabel('新密钥', { exact: true })).toHaveCount(0);
  expect(await page.evaluate(() => Object.keys(localStorage).length)).toBe(0);
  expect(await page.evaluate(() => Object.keys(sessionStorage).length)).toBe(0);
  await page.getByRole('button', { name: '退出登录' }).click();
  await expect(page.getByRole('link', { name: /使用组织账号登录/ })).toBeVisible();
  expect(errors).toEqual([]);
});

test('responsive layout, keyboard navigation and API failure recovery', async ({
  page,
  baseURL,
}) => {
  await page.goto('/');
  await page.getByRole('link', { name: /使用组织账号登录/ }).click();
  await expect(page.getByRole('heading', { name: '项目概览.' })).toBeVisible();
  const session = (await (await page.request.get('/auth/session')).json()) as {
    csrf_token: string;
  };
  const headers = { Origin: baseURL!, 'X-CSRF-Token': session.csrf_token };
  const orgResponse = await page.request.post('/api/v1/organizations', {
    headers,
    data: { name: '移动端团队', period: '2026-11', limit_amount: 100 },
  });
  expect(orgResponse.status()).toBe(201);
  const organization = (await orgResponse.json()) as { id: string };
  const projectResponse = await page.request.post(
    `/api/v1/organizations/${organization.id}/projects`,
    { headers, data: { name: '移动端项目', limit_amount: 50 } },
  );
  expect(projectResponse.status()).toBe(201);
  const project = (await projectResponse.json()) as { id: string };
  await page.goto(`/?org=${organization.id}&project=${project.id}`);
  await expect(page.getByRole('heading', { name: '移动端项目' })).toBeVisible();
  await page.keyboard.press('Tab');
  await expect(page.getByRole('link', { name: '跳到主要内容' })).toBeFocused();
  await page.setViewportSize({ width: 390, height: 844 });
  await expect(page.getByLabel('组织', { exact: true })).toBeVisible();
  expect(
    await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth),
  ).toBeTruthy();
  await page.screenshot({ path: '/tmp/aigw-console-mobile.png', fullPage: true });
  await page.route('**/api/v1/organizations/*/projects/*/keys?*', (route) =>
    route.fulfill({
      status: 503,
      contentType: 'application/json',
      body: '{"error":{"code":"service_unavailable"}}',
    }),
  );
  await page.reload();
  await expect(page.getByRole('alert').filter({ hasText: '服务暂时不可用' })).toBeVisible();
  await page.unrouteAll();
  await page.getByRole('button', { name: '重试', exact: true }).click();
  await expect(page.getByRole('alert')).toHaveCount(0);
  await page.getByRole('button', { name: '审计记录' }).click();
  await expect(page.getByRole('heading', { name: '组织变更记录' })).toBeVisible();
  await expect(page.getByRole('cell', { name: '创建项目', exact: true })).toBeVisible();
});

test('an expired session clears the authenticated workspace', async ({ page }) => {
  await page.goto('/');
  await page.getByRole('link', { name: /使用组织账号登录/ }).click();
  await expect(page.getByRole('heading', { name: '项目概览.' })).toBeVisible();
  await page.route('**/api/v1/organizations?*', (route) =>
    route.fulfill({
      status: 401,
      contentType: 'application/json',
      body: '{"error":{"code":"unauthenticated"}}',
    }),
  );
  await page.reload();
  await expect(page.getByRole('link', { name: /使用组织账号登录/ })).toBeVisible();
  await expect(page.getByRole('heading', { name: '项目概览.' })).toHaveCount(0);
});

test('request ledger follows real inference, filters, pages and isolates projects', async ({
  page,
  baseURL,
}) => {
  await page.goto('/');
  await page.getByRole('link', { name: /使用组织账号登录/ }).click();
  await expect(page.getByRole('heading', { name: '项目概览.' })).toBeVisible();
  const session = (await (await page.request.get('/auth/session')).json()) as {
    csrf_token: string;
  };
  const headers = { Origin: baseURL!, 'X-CSRF-Token': session.csrf_token };
  const orgResponse = await page.request.post('/api/v1/organizations', {
    headers,
    data: { name: '账本验收团队', period: '2026-11', limit_amount: 1000 },
  });
  expect(orgResponse.status()).toBe(201);
  const org = (await orgResponse.json()) as { id: string };
  const orgPath = `/api/v1/organizations/${org.id}`;
  const projectResponse = await page.request.post(`${orgPath}/projects`, {
    headers,
    data: { name: '请求账本项目', limit_amount: 500 },
  });
  expect(projectResponse.status()).toBe(201);
  const project = (await projectResponse.json()) as { id: string };
  const projectPath = `${orgPath}/projects/${project.id}`;
  const keyResponse = await page.request.post(`${projectPath}/keys`, {
    headers,
    data: { name: '账本测试凭证', models: ['mock-text'], ttl_seconds: 3600 },
  });
  expect(keyResponse.status()).toBe(201);
  const key = (await keyResponse.json()) as { secret: string };
  let requestID = '';
  for (let index = 0; index < 51; index++) {
    const response = await page.request.post('/v1/chat/completions', {
      headers: { Authorization: `Bearer ${key.secret}` },
      data: {
        model: 'mock-text',
        stream: true,
        max_tokens: 8,
        messages: [{ role: 'user', content: 'synthetic-browser-fixture' }],
      },
    });
    expect(response.status()).toBe(200);
    expect((await response.text()).endsWith('data: [DONE]\n\n')).toBeTruthy();
    requestID = response.headers()['x-request-id'];
  }
  await page.goto(`/?org=${org.id}&project=${project.id}&view=requests`);
  await expect(page.getByRole('heading', { name: '项目调用账本', exact: true })).toBeVisible();
  await expect(page.getByRole('button', { name: '加载更多调用' })).toBeVisible();
  await expect(page.locator('tbody tr')).toHaveCount(50);
  await page.getByRole('button', { name: '加载更多调用' }).click();
  await expect(page.locator('tbody tr')).toHaveCount(51);
  await expect(page.getByRole('button', { name: '加载更多调用' })).toHaveCount(0);
  await page.getByLabel('账本状态', { exact: true }).selectOption('uncertain');
  await expect(page.getByText('暂无调用账本', { exact: true })).toBeVisible();
  expect(new URL(page.url()).searchParams.get('state')).toBe('uncertain');
  await page.getByLabel('账本状态', { exact: true }).selectOption('settled');
  await expect(page.locator('tbody tr')).toHaveCount(50);
  await page.getByLabel('请求编号', { exact: true }).fill(requestID);
  await page.getByRole('button', { name: '查询详情', exact: true }).click();
  const detail = page.getByRole('region', { name: '调用账本详情' });
  await expect(detail.getByText(requestID, { exact: true })).toBeVisible();
  await expect(detail.getByText('10 / 4', { exact: true })).toBeVisible();
  await expect(detail.getByText('mock-v1', { exact: true })).toBeVisible();
  await page.reload();
  await expect(detail.getByText(requestID, { exact: true })).toBeVisible();
  await page.screenshot({ path: '/tmp/aigw-request-ledger-desktop.png', fullPage: true });
  const accessibility = await new AxeBuilder({ page })
    .withTags(['wcag2a', 'wcag2aa', 'wcag21aa'])
    .analyze();
  expect(accessibility.violations.map((issue) => issue.id)).toEqual([]);
  await page.setViewportSize({ width: 390, height: 844 });
  expect(
    await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth),
  ).toBeTruthy();
  await page.screenshot({ path: '/tmp/aigw-request-ledger-mobile.png', fullPage: true });
  await page.getByLabel('请求编号', { exact: true }).fill('unknown-request');
  await page.getByRole('button', { name: '查询详情', exact: true }).click();
  await expect(detail.getByRole('alert')).toBeVisible();
  await expect(detail.getByText(requestID, { exact: true })).toHaveCount(0);
  const otherResponse = await page.request.post(`${orgPath}/projects`, {
    headers,
    data: { name: '无调用项目', limit_amount: 50 },
  });
  const other = (await otherResponse.json()) as { id: string };
  await page.goto(`/?org=${org.id}&project=${other.id}&view=requests&request=${requestID}`);
  await expect(page.getByText('暂无调用账本', { exact: true })).toBeVisible();
  await expect(detail.getByRole('alert')).toBeVisible();
  await expect(detail.getByText('10 / 4', { exact: true })).toHaveCount(0);
});
