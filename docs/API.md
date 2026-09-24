# EdgeRouteGW API 文档

Base URL: `http://<host>/api`

除 `POST /login` 外，所有接口都需要 `Authorization: Bearer <token>` 头；缺失或过期返回 `401 {"error":"Unauthorized"}`。

## 1. 通用约定

### 响应与错误格式
- 成功响应保持各接口既有形状：列表接口返回顶层数组或 `{rules, groups}` 之类的信封，变更接口返回 `{"success": true, ...}`。
- **标准错误信封**用于本次新增/改造的接口、所有 404 / 409 / 403（高危确认）以及全部 5xx：
  ```json
  { "success": false, "error": "<可读信息>", "error_code": "<稳定代码>" }
  ```
  `error_code` 取值：`NOT_FOUND`、`BAD_REQUEST`、`INTERNAL_ERROR`、`HIGH_RISK_CONFIRM_REQUIRED`（403）、`HIGH_RISK_ACTION_BUSY`（409）、`DEPLOY_IN_PROGRESS`（409）。
- 其余较早的接口在 4xx 时仍只返回 `{"error": "..."}`（部分附 `success:false`），没有 `error_code`；`/config/*` 的错误是 `text/plain`。客户端应始终以 `error` 键为准。
- 5xx 响应不回显内部错误文本（固定文案），细节见 `journalctl -u proxygw`。

### 分页
`/rules`、`/nodes`、`/remote_nodes`、`/lan_acls`（分页 `acls`）、`/protected_ips`（分页 `items`）支持可选的 `?limit=&offset=`：
- 不带参数时行为与旧版完全相同；
- 带 `limit` **或** `offset` 时响应形状不变，并附带 `X-Total-Count` 头（分页前的总数）；
- `limit` 上限 1000（超出被截断，不报错）；非正数或非数字的 `limit`、负数 `offset` 返回 400 `BAD_REQUEST`。

### 高危操作确认与互斥
- 需要 `X-EdgeRouteGW-Confirm: APPLY` 头或 `?confirm=APPLY`：`POST /mode`、`/apply`、`/network_config`、`/ospf/settings`、`/ospf/reset_pending`、`/remote_nodes/:id/hostkey`。缺失返回 403 `HIGH_RISK_CONFIRM_REQUIRED`（响应含 `action`、`path`、`hint`）。
- 前五个支持 `?dry_run=1` 返回执行计划而不落地；`hostkey` 没有 dry run。
- 互斥锁：`/mode`、`/apply`、`/network_config` 共用 `config_writers` 锁组（任一进行中，其余返回 409 `HIGH_RISK_ACTION_BUSY`）；`/ospf/settings` 与 `/ospf/reset_pending` 各自独占；`POST /update/:component` **只有互斥锁、不需要确认头**。

### 压缩与缓存
- 客户端声明 `Accept-Encoding: gzip` 时，JSON 与文本响应压缩；`Content-Length` 小于 1KB、带 `Range` 头或 WebSocket 升级的请求不压缩。
- `/ui`、`/ui/`、`/ui/index.html` 始终 `no-store`；其余 `/ui/*` 与 `/favicon.ico` 为 `public, max-age=3600` 并支持 304 复验（升级后请强制刷新浏览器）。
- 所有响应携带 `X-Content-Type-Options: nosniff`、`X-Frame-Options: DENY`、`Referrer-Policy`、`Permissions-Policy` 与 report-only 的 CSP。

