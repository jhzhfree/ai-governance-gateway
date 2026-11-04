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

test('mainland catalog authorizes three suppliers and preserves CNY accounting', async ({
  page,
}) => {
  await page.goto('/');
  await page.getByRole('link', { name: /使用组织账号登录/ }).click();
  await expect(page.getByRole('heading', { name: '项目概览.' })).toBeVisible();
  await page.getByRole('button', { name: /新建组织/ }).click();
  await page.getByLabel('组织名称', { exact: true }).fill('大陆供应商验收');
  await page.getByLabel('预算单位', { exact: true }).selectOption('CNY_MICRO');
  await expect(page.getByLabel('预算上限', { exact: true })).toHaveValue('1000000');
  await page.getByRole('button', { name: '确认创建', exact: true }).click();
  await expect(page.getByRole('status').filter({ hasText: '组织已创建' })).toBeVisible();
  await page.getByRole('button', { name: /新建项目/ }).click();
  await page.getByLabel('项目名称', { exact: true }).fill('三家适配项目');
  await page.getByRole('button', { name: '确认创建', exact: true }).click();
  await expect(page.getByRole('heading', { name: '三家适配项目' })).toBeVisible();
  await page.getByRole('button', { name: '配置模型', exact: true }).click();
  const dialog = page.getByRole('dialog', { name: '项目模型授权' });
  await expect(dialog.getByRole('checkbox').first()).toBeFocused();
  await page.keyboard.press('Shift+Tab');
  await expect(dialog.getByRole('button', { name: '保存模型授权', exact: true })).toBeFocused();
  await page.keyboard.press('Tab');
  await expect(dialog.getByRole('checkbox').first()).toBeFocused();
  await page.keyboard.press('Space');
  for (const supplier of ['aliyun', 'volcengine', 'zhipu'])
    await dialog.getByRole('checkbox', { name: new RegExp(`${supplier}/fixture`) }).check();
  await expect(dialog.getByText(/供应商、网关、数据库、日志与备份/)).toBeVisible();
  await page.getByRole('button', { name: '保存模型授权', exact: true }).click();
  await expect(dialog).not.toBeVisible();
  await expect(
    page.getByText('aliyun/fixture · volcengine/fixture · zhipu/fixture', { exact: true }),
  ).toBeVisible();
  await page.getByRole('button', { name: /创建密钥/ }).click();
  await page.getByLabel('密钥名称', { exact: true }).fill('三家协议测试密钥');
  await page.getByRole('button', { name: '确认创建', exact: true }).click();
  const key = await page.getByLabel('新密钥', { exact: true }).inputValue();
  await page.getByRole('button', { name: '我已保存，关闭', exact: true }).click();
  let requestID = '';
  for (const supplier of ['aliyun', 'volcengine', 'zhipu']) {
    const response = await page.request.post('/v1/chat/completions', {
      headers: { Authorization: `Bearer ${key}` },
      data: {
        model: `${supplier}/fixture`,
        max_tokens: 8,
        messages: [{ role: 'user', content: 'synthetic mainland protocol fixture' }],
      },
    });
    expect(response.status()).toBe(200);
    requestID = response.headers()['x-request-id'];
    const result = (await response.json()) as { model: string; usage: { total_tokens: number } };
    expect(result.model).toBe(`${supplier}/fixture`);
    expect(result.usage.total_tokens).toBe(5);
  }
  await page.getByRole('button', { name: '流量与核查', exact: true }).click();
  await expect(page.getByRole('heading', { name: '供应商运行状态' })).toBeVisible();
  const circuits = page.getByRole('region', { name: '供应商熔断状态表' });
  await expect(circuits.getByRole('cell', { name: '正常放行', exact: true })).toHaveCount(3);
  await expect(page.getByText(/请求量：3/)).toBeVisible();
  await page.reload();
  await expect(circuits.getByRole('cell', { name: '正常放行', exact: true })).toHaveCount(3);
  await page.getByRole('button', { name: '调用账本', exact: true }).click();
  await expect(page.locator('tbody tr')).toHaveCount(3);
  await page.getByLabel('执行结果', { exact: true }).selectOption('failed');
  await expect(page.getByText('暂无调用账本', { exact: true })).toBeVisible();
  await page.getByLabel('执行结果', { exact: true }).selectOption('completed');
  await expect(page.locator('tbody tr')).toHaveCount(3);
  expect(new URL(page.url()).searchParams.get('outcome')).toBe('completed');
  await page.getByLabel('请求编号', { exact: true }).fill(requestID);
  await page.getByRole('button', { name: '查询详情', exact: true }).click();
  const metadata = page.getByRole('region', { name: '调用账本详情' });
  await expect(metadata.getByText('zhipu/fixture / zhipu', { exact: true })).toBeVisible();
  await expect(metadata.getByText('执行完成', { exact: true })).toBeVisible();
  await expect(metadata.getByText('3 / 2', { exact: true })).toBeVisible();
  await expect(metadata.getByText(/^[0-9]+ ms$/)).toBeVisible();
  await page.screenshot({ path: '/tmp/aigw-execution-details.png', fullPage: true });

  await expect(page.getByRole('cell', { name: '2026-11 / CNY_MICRO', exact: true })).toHaveCount(3);
  await page.getByRole('button', { name: '项目概览', exact: true }).click();
  await page.getByRole('button', { name: '刷新', exact: true }).click();
  await expect(page.getByRole('progressbar', { name: '预算占用比例' })).toHaveAttribute(
    'value',
    '42',
  );
  await page.screenshot({ path: '/tmp/aigw-mainland-providers-desktop.png', fullPage: true });
  await page.getByRole('button', { name: '配置模型', exact: true }).click();
  await dialog.getByRole('checkbox', { name: /zhipu\/fixture/ }).uncheck();
  await page.getByRole('button', { name: '保存模型授权', exact: true }).click();
  await expect(dialog).not.toBeVisible();
  const denied = await page.request.post('/v1/chat/completions', {
    headers: { Authorization: `Bearer ${key}` },
    data: {
      model: 'zhipu/fixture',
      max_tokens: 8,
      messages: [{ role: 'user', content: 'synthetic denied request' }],
    },
  });
  expect(denied.status()).toBe(403);
});

