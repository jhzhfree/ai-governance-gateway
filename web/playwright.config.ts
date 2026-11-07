import { defineConfig } from '@playwright/test';

if (!process.env.AIGW_E2E_BASE_URL)
  throw new Error(
    'Run the integrated Go fixture through scripts/verify.sh with AIGW_VERIFY_WEB=1.',
  );
export default defineConfig({
  testDir: './tests',
  testMatch:
    process.env.AIGW_E2E_AUTH_MODE === 'newapi'
      ? '**/newapi.spec.ts'
      : process.env.AIGW_E2E_AUTH_MODE === 'local'
        ? '**/local.spec.ts'
        : '**/console.spec.ts',
  fullyParallel: false,
  workers: 1,
  retries: 0,
  timeout: 30_000,
  reporter: 'list',
  use: {
    baseURL: process.env.AIGW_E2E_BASE_URL,
    browserName: 'chromium',
    viewport: { width: 1440, height: 1050 },
    trace: 'off',
    screenshot: 'off',
    launchOptions: { executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE },
  },
});
