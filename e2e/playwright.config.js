const { defineConfig } = require('@playwright/test');

const port = process.env.PLAYWRIGHT_PORT || '4173';
const baseURL = process.env.PLAYWRIGHT_BASE_URL || `http://127.0.0.1:${port}`;

module.exports = defineConfig({
  testDir: './tests',
  timeout: 30_000,
  expect: { timeout: 8_000 },
  use: {
    baseURL,
    headless: true,
  },
  webServer: {
    command: `python3 -m http.server ${port} --directory ../frontend/dist`,
    url: baseURL,
    reuseExistingServer: true,
    timeout: 30_000,
  },
});
