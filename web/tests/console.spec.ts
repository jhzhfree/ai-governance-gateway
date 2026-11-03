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