test('project traffic policy saves with revision and inspection stays read only', async ({
  page,
}) => {
  await page.goto('/');
  await page.getByRole('link', { name: /使用组织账号登录/ }).click();
  await page.getByRole('button', { name: /新建组织/ }).click();
  await page.getByLabel('组织名称', { exact: true }).fill('流量核查团队');
  await page.getByRole('button', { name: '确认创建' }).click();
  await expect(page.getByRole('status').filter({ hasText: '组织已创建' })).toBeVisible();
  await page.getByRole('button', { name: /新建项目/ }).click();
  await page.getByLabel('项目名称', { exact: true }).fill('流量核查项目');
  await page.getByRole('button', { name: '确认创建' }).click();
  await expect(page.getByRole('heading', { name: '流量核查项目' })).toBeVisible();
  await page.getByRole('button', { name: /流量与核查/ }).click();
  await expect(page.getByRole('heading', { name: '项目流量控制' })).toBeVisible();
  await expect(page.getByLabel('每60秒请求上限')).toHaveValue('60');
  await page.getByLabel('每60秒请求上限').fill('12');
  await page.getByLabel('并发上限', { exact: true }).fill('2');
  await page.getByRole('button', { name: '保存流量策略' }).click();
  await expect(page.getByRole('status').filter({ hasText: '策略已保存' })).toBeVisible();
  await page.reload();
  await expect(page.getByLabel('每60秒请求上限')).toHaveValue('12');
  await expect(page.getByLabel('并发上限', { exact: true })).toHaveValue('2');
  await page.getByLabel('核查时间阈值').selectOption('3600');
  await expect(page.getByRole('heading', { name: '暂无待核查请求' })).toBeVisible();
  await page.screenshot({ path: '/tmp/aigw-operations-desktop.png', fullPage: true });
  await page.setViewportSize({ width: 390, height: 844 });
  await page.screenshot({ path: '/tmp/aigw-operations-mobile.png', fullPage: true });
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  const accessibility = await new AxeBuilder({ page })
    .withTags(['wcag2a', 'wcag2aa', 'wcag21aa'])
    .analyze();
  expect(accessibility.violations.map((issue) => issue.id)).toEqual([]);
});

