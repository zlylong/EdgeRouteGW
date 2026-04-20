const { defineConfig } = require('@playwright/test');

const port = process.env.PLAYWRIGHT_PORT || '4173';
const baseURL = process.env.PLAYWRIGHT_BASE_URL || `http://127.0.0.1:${port}`;

module.exports = defineConfig({
  testDir: './tests',
  timeout: 30000,
  retries: 0,
  use: {
    baseURL,
    headless: true,
    viewport: { width: 1440, height: 960 },
    ignoreHTTPSErrors: true,
  },
  webServer: {
    command: `python3 -m http.server ${port} --directory /root/proxygw_full/frontend/dist`,
    url: baseURL,
    reuseExistingServer: true,
    timeout: 30000,
  },
});
