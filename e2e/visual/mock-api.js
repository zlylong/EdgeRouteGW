// API mock used by the visual-regression harness (visual/snap.js).
// It mirrors the mock in tests/app-buttons.spec.js and adds data for the
// tabs that spec never visits, so every template branch gets rendered.

function buildState(mode) {
  return {
    mode,
    dns: { local: '223.5.5.5', remote: '8.8.8.8', lazy: true, mode: 'smart', log_level: 'info', cache_size: 10240, lazy_ttl: 86400 },
    nodes: [
      { id: 1, name: 'n1', group: 'g1', type: 'Vmess', address: '1.1.1.1', port: 443, uuid: 'u1', active: true, ping: 10, params: '{}', is_default: false },
      { id: 2, name: 'n2', group: 'g2', type: 'Vless', address: '2.2.2.2', port: 8443, uuid: 'u2', active: true, ping: 220, params: '{"flow":"xtls-rprx-vision"}', is_default: true },
      { id: 3, name: 'n3', group: 'g3', type: 'Wireguard', address: '3.3.3.3', port: 51820, uuid: 'u3', active: false, ping: 0, params: '{}', is_default: false },
    ],
    rules: [
      { id: 1, type: 'domain', value: 'example.com', policy: 'proxy', group_id: 'group-000000000001' },
      { id: 2, type: 'geosite', value: 'cn', policy: 'direct' },
      { id: 3, type: 'ip', value: '1.1.1.1/32', policy: 'block' },
      { id: 4, type: 'domain', value: '**.example.org', policy: 'proxy-2' },
      { id: 5, type: 'domain', value: '*.example.net', policy: 'ha-1-2' },
    ],
    groups: [{ group_id: 'group-000000000001', group_name: 'demo-group', rule_count: 1 }],
    remoteNodes: [
      { id: 2, name: '192.168.20.152', type: 'vless', ssh_host: '192.168.20.152', region: 'lab', status: 'Online', remark: 'seed', created_at: '2026-04-20 09:53:46' },
      { id: 3, name: 'wg-node', type: 'wg', ssh_host: '192.168.20.153', region: 'lab', status: 'Deploying', remark: '', created_at: '2026-04-20 09:53:46' },
      { id: 4, name: 'dead-node', type: 'vless', ssh_host: '192.168.20.154', region: 'lab', status: 'Failed', remark: 'x', created_at: '2026-04-20 09:53:46' },
      { id: 5, name: 'new-node', type: 'vless', ssh_host: '192.168.20.155', region: '', status: '', remark: '', created_at: '2026-04-20 09:53:46' },
    ],
  };
}

const CONFIG_TEXT = {
  xray: '{\n  "log": { "loglevel": "warning" },\n  "outbounds": [ { "tag": "proxy" } ]\n}',
  mosdns: 'log:\n  level: info\nplugins:\n  - tag: cache\n',
  nftables: 'table inet proxygw {\n  chain prerouting {\n    type filter hook prerouting priority mangle;\n  }\n}\n',
  frr: 'router ospf\n ospf router-id 10.0.0.1\n network 10.0.0.0/24 area 0\n',
};

