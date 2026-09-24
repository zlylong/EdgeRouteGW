# EdgeRouteGW API 文档

Base URL: `http://<host>/api`

除 `POST /login` 外，所有接口都需要 `Authorization: Bearer <token>` 头。

## 1. 通用约定

### 响应与错误格式
- 成功响应保持各接口既有形状（列表接口返回顶层数组或 `{rules, groups}` 等信封；变更接口返回 `{"success": true, ...}`）。
- 错误响应统一为：
  ```json
  { "success": false, "error": "<可读信息>", "error_code": "<稳定代码>" }
  ```
  常见 `error_code`：`NOT_FOUND`（对不存在 ID 的删除/更新/切换，HTTP 404）、`BAD_REQUEST`、`INTERNAL_ERROR`、`HIGH_RISK_CONFIRM_REQUIRED`（403，缺少确认头）、`HIGH_RISK_ACTION_BUSY`（409，同类高危操作正在进行）、`DEPLOY_IN_PROGRESS`（409）。
- 5xx 响应不再回显内部错误文本；细节记录在 `journalctl -u proxygw`。

### 分页
列表接口 `/rules`、`/nodes`、`/remote_nodes`、`/lan_acls`、`/protected_ips` 支持可选的 `?limit=&offset=`：
- 不带参数时行为与旧版完全相同；
- 带 `limit` 时响应形状不变，并附带 `X-Total-Count` 响应头（分页前的总数）；`limit` 上限 1000。

### 高危操作确认
`POST /mode`、`/apply`、`/network_config`、`/ospf/settings`、`/ospf/reset_pending`、`/remote_nodes/:id/hostkey` 需要 `X-EdgeRouteGW-Confirm: APPLY` 头或 `?confirm=APPLY`。支持 `?dry_run=1` 返回执行计划。

### 压缩与缓存
JSON 与文本响应在客户端声明 `Accept-Encoding: gzip` 时压缩。`/ui/index.html` 始终 `no-store`；`/ui/libs/*` 缓存 1 小时并支持 304 复验。

## 2. 认证
| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/login` | `{"Password":"..."}` → `{"token":"..."}`。同一来源 IP 连续失败 >5 次后每次延迟 2s，>10 次返回 429；失败与锁定记录到事件日志（`module=auth`）。 |
| POST | `/logout` | 注销当前 token。 |
| POST | `/password` | `{"old_password","new_password"}`；成功后撤销全部会话。 |

## 3. 系统状态
| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/status` | 模式、各服务 active 状态、CPU/内存、版本信息、月流量、网卡角色。版本/OS/提交号等静态信息缓存 60s（组件更新后立即失效）。 |
| GET | `/traffic` | `{speed:{up,down}, total_month:{up,down}, node_ranking:[...]}`。 |
| GET/POST | `/cron` | GeoData 定时更新计划。 |
| GET | `/ospf` | 邻居数、已发布/待发布路由数、最近日志、控制器参数。 |
| POST | `/ospf/settings` | `push_batch_limit`、`push_interval_seconds`、`resolve_workers`、`publish_ip_allowlist`（需确认头）。 |
| POST | `/ospf/reset_pending` | 清空待发布静态路由（需确认头）。 |
| POST | `/mode` | `{"mode":"A|B|C"}` 切换模式（需确认头）。 |
| POST | `/apply` | `{"mosdns":true,"xray":true,"dynamic":true}` 重新生成并下发配置（需确认头）。mosdns 仅在渲染结果变化或服务未运行时重启。 |
| POST | `/network_config` | `{"management_iface","service_iface"}`（需确认头）。 |
| GET | `/config/{xray,mosdns,nftables,frr}` | 以 `text/plain` 返回当前生效配置。 |
| GET | `/events` | 事件日志。参数：`limit`(≤1000)、`offset`、`module`、`level`、`before_id`、`after_id`、`since`（RFC3339 或 `30s/15m/2h/1d`）。返回 `{"success":true,"events":[...]}`，按 id 倒序。 |
| GET | `/logs/:service` | `journalctl` 输出，`service ∈ proxygw|xray|mosdns|frr|nftables`。参数：`lines`(50–2000，默认 200)、`since`、`priority`(0–7 或级别名)、`grep`（仅允许字母数字与 `._:@/=-` 空格）。相同查询 2s 内共享结果。 |
| GET | `/nftables/stats` | prerouting 链计数器与增量。 |
| GET | `/connections` | 最近连接（最多 200 条，`?ip=` 过滤客户端或目标，`?limit=`）。`data` 恒为数组。 |