test('normalized bill import, duplicate detection and rejection preserve budget', async ({
  page,
}) => {
  await page.goto('/');
  await page.getByRole('link', { name: /使用组织账号登录/ }).click();
  await page.getByRole('button', { name: /新建组织/ }).click();
  await page.getByLabel('组织名称', { exact: true }).fill('账单核对团队');
  await page.getByLabel('预算单位', { exact: true }).selectOption('CNY_MICRO');
  await page.getByRole('button', { name: '确认创建', exact: true }).click();
  await expect(page.getByRole('status').filter({ hasText: '组织已创建' })).toBeVisible();
  await page.getByRole('button', { name: /新建项目/ }).click();
  await page.getByLabel('项目名称', { exact: true }).fill('账单核对项目');
  await page.getByRole('button', { name: '确认创建', exact: true }).click();
  await expect(page.getByRole('heading', { name: '账单核对项目' })).toBeVisible();
  await page.getByRole('button', { name: '流量与核查', exact: true }).click();
  await expect(page.getByRole('heading', { name: '账单对账工作台' })).toBeVisible();
  await page.getByLabel('供应商账户引用', { exact: true }).fill('account-browser');
  await page.getByLabel('证据引用', { exact: true }).fill('synthetic-final-invoice');
  await page
    .getByLabel('标准账单CSV', { exact: true })
    .fill(
      'record_id,request_id,period,currency,amount,input_tokens,output_tokens\nbrowser-invoice,,2026-11,CNY_MICRO,10,0,0\n',
    );
  await page.getByRole('button', { name: '导入标准账单', exact: true }).click();
  await expect(page.getByRole('status').filter({ hasText: '已导入 1 条账单' })).toBeVisible();
  await page.getByRole('button', { name: '导入标准账单', exact: true }).click();
  await expect(page.getByRole('status').filter({ hasText: '相同账单已导入' })).toBeVisible();
  await page.getByRole('button', { name: '查看或审核 browser-invoice', exact: true }).click();
  await expect(page.getByLabel('审核动作', { exact: true })).toHaveValue('reject');
  await page.getByLabel('审核原因', { exact: true }).fill('合成证据缺少请求映射，驳回核查');
  await page.getByRole('button', { name: '提交账单审核', exact: true }).click();
  await expect(page.getByRole('status').filter({ hasText: '账单记录已驳回' })).toBeVisible();
  await page.reload();
  const table = page.getByRole('region', { name: '项目账单对账表' });
  await expect(table.getByRole('cell', { name: /已驳回/ })).toBeVisible();
  await page.setViewportSize({ width: 390, height: 844 });
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  const a11y = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa']).analyze();
  expect(a11y.violations.map((v) => v.id)).toEqual([]);
  await page.getByRole('button', { name: '项目概览', exact: true }).click();
  await expect(page.getByRole('progressbar', { name: '预算占用比例' })).toHaveAttribute(
    'value',
    '0',
  );
});