async function installMockApi(page, { mode = 'B', token = 'e2e-token', uiMode = 'advanced' } = {}) {
  const state = buildState(mode);

  await page.addInitScript(({ token, uiMode }) => {
    if (token) localStorage.setItem('token', token); else localStorage.removeItem('token');
    localStorage.setItem('ui_mode', uiMode);
  }, { token, uiMode });

  await page.route('**/api/**', async route => {
    const req = route.request();
    const url = new URL(req.url());
    const path = url.pathname;
    const method = req.method();
    const json = (body) => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) });
    const text = (body) => route.fulfill({ status: 200, contentType: 'text/plain', body });

    if (path === '/api/status') return json({
      status: 'running', mode: state.mode, xray: true, ospf: true, mosdns: false,
      cpu: '12.5', ram: '43.1', up: '1.2 MB', down: '3.4 MB', geo: '2026-04-20',
      xrayVersion: '26.3.27', geoVersion: '2026-04-20', mosdnsVersion: 'v5.3.3', frrVersion: '10.2',
      appVersion: 'v1.8.1', osVersion: 'Debian 12', commit: 'abcdef1', binary_build_time: '2026-04-20T09:00:00Z',
      interface_options: [
        { name: 'eth0', ipv4: '192.168.1.2', subnet: '192.168.1.0/24' },
        { name: 'eth1', ipv4: '10.0.0.2', subnet: '10.0.0.0/24' },
      ],
      management_network: { iface: 'eth0', ip: '192.168.1.2', subnet: '192.168.1.0/24' },
      service_network: { iface: 'eth1', ip: '10.0.0.2', subnet: '10.0.0.0/24' },
    });
    if (path === '/api/traffic') return json({
      speed: { up: 1024, down: 2048000 },
      total_month: { up: 1073741824, down: 5368709120 },
      node_ranking: [
        { node_id: 1, node_name: 'n1', up: 1048576, down: 10485760, total_bytes: 11534336 },
        { node_id: 2, node_name: 'n2', up: 524288, down: 2097152, total_bytes: 2621440 },
      ],
    });
    if (path === '/api/cron') return json({ enabled: true, time: '04:00', schedule_type: 'weekly', weekday: 2, monthday: 1 });
    if (path === '/api/dns' && method === 'GET') return json(state.dns);
    if (path === '/api/nodes' && method === 'GET') return json(state.nodes);
    if (path === '/api/nodes/failover_mode' && method === 'GET') return json({ mode: state.mode === 'A' ? 'strict' : 'normal' });
    if (path === '/api/rules' && method === 'GET') return json({ rules: state.rules, groups: state.groups });
    if (path === '/api/rules/categories') return json({ geosite: ['cn', 'gfw', 'google'], geoip: ['cn', 'private', 'us'] });
    if (path === '/api/remote_nodes' && method === 'GET') return json(state.remoteNodes);
    if (path === '/api/remote_nodes/2' && method === 'GET') return json({
      id: 2, name: '192.168.20.152', type: 'vless', ssh_host: '192.168.20.152', ssh_port: 22, region: 'lab', status: 'Online', remark: 'seed',
      vless: {
        reality_pub: 'pub', short_id: '6c4368e699a21562', server_name: 'www.microsoft.com', dest: 'www.microsoft.com:443', port: 21508,
        share_link: 'vless://a64bc5e0-abd8-4015-a904-4ababd2b88ce@192.168.20.152:21508?security=reality&sni=www.microsoft.com&fp=chrome&pbk=pub&sid=6c4368e699a21562&type=tcp&flow=xtls-rprx-vision&encryption=none#192.168.20.152',
      },
    });
    if (path === '/api/remote_nodes/2/history') return json([
      { id: 1, params: '{"port":21508}', created_at: '2026-04-20 09:53:46' },
      { id: 2, params: '{"port":21509}', created_at: '2026-04-21 09:53:46' },
    ]);
    if (path === '/api/lan_acls' && method === 'GET') return json({
      default_policy: 'proxy',
      acls: [
        { id: 1, type: 'mac', value: 'aa:bb:cc:dd:ee:ff', policy: 'proxy' },
        { id: 2, type: 'ip', value: '192.168.1.20', policy: 'direct' },
        { id: 3, type: 'ip', value: '192.168.1.30', policy: 'reject' },
      ],
    });
    if (path === '/api/protected_ips' && method === 'GET') return json({
      items: [
        { id: 1, value: '10.0.0.1/32', remark: 'router', created_at: '2026-04-20 09:53:46' },
        { id: 2, value: '10.0.0.0/24', remark: '', created_at: '2026-04-20 09:53:46' },
      ],
    });
    if (path === '/api/connections') return json({
      success: true,
      data: [
        { client: '192.168.1.10', network: 'tcp', target: 'example.com:443', policy: 'proxy', rule_id: 1, rule_type: 'domain', match_value: 'example.com', time: '2026-04-20 09:53:46' },
        { client: '192.168.1.11', network: 'udp', target: '8.8.8.8:53', policy: 'direct', unmatched_reason: 'no rule matched', time: '2026-04-20 09:53:47' },
      ],
    });
    if (path === '/api/ospf') return json({
      neighbors: 2, published: 120, pending: 3,
      logs: ['[ADD] 1.1.1.0/24 via candidate', '[DEL] 2.2.2.0/24 expired', '[INFO] sync completed'],
      push_batch_limit: 500, push_interval_seconds: 10, resolve_workers: 16,
    });
    if (path.startsWith('/api/config/')) return text(CONFIG_TEXT[path.split('/').pop()] || '# empty');
    if (path.startsWith('/api/logs/')) return json({ success: true, logs: `[${path.split('/').pop()}] line 1\n[${path.split('/').pop()}] line 2\n` });
    if (path === '/api/events') return json({
      success: true,
      events: [
        { ts: '2026-04-20 09:53:46', level: 'warn', module: 'xray', event_type: 'restart', message: 'service restarted', trace_id: 'trace-1', path: '/api/apply', status: 200, duration_ms: 12, details: 'ok' },
        { ts: '2026-04-20 09:53:47', level: 'info', module: 'mosdns', event_type: 'reload', message: 'config reloaded' },
      ],
    });
    if (path === '/api/nftables/stats') return json({
      success: true, updated_at: '2026-04-20 09:53:46',
      summary: { proxy_total: { packets: 10, bytes: 2000 }, direct_total: { packets: 20, bytes: 4000 }, proxy_delta: { packets: 1, bytes: 200 }, direct_delta: { packets: 2, bytes: 400 } },
      counters: [{ name: 'proxy_tcp', packets: 5, bytes: 1000 }, { name: 'direct_udp', packets: 6, bytes: 1200 }],
    });
    if (path === '/api/test/health_check') return json({
      mode: state.mode,
      results: [
        { component: 'Xray', status: 'OK', details: 'listening on 12345' },
        { component: 'Mosdns', status: 'Warn', details: 'cache disabled' },
        { component: 'FRR', status: 'Fail', details: 'ospfd not running' },
      ],
    });
    if (path === '/api/test/trace') return json({
      type: 'domain', outbound: 'proxy', reason: 'matched rule #1',
      matched_rule: { id: 1, type: 'domain', value: 'example.com', policy: 'proxy' },
    });
    if (path.startsWith('/api/geo/query')) return json({
      input: 'geosite:cn', mode: 'tag', exists: true, count: 2,
      values: ['a.example.cn', 'b.example.cn'], resolved_ips: ['1.2.3.4'], geoip_matches: ['cn'], geosite_matches: ['cn'],
    });
    if (path === '/api/xray/versions' || path === '/api/mosdns/versions') return json({ versions: ['v1.0.0', 'v1.1.0'] });

    return json({ success: true });
  });

  return state;
}

module.exports = { installMockApi };