## 4. 节点
| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/nodes` | 顶层数组；支持分页。 |
| POST | `/nodes` | `{Name,Type,Address,Port,UUID,Params}`。 |
| PUT | `/nodes/:id` | 更新；不存在 → 404。节点变更只重同步该节点的 Xray 出站。 |
| DELETE | `/nodes/:id` | 不存在 → 404。 |
| PUT | `/nodes/:id/toggle` | 启用/停用；不存在 → 404。 |
| PUT | `/nodes/:id/default` | 设为默认；不存在 → 404。 |
| POST | `/nodes/import` | `{"Url":"vless://... | vmess://... | wg://..."}`。 |
| POST | `/nodes/ping` | 后台测速。返回 `{"success":true,"started":N,"completed":bool}`；`?wait=1` 最多等待 5s 完成。 |
| GET/PUT | `/nodes/failover_mode` | `{"mode":"normal|strict"}`。 |

## 5. 分流规则
| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/rules` | `{"rules":[...],"groups":[...]}`；`?group_id=` 过滤；支持分页（只对 `rules` 生效）。 |
| POST | `/rules` | `{Type,Value,Policy,GroupName}`；域名规则支持逗号批量。 |
| DELETE | `/rules/:id` | 不存在 → 404。 |
| PUT | `/rules/reorder` | `{"ids":[...]}`。重排不再重启 mosdns。 |
| PUT/DELETE | `/rules/group/:group_id` | 重命名/删除分组。 |
| GET | `/rules/categories` | 可用 geosite/geoip 标签。 |
| GET | `/geo/query` | `?input=` 为 IP/域名时返回命中标签；为 `geoip:<tag>`/`geosite:<tag>` 时展开。展开默认最多返回 2000 条（`?limit=` 调整），响应含 `count`（全量）与 `truncated`。 |

域名规则语义与 Xray 一致：`c.com` 仅根域；`**.c.com` 根域+任意子域；`*.c.com` 根域+一层子域（仅 Mode A 允许通配）。

## 6. DNS / 设备分流 / 保护 IP
| 方法 | 路径 | 说明 |
|---|---|---|
| GET/POST | `/dns` | `{Local,Remote,Lazy,Mode,log_level,cache_size,lazy_ttl}`。`log_level ∈ debug|info|warn|error`，`Mode ∈ smart`。下发失败时设置自动回滚；仅上游或模式变化时清空域名解析缓存。 |
| GET/POST/DELETE | `/lan_acls`, `/lan_acls/:id` | 设备策略（`type ∈ mac|ip`，`policy ∈ proxy|direct`）。nftables 下发失败时数据库变更回滚。 |
| POST | `/lan_acls/default_policy` | `{"policy":"proxy|direct"}`。 |
| GET/POST/DELETE | `/protected_ips`, `/protected_ips/:id` | 保护 IP（IPv4/CIDR）。 |

## 7. 远程节点部署
| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/remote_nodes` | 顶层数组；支持分页。后台每 5 分钟自动探测一次状态（`Deploying` 中的节点除外）。 |
| POST | `/remote_nodes` | 部署请求会做字段校验（`type ∈ wg|vless`、主机名/IP、端口、用户名、`ssh_auth_type ∈ password|key`、REALITY 覆盖项）。 |
| POST | `/remote_nodes/batch` | 数组，最多 20 个；返回 `{started, failed}`。 |
| GET | `/remote_nodes/:id` | 详情（不含私钥）。 |
| GET | `/remote_nodes/:id/history` | 历史参数版本；`params` 中不再包含 `server_priv/client_priv/reality_priv`（数据库保留用于回滚）。 |
| GET | `/remote_nodes/:id/logs` | `?limit=`（默认 100）返回部署/检查日志 `{"success":true,"logs":[{id,action,status,log_text,created_at}]}`。 |
| POST | `/remote_nodes/:id/check` | 立即探测；`{success,status,reason?}`。SSH 命令 15s 超时。 |
| POST | `/remote_nodes/:id/regenerate` | 重新生成参数并下发；部署进行中 → 409。 |
| POST | `/remote_nodes/:id/rollback` | `{"history_id":N}`；部署进行中 → 409。 |
| POST | `/remote_nodes/:id/hostkey` | 显式更新 SSH 指纹（需确认头）。 |
| DELETE | `/remote_nodes/:id` | 删除并异步清理远端服务（60s 超时）；不存在 → 404。 |

## 8. 组件更新
| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/xray/versions`, `/mosdns/versions` | `{"versions":[...]}`（空时为 `[]`）；上游 API 失败返回 502。 |
| POST | `/update/:component` | `component ∈ xray|mosdns|geodata|rollback_xray|rollback_mosdns`；`{"version":"vX.Y.Z"}`。下载按官方摘要校验，二进制安装前先做 `version` 冒烟测试；同一时间只允许一个更新（否则 409）。 |

## 9. 诊断
| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/test/trace` | `?target=` 模拟分流，返回 `{type,outbound,matched_rule,reason}`。 |
| GET | `/test/health_check` | 全组件健康检查 `{results:[{component,status,details}],mode}`。 |
