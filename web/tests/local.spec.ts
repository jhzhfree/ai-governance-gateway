import { test, expect } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';

test('offline administrator, forced password changes and user offboarding', async ({
  page,
  baseURL,
  browser,
}) => {
  test.setTimeout(90000);
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
  // A second real browser session proves that only the intended recipient can
  // accept; no mocked auth response or shared cookie impersonation is used.
  const adminSession = (await (await page.request.get('/auth/session')).json()) as {
    csrf_token: string;
    user: { issuer: string; subject: string };
  };
  const adminHeaders = { Origin: baseURL!, 'X-CSRF-Token': adminSession.csrf_token };
  const initialHandoffPassword = 'synthetic-handoff-temp-passphrase';
  const finalHandoffPassword = 'synthetic-handoff-final-passphrase';
  const createdUser = await page.request.post('/auth/local/users', {
    headers: adminHeaders,
    data: { username: 'handoff-recipient', password: initialHandoffPassword, administrator: false },
  });
  expect(createdUser.status()).toBe(201);
  const recipientUser = (await createdUser.json()) as { id: string };
  const createdOrg = await page.request.post('/api/v1/organizations', {
    headers: adminHeaders,
    data: { name: '双人交接组织', period: '2026-11', currency: 'CNY_MICRO', limit_amount: 1000 },
  });
  expect(createdOrg.status()).toBe(201);
  const org = (await createdOrg.json()) as { id: string };
  const orgPath = `/api/v1/organizations/${org.id}`;
  expect(
    (
      await page.request.put(`${orgPath}/members`, {
        headers: adminHeaders,
        data: {
          issuer: adminSession.user.issuer,
          subject: recipientUser.id,
          role: 'developer',
          expected_role: '',
        },
      })
    ).status(),
  ).toBe(204);
  const recipientContext = await browser.newContext({ baseURL });
  try {
    const receiver = await recipientContext.newPage();
    await receiver.goto('/');
    const recipientLogin = async (password: string) => {
      await receiver.getByLabel('本地用户名', { exact: true }).fill('handoff-recipient');
      await receiver.getByLabel('本地密码', { exact: true }).fill(password);
      await receiver.getByRole('button', { name: '登录本地账号', exact: true }).click();
    };
    await recipientLogin(initialHandoffPassword);
    await expect(
      receiver.getByRole('heading', { name: '本地账号安全初始化', exact: true }),
    ).toBeVisible();
    await receiver.getByLabel('当前本地密码', { exact: true }).fill(initialHandoffPassword);
    await receiver.getByLabel('新的本地密码', { exact: true }).fill(finalHandoffPassword);
    await receiver.getByLabel('确认新的本地密码', { exact: true }).fill(finalHandoffPassword);
    await receiver.getByRole('button', { name: '保存本地密码并重新登录', exact: true }).click();
    await expect(
      receiver.getByRole('heading', { name: '本地账号登录', exact: true }),
    ).toBeVisible();
    await recipientLogin(finalHandoffPassword);
    await expect(receiver.getByRole('heading', { name: '项目概览.' })).toBeVisible();
    await page.goto(`/?view=ownership&org=${org.id}`);
    await page
      .getByRole('combobox', { name: '接收成员', exact: true })
      .selectOption(JSON.stringify([adminSession.user.issuer, recipientUser.id]));
    await page.getByLabel('组织交接原因', { exact: true }).fill('双账号合成交接验收');
    await page.getByLabel('我已核对接收身份，确认后我将成为管理员', { exact: true }).check();
    await page.getByRole('button', { name: '发起组织交接', exact: true }).click();
    await expect(page.getByRole('status').filter({ hasText: '交接请求已创建' })).toBeVisible();
    const pending = (await (await page.request.get(`${orgPath}/ownership-transfer`)).json()) as {
      transfer: { id: string };
    };
    expect(
      (
        await page.request.post(`${orgPath}/ownership-transfer/${pending.transfer.id}/accept`, {
          headers: adminHeaders,
        })
      ).status(),
    ).toBe(403);
    await receiver.goto(`/?view=ownership&org=${org.id}`);
    const accept = receiver.getByRole('button', { name: '确认接任所有者', exact: true });
    await expect(accept).toBeDisabled();
    expect(
      (
        await receiver.request.post(`${orgPath}/ownership-transfer/${pending.transfer.id}/accept`)
      ).status(),
    ).toBe(403);
    await receiver
      .getByLabel('我确认接任组织所有者，并了解原所有者将成为管理员', { exact: true })
      .check();
    await accept.click();
    await expect(
      receiver.getByRole('status').filter({ hasText: '组织所有权交接已完成' }),
    ).toBeVisible();
    const organizations = (await (await receiver.request.get('/api/v1/organizations')).json()) as {
      data: { id: string; role: string }[];
    };
    expect(organizations.data.find((item) => item.id === org.id)?.role).toBe('owner');
    await page.getByRole('button', { name: '刷新组织交接', exact: true }).click();
    await expect(page.getByRole('button', { name: '发起组织交接', exact: true })).toHaveCount(0);
    await receiver.setViewportSize({ width: 390, height: 844 });
    expect(await receiver.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(
      true,
    );
    const recipientA11y = await new AxeBuilder({ page: receiver })
      .withTags(['wcag2a', 'wcag2aa', 'wcag21aa'])
      .analyze();
    expect(recipientA11y.violations.map((item) => item.id)).toEqual([]);
  } finally {
    await recipientContext.close();
  }
  await page.getByRole('button', { name: '本地账号', exact: true }).click();
  await expect(page.getByRole('heading', { name: '本地账号管理', exact: true })).toBeVisible();
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