## 2. 认证
| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/login` | 请求 `{"Password":"..."}`，响应 `{"token":"..."}`。限速按来源 IP 计数（400 也计数）：第 7–11 次尝试各延迟 2s，第 12 次起返回 429，窗口 30 分钟（滑动）；成功登录清零。失败与锁定记录到事件日志（`module=auth`，`login_failed` / `login_locked_out`，每 IP 10s 节流一条）。 |
| POST | `/logout` | 注销当前 token。 |
| POST | `/password` | 请求 `{"Old":"...","New":"..."}`（键名大小写不敏感）。新密码至少 8 位；旧密码错误 401；成功后撤销全部会话（包括当前）。 |

会话 token 存于内存，后端重启后全部失效；有效期 24 小时，每小时最多滑动续期一次。

## 3. 系统状态与设置
| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/status` | 键：`status, mode, xray, ospf, mosdns, xrayVersion, frrVersion, osVersion, appVersion, geoVersion, mosdnsVersion, cpu, ram, up, down, interface_options[{name,ipv4,subnet}], management_network{iface,ip,subnet}, service_network{...}, commit, binary_build_time`。版本/OS/提交号缓存 60s（组件更新后立即失效，FRR 启停时刷新）；`geoVersion` 不缓存。 |
| GET | `/traffic` | `{speed:{up,down}, total_month:{up,down}, node_ranking:[{node_id,node_name,up,down,total_bytes}]}`（前 10 名）。 |
| GET/POST | `/cron` | 字段 `enabled, time("HH:MM")、schedule_type(daily|weekly|monthly)、weekday、monthday`（也接受旧的 PascalCase 键）。POST 响应回显保存值加 `success`。 |
| GET | `/ospf` | `neighbors, published, pending, logs[], push_batch_limit, push_interval_seconds, resolve_workers, publish_ip_allowlist, publish_ip_allowlist_on`。 |
| POST | `/ospf/settings` | `{push_batch_limit, push_interval_seconds, resolve_workers, publish_ip_allowlist}`（严格 JSON，数值被夹到合法范围，allowlist 为逗号分隔的 IPv4 CIDR）。需确认头。 |
| POST | `/ospf/reset_pending` | 清空待发布静态路由；响应 `{success, deleted_pending, published, pending}`。需确认头。 |
| POST | `/mode` | `{"Mode":"A|B|C"}`（严格 JSON，键名大小写不敏感，多余键 400）。失败时已回滚并返回 500 标准信封。需确认头。 |
| POST | `/apply` | `{"mosdns":true,"xray":true,"dynamic_xray":true}`（严格 JSON，可为空体，缺省全为 true）。`dynamic_xray=true` 时 Xray 走热更新（`api adrules/rmo/ado`），仅在热更新失败或显式 `false` 时重启 Xray；mosdns 仅在渲染结果变化或服务未运行时重启。需确认头。 |
| POST | `/network_config` | `{"management_iface","service_iface"}` 两者必填且须为存在的私网 IPv4 接口（严格 JSON）。需确认头。 |
| GET | `/config/{xray,mosdns,nftables,frr}` | 以 `text/plain` 返回当前生效配置；失败为纯文本 500。 |
| GET | `/events` | 事件日志 `{"success":true,"events":[...]}`，按 id 倒序。参数：`limit`（默认 200，最大 1000，非法值忽略）、`offset`、`module`、`level`、`before_id`、`after_id`、`since`（RFC3339 或 `^\d{1,6}[smhd]$`，如 `15m`/`2h`/`1d`）。非法 `offset/before_id/after_id/since` 返回 400。 |
| GET | `/logs/:service` | `service ∈ proxygw|xray|mosdns|frr|nftables`，响应 `{"success":true,"logs":"<多行字符串>"}`。参数：`lines`（50–2000，默认 200，非整数 400）、`since`（同 `/events`）、`priority`（`0–7` 或 `emerg|alert|crit|err|warning|notice|info|debug`）、`grep`（`^[A-Za-z0-9 ._:@/=-]{1,64}$`，作为 argv 传给 `journalctl --grep`）。相同查询 2s 内共享结果。 |
| GET | `/nftables/stats` | `{success, updated_at, counters[{name,packets,bytes}], deltas[...], summary{proxy_total,direct_total,proxy_delta,direct_delta}}`。 |
| GET | `/connections` | `{success, data:[...]}`，`data` 恒为数组，最多 200 条（内存环）。`?ip=` 对客户端与目标做大小写不敏感的子串匹配；`?limit=` 仅在 0 < n < 200 时生效。 |

