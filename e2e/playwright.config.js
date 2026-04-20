const { defineConfig } = require('@playwright/test');

module.exports = defineConfig({
  testDir: './tests',
  timeout: 30000,
  retries: 0,
  use: {
    baseURL: process.env.PLAYWRIGHT_BASE_URL || 'http://127.0.0.1',
    headless: true,
    viewport: { width: 1440, height: 960 },
    ignoreHTTPSErrors: true,
  },
});
