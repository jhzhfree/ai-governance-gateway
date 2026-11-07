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

test('mainland catalog authorizes four suppliers and preserves CNY accounting', async ({
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
  await page.getByLabel('项目名称', { exact: true }).fill('四家适配项目');
  await page.getByRole('button', { name: '确认创建', exact: true }).click();
  await expect(page.getByRole('heading', { name: '四家适配项目' })).toBeVisible();
  await page.getByRole('button', { name: '配置模型', exact: true }).click();
  const dialog = page.getByRole('dialog', { name: '项目模型授权' });
  await expect(dialog.getByRole('checkbox').first()).toBeFocused();
  await page.keyboard.press('Shift+Tab');
  await expect(dialog.getByRole('button', { name: '保存模型授权', exact: true })).toBeFocused();
  await page.keyboard.press('Tab');
  await expect(dialog.getByRole('checkbox').first()).toBeFocused();
  await page.keyboard.press('Space');
  for (const supplier of ['aliyun', 'volcengine', 'zhipu', 'deepseek'])
    await dialog.getByRole('checkbox', { name: new RegExp(`${supplier}/fixture`) }).check();
  await expect(dialog.getByText(/供应商、网关、数据库、日志与备份/)).toBeVisible();
  await page.getByRole('button', { name: '保存模型授权', exact: true }).click();
  await expect(dialog).not.toBeVisible();
  await expect(
    page.getByText('aliyun/fixture · deepseek/fixture · volcengine/fixture · zhipu/fixture', {
      exact: true,
    }),
  ).toBeVisible();
  await page.getByRole('button', { name: /创建密钥/ }).click();
  await page.getByLabel('密钥名称', { exact: true }).fill('四家协议测试密钥');
  await page.getByRole('button', { name: '确认创建', exact: true }).click();
  const key = await page.getByLabel('新密钥', { exact: true }).inputValue();
  await page.getByRole('button', { name: '我已保存，关闭', exact: true }).click();
  let requestID = '';
  for (const supplier of ['aliyun', 'volcengine', 'zhipu', 'deepseek']) {
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
  await expect(circuits.getByRole('cell', { name: '正常放行', exact: true })).toHaveCount(4);
  await expect(page.getByText(/请求量：4/)).toBeVisible();
  await page.reload();
  await expect(circuits.getByRole('cell', { name: '正常放行', exact: true })).toHaveCount(4);
  await page.getByRole('button', { name: '调用账本', exact: true }).click();
  await expect(page.locator('tbody tr')).toHaveCount(4);
  await page.getByLabel('执行结果', { exact: true }).selectOption('failed');
  await expect(page.getByText('暂无调用账本', { exact: true })).toBeVisible();
  await page.getByLabel('执行结果', { exact: true }).selectOption('completed');
  await expect(page.locator('tbody tr')).toHaveCount(4);
  expect(new URL(page.url()).searchParams.get('outcome')).toBe('completed');
  await page.getByLabel('请求编号', { exact: true }).fill(requestID);
  await page.getByRole('button', { name: '查询详情', exact: true }).click();
  const metadata = page.getByRole('region', { name: '调用账本详情' });
  await expect(metadata.getByText('deepseek/fixture / deepseek', { exact: true })).toBeVisible();
  await expect(metadata.getByText('执行完成', { exact: true })).toBeVisible();
  await expect(metadata.getByText('3 / 2', { exact: true })).toBeVisible();
  await expect(metadata.getByText(/^[0-9]+ ms$/)).toBeVisible();
  await page.screenshot({ path: '/tmp/aigw-execution-details.png', fullPage: true });

  await expect(page.getByRole('cell', { name: '2026-11 / CNY_MICRO', exact: true })).toHaveCount(4);
  await page.getByRole('button', { name: '项目概览', exact: true }).click();
  await page.getByRole('button', { name: '刷新', exact: true }).click();
  await expect(page.getByRole('progressbar', { name: '预算占用比例' })).toHaveAttribute(
    'value',
    '56',
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
  await page.getByLabel('账单供应商', { exact: true }).selectOption('__reviewed');
  await page.getByLabel('已审核供应商标识', { exact: true }).fill('reviewed-browser');
  const rejectedSupplier = page.waitForResponse(
    (response) =>
      response.url().endsWith('/billing/imports') && response.request().method() === 'POST',
  );
  await page.getByRole('button', { name: '导入标准账单', exact: true }).click();
  const rejectedResponse = await rejectedSupplier;
  expect(rejectedResponse.status()).toBe(400);
  expect(rejectedResponse.request().postDataJSON().provider).toBe('reviewed-browser');
  await page.getByLabel('账单供应商', { exact: true }).selectOption('aliyun');
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
  // Sample the real button styles through the disabled -> enabled transition.
  // A cloned control keeps this deterministic without issuing another API call.
  const contrast = await page
    .getByRole('button', { name: '刷新账单', exact: true })
    .evaluate(async (element) => {
      const button = element.cloneNode(true) as HTMLButtonElement;
      element.parentElement!.append(button);
      const frame = () => new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
      const luminance = (color: string) => {
        const rgb = color
          .match(/[\d.]+/g)!
          .slice(0, 3)
          .map(Number)
          .map((value) => {
            const channel = value / 255;
            return channel <= 0.04045 ? channel / 12.92 : ((channel + 0.055) / 1.055) ** 2.4;
          });
        return rgb[0] * 0.2126 + rgb[1] * 0.7152 + rgb[2] * 0.0722;
      };
      try {
        button.disabled = true;
        button.classList.add('t-is-disabled');
        const disabledAt = performance.now();
        while (performance.now() - disabledAt < 250) await frame();
        button.disabled = false;
        button.removeAttribute('aria-disabled');
        button.classList.remove('t-is-disabled');
        const enabledAt = performance.now();
        let minimum = Infinity;
        while (performance.now() - enabledAt < 250) {
          const foreground = luminance(
            getComputedStyle(button.querySelector('.t-button__text')!).color,
          );
          const background = luminance(getComputedStyle(button).backgroundColor);
          minimum = Math.min(
            minimum,
            (Math.max(foreground, background) + 0.05) / (Math.min(foreground, background) + 0.05),
          );
          await frame();
        }
        return { minimum, transitionProperty: getComputedStyle(button).transitionProperty };
      } finally {
        button.remove();
      }
    });
  expect(Number.isFinite(contrast.minimum)).toBe(true);
  expect(contrast.minimum, JSON.stringify(contrast)).toBeGreaterThanOrEqual(4.5);
  await page.setViewportSize({ width: 390, height: 844 });
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  const a11y = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa']).analyze();
  expect(
    a11y.violations.map((v) => ({
      id: v.id,
      nodes: v.nodes.map((n) => ({ target: n.target, failureSummary: n.failureSummary })),
    })),
  ).toEqual([]);
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
  expect(
    a11y.violations.map((v) => ({
      id: v.id,
      nodes: v.nodes.map((n) => ({ target: n.target, failureSummary: n.failureSummary })),
    })),
  ).toEqual([]);
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
  expect(
    a11y.violations.map((v) => ({
      id: v.id,
      nodes: v.nodes.map((n) => ({ target: n.target, failureSummary: n.failureSummary })),
    })),
  ).toEqual([]);
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
  const syntheticKey = 'synthetic-AIGW_ALIYUN_API_KEY';
  await page.getByLabel('阿里云百炼 API Key', { exact: true }).fill(syntheticKey);
  await page.getByRole('button', { name: '保存阿里云百炼密钥', exact: true }).click();
  await expect
    .poll(async () => {
      const status = (await (await page.request.get('/api/v1/providers')).json()) as {
        suppliers: { id: string; credential_source: string }[];
      };
      return status.suppliers.find((supplier) => supplier.id === 'aliyun')?.credential_source;
    })
    .toBe('database');
  await expect(page.getByLabel('阿里云百炼 API Key', { exact: true })).toHaveValue('');
  expect(await (await page.request.get('/api/v1/providers')).text()).not.toContain(syntheticKey);

  await page.getByRole('button', { name: '添加模型路由', exact: true }).click();
  await page.getByLabel('网关模型别名 5', { exact: true }).fill('aliyun/web-managed');
  await page.getByLabel('厂商模型 5', { exact: true }).fill('fixture-model');
  await page.getByLabel('价格版本 5', { exact: true }).fill('web-synthetic-v1');
  await page.getByLabel('输入单价（微元/百万 token） 5', { exact: true }).fill('2000000');
  await page.getByLabel('输出单价（微元/百万 token） 5', { exact: true }).fill('4000000');
  await page.getByLabel('最大输入 token 5', { exact: true }).fill('128');
  await page.getByLabel('最大输出 token 5', { exact: true }).fill('8');
  await page.getByLabel('模型驻留证据 5', { exact: true }).fill('synthetic-browser-evidence');
  await page
    .getByLabel('模型证据有效期 5', { exact: true })
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
  await expect(page.getByLabel('网关模型别名 5', { exact: true })).toHaveValue(
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
  expect(
    a11y.violations.map((v) => ({
      id: v.id,
      nodes: v.nodes.map((n) => ({ target: n.target, failureSummary: n.failureSummary })),
    })),
  ).toEqual([]);
});

test('budget editing, monthly periods and keyword rules govern real inference', async ({
  page,
  baseURL,
}) => {
  await page.goto('/');
  await page.getByRole('link', { name: /使用组织账号登录/ }).click();
  const session = (await (await page.request.get('/auth/session')).json()) as {
    csrf_token: string;
  };
  const headers = { Origin: baseURL!, 'X-CSRF-Token': session.csrf_token };
  const orgResponse = await page.request.post('/api/v1/organizations', {
    headers,
    data: { name: '预算规则团队', period: '2026-11', currency: 'CNY_MICRO', limit_amount: 10000 },
  });
  expect(orgResponse.status()).toBe(201);
  const org = (await orgResponse.json()) as { id: string };
  const orgPath = `/api/v1/organizations/${org.id}`;
  const projectResponse = await page.request.post(`${orgPath}/projects`, {
    headers,
    data: { name: '预算规则项目', limit_amount: 5000 },
  });
  expect(projectResponse.status()).toBe(201);
  const project = (await projectResponse.json()) as { id: string };
  const projectPath = `${orgPath}/projects/${project.id}`;
  expect(
    (
      await page.request.put(`${projectPath}/models`, {
        headers,
        data: { models: ['aliyun/fixture'] },
      })
    ).status(),
  ).toBe(204);
  const keyResponse = await page.request.post(`${projectPath}/keys`, {
    headers,
    data: { name: '治理验证', models: ['aliyun/fixture'], ttl_seconds: 3600 },
  });
  expect(keyResponse.status()).toBe(201);
  const key = (await keyResponse.json()) as { secret: string };
  await page.goto(`/?view=budget&org=${org.id}&project=${project.id}`);
  await page.getByLabel('项目预算上限新上限', { exact: true }).fill('9000');
  await page.getByLabel('项目预算上限调整原因', { exact: true }).fill('本地合成验收调整');
  await page.getByRole('button', { name: '保存项目预算上限', exact: true }).click();
  await expect(page.getByRole('status').filter({ hasText: '预算已更新' })).toBeVisible();
  await page.getByLabel('启用预算月度轮转', { exact: true }).check();
  await page.getByRole('button', { name: '保存月度轮转设置', exact: true }).click();
  await expect(page.getByRole('status').filter({ hasText: '月度轮转设置已更新' })).toBeVisible();
  await page.getByRole('button', { name: '关键字匹配', exact: true }).click();
  await page.getByRole('button', { name: '添加关键字规则', exact: true }).click();
  await page.getByLabel('关键字文本 1', { exact: true }).fill('stop-word');
  await page.getByRole('button', { name: '保存关键字策略', exact: true }).click();
  await expect
    .poll(
      async () =>
        ((await (await page.request.get(`${projectPath}/keywords`)).json()) as { revision: number })
          .revision,
    )
    .toBe(1);
  const invoke = (stream: boolean, text: string) =>
    page.request.post('/v1/chat/completions', {
      headers: { Authorization: `Bearer ${key.secret}` },
      data: {
        model: 'aliyun/fixture',
        max_tokens: 8,
        stream,
        messages: [{ role: 'user', content: text }],
      },
    });
  expect((await invoke(false, 'stop-word')).status()).toBe(403);
  await page.getByRole('button', { name: '刷新命中记录', exact: true }).click();
  await expect(
    page
      .getByRole('region', { name: '关键字命中记录表' })
      .getByRole('cell', { name: '输入 / 阻断', exact: true }),
  ).toBeVisible();
  await page.getByLabel('关键字文本 1', { exact: true }).fill('synthetic');
  await page.getByLabel('关键字方向 1', { exact: true }).selectOption('output');
  await page.getByLabel('关键字处置 1', { exact: true }).selectOption('redact');
  await page.getByRole('button', { name: '保存关键字策略', exact: true }).click();
  await expect
    .poll(
      async () =>
        ((await (await page.request.get(`${projectPath}/keywords`)).json()) as { revision: number })
          .revision,
    )
    .toBe(2);
  const redacted = await invoke(false, 'allowed message');
  expect(redacted.status()).toBe(200);
  const result = (await redacted.json()) as { choices: { message: { content: string } }[] };
  expect(result.choices[0].message.content).toContain('[REDACTED]');
  expect(result.choices[0].message.content).not.toContain('synthetic');
  expect((await invoke(true, 'allowed message')).status()).toBe(400);
  await page.getByLabel('关键字处置 1', { exact: true }).selectOption('alert');
  await page.getByRole('button', { name: '保存关键字策略', exact: true }).click();
  await expect
    .poll(
      async () =>
        ((await (await page.request.get(`${projectPath}/keywords`)).json()) as { revision: number })
          .revision,
    )
    .toBe(3);
  const streamed = await invoke(true, 'allowed message');
  expect(streamed.status()).toBe(200);
  expect(await streamed.text()).toContain('[DONE]');
  await page.getByRole('button', { name: '预算管理', exact: true }).click();
  const account = (await (await page.request.get(`${orgPath}/budget`)).json()) as {
    period: string;
  };
  const [year, month] = account.period.split('-').map(Number);
  const next = new Date(Date.UTC(year, month, 1)).toISOString().slice(0, 7);
  await page.getByLabel('新预算周期', { exact: true }).fill(next);
  await page.getByLabel('确认立即切换预算周期', { exact: true }).check();
  await page.getByRole('button', { name: '切换预算周期', exact: true }).click();
  await expect(page.getByRole('status').filter({ hasText: '预算周期已切换' })).toBeVisible();
  await page.setViewportSize({ width: 390, height: 844 });
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  const a11y = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa']).analyze();
  expect(
    a11y.violations.map((v) => ({
      id: v.id,
      nodes: v.nodes.map((n) => ({ target: n.target, failureSummary: n.failureSummary })),
    })),
  ).toEqual([]);
});

test('cost report reads real ledger, exports audited CSV and fits mobile', async ({
  page,
  baseURL,
}) => {
  await page.goto('/');
  await page.getByRole('link', { name: /使用组织账号登录/ }).click();
  const session = (await (await page.request.get('/auth/session')).json()) as {
    csrf_token: string;
  };
  const headers = { Origin: baseURL!, 'X-CSRF-Token': session.csrf_token };
  const org = (await (
    await page.request.post('/api/v1/organizations', {
      headers,
      data: { name: '成本团队', period: '2026-11', currency: 'CNY_MICRO', limit_amount: 10000 },
    })
  ).json()) as { id: string };
  const orgPath = `/api/v1/organizations/${org.id}`;
  const project = (await (
    await page.request.post(`${orgPath}/projects`, {
      headers,
      data: { name: '成本项目', limit_amount: 5000 },
    })
  ).json()) as { id: string };
  const path = `${orgPath}/projects/${project.id}`;
  expect(
    (
      await page.request.put(`${path}/models`, { headers, data: { models: ['aliyun/fixture'] } })
    ).status(),
  ).toBe(204);
  const key = (await (
    await page.request.post(`${path}/keys`, {
      headers,
      data: { name: '成本调用', models: ['aliyun/fixture'], ttl_seconds: 3600 },
    })
  ).json()) as { secret: string };
  expect(
    (
      await page.request.post('/v1/chat/completions', {
        headers: { Authorization: `Bearer ${key.secret}` },
        data: {
          model: 'aliyun/fixture',
          max_tokens: 8,
          messages: [{ role: 'user', content: 'synthetic report fixture' }],
        },
      })
    ).status(),
  ).toBe(200);
  await page.goto(`/?view=reports&org=${org.id}&project=${project.id}`);
  const table = page.getByRole('region', { name: '成本明细表', exact: true });
  await expect(
    table.getByRole('cell', { name: 'aliyun/fixture / aliyun', exact: true }),
  ).toBeVisible();
  await expect(table.getByRole('cell', { name: 'CNY_MICRO', exact: true })).toBeVisible();
  const now = new Date();
  const from = now.toISOString().slice(0, 10);
  now.setUTCDate(now.getUTCDate() + 1);
  const to = now.toISOString().slice(0, 10);
  expect(
    (await page.request.post(`${path}/cost-report/export?from=${from}&to=${to}`)).status(),
  ).toBe(403);
  const downloading = page.waitForEvent('download');
  await page.getByRole('button', { name: '导出成本 CSV', exact: true }).click();
  const download = await downloading;
  expect(download.suggestedFilename()).toBe('aigw-cost-report.csv');
  const stream = await download.createReadStream();
  let csv = '';
  for await (const chunk of stream) csv += String(chunk);
  expect(csv).toContain('day_utc,project_id,model,provider,currency');
  expect(csv).toContain('aliyun/fixture,aliyun,CNY_MICRO');
  const audit = (await (await page.request.get(`${orgPath}/audit`)).json()) as {
    data: { action: string }[];
  };
  expect(audit.data.some((item) => item.action === 'cost_report.exported')).toBe(true);
  await page.getByLabel('汇总整个组织', { exact: true }).check();
  await page.getByRole('button', { name: '查询成本报表', exact: true }).click();
  await expect(page.getByText(/观察时间：.*整个组织/)).toBeVisible();
  await page.setViewportSize({ width: 390, height: 844 });
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  const a11y = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa']).analyze();
  expect(a11y.violations.map((item) => item.id)).toEqual([]);
});

test('budget notifications persist short holds and acknowledgment never changes money', async ({
  page,
  baseURL,
}) => {
  await page.goto('/');
  await page.getByRole('link', { name: /使用组织账号登录/ }).click();
  const session = (await (await page.request.get('/auth/session')).json()) as {
    csrf_token: string;
  };
  const headers = { Origin: baseURL!, 'X-CSRF-Token': session.csrf_token };
  const org = (await (
    await page.request.post('/api/v1/organizations', {
      headers,
      data: { name: '告警团队', period: '2026-11', currency: 'CNY_MICRO', limit_amount: 10000 },
    })
  ).json()) as { id: string };
  const orgPath = `/api/v1/organizations/${org.id}`;
  const project = (await (
    await page.request.post(`${orgPath}/projects`, {
      headers,
      data: { name: '告警项目', limit_amount: 400 },
    })
  ).json()) as { id: string };
  const path = `${orgPath}/projects/${project.id}`;
  expect(
    (
      await page.request.put(`${path}/models`, { headers, data: { models: ['aliyun/fixture'] } })
    ).status(),
  ).toBe(204);
  const key = (await (
    await page.request.post(`${path}/keys`, {
      headers,
      data: { name: '告警验收', models: ['aliyun/fixture'], ttl_seconds: 3600 },
    })
  ).json()) as { secret: string };
  await page.goto(`/?view=alerts&org=${org.id}&project=${project.id}`);
  await page.getByLabel('启用项目预算告警', { exact: true }).check();
  const section = page
    .getByRole('heading', { name: '项目预算告警', exact: true })
    .locator('xpath=ancestor::section[1]');
  await section.getByLabel('50%', { exact: true }).check();
  await page.getByLabel('项目预算告警调整原因', { exact: true }).fill('合成调用阈值验收');
  await page.getByRole('button', { name: '保存项目预算告警策略', exact: true }).click();
  await expect(page.getByRole('status').filter({ hasText: '预算告警策略已更新' })).toBeVisible();
  expect(
    (
      await page.request.post('/v1/chat/completions', {
        headers: { Authorization: `Bearer ${key.secret}` },
        data: {
          model: 'aliyun/fixture',
          max_tokens: 8,
          messages: [{ role: 'user', content: 'synthetic alert fixture' }],
        },
      })
    ).status(),
  ).toBe(200);
  await page.getByRole('button', { name: '刷新项目预算告警', exact: true }).click();
  const table = page.getByRole('region', { name: '项目预算告警记录表', exact: true });
  await expect(table.getByRole('cell', { name: '50%', exact: true })).toBeVisible();
  await expect(table.getByText('待确认', { exact: true })).toBeVisible();
  const alertPage = (await (await page.request.get(`${path}/budget/alerts`)).json()) as {
    data: { id: number; held: string }[];
  };
  expect(alertPage.data.length).toBe(1);
  expect(BigInt(alertPage.data[0].held) > 0n).toBe(true);
  expect(
    (
      await page.request.post(`${path}/budget/alerts/${alertPage.data[0].id}/acknowledge`, {
        data: { reason: 'missing csrf' },
      })
    ).status(),
  ).toBe(403);
  const before = await (await page.request.get(`${path}/usage`)).json();
  await table.getByRole('button', { name: /^处理项目预算告警 / }).click();
  await page.getByLabel('项目预算告警处理说明', { exact: true }).fill('已核查占额已结算，继续观察');
  await page.getByRole('button', { name: '确认处理项目预算告警', exact: true }).click();
  await expect(table.getByText('已确认', { exact: true })).toBeVisible();
  expect(await (await page.request.get(`${path}/usage`)).json()).toEqual(before);
  await page.reload();
  await expect(
    page
      .getByRole('region', { name: '项目预算告警记录表', exact: true })
      .getByText('已确认', { exact: true }),
  ).toBeVisible();
  await page.setViewportSize({ width: 390, height: 844 });
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  const a11y = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa']).analyze();
  expect(
    a11y.violations.map((issue) => ({
      id: issue.id,
      nodes: issue.nodes.map((n) => ({ target: n.target, failureSummary: n.failureSummary })),
    })),
  ).toEqual([]);
});

test('department tree, scoped grants, project assignment and history persist through real API', async ({
  page,
  baseURL,
}) => {
  await page.goto('/');
  await page.getByRole('link', { name: /使用组织账号登录/ }).click();
  const session = (await (await page.request.get('/auth/session')).json()) as {
    csrf_token: string;
    user: { issuer: string; subject: string };
  };
  const headers = { Origin: baseURL!, 'X-CSRF-Token': session.csrf_token };
  const orgResponse = await page.request.post('/api/v1/organizations', {
    headers,
    data: { name: '部门验收组织', period: '2026-11', limit_amount: 1000 },
  });
  expect(orgResponse.status()).toBe(201);
  const org = (await orgResponse.json()) as { id: string };
  const orgPath = `/api/v1/organizations/${org.id}`;
  const projectResponse = await page.request.post(`${orgPath}/projects`, {
    headers,
    data: { name: '部门验收项目', limit_amount: 500 },
  });
  expect(projectResponse.status()).toBe(201);
  const project = (await projectResponse.json()) as { id: string };
  expect(
    (
      await page.request.put(`${orgPath}/members`, {
        headers,
        data: { issuer: session.user.issuer, subject: 'department-test-member', role: 'developer' },
      })
    ).status(),
  ).toBe(204);
  await page.goto(`/?org=${org.id}&project=${project.id}&view=departments`);
  await expect(page.getByRole('heading', { name: '部门与项目授权', exact: true })).toBeVisible();
  const prepare = async (reason: string) => {
    await page.getByLabel('部门配置变更原因', { exact: true }).fill(reason);
    await page.getByLabel('我已核查部门继承影响，既有密钥将单独处理', { exact: true }).check();
  };
  await prepare('创建研发根部门');
  await page.getByLabel('部门名称', { exact: true }).fill('研发');
  await page.getByRole('button', { name: '保存部门层级', exact: true }).click();
  await expect(
    page.getByText('组织内的部门树与项目归属 · 配置版本 1', { exact: true }),
  ).toBeVisible();
  await prepare('创建平台子部门');
  await page.getByLabel('部门名称', { exact: true }).fill('平台');
  await page
    .getByRole('combobox', { name: '上级部门', exact: true })
    .selectOption({ label: '研发' });
  await page.getByRole('button', { name: '保存部门层级', exact: true }).click();
  await expect(
    page.getByText('组织内的部门树与项目归属 · 配置版本 2', { exact: true }),
  ).toBeVisible();
  await prepare('授予研发部门开发权限');
  await page
    .getByRole('combobox', { name: '授权部门', exact: true })
    .selectOption({ label: '研发' });
  await page
    .getByRole('combobox', { name: '授权组织成员', exact: true })
    .selectOption({ label: `department-test-member · ${session.user.issuer}` });
  await page.getByRole('combobox', { name: '部门角色', exact: true }).selectOption('developer');
  await page.getByRole('button', { name: '保存部门成员授权', exact: true }).click();
  await expect(
    page
      .getByRole('region', { name: '部门授权表', exact: true })
      .getByRole('row')
      .filter({ hasText: 'department-test-member' }),
  ).toBeVisible();
  await prepare('项目归属平台子部门');
  await page.getByRole('combobox', { name: '归属项目', exact: true }).selectOption(project.id);
  await page
    .getByRole('combobox', { name: '归属部门', exact: true })
    .selectOption({ label: '研发 / 平台' });
  await page.getByRole('button', { name: '保存项目归属', exact: true }).click();
  await expect(
    page.getByText('组织内的部门树与项目归属 · 配置版本 4', { exact: true }),
  ).toBeVisible();
  await page.reload();
  await expect(page.getByText('部门验收项目 → 研发 / 平台', { exact: true })).toBeVisible();
  const configuration = (await (await page.request.get(`${orgPath}/departments`)).json()) as {
    revision: number;
    grants: unknown[];
    projects: { project_id: string }[];
  };
  expect(configuration.revision).toBe(4);
  expect(configuration.grants).toHaveLength(1);
  expect(configuration.projects[0].project_id).toBe(project.id);
  expect(
    (
      await page.request.put(`${orgPath}/departments`, {
        headers,
        data: { configuration, reason: '并发管理员合成验收' },
      })
    ).status(),
  ).toBe(200);
  await prepare('过期版本不能覆盖');
  await page
    .getByRole('button', { name: '移除部门授权 department-test-member 研发', exact: true })
    .click();
  await expect(page.getByRole('alert')).toContainText(
    '记录已变更或当前状态不允许此操作，请刷新核对。',
  );
  await expect(
    page
      .getByRole('region', { name: '部门授权表', exact: true })
      .getByRole('row')
      .filter({ hasText: 'department-test-member' }),
  ).toBeVisible();
  await page.getByRole('button', { name: '刷新部门配置', exact: true }).click();
  await expect(
    page.getByText('组织内的部门树与项目归属 · 配置版本 5', { exact: true }),
  ).toBeVisible();
  await prepare('移除部门授权');
  await page
    .getByRole('button', { name: '移除部门授权 department-test-member 研发', exact: true })
    .click();
  await expect(page.getByText('暂无部门成员授权。', { exact: true })).toBeVisible();
  await expect(page.getByText(/版本 6 ·/)).toContainText('移除部门授权');
  await page.setViewportSize({ width: 390, height: 844 });
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  const accessibility = await new AxeBuilder({ page })
    .withTags(['wcag2a', 'wcag2aa', 'wcag21aa'])
    .analyze();
  expect(
    accessibility.violations.map((v) => ({ id: v.id, nodes: v.nodes.map((n) => n.target) })),
  ).toEqual([]);
});

test('department costs retain admission snapshot and CSV uses applied historical filters', async ({
  page,
  baseURL,
}) => {
  await page.goto('/');
  await page.getByRole('link', { name: /使用组织账号登录/ }).click();
  const session = (await (await page.request.get('/auth/session')).json()) as {
    csrf_token: string;
  };
  const headers = { Origin: baseURL!, 'X-CSRF-Token': session.csrf_token };
  const orgResponse = await page.request.post('/api/v1/organizations', {
    headers,
    data: { name: '部门成本验收', period: '2026-11', currency: 'CNY_MICRO', limit_amount: 10000 },
  });
  expect(orgResponse.status()).toBe(201);
  const org = (await orgResponse.json()) as { id: string };
  const orgPath = `/api/v1/organizations/${org.id}`;
  const projectResponse = await page.request.post(`${orgPath}/projects`, {
    headers,
    data: { name: '部门费用项目', limit_amount: 5000 },
  });
  expect(projectResponse.status()).toBe(201);
  const project = (await projectResponse.json()) as { id: string };
  const path = `${orgPath}/projects/${project.id}`;
  expect(
    (
      await page.request.put(`${path}/models`, { headers, data: { models: ['aliyun/fixture'] } })
    ).status(),
  ).toBe(204);
  const issued = await page.request.post(`${path}/keys`, {
    headers,
    data: { name: '部门成本合成密钥', models: ['aliyun/fixture'], ttl_seconds: 3600 },
  });
  expect(issued.status()).toBe(201);
  const key = (await issued.json()) as { secret: string };
  let configuration = {
    revision: 0,
    departments: [
      { id: 'engineering', name: '研发', parent_id: '' },
      { id: 'platform', name: '平台', parent_id: 'engineering' },
      { id: 'sales', name: '销售', parent_id: '' },
    ],
    grants: [],
    projects: [{ project_id: project.id, department_id: 'platform' }],
  };
  const publish = async () => {
    const result = await page.request.put(`${orgPath}/departments`, {
      headers,
      data: { configuration, reason: '部门成本快照合成验收' },
    });
    expect(result.status()).toBe(200);
    configuration = (await result.json()) as typeof configuration;
  };
  const infer = async () => {
    expect(
      (
        await page.request.post('/v1/chat/completions', {
          headers: { Authorization: `Bearer ${key.secret}` },
          data: {
            model: 'aliyun/fixture',
            max_tokens: 8,
            messages: [{ role: 'user', content: 'synthetic attribution fixture' }],
          },
        })
      ).status(),
    ).toBe(200);
  };
  await publish();
  await infer();
  configuration.departments[0].name = '研发新名称';
  configuration.projects[0].department_id = 'sales';
  await publish();
  await infer();
  await page.goto(`/?view=reports&org=${org.id}&project=${project.id}`);
  const table = page.getByRole('region', { name: '成本明细表', exact: true });
  await expect(
    table.getByRole('cell', { name: '研发 (engineering) / 平台 (platform)', exact: true }),
  ).toBeVisible();
  await expect(table.getByRole('cell', { name: '销售 (sales)', exact: true })).toBeVisible();
  await expect(table.getByText('研发新名称', { exact: false })).toHaveCount(0);
  await page.getByLabel('部门 ID（含历史下级）', { exact: true }).fill('engineering');
  await page.getByRole('button', { name: '查询成本报表', exact: true }).click();
  await expect(table.getByRole('row')).toHaveCount(2);
  await expect(
    table.getByRole('cell', { name: '研发 (engineering) / 平台 (platform)', exact: true }),
  ).toBeVisible();
  // Unsaved form changes must not change the CSV scope.
  await page.getByLabel('部门 ID（含历史下级）', { exact: true }).fill('sales');
  const downloading = page.waitForEvent('download');
  await page.getByRole('button', { name: '导出成本 CSV', exact: true }).click();
  const download = await downloading;
  const stream = await download.createReadStream();
  let csv = '';
  for await (const chunk of stream) csv += String(chunk);
  expect(csv).toContain('department_state,department_path');
  expect(csv).toContain('engineering');
  expect(csv).not.toContain('sales');
  await page.getByLabel('部门 ID（含历史下级）', { exact: true }).fill('');
  await page
    .getByRole('combobox', { name: '部门归属状态', exact: true })
    .selectOption('historical');
  await page.getByRole('button', { name: '查询成本报表', exact: true }).click();
  await expect(page.getByText('所选时间范围没有账本记录。', { exact: true })).toBeVisible();
  await page.setViewportSize({ width: 390, height: 844 });
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  const accessibility = await new AxeBuilder({ page })
    .withTags(['wcag2a', 'wcag2aa', 'wcag21aa'])
    .analyze();
  expect(accessibility.violations.map((v) => v.id)).toEqual([]);
});

test('device enrollment, required policy, DPoP inference and revocation use real APIs', async ({
  page,
  baseURL,
}) => {
  const { generateKeyPairSync, createHash, randomUUID, sign } = await import('node:crypto');
  const pair = generateKeyPairSync('ed25519');
  const jwk = pair.publicKey.export({ format: 'jwk' });
  const publicKey = String(jwk.x);
  const proof = (method: string, path: string, token = '') => {
    const header = Buffer.from(JSON.stringify({ typ: 'dpop+jwt', alg: 'EdDSA', jwk })).toString(
      'base64url',
    );
    const payload = Buffer.from(
      JSON.stringify({
        jti: randomUUID(),
        htm: method,
        htu: baseURL! + path,
        iat: Math.floor(Date.now() / 1000),
        ...(token ? { ath: createHash('sha256').update(token).digest('base64url') } : {}),
      }),
    ).toString('base64url');
    const data = `${header}.${payload}`;
    return `${data}.${sign(null, Buffer.from(data), pair.privateKey).toString('base64url')}`;
  };
  await page.goto('/');
  await page.getByRole('link', { name: /使用组织账号登录/ }).click();
  const session = (await (await page.request.get('/auth/session')).json()) as {
    csrf_token: string;
  };
  const headers = { Origin: baseURL!, 'X-CSRF-Token': session.csrf_token };
  const orgResponse = await page.request.post('/api/v1/organizations', {
    headers,
    data: { name: '终端准入验收', period: '2026-11', currency: 'CNY_MICRO', limit_amount: 10000 },
  });
  expect(orgResponse.status()).toBe(201);
  const org = (await orgResponse.json()) as { id: string };
  const orgPath = `/api/v1/organizations/${org.id}`;
  const pr = await page.request.post(`${orgPath}/projects`, {
    headers,
    data: { name: '设备调用项目', limit_amount: 5000 },
  });
  expect(pr.status()).toBe(201);
  const project = (await pr.json()) as { id: string };
  const projectPath = `${orgPath}/projects/${project.id}`;
  expect(
    (
      await page.request.put(`${projectPath}/models`, {
        headers,
        data: { models: ['aliyun/fixture'] },
      })
    ).status(),
  ).toBe(204);
  const issued = await page.request.post(`${projectPath}/keys`, {
    headers,
    data: { name: '被准入策略阻断的静态密钥', models: ['aliyun/fixture'], ttl_seconds: 3600 },
  });
  expect(issued.status()).toBe(201);
  const oldKey = (await issued.json()) as { secret: string };
  await page.goto(`/?org=${org.id}&project=${project.id}&view=devices`);
  await expect(page.getByRole('heading', { name: '终端基础准入', exact: true })).toBeVisible();
  await page.getByLabel('设备名称', { exact: true }).fill('Ubuntu 设备');
  await page.getByLabel('Ed25519 设备公钥', { exact: true }).fill(publicKey);
  await page.getByRole('button', { name: '登记设备公钥', exact: true }).click();
  await expect(page.getByRole('status').filter({ hasText: '公钥已登记' })).toBeVisible();
  await page.getByRole('combobox', { name: '设备操作', exact: true }).selectOption('active');
  await page.getByLabel('设备操作原因', { exact: true }).fill('合成验收：人工核对公钥');
  await page
    .getByLabel('我已核对设备公钥和所属人员，此确认不代表安全体检', { exact: true })
    .check();
  await page.getByRole('button', { name: '提交设备操作', exact: true }).click();
  await expect(page.getByText(/设备编号 .*已确认.*版本 1/)).toBeVisible();
  const devices = (await (await page.request.get(`${orgPath}/devices`)).json()) as {
    data: { id: string }[];
  };
  expect(devices.data).toHaveLength(1);
  const device = devices.data[0];
  const sessionPath = `${projectPath}/devices/${device.id}/sessions`;
  const tokenResponse = await page.request.post(sessionPath, {
    headers: { ...headers, DPoP: proof('POST', sessionPath) },
    data: { models: ['aliyun/fixture'] },
  });
  expect(tokenResponse.status()).toBe(201);
  const deviceToken = (await tokenResponse.json()) as { token: string };
  await page.getByLabel('要求已确认设备与请求证明', { exact: true }).check();
  await page.getByLabel('准入策略变更原因', { exact: true }).fill('合成验收：强制终端准入');
  await page.getByLabel('我确认开启后原项目密钥将被阻断', { exact: true }).check();
  await page.getByRole('button', { name: '保存项目准入策略', exact: true }).click();
  await expect(
    page.getByText('当前状态：强制设备准入 · 策略版本 1', { exact: true }),
  ).toBeVisible();
  expect(
    (
      await page.request.get('/v1/models', {
        headers: { Authorization: `Bearer ${oldKey.secret}`, 'X-Device-ID': device.id },
      })
    ).status(),
  ).toBe(403);
  expect(
    (
      await page.request.get('/v1/models', {
        headers: { Authorization: `Bearer ${deviceToken.token}` },
      })
    ).status(),
  ).toBe(401);
  const inference = await page.request.post('/v1/chat/completions', {
    headers: {
      Authorization: `DPoP ${deviceToken.token}`,
      DPoP: proof('POST', '/v1/chat/completions', deviceToken.token),
    },
    data: {
      model: 'aliyun/fixture',
      max_tokens: 8,
      messages: [{ role: 'user', content: 'synthetic device admission fixture' }],
    },
  });
  expect(inference.status()).toBe(200);
  const requestID = inference.headers()['x-request-id'];
  await page.getByRole('button', { name: '刷新设备', exact: true }).click();
  await page.reload();
  await page.getByRole('combobox', { name: '选择设备', exact: true }).selectOption(device.id);
  await expect(page.getByRole('link', { name: requestID, exact: true })).toBeVisible();
  await page.setViewportSize({ width: 390, height: 844 });
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  const a11y = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa']).analyze();
  expect(a11y.violations.map((v) => ({ id: v.id, nodes: v.nodes.map((n) => n.target) }))).toEqual(
    [],
  );
  await page.getByRole('combobox', { name: '设备操作', exact: true }).selectOption('revoked');
  await page.getByLabel('设备操作原因', { exact: true }).fill('合成验收：设备丢失');
  await page.getByLabel('我确认永久撤销，恢复账号也不会复活此设备', { exact: true }).check();
  await page.getByRole('button', { name: '提交设备操作', exact: true }).click();
  await expect(
    page.getByText('设备已永久撤销。重新接入须生成新密钥并重新登记，不能复用已撤销公钥。', {
      exact: true,
    }),
  ).toBeVisible();
  expect(
    (
      await page.request.get('/v1/models', {
        headers: {
          Authorization: `DPoP ${deviceToken.token}`,
          DPoP: proof('GET', '/v1/models', deviceToken.token),
        },
      })
    ).status(),
  ).toBe(401);
});

test('system host monitoring and audited network confirmation', async ({ page, baseURL }) => {
  await page.goto('/');
  await page.getByRole('link', { name: /使用组织账号登录/ }).click();
  await page.getByRole('button', { name: '系统管理', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'CPU 使用率', exact: true })).toBeVisible();
  await expect(page.getByText(/节点：browser-fixture/)).toBeVisible();
  const denied = await page.request.post('/api/v1/system/network', {
    headers: { Origin: baseURL! },
    data: { node: 'browser-fixture', reason: 'missing CSRF', change: {} },
  });
  expect(denied.status()).toBe(403);
  await page.getByLabel('IPv4 DNS（逗号分隔）', { exact: true }).fill('192.0.2.53');
  await page.getByLabel('变更原因', { exact: true }).fill('合成网络变更验收');
  await page.getByRole('button', { name: '预览网络变更', exact: true }).click();
  await expect(page.getByRole('heading', { name: '拟应用配置', exact: true })).toBeVisible();
  await page.getByRole('button', { name: '临时应用，等待连通确认', exact: true }).click();
  await expect(page.getByRole('heading', { name: '网络变更待处理' })).toBeVisible();
  await page.getByLabel('确认或回退原因', { exact: true }).fill('合成连通性核验');
  await page.getByRole('button', { name: '确认连通并保存', exact: true }).click();
  await expect(page.getByText('网络配置已确认保存。', { exact: true })).toBeVisible();
  await expect(page.getByLabel('IPv4 DNS（逗号分隔）', { exact: true })).toHaveValue('192.0.2.53');
  await expect(page.getByRole('cell', { name: 'confirm', exact: true }).first()).toBeVisible();
  await page.screenshot({ path: '/tmp/aigw-system-management.png', fullPage: true });
  await page.setViewportSize({ width: 390, height: 844 });
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  const a11y = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa']).analyze();
  expect(a11y.violations.map((v) => ({ id: v.id, nodes: v.nodes.map((n) => n.target) }))).toEqual(
    [],
  );
});