## 4. 节点
| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/nodes` | 顶层数组 `{id,name,group,type,address,port,uuid,active,ping,params,is_default}`；支持分页。 |
| POST | `/nodes` | `{Name, Group, Type, Address, Port, UUID, Params}`（`Params` 为 JSON 字符串，缺省 `"{}"`）。 |
| PUT | `/nodes/:id` | 同上字段；`Port ≤ 0` 时默认 443。不存在 → 404 `NOT_FOUND`。只重同步该节点的 Xray 出站。 |
| DELETE | `/nodes/:id` | 不存在 → 404。只移除该节点的出站。 |
| PUT | `/nodes/:id/toggle` | 启用/停用（非幂等，每次翻转）；不存在 → 404。只重同步该节点的出站。 |
| PUT | `/nodes/:id/default` | 设为默认节点；不存在 → 404。触发全量出站重同步。 |
| POST | `/nodes/import` | `{"Url":"vmess://... | vless://... | wireguard://..."}`；不支持的 scheme 返回 400 `Unsupported URL format`。 |
| POST | `/nodes/ping` | 后台并发测速；响应 `{"success":true,"started":N,"completed":bool}`。`?wait=1`（或 `true`）最多等待 5s 完成。 |
| GET/PUT | `/nodes/failover_mode` | GET `{mode}`；PUT `{"mode":"normal|strict"}` → `{success, mode}`。 |

## 5. 分流规则
| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/rules` | `{"rules":[{id,type,value,policy,priority,group_id,group_name}],"groups":[{group_id,group_name,rule_count}]}`；`?group_id=` 过滤；分页只作用于 `rules`。 |
| POST | `/rules` | `{Type, Value, Policy, GroupName}`。`Type ∈ domain|geosite|geoip|geolocation|ip`；`Policy ∈ direct|block|proxy|proxy-<节点id>|ha-<主id>-<备id>`；域名支持逗号批量，`GroupName` 仅在批量条数 >1 时生效。响应 `{success, count, group_id, group_name}`。重复的 `type+value` → 409；`*.`/`**.` 通配仅 Mode A 允许。 |
| DELETE | `/rules/:id` | 不存在 → 404 `NOT_FOUND`。 |
| PUT | `/rules/reorder` | `{"ids":[...]}`（正整数、不重复）。会重新渲染 mosdns 域名集，但仅当内容变化或 mosdns 未运行时才重启它。 |
| PUT | `/rules/group/:group_id` | `{"group_name":"..."}` → `{success, group_id, group_name}`；组不存在 → 404（无 `error_code`）。 |
| DELETE | `/rules/group/:group_id` | → `{success, deleted}`；组不存在 → 404（无 `error_code`）。 |
| GET | `/rules/categories` | `{geosite:[...], geoip:[...]}`（`geoip` 含 `!cn`）。 |
| GET | `/geo/query` | `?input=` 必填（缺失 400）。输入为 `geoip:<tag>` / `geosite:<tag>` 时展开：`{mode:"expand", query_type, input, rule, exists, count, truncated, values}`，默认最多返回 2000 条（`?limit=` 调整，非法值忽略），`count` 始终为全量。输入为 IP/域名时查询命中：`{mode:"lookup", query_type:"ip"|"domain", input, resolved_ips, geoip_matches, geosite_matches}`。 |

域名规则语义与 Xray 一致：`c.com` 仅根域；`**.c.com` 根域+任意子域；`*.c.com` 根域+一层子域。

