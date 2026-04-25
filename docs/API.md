# ProxyGW API 文档

Base URL: `http://<host>/api`

## 认证

### POST /login
请求：
```json
{ "password": "admin" }
```
返回：
```json
{ "token": "<dynamic-token>" }
```

说明：token 为后端启动时动态生成。

## 配置查看

- GET `/config/xray`
- GET `/config/mosdns`

## 系统状态

### GET /status
返回关键字段：
- `xray`, `ospf`, `mosdns`（服务状态）
- `mode`（A/B/C）
- `xrayVersion`, `geoVersion`
- `cpu`, `ram`, `up`, `down`

## 模式切换

### POST /mode
请求：
```json
{ "mode": "A" }
```
行为：
- A (全局模式): 停用并清理 OSPF，通过 Nftables 强制接管局域网流量。
- B (纯 Fake-IP): 启用 OSPF 并仅向外发布 `198.18.0.0/16` Fake-IP 网段。
- C (纯 OSPF): 关闭 Fake-IP 并清理 Xray/Mosdns 的假域配置，启用 OSPF 动态向外发布真实 GeoIP 代理网段。

## 节点管理

- GET `/nodes`
- POST `/nodes`
- POST `/nodes/import`（支持 vmess:// 与 vless://）
- POST `/nodes/ping`
- PUT `/nodes/:id/toggle`
- DELETE `/nodes/:id`

## 规则管理

- GET `/rules/categories`
- GET `/rules`
- POST `/rules`
- DELETE `/rules/:id`

### POST /rules（域名规则约束）

当 `type=domain` 时，输入语义如下：
- `c.com` => 仅根域（`full:c.com`）
- `**.c.com` => 根域 + 任意层子域（`domain:c.com`）
- `*.c.com` => 根域 + 零或一层子域（regexp）

模式约束：
- Mode A：支持以上三种输入。
- Mode B/C：拒绝 `*.` / `**.` 通配域名，返回 400。

## DNS

- GET `/dns`
- POST `/dns`

请求示例：
```json
{ "local": "223.5.5.5,114.114.114.114", "remote": "8.8.8.8,1.1.1.1", "lazy": true }
```

- GET `/dns/logs`
- GET `/dns/logs/ws`

## OSPF

- GET `/ospf`

## 组件更新

- GET `/xray/versions`
- POST `/update/geodata`
- POST `/update/xray`
- POST `/update/rollback_xray`

### POST /update/xray
请求（可选 version）：
```json
{ "version": "v26.3.27" }
```
- 不传或传 `latest` => 最新版
- 自定义版本需通过白名单正则：`^v[0-9A-Za-z._-]+$`

## 一键应用

- POST `/apply`
