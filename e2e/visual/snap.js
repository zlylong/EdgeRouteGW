#!/usr/bin/env node
// Visual-regression snapshotter for the dashboard.
//
//   node visual/snap.js <distDir> <outDir> [port]
//
// Serves <distDir> with python's http.server, opens every tab / modal of the
// app in headless Chromium (all /api/** calls mocked, see mock-api.js) and
// writes one PNG per scenario into <outDir>. Compare two runs with compare.js.

const fs = require('fs');
const path = require('path');
const http = require('http');
const { spawn } = require('child_process');
const { chromium } = require('@playwright/test');
const { installMockApi } = require('./mock-api');
const { resolveChromiumExecutable } = require('../browser');

const [distDir, outDir, portArg] = process.argv.slice(2);
if (!distDir || !outDir) {
  console.error('usage: node visual/snap.js <distDir> <outDir> [port]');
  process.exit(2);
}
const port = Number(portArg || 4199);
const baseURL = `http://127.0.0.1:${port}`;

const tab = (label) => async (page) => {
  await page.locator(`nav a:has-text("${label}")`).first().click();
};
const seq = (...steps) => async (page) => { for (const s of steps) await s(page); };
const click = (selector) => async (page) => { await page.locator(selector).first().click(); };
const fill = (selector, value) => async (page) => { await page.locator(selector).first().fill(value); };
const settle = (ms) => async (page) => { await page.waitForTimeout(ms); };

const scenarios = [
  { name: 'login', token: '', run: async () => {} },
  { name: 'system', run: tab('系统状态') },
  { name: 'system-normal-ui', uiMode: 'normal', run: tab('系统状态') },
  { name: 'system-modeA', mode: 'A', run: tab('系统状态') },
  { name: 'connections', run: tab('实时连接追踪') },
  { name: 'nodes', run: tab('节点管理') },
  { name: 'nodes-modeA-strict', mode: 'A', run: tab('节点管理') },
  { name: 'nodes-add-modal', run: seq(tab('节点管理'), click('button:has-text("手动添加")')) },
  { name: 'nodes-delete-confirm', run: seq(tab('节点管理'), click('tr:has-text("n1") button:has-text("删除")')) },
  { name: 'remote-nodes', run: tab('节点自动部署') },
  { name: 'remote-nodes-add-modal', run: seq(tab('节点自动部署'), click('main button:has-text("新增部署")')) },
  { name: 'remote-nodes-batch-modal', run: seq(tab('节点自动部署'), click('main button:has-text("批量导入")')) },
  { name: 'remote-nodes-details-modal', run: seq(tab('节点自动部署'), click('i[title="查看配置"]')) },
  { name: 'remote-nodes-history-modal', run: seq(tab('节点自动部署'), click('i[title="查看配置"]'), click('button:has-text("查看历史回退版本")')) },
  { name: 'rules', run: tab('路由分流规则') },
  { name: 'rules-error-toast', run: seq(tab('路由分流规则'), click('button:has-text("添加规则")')) },
  { name: 'lan-acls', mode: 'A', run: tab('设备分流规则') },
  { name: 'lan-acls-add-modal', mode: 'A', run: seq(tab('设备分流规则'), click('main button:has-text("添加独立设备")')) },
  { name: 'protected-ips', mode: 'A', run: tab('规则变更保护IP') },
  { name: 'dns', run: tab('DNS 设置') },
  { name: 'dns-success-toast', run: seq(tab('DNS 设置'), click('button:has-text("恢复默认推荐值")')) },
  { name: 'ospf', run: tab('OSPF 状态') },
  { name: 'configs-xray', run: tab('配置信息面板') },
  { name: 'configs-mosdns', run: seq(tab('配置信息面板'), click('main button:has-text("Mosdns")')) },
  { name: 'configs-nftables', run: seq(tab('配置信息面板'), click('main button:has-text("nftables")')) },
  { name: 'configs-frr', run: seq(tab('配置信息面板'), click('main button:has-text("FRR")')) },
  { name: 'syslogs-proxygw', run: tab('核心日志面板') },
  { name: 'syslogs-alerts', run: seq(tab('核心日志面板'), click('main button:has-text("告警事件流")')) },
  { name: 'syslogs-nft', run: seq(tab('核心日志面板'), click('main button:has-text("TProxy 命中计数")')) },
  { name: 'test-tools', run: tab('诊断测试工具') },
  {
    name: 'test-tools-results',
    run: seq(
      tab('诊断测试工具'),
      fill('input[placeholder*="测试域名"]', 'example.com'),
      click('button:has-text("追踪")'),
      settle(300),
      fill('input[placeholder*="geosite"]', 'geosite:cn'),
      click('button:has-text("资产查询")'),
      settle(300),
    ),
  },
  { name: 'pwd-modal', run: click('button:has-text("修改密码")') },
  { name: 'xray-rollback-modal', run: seq(tab('系统状态'), click('button:has-text("指定版本")')) },
  { name: 'mosdns-rollback-modal', run: seq(tab('系统状态'), async (page) => { await page.locator('button:has-text("指定版本")').nth(1).click(); }) },
];

