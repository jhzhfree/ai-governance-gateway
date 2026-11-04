import { test, expect } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';

test('offline administrator, forced password changes and user offboarding', async ({
  page,
  baseURL,
}) => {
  test.setTimeout(60000);
  const initial = 'synthetic-initial-local-passphrase';
  const updated = 'synthetic-updated-local-passphrase';
  const temporary = 'synthetic-worker-temp-passphrase';
  const workerPassword = 'synthetic-worker-final-passphrase';
  const login = async (username: string, password: string) => {
    await page.getByLabel('本地用户名', { exact: true }).fill(username);
    await page.getByLabel('本地密码', { exact: true }).fill(password);
    await page.getByRole('button', { name: '登录本地账号', exact: true }).click();
  };
  const change = async (current: string, next: string) => {
    await page.getByLabel('当前本地密码', { exact: true }).fill(current);
    await page.getByLabel('新的本地密码', { exact: true }).fill(next);
    await page.getByLabel('确认新的本地密码', { exact: true }).fill(next);
    await page.getByRole('button', { name: '保存本地密码并重新登录', exact: true }).click();
    await expect(page.getByRole('heading', { name: '本地账号登录', exact: true })).toBeVisible();
  };
  await page.goto('/');
  await expect(page.getByRole('heading', { name: '本地账号登录', exact: true })).toBeVisible();
  await login('admin', initial);
  await expect(
    page.getByRole('heading', { name: '本地账号安全初始化', exact: true }),
  ).toBeVisible();
  expect((await page.request.get('/api/v1/organizations')).status()).toBe(403);
  await change(initial, updated);
  await login('admin', updated);
  await expect(page.getByRole('heading', { name: '项目概览.' })).toBeVisible();
  await page.getByRole('button', { name: '本地账号', exact: true }).click();
  await page.getByLabel('新本地用户名', { exact: true }).fill('worker');
  await page.getByLabel('新本地账号初始密码', { exact: true }).fill(temporary);
  await page.getByRole('button', { name: '创建本地账号', exact: true }).click();
  const row = page
    .getByRole('region', { name: '本地账号列表', exact: true })
    .getByRole('row')
    .filter({ hasText: 'worker' });
  await expect(row).toContainText('待改密');
  await page.getByRole('button', { name: '退出登录', exact: true }).click();
  await login('worker', temporary);
  await expect(
    page.getByRole('heading', { name: '本地账号安全初始化', exact: true }),
  ).toBeVisible();
  await change(temporary, workerPassword);
  await login('worker', workerPassword);
  await expect(page.getByRole('heading', { name: '项目概览.' })).toBeVisible();
  await page.getByRole('button', { name: '本地账号', exact: true }).click();
  await expect(page.getByRole('heading', { name: '本地账号管理', exact: true })).toHaveCount(0);
  expect((await page.request.get('/auth/local/users')).status()).toBe(403);
  const tokenResponse = await page.request.post('/auth/local/token', {
    headers: { Origin: baseURL! },
    data: { username: 'worker', password: workerPassword },
  });
  expect(tokenResponse.status()).toBe(200);
  const token = (await tokenResponse.json()) as { access_token: string };
  await page.getByRole('button', { name: '退出登录', exact: true }).click();
  await login('admin', updated);
  await expect(page.getByRole('heading', { name: '项目概览.' })).toBeVisible();
  await page.getByRole('button', { name: '本地账号', exact: true }).click();
  await row.getByRole('button', { name: '停用 worker', exact: true }).click();
  await page.getByRole('button', { name: '确认停用账号及撤销密钥', exact: true }).click();
  await expect(row.getByRole('cell', { name: '停用', exact: true })).toBeVisible();
  expect(
    (
      await page.request.get('/api/v1/organizations', {
        headers: { Authorization: `Bearer ${token.access_token}` },
      })
    ).status(),
  ).toBe(401);
  await row.getByRole('button', { name: '启用 worker', exact: true }).click();
  await expect(row.getByRole('cell', { name: '启用', exact: true })).toBeVisible();
  await row.getByRole('button', { name: '重置密码 worker', exact: true }).click();
  await page.getByLabel('重置后的本地临时密码', { exact: true }).fill(temporary);
  await page.getByRole('button', { name: '确认重置本地密码', exact: true }).click();
  await expect(row).toContainText('待改密');
  expect(await page.evaluate(() => Object.keys(localStorage).length)).toBe(0);
  expect(await page.evaluate(() => Object.keys(sessionStorage).length)).toBe(0);
  await page.setViewportSize({ width: 390, height: 844 });
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  const a11y = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa']).analyze();
  expect(
    a11y.violations.map((v) => ({
      id: v.id,
      nodes: v.nodes.map((node) => ({ target: node.target, summary: node.failureSummary })),
    })),
  ).toEqual([]);
});
