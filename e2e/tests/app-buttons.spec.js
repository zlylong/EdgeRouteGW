const { test, expect } = require('@playwright/test');

async function mockDashboardApis(page) {
  const state = {
    importCalls: 0,
    regenerateCalls: 0,
    rollbackCalls: 0,
    checkCalls: 0,
    lastImport: null,
    lastRegenerate: null,
    lastRollback: null,
    lastCheck: null,
    remoteNodes: [
      {
        id: 2,
        name: '192.168.20.152',
        type: 'vless',
        ssh_host: '192.168.20.152',
        region: 'lab',
        status: 'Online',
        remark: 'seed',
        created_at: '2026-04-20 09:53:46'
      }
    ]
  };

  await page.addInitScript(() => {
    localStorage.setItem('token', 'e2e-token');
  });

  await page.route('**/api/**', async route => {
    const req = route.request();
    const url = new URL(req.url());
    const path = url.pathname;
    const method = req.method();

    const json = (body) => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) });

    if (path === '/api/status') return json({ status: 'running', mode: 'B', xray: true, ospf: true, mosdns: true, xrayVersion: '26.3.27', geoVersion: '2026-04-20', mosdnsVersion: 'v5', cpu: '1.0', ram: '10.0', up: '0 MB', down: '0 MB' });
    if (path === '/api/traffic') return json({ speed: { up: 0, down: 0 }, total_24h: { up: 0, down: 0 } });
    if (path === '/api/cron') return json({ enabled: false, time: '04:00' });
    if (path === '/api/dns') return json({ local: '223.5.5.5', remote: '8.8.8.8', lazy: true, mode: 'smart' });
    if (path === '/api/nodes' && method === 'GET') return json([]);
    if (path === '/api/rules') return json([]);
    if (path === '/api/rules/categories') return json({ geosite: [], geoip: [] });
    if (path === '/api/remote_nodes' && method === 'GET') return json(state.remoteNodes);
    if (path === '/api/remote_nodes/2' && method === 'GET') {
      return json({
        id: 2,
        name: '192.168.20.152',
        type: 'vless',
        ssh_host: '192.168.20.152',
        ssh_port: 22,
        region: 'lab',
        status: 'Online',
        remark: 'seed',
        vless: {
          reality_pub: 'pub',
          short_id: '6c4368e699a21562',
          server_name: 'www.microsoft.com',
          dest: 'www.microsoft.com:443',
          port: 21508,
          share_link: 'vless://a64bc5e0-abd8-4015-a904-4ababd2b88ce@192.168.20.152:21508?security=reality&sni=www.microsoft.com&fp=chrome&pbk=pub&sid=6c4368e699a21562&type=tcp&flow=xtls-rprx-vision&encryption=none#192.168.20.152'
        }
      });
    }
    if (path === '/api/remote_nodes/2/history' && method === 'GET') {
      return json([{ id: 1, params: '{"port":21508}', created_at: '2026-04-20 09:53:46' }]);
    }
    if (path === '/api/remote_nodes/2/check' && method === 'POST') {
      state.checkCalls += 1;
      state.lastCheck = { path, method };
      return json({ success: true, status: 'Online' });
    }
    if (path === '/api/remote_nodes/2/regenerate' && method === 'POST') {
      state.regenerateCalls += 1;
      state.lastRegenerate = { path, method };
      return json({ success: true, message: 'Regeneration started' });
    }
    if (path === '/api/remote_nodes/2/rollback' && method === 'POST') {
      state.rollbackCalls += 1;
      state.lastRollback = { path, method, body: req.postDataJSON() };
      return json({ success: true, message: 'Rollback started' });
    }
    if (path === '/api/remote_nodes/2' && method === 'DELETE') {
      state.remoteNodes = [];
      return json({ success: true });
    }
    if (path === '/api/nodes/import' && method === 'POST') {
      state.importCalls += 1;
      state.lastImport = { path, method, body: req.postDataJSON() };
      return json({ success: true });
    }

    return json({ success: true });
  });

  return state;
}

test.describe('remote node button e2e', () => {
  test.beforeEach(async ({ page }) => {
    page.on('dialog', dialog => dialog.accept());
  });

  test('import button imports remote vless share link into gateway list', async ({ page }) => {
    const state = await mockDashboardApis(page);
    await page.goto('/');

    await page.getByText('节点自动部署').click();
    await page.locator('i[title="查看配置"]').first().click();
    await expect(page.getByText('导入至网关节点列表')).toBeVisible();
    await page.getByText('导入至网关节点列表').click();

    await expect(page.getByText('已导入至网关节点列表')).toBeVisible();
    expect(state.importCalls).toBe(1);
    expect(state.lastImport).toEqual({
      path: '/api/nodes/import',
      method: 'POST',
      body: {
        Url: 'vless://a64bc5e0-abd8-4015-a904-4ababd2b88ce@192.168.20.152:21508?security=reality&sni=www.microsoft.com&fp=chrome&pbk=pub&sid=6c4368e699a21562&type=tcp&flow=xtls-rprx-vision&encryption=none#192.168.20.152'
      }
    });
  });

  test('check and regenerate buttons dispatch requests', async ({ page }) => {
    const state = await mockDashboardApis(page);
    await page.goto('/');

    await page.getByText('节点自动部署').click();
    await page.locator('i[title="健康检查"]').first().click();
    await expect(page.getByText('节点状态正常: Online')).toBeVisible();
    expect(state.checkCalls).toBe(1);
    expect(state.lastCheck).toEqual({ path: '/api/remote_nodes/2/check', method: 'POST' });

    await page.locator('i[title="查看配置"]').first().click();
    await page.getByText('重新生成参数 (当前分享链接将作废)').click();
    await expect(page.getByText('重新生成任务已下发')).toBeVisible();
    expect(state.regenerateCalls).toBe(1);
    expect(state.lastRegenerate).toEqual({ path: '/api/remote_nodes/2/regenerate', method: 'POST' });
  });

  test('history rollback button dispatches rollback request', async ({ page }) => {
    const state = await mockDashboardApis(page);
    await page.goto('/');

    await page.getByText('节点自动部署').click();
    await page.locator('i[title="查看配置"]').first().click();
    await page.getByText('查看历史回退版本').click();
    await expect(page.getByText('历史参数版本')).toBeVisible();
    await page.getByText('强行恢复此版本并下发').click();

    await expect(page.getByText('回退任务已下发，正在还原远端服务器')).toBeVisible();
    expect(state.rollbackCalls).toBe(1);
    expect(state.lastRollback).toEqual({
      path: '/api/remote_nodes/2/rollback',
      method: 'POST',
      body: { history_id: 1 }
    });
  });
});
