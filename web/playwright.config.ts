import { defineConfig } from '@playwright/test'

// The suite drives the real binary (serve -no-reconcile) on a scratch data
// directory; ERGW_E2E_URL points at it and ERGW_E2E_PASSWORD_FILE at its
// bootstrap password (see scripts/e2e.sh).
export default defineConfig({
  testDir: './e2e',
  timeout: 30_000,
  retries: 0,
  use: {
    baseURL: process.env.ERGW_E2E_URL ?? 'http://127.0.0.1:18085',
    trace: 'retain-on-failure',
  },
  reporter: [['list']],
})