function portInUse() {
  return new Promise(resolve => {
    http.get(baseURL + '/', res => { res.resume(); resolve(true); }).on('error', () => resolve(false));
  });
}

async function startServer() {
  // Refuse to run against a server we did not start: a stale process on the same
  // port would silently serve some other directory and invalidate the comparison.
  if (await portInUse()) throw new Error(`port ${port} is already in use; stop that server or pass another port`);
  const proc = spawn('python3', ['-m', 'http.server', String(port), '--directory', path.resolve(distDir), '--bind', '127.0.0.1'], { stdio: 'ignore' });
  return new Promise((resolve, reject) => {
    let exited = false;
    proc.on('exit', code => { exited = true; reject(new Error(`http.server exited early (code ${code})`)); });
    const started = Date.now();
    const probe = () => {
      if (exited) return;
      http.get(baseURL + '/', res => { res.resume(); resolve(proc); })
        .on('error', () => {
          if (Date.now() - started > 15000) return reject(new Error('http.server did not start'));
          setTimeout(probe, 150);
        });
    };
    probe();
  });
}

(async () => {
  fs.mkdirSync(outDir, { recursive: true });
  const server = await startServer();
  const browser = await chromium.launch({ executablePath: resolveChromiumExecutable() });
  let failures = 0;
  let announced = false;
  try {
    for (const sc of scenarios) {
      const context = await browser.newContext({ viewport: { width: 1440, height: 2000 }, deviceScaleFactor: 1, locale: 'zh-CN', timezoneId: 'UTC' });
      const page = await context.newPage();
      page.on('dialog', d => d.dismiss().catch(() => {}));
      const errors = [];
      page.on('pageerror', e => errors.push(String(e)));
      try {
        await installMockApi(page, { mode: sc.mode || 'B', token: sc.token === undefined ? 'e2e-token' : sc.token, uiMode: sc.uiMode || 'advanced' });
        await page.goto(baseURL + '/', { waitUntil: 'networkidle' });
        if (!announced) {
          // Say which build is really being exercised (guards against serving a stale directory).
          const assets = await page.evaluate(() => [...document.scripts].map(s => s.src).concat([...document.styleSheets].map(s => s.href || '')).filter(Boolean).map(u => u.split('/').pop()));
          console.log(`serving ${path.resolve(distDir)} -> ${assets.join(', ')}`);
          announced = true;
        }
        await page.waitForTimeout(500);
        await sc.run(page);
        await page.waitForTimeout(900);
        await page.screenshot({ path: path.join(outDir, sc.name + '.png'), animations: 'disabled', caret: 'hide' });
        console.log(`ok   ${sc.name}${errors.length ? '  (page errors: ' + errors.join(' | ') + ')' : ''}`);
      } catch (e) {
        failures++;
        console.log(`FAIL ${sc.name}: ${e.message.split('\n')[0]}`);
      } finally {
        await context.close();
      }
    }
  } finally {
    await browser.close();
    server.kill();
  }
  process.exit(failures ? 1 : 0);
})();
