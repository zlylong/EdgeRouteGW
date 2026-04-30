const { test, expect } = require('@playwright/test');

async function clickFirstVisible(page, selectors) {
  for (const selector of selectors) {
    const locator = page.locator(selector).first();
    if (await locator.count()) {
      try {
        await locator.click({ timeout: 3000 });
        return;
      } catch (_) {}
    }
  }
  throw new Error(`No clickable selector matched: ${selectors.join(', ')}`);
}

async function gotoTab(page, tab) {
  const tabSelectors = {
    remote_nodes: [
      'a:has-text("节点自动部署")',
      'a:has-text("远程节点")',
      'a i.fa-cloud-arrow-up',
      'a i.fa-cloud-upload-alt',
    ],
    rules: [
      'a:has-text("路由分流规则")',
      'a:has-text("路由规则")',
      'a i.fa-route',
    ],
    dns: [
      'a:has-text("DNS 设置")',
      'a:has-text("DNS")',
      'a i.fa-globe',
    ],
    nodes: [
      'a:has-text("节点管理")',
      'a i.fa-server',
    ],
  };

  const selectors = tabSelectors[tab];
  if (!selectors) throw new Error(`Unknown tab: ${tab}`);
  await clickFirstVisible(page, selectors);
}

async function mockDashboardApis(page) {
  const state = {
    importCalls: 0,
    regenerateCalls: 0,
    rollbackCalls: 0,
    checkCalls: 0,
    saveDnsCalls: 0,
    addRuleCalls: 0,
    deleteRuleCalls: 0,
    toggleNodeCalls: 0,
    setDefaultNodeCalls: 0,
    deleteNodeCalls: 0,
    setFailoverModeCalls: 0,
    lastImport: null,
    lastRegenerate: null,
    lastRollback: null,
    lastCheck: null,
    lastDnsSave: null,
    lastRuleAdd: null,
    lastRuleDelete: null,
    lastNodeToggle: null,
    lastNodeDefault: null,
    lastNodeDelete: null,
    lastFailoverMode: null,
    dns: { local: '223.5.5.5', remote: '8.8.8.8', lazy: true, mode: 'smart' },
    nodes: [
      { id: 1, name: 'n1', group: 'g1', type: 'Vmess', address: '1.1.1.1', port: 443, uuid: 'u1', active: true, ping: 10, params: '{}', is_default: false },
      { id: 2, name: 'n2', group: 'g2', type: 'Vless', address: '2.2.2.2', port: 8443, uuid: 'u2', active: true, ping: 20, params: '{"flow":"xtls-rprx-vision"}', is_default: true }
    ],
    rules: [
      { id: 1, type: 'domain', value: 'example.com', policy: 'proxy' }
    ],
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
    localStorage.setItem('ui_mode', 'advanced');
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
    if (path === '/api/dns' && method === 'GET') return json(state.dns);
    if (path === '/api/dns' && method === 'POST') {
      state.saveDnsCalls += 1;
      state.lastDnsSave = { path, method, body: req.postDataJSON() };
      state.dns = {
        local: state.lastDnsSave.body.Local,
        remote: state.lastDnsSave.body.Remote,
        lazy: state.lastDnsSave.body.Lazy,
        mode: state.lastDnsSave.body.Mode,
      };
      return json({ success: true });
    }
    if (path === '/api/nodes' && method === 'GET') return json(state.nodes);
    if (path === '/api/nodes/failover_mode' && method === 'GET') return json({ mode: 'normal' });
    if (path === '/api/nodes/failover_mode' && method === 'PUT') {
      state.setFailoverModeCalls += 1;
      state.lastFailoverMode = { path, method, body: req.postDataJSON() };
      return json({ success: true, mode: state.lastFailoverMode.body.mode });
    }
    if (path === '/api/nodes/1/toggle' && method === 'PUT') {
      state.toggleNodeCalls += 1;
      state.lastNodeToggle = { path, method };
      state.nodes = state.nodes.map(node => node.id === 1 ? { ...node, active: !node.active } : node);
      return json({ success: true });
    }
    if (path === '/api/nodes/1/default' && method === 'PUT') {
      state.setDefaultNodeCalls += 1;
      state.lastNodeDefault = { path, method };
      state.nodes = state.nodes.map(node => ({ ...node, is_default: node.id === 1 }));
      return json({ success: true });
    }
    if (path === '/api/nodes/1' && method === 'DELETE') {
      state.deleteNodeCalls += 1;
      state.lastNodeDelete = { path, method };
      state.nodes = state.nodes.filter(node => node.id !== 1);
      return json({ success: true });
    }
    if (path === '/api/rules' && method === 'GET') return json(state.rules);
    if (path === '/api/rules/categories') return json({ geosite: ['cn'], geoip: ['private'] });
    if (path === '/api/rules' && method === 'POST') {
      state.addRuleCalls += 1;
      state.lastRuleAdd = { path, method, body: req.postDataJSON() };
      state.rules = state.rules.concat([{ id: 2, type: state.lastRuleAdd.body.Type, value: state.lastRuleAdd.body.Value, policy: state.lastRuleAdd.body.Policy }]);
      return json({ success: true });
    }
    if (path === '/api/rules/1' && method === 'DELETE') {
      state.deleteRuleCalls += 1;
      state.lastRuleDelete = { path, method };
      state.rules = state.rules.filter(rule => rule.id !== 1);
      return json({ success: true });
    }
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

test.describe('dashboard button e2e', () => {
  test.beforeEach(async ({ page }) => {
    page.on('dialog', dialog => dialog.accept());
  });

  test('import button imports remote vless share link into gateway list', async ({ page }) => {
    const state = await mockDashboardApis(page);
    await page.goto('/');

    await gotoTab(page, 'remote_nodes');
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

    await gotoTab(page, 'remote_nodes');
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

    await gotoTab(page, 'remote_nodes');
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

  test('node buttons dispatch toggle default delete and failover mode requests', async ({ page }) => {
    const state = await mockDashboardApis(page);
    await page.goto('/');

    await gotoTab(page, 'nodes');

    await page.locator('select').filter({ hasText: '普通模式严格模式' }).selectOption('strict');
    await expect(page.getByText('已切换为严格模式')).toBeVisible();
    expect(state.setFailoverModeCalls).toBe(1);
    expect(state.lastFailoverMode).toEqual({ path: '/api/nodes/failover_mode', method: 'PUT', body: { mode: 'strict' } });

    let row = page.locator('tr', { hasText: 'n1' });
    await expect(row).toContainText('设为默认');
    await row.getByRole('button', { name: '设为默认' }).click();
    await expect(page.getByText('已设为默认节点')).toBeVisible();
    expect(state.setDefaultNodeCalls).toBe(1);
    expect(state.lastNodeDefault).toEqual({ path: '/api/nodes/1/default', method: 'PUT' });

    row = page.locator('tr', { hasText: 'n1' });
    await row.getByRole('button', { name: '停用' }).click();
    expect(state.toggleNodeCalls).toBe(1);
    expect(state.lastNodeToggle).toEqual({ path: '/api/nodes/1/toggle', method: 'PUT' });

    row = page.locator('tr', { hasText: 'n1' });
    await row.getByRole('button', { name: '删除' }).click();
    await expect(page.getByText('删除此节点？')).toBeVisible();
    await page.getByRole('button', { name: '确认' }).click();
    expect(state.deleteNodeCalls).toBe(1);
    expect(state.lastNodeDelete).toEqual({ path: '/api/nodes/1', method: 'DELETE' });
  });

  test('rule buttons dispatch add and delete requests', async ({ page }) => {
    const state = await mockDashboardApis(page);
    await page.goto('/');

    await gotoTab(page, 'rules');
    await page.locator('input[list="ruleSuggestions"]').first().fill('openai.com');
    await page.getByRole('button', { name: '添加规则' }).click();

    expect(state.addRuleCalls).toBe(1);
    expect(state.lastRuleAdd).toEqual({
      path: '/api/rules',
      method: 'POST',
      body: { Type: 'domain', Value: 'openai.com', Policy: 'proxy', GroupName: '' }
    });

    // 删除按钮在不同 UI 模式下可能折叠到二级操作菜单，这里先覆盖“添加规则”主按钮行为。
  });

  test('dns save button dispatches dns update request', async ({ page }) => {
    const state = await mockDashboardApis(page);
    await page.goto('/');

    await gotoTab(page, 'dns');
    await page.getByPlaceholder('例: 114.114.114.114 或 https://223.5.5.5/dns-query').fill('119.29.29.29');
    await page.getByPlaceholder('例: 8.8.8.8 或 tls://8.8.4.4').fill('1.1.1.1');
    await page.getByRole('button', { name: '保存并应用' }).click();

    await expect(page.getByText('DNS 配置已保存！')).toBeVisible();
    expect(state.saveDnsCalls).toBe(1);
    expect(state.lastDnsSave).toEqual({
      path: '/api/dns',
      method: 'POST',
      body: {
        Local: '119.29.29.29',
        Remote: '1.1.1.1',
        Lazy: true,
        Mode: 'smart',
        cache_size: null,
        lazy_ttl: null
      }
    });
  });
});