test('project-key model discovery and web member grants stay scoped', async ({ page }) => {
  await page.goto('/');
  await page.getByRole('link', { name: /使用组织账号登录/ }).click();
  await page.getByRole('button', { name: /新建组织/ }).click();
  await page.getByLabel('组织名称', { exact: true }).fill('成员协作团队');
  await page.getByLabel('预算单位', { exact: true }).selectOption('MOCK');
  await page.getByRole('button', { name: '确认创建', exact: true }).click();
  await expect(page.getByRole('status').filter({ hasText: '组织已创建' })).toBeVisible();
  await page.getByRole('button', { name: /新建项目/ }).click();
  await page.getByLabel('项目名称', { exact: true }).fill('成员协作项目');
  await page.getByRole('button', { name: '确认创建', exact: true }).click();
  await expect(page.getByRole('heading', { name: '成员协作项目' })).toBeVisible();
  await page.getByRole('button', { name: /创建密钥/ }).click();
  await page.getByLabel('密钥名称', { exact: true }).fill('模型发现密钥');
  await page.getByRole('button', { name: '确认创建', exact: true }).click();
  const key = await page.getByLabel('新密钥', { exact: true }).inputValue();
  await page.getByRole('button', { name: '我已保存，关闭', exact: true }).click();
  const response = await page.request.get('/v1/models', {
    headers: { Authorization: `Bearer ${key}` },
  });
  expect(response.status()).toBe(200);
  const models = (await response.json()) as {
    object: string;
    data: { id: string; object: string }[];
  };
  expect(models.object).toBe('list');
  expect(models.data.map((model) => model.id)).toEqual(['mock-text']);
  await page.getByRole('button', { name: '成员管理', exact: true }).click();
  await expect(page.getByRole('heading', { name: '成员与授权' })).toBeVisible();
  await page.getByLabel('成员身份subject', { exact: true }).fill('browser-developer');
  await page.getByLabel('分配角色', { exact: true }).selectOption('developer');
  await page.getByRole('button', { name: '保存成员权限', exact: true }).click();
  await expect(page.getByRole('status').filter({ hasText: '成员权限已保存' })).toBeVisible();
  await page.getByLabel('成员范围', { exact: true }).selectOption('project');
  const table = page.getByRole('region', { name: '成员权限表' });
  const row = table.getByRole('row').filter({ hasText: 'browser-developer' });
  await expect(row.getByRole('cell', { name: '未授权', exact: true })).toBeVisible();
  await row.getByRole('button', { name: '配置权限 browser-developer', exact: true }).click();
  await page.getByLabel('分配角色', { exact: true }).selectOption('viewer');
  await page.getByRole('button', { name: '保存成员权限', exact: true }).click();
  await expect(row.getByRole('cell', { name: '只读成员', exact: true })).toHaveCount(2);
  await row.getByRole('button', { name: '移除授权 browser-developer', exact: true }).click();
  await page.getByRole('button', { name: '确认移除项目授权', exact: true }).click();
  await expect(page.getByRole('status').filter({ hasText: '项目单独授权已移除' })).toBeVisible();
  await expect(row.getByRole('cell', { name: '未授权', exact: true })).toBeVisible();
  await page.reload();
  await page.getByLabel('成员范围', { exact: true }).selectOption('project');
  await expect(row.getByRole('cell', { name: '未授权', exact: true })).toBeVisible();
  await page.screenshot({ path: '/tmp/aigw-members-desktop.png', fullPage: true });
  await page.setViewportSize({ width: 390, height: 844 });
  await page.screenshot({ path: '/tmp/aigw-members-mobile.png', fullPage: true });
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  const a11y = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa']).analyze();
  expect(a11y.violations.map((v) => v.id)).toEqual([]);
});

