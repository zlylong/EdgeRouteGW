# EdgeRouteGW API v2(M1 快照)

Base URL:`http://<gateway>/api/v2`。所有请求与响应均为 JSON(`/artifacts/*` 与 SSE 除外)。

## 鉴权

- 浏览器:`POST /auth/login {"password"}` 设置 `ergw_session`(HttpOnly, SameSite=Strict)与 `ergw_csrf` 两个 cookie;之后所有非 GET 请求必须带 `X-CSRF-Token` 头且与 `ergw_csrf` cookie 一致,否则 403 `CSRF`。空闲 24h / 绝对 7 天过期。
- 自动化:`Authorization: Bearer ergw_…`,token 在「系统 → 安全」创建,明文只显示一次;`scope=read` 只能发 GET,否则 403 `READ_ONLY`。
- 未登录访问 API 一律 401 `UNAUTHENTICATED`;`/auth/me` 与 `/auth/login` 例外。
- 首次启动生成随机密码写入 `<data-dir>/bootstrap_password.txt`(0600),`/auth/me.must_change_password=true` 直到改密,改密后文件删除、其它会话注销。
- 登录限速:同一来源 5 次失败后每次延迟 2s,10 次后锁 15 分钟(429)。

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/auth/login` | `{"password"}` → `MeResponse`(含 `csrf_token`) |
| POST | `/auth/logout` | 注销当前会话 |
| GET | `/auth/me` | `{authenticated, kind, name, scope, must_change_password, csrf_token}` |
| POST | `/auth/password` | `{old_password, new_password}`(≥10 字符),仅浏览器会话 |
| GET / DELETE | `/auth/sessions`, `/auth/sessions/{idPrefix}` | 会话列表 / 注销某会话 |
| GET / POST / DELETE | `/auth/tokens`, `/auth/tokens/{id}` | token 列表 / 创建 `{name, scope, expires_in("720h")}` / 撤销 |

## 观测

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/trace?target=` | 评估域名或 IP 会命中哪条规则、走哪个出站(Go 侧按渲染语义评估;Xray TestRoute 将在 gRPC 读侧接入后替换) |
| GET | `/connections?limit=&q=` | 最近连接(来自 Xray access log,环形缓冲 2000 条);SSE 同时推送 `connection` 事件 |

## 约定

- 列表:`{"items":[...]}`;单个:`{"item":{...}}`;写操作附带 `"generation": N`(期望状态版本号)。
- 错误:`{"error":{"code","message","details"}}`。`code` 取值:`BAD_REQUEST`、`VALIDATION_FAILED`(422,`details.field` 指出字段)、`NOT_FOUND`(404)、`CONFLICT`(409,唯一约束/外键)、`METHOD_NOT_ALLOWED`(405,带 `Allow` 头)、`INTERNAL_ERROR`(500)。
- 请求体严格解析:未知字段、尾部多余数据都是 422。
- 每个写操作在一个事务内完成并把 `generation` 加一,记录一条 changeset,然后唤醒 Reconciler;Reconciler 在 300ms 合并窗口后按组件做最小动作下发。写接口返回时下发**尚未**发生,通过 `/state` 或 SSE 观察 `last_applied_generation` 追上 `generation`。
- `X-Request-ID` 可由客户端提供(≤64 个 `[A-Za-z0-9_-]`),否则服务端生成并回显。