## 6. DNS / 设备分流 / 保护 IP
| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/dns` | `{local, remote, lazy(bool), mode, log_level, cache_size, lazy_ttl, hint}`。 |
| POST | `/dns` | `{Local, Remote, Lazy, Mode, log_level, cache_size, lazy_ttl}`。`Local`/`Remote` 必填且为合法上游列表；`Mode ∈ smart`（空视为 smart）；`log_level ∈ debug|info|warn|error`；`cache_size` 0–10,000,000、`lazy_ttl` 0–2,592,000（0 表示保持原值）。mosdns 下发失败时设置自动回滚并返回 500；仅当上游或模式变化时清空域名解析缓存。 |
| GET | `/lan_acls` | `{acls:[{id,type,value,policy,remark,created_at}], default_policy}`；支持分页。 |
| POST | `/lan_acls` | `{type("mac"|"ip"), value, policy("proxy"|"direct"), remark}`。nftables 下发失败时删除刚插入的记录并返回 500。 |
| DELETE | `/lan_acls/:id` | 不存在 → 404；nftables 下发失败时恢复记录。（没有 `GET /lan_acls/:id`。） |
| POST | `/lan_acls/default_policy` | `{"policy":"proxy|direct"}`；下发失败时恢复原策略。 |
| GET | `/protected_ips` | `{items:[{id,value,remark,created_at}]}`；支持分页。 |
| POST | `/protected_ips` | `{value, remark}`；`value` 为 IPv4 或 IPv4 CIDR（单 IP 归一化为 `/32`）；重复 → 409；下发失败回滚。 |
| DELETE | `/protected_ips/:id` | 不存在 → 404；下发失败恢复记录。 |

## 7. 远程节点部署
| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/remote_nodes` | 顶层数组 `{id,name,type,ssh_host,region,status,remark,created_at}`；支持分页。后台在启动 1 分钟后、之后每 5 分钟自动探测一次状态（`Deploying` 中的节点除外）。 |
| POST | `/remote_nodes` | `{name, type("wg"|"vless"), ssh_host, ssh_port(默认 22), ssh_user(默认 root), ssh_auth_type("password"|"key"), ssh_credential(必填), ssh_host_key, region, remark, port, server_name, dest}`；后三项为 REALITY 覆盖，仅 vless 校验。字段非法 400。响应 `{success, message, id}`。 |
| POST | `/remote_nodes/batch` | 上述对象的数组，最多 20 个；任一项非法整批 400。响应 `{success, started, failed, message}`。 |
| GET | `/remote_nodes/:id` | 基本信息 + `vless{reality_pub, short_id, server_name, dest, port, share_link}` 或 `wg{server_pub, client_pub, endpoint, port, tunnel_addr, client_addr, share_link}`。服务端私钥与 REALITY 私钥不返回；**WG 分享链接按协议要求包含客户端私钥**（供客户端导入）。不存在 → 404。 |
| GET | `/remote_nodes/:id/history` | 顶层数组 `{id, params(JSON 字符串), created_at}`；`params` 中不含 `server_priv/client_priv/reality_priv`（数据库保留用于回滚）。未知节点返回 `[]`。 |
| GET | `/remote_nodes/:id/logs` | `?limit=`（默认 100；非法或 >500 时回退为 100）→ `{"success":true,"logs":[{id,action,status,log_text,created_at}]}`，新到旧。非数字 id → 400，不存在 → 404。 |
| POST | `/remote_nodes/:id/check` | 立即探测：`{success:true,status}` 或 SSH 失败时 `{success:false,status:"Offline",reason}`（HTTP 仍为 200）；远端命令 15s 超时。不存在 → 404。 |
| POST | `/remote_nodes/:id/regenerate` | 重新生成参数并下发；部署进行中 → 409 `DEPLOY_IN_PROGRESS`；非数字 id → 400。 |
| POST | `/remote_nodes/:id/rollback` | `{"history_id":N}`；历史记录不存在 → 404；记录不可解析 → 422；部署进行中 → 409。 |
| POST | `/remote_nodes/:id/hostkey` | `{"ssh_host_key":"SHA256:<43-44 位 base64>"}` → `{success, ssh_host_key}`。需确认头。 |
| DELETE | `/remote_nodes/:id` | 删除记录并异步清理远端服务（60s 超时，结果写入日志）；不存在 → 404。 |

## 8. 组件更新
| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/xray/versions`, `/mosdns/versions` | `{"versions":[...]}`（空时为 `[]`）；GitHub API 失败返回 502。 |
| POST | `/update/:component` | `component ∈ xray|mosdns|geodata|rollback_xray|rollback_mosdns`。请求 `{"version":"vX.Y.Z"}`（严格 JSON，可为空体；仅 xray/mosdns 读取，空表示最新；mosdns 无法解析最新版本时 502）。下载按官方摘要校验，解压出的二进制先执行 `version` 冒烟测试再安装，失败回滚 `.bak`。同一时间只允许一个 HTTP 更新请求（否则 409）；定时 GeoData 更新与启动自愈不经过此锁。未知组件 400。 |

## 9. 诊断
| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/test/trace` | `?target=`（必填）模拟分流 → `{target, type, outbound, matched_rule{id,type,value,policy}, reason}`。 |
| GET | `/test/health_check` | `{success, results:[{component,status,details}], mode}`。 |

## 10. 非 API 路径
`GET /` 重定向到 `/ui/`；`/ui/*` 为静态前端；`/favicon.ico`。