test('content audit is opt in, explicitly granted and hidden after viewing', async ({ page }) => {
  await page.goto('/');
  await page.getByRole('link', { name: /使用组织账号登录/ }).click();
  await page.getByRole('button', { name: /新建组织/ }).click();
  await page.getByLabel('组织名称', { exact: true }).fill('内容审计团队');
  await page.getByLabel('预算单位', { exact: true }).selectOption('CNY_MICRO');
  await page.getByRole('button', { name: '确认创建', exact: true }).click();
  await expect(page.getByRole('status').filter({ hasText: '组织已创建' })).toBeVisible();
  await page.getByRole('button', { name: /新建项目/ }).click();
  await page.getByLabel('项目名称', { exact: true }).fill('内容审计项目');
  await page.getByRole('button', { name: '确认创建', exact: true }).click();
  await expect(page.getByRole('heading', { name: '内容审计项目' })).toBeVisible();
  await page.getByRole('button', { name: '配置模型', exact: true }).click();
  const dialog = page.getByRole('dialog', { name: '项目模型授权' });
  await dialog.getByRole('checkbox', { name: /aliyun\/fixture/ }).check();
  await page.getByRole('button', { name: '保存模型授权', exact: true }).click();
  await expect(dialog).not.toBeVisible();
  await page.getByRole('button', { name: /创建密钥/ }).click();
  await page.getByLabel('密钥名称', { exact: true }).fill('审计合成密钥');
  await page.getByRole('button', { name: '确认创建', exact: true }).click();
  const key = await page.getByLabel('新密钥', { exact: true }).inputValue();
  await page.getByRole('button', { name: '我已保存，关闭', exact: true }).click();
  await page.getByRole('button', { name: '内容审计', exact: true }).click();
  await expect(page.getByRole('heading', { name: '内容采集策略' })).toBeVisible();
  await expect(page.getByLabel('启用内容审计采集')).not.toBeChecked();
  await page.getByLabel('启用内容审计采集').check();
  await page.getByLabel('正文保留天数').fill('1');
  await page.getByLabel('告知与用途证据引用').fill('synthetic-consent');
  await page.getByRole('button', { name: '保存采集策略', exact: true }).click();
  await expect(page.getByRole('status').filter({ hasText: '内容审计策略已保存' })).toBeVisible();
  const session = (await (await page.request.get('/auth/session')).json()) as {
    user: { subject: string };
  };
  await page.getByLabel('审计员subject', { exact: true }).fill(session.user.subject);
  await page.getByRole('button', { name: '保存审计员授权', exact: true }).click();
  await expect(
    page.getByRole('status').filter({ hasText: '独立内容审计授权已保存' }),
  ).toBeVisible();
  const response = await page.request.post('/v1/chat/completions', {
    headers: { Authorization: `Bearer ${key}` },
    data: {
      model: 'aliyun/fixture',
      max_tokens: 8,
      messages: [{ role: 'user', content: 'synthetic private audit request' }],
    },
  });
  expect(response.status()).toBe(200);
  const id = response.headers()['x-request-id'];
  await page.getByRole('button', { name: '刷新内容记录', exact: true }).click();
  await page.getByLabel('审计请求编号', { exact: true }).fill(id);
  await page.getByLabel('内容查看理由', { exact: true }).fill('合成数据验证内容采集');
  await page.getByRole('button', { name: '留痕并查看正文', exact: true }).click();
  const detail = page.getByRole('region', { name: '审计正文详情' });
  await expect(detail).toContainText('synthetic private audit request');
  await page.getByRole('button', { name: '隐藏正文', exact: true }).click();
  await expect(detail).toHaveCount(0);
  await page.screenshot({ path: '/tmp/aigw-content-desktop.png', fullPage: true });
  await page.getByRole('link', { name: '打开当前项目内容审计专用入口' }).click();
  await expect(page.getByRole('heading', { name: '项目内容审计专用访问' })).toBeVisible();
  await expect(page.getByRole('heading', { name: '内容采集策略' })).toHaveCount(0);
  await page.getByLabel('审计请求编号', { exact: true }).fill(id);
  await page.getByLabel('内容查看理由', { exact: true }).fill('验证独立审计入口');
  await page.getByRole('button', { name: '留痕并查看正文', exact: true }).click();
  await expect(detail).toContainText('synthetic private audit request');
  await page.getByRole('button', { name: '隐藏正文', exact: true }).click();
  await page.setViewportSize({ width: 390, height: 844 });
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  const a11y = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa']).analyze();
  expect(a11y.violations.map((v) => v.id)).toEqual([]);
});