## 状态与下发

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/state` | 版本、模式、`generation` / `last_applied_generation` / `pending` / `applying`、各组件健康(`unknown|active|degraded|down|disabled`)、最近 apply 错误与重试倒计时、渲染警告 |
| GET | `/plan` | 干跑:每个组件的 Plan(动作列表 + 文本 diff)、警告、产物大小;不改变任何东西 |
| POST | `/reconcile` | 立即同步执行一次 reconcile,返回 `{state}`;失败 409 并附 `error` |
| GET | `/artifacts/{xray|mosdns|mosdns-domains|nftables}` | 按当前期望状态渲染出的配置文本 |
| GET | `/events?module=&level=&before_id=&limit=` | 持久化事件(倒序) |
| GET | `/events/stream` | SSE。首条 `event: state` 为完整状态;之后转发 `state.changed`、`apply.started`、`apply.finished`、`apply.failed`、`component.health`;apply 结束与写入后会再推一条 `state`;每 15s 一条注释心跳 |
| GET | `/changesets?limit=` | 变更历史(倒序) |
| GET | `/apply-log?generation=&limit=` | 各组件每次 apply 的动作、耗时、结果 |

## 节点与出站组

| 方法 | 路径 | 说明 |
|---|---|---|
| GET / POST | `/nodes` | `NodeInput`:`name, protocol(vmess|vless|trojan|shadowsocks|socks|http|wireguard|custom), address, port, params(JSON 对象字串), enabled?, credential?`。`credential` 只写不读,响应用 `has_credential` 表示;vmess/vless/trojan/shadowsocks/wireguard 必须有凭证;wireguard/custom 必须提供 `params.settings` |
| GET / PUT / DELETE | `/nodes/{id}` | PUT 不带 `credential` 则保留原值,带 `""` 则清空。DELETE 把指向该节点的规则改回默认出站,响应含 `retargeted_rules` |
| POST | `/nodes/{id}/default` | 设为默认节点(唯一) |
| POST | `/nodes/{id}/enabled` | `{"enabled": bool}` |
| POST | `/node-imports` | `{"links":[...], "dry_run": bool}`,支持 vmess/vless/trojan/ss/wireguard 分享链接;`dry_run` 只返回解析结果不入库;响应中的链接已脱敏 |
| GET / POST | `/groups` | `GroupInput`:`name, kind(failover|loadbalance), member_ids[](有序), strategy(JSON)`。failover 取前两个启用成员为主/备;loadbalance 默认 `leastPing` |
| GET / PUT / DELETE | `/groups/{id}` | DELETE 同样回收引用它的规则 |

## 分流

| 方法 | 路径 | 说明 |
|---|---|---|
| GET / POST | `/rule-sets` | `{name, enabled?}`;新集合追加到末尾 |
| PUT / DELETE | `/rule-sets/{id}` | 改名/启停;删除级联删规则 |
| POST | `/rule-set-order` | `{"ids":[...]}` 必须包含全部集合 |
| POST | `/rule-sets/{id}/reorder` | `{"ids":[...]}` 必须包含该集合全部规则 |
| GET | `/rules?set_id=` | 按集合顺序、规则顺序返回 |
| POST | `/rules` | `RuleInput`:`set_id, match_kind(domain|ip|geosite|geoip), match_value, action_kind(direct|proxy|block|node|group), action_ref?, enabled?, remark?`。`match_value` 可用逗号/空格/换行分隔批量创建(≤500)。域名语义同 Xray:`c.com` 仅根域、`**.c.com` 任意子域、`*.c.com` 一层子域;通配仅 Mode A 允许。IP 归一化为 CIDR;geo 标签小写 |
| PUT / DELETE | `/rules/{id}` | |
| POST | `/rules/{id}/move` | `{"set_id"}` 移到另一集合末尾 |
| GET / POST | `/device-policies` | `{kind(mac|ip|cidr), value, action(proxy|direct|reject), remark?}`;`/32`、`/128` 自动归为 `ip`,其余为 `cidr`;仅 Mode A 生效 |
| PUT / DELETE | `/device-policies/{id}` | |
| GET / POST | `/bypass` | `{cidr, remark?}`,仅 IPv4;Mode A 下这些目标绕过 Xray |
| DELETE | `/bypass/{id}` | |

## 配置

| 方法 | 路径 | 说明 |
|---|---|---|
| GET / PUT | `/dns` | `local_upstreams[]`、`remote_upstreams[]`(1~8 个;公网远端上游自动经 127.0.0.1:10808 SOCKS 并强制 TCP)、`cache_size`、`lazy_cache`、`lazy_cache_ttl`、`log_level` |
| GET / PUT | `/network` | `mode(A|B|C)`、`management_iface`、`service_iface`、`fakeip_pool`(IPv4,≥/24)、`tproxy_port`(1024~65535)、`lan_default_policy(proxy|direct)`、`ospf_push_batch`、`ospf_push_interval_s`、`ospf_resolve_workers`、`ospf_publish_allowlist[]`、`node_failover_mode(normal|strict)`。切到 B/C 时若存在通配域名规则则拒绝;B/C 的受保护端点 preflight 在 M3 加入 |

## 失败语义(与 1.x 的差别)

- `node_failover_mode=normal`:规则指向的节点/组没有任何启用成员时,流量走**默认出站**(默认节点 → 全节点 leastPing → 直连);`strict` 时走 `block`,宁断勿漏。
- 下发失败不回滚数据库:期望状态保留,组件标记 `degraded`,`/state.last_error` 给出原因,按 1s→2s→…→60s 退避重试;30s 周期对齐会修复被外部改掉的 nft 表 / ip rule。