test('platform provider editor publishes a database route used by real inference', async ({
  page,
}) => {
  await page.goto('/');
  await page.getByRole('link', { name: /使用组织账号登录/ }).click();
  await page.getByRole('button', { name: '供应商配置', exact: true }).click();
  await expect(page.getByRole('heading', { name: '真实供应商配置' })).toBeVisible();
  await expect(page.getByRole('heading', { name: '发布真实路由与价格' })).toBeVisible();
  const before = (await (await page.request.get('/api/v1/providers')).json()) as {
    revision: number;
  };
  await page.getByRole('button', { name: '添加模型路由', exact: true }).click();
  await page.getByLabel('网关模型别名 4', { exact: true }).fill('aliyun/web-managed');
  await page.getByLabel('厂商模型 4', { exact: true }).fill('fixture-model');
  await page.getByLabel('价格版本 4', { exact: true }).fill('web-synthetic-v1');
  await page.getByLabel('输入单价（微元/百万 token） 4', { exact: true }).fill('2000000');
  await page.getByLabel('输出单价（微元/百万 token） 4', { exact: true }).fill('4000000');
  await page.getByLabel('最大输入 token 4', { exact: true }).fill('128');
  await page.getByLabel('最大输出 token 4', { exact: true }).fill('8');
  await page.getByLabel('模型驻留证据 4', { exact: true }).fill('synthetic-browser-evidence');
  await page
    .getByLabel('模型证据有效期 4', { exact: true })
    .fill(new Date(Date.now() + 3600000).toISOString().slice(0, 16));
  await page.getByRole('button', { name: '发布供应商配置', exact: true }).click();
  await expect
    .poll(
      async () =>
        ((await (await page.request.get('/api/v1/providers')).json()) as { revision: number })
          .revision,
    )
    .toBe(before.revision + 1);
  await page.reload();
  await expect(page.getByLabel('网关模型别名 4', { exact: true })).toHaveValue(
    'aliyun/web-managed',
  );
  const session = (await (await page.request.get('/auth/session')).json()) as {
    csrf_token: string;
  };
  const headers = { 'X-CSRF-Token': session.csrf_token, Origin: new URL(page.url()).origin };
  const orgResponse = await page.request.post('/api/v1/organizations', {
    headers,
    data: {
      name: '已发布供应商团队',
      period: '2026-11',
      currency: 'CNY_MICRO',
      limit_amount: 10000,
    },
  });
  expect(orgResponse.status()).toBe(201);
  const org = (await orgResponse.json()) as { id: string };
  const orgPath = `/api/v1/organizations/${org.id}`;
  const projectResponse = await page.request.post(`${orgPath}/projects`, {
    headers,
    data: { name: '真实路由接入', limit_amount: 5000 },
  });
  expect(projectResponse.status()).toBe(201);
  const project = (await projectResponse.json()) as { id: string };
  const projectPath = `${orgPath}/projects/${project.id}`;
  expect(
    (
      await page.request.put(`${projectPath}/models`, {
        headers,
        data: { models: ['aliyun/web-managed'] },
      })
    ).status(),
  ).toBe(204);
  const keyResponse = await page.request.post(`${projectPath}/keys`, {
    headers,
    data: { name: '页面发布路由验证', models: ['aliyun/web-managed'], ttl_seconds: 3600 },
  });
  expect(keyResponse.status()).toBe(201);
  const key = (await keyResponse.json()) as { secret: string };
  const response = await page.request.post('/v1/chat/completions', {
    headers: { Authorization: `Bearer ${key.secret}` },
    data: {
      model: 'aliyun/web-managed',
      max_tokens: 8,
      messages: [{ role: 'user', content: 'synthetic management publication test' }],
    },
  });
  expect(response.status()).toBe(200);
  expect(((await response.json()) as { model: string }).model).toBe('aliyun/web-managed');
  await page.setViewportSize({ width: 390, height: 844 });
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  const a11y = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa']).analyze();
  expect(a11y.violations.map((v) => v.id)).toEqual([]);
});
