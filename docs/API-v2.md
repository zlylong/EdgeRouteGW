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

## 网络、模式与 OSPF(M3)

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/interfaces` | 主机网卡列表 `{name, up, loopback, addresses[], mac}`,供选择服务/管理网卡 |
| POST | `/network/preflight` | `{"mode"}` → `PreflightResult{ok, routes, rejected, protected_conflicts[], problems[], warnings[]}`;不改任何东西 |
| GET | `/ospf` | 模式是否启用 OSPF、服务网卡与 LAN 前缀、邻居列表(vtysh JSON)、路由计数(published/pending/failed/protected)、解析缓存统计 |
| GET | `/geo/lookup?target=` | 域名或 IP(可带端口)→ `{target, kind(domain\|ip), geosite[], geoip[]}` 所属标签 |
| GET | `/geo/entries?kind=&tag=&q=&limit=` | 标签内容预览 `{kind, tag, total, items[]}`(默认 300 条,q 子串过滤) |
| GET | `/geo/tags?kind=geosite\|geoip` | 已安装 geodata 的全部标签 `{kind, installed, version?, items[]}`;未安装时 `installed=false`、空列表(规则编辑器据此退回手工输入) |
| GET | `/routes?state=&q=&limit=` | 路由引擎的决定:前缀、来源(fakeip/rule/geo/domain)、状态与原因 |
| POST | `/routes/reset` | 清空记录,下一轮 reconcile 重新计算并对齐内核表 100 |
| POST | `/dns/cache-clear` | 清空解析缓存并立即重新解析 |

`PUT /network` 把模式从 A 切到 B/C 时会跑同一套 preflight,`problems` 非空则 422,`details.preflight` 带完整结果。

### 路由发布规则

- 只发布动作为代理(proxy/node/group)的规则;direct 规则不宣告(主路由不该把这部分流量送来)。
- Mode B:Fake-IP 池 + ip/geoip 规则。Mode C:ip/geoip + 域名规则解析出的 /32 + geosite(同名 geoip 存在则取其 CIDR,否则展开可解析域名,每个标签最多 2000 个)。
- 拒绝:默认路由、环回、链路本地、组播/保留、RFC1918 超网(10/8、172.16/12、192.168/16 及更大)。`geoip:!xx` 取反标签无法表达为路由,跳过。
- 受保护端点:所有节点地址与托管主机(域名会先解析)所在的候选前缀标记 `protected` 不发布,避免 OSPF 把去节点的流量踢回本机。
- 宣告白名单非空时只发布落在白名单内的前缀。被更广前缀覆盖的前缀会被剪掉。
- 内核侧:路由写入 table 200(proto 200,dev lo;与 TPROXY 用的 table 100 分开),FRR 用 `ip import-table 200` 导入并 `redistribute table 200`;frr.conf 固定,只随 router-id/LAN 网段变化。

## 托管主机与部署(M4)

| 方法 | 路径 | 说明 |
|---|---|---|
| GET / POST | `/hosts` | `HostInput{name, ssh_host, ssh_port(22), ssh_user(root), ssh_auth_kind(password|key), credential(只写), region, remark}`;响应含 `has_credential`、`host_key`、`last_deploy` |
| GET / PUT / DELETE | `/hosts/{id}` | DELETE 加 `?cleanup=1` 会先 SSH 上去卸载已部署服务(尽力) |
| POST | `/hosts/{id}/check` | 连接探测;首次连接记录主机指纹(TOFU),之后指纹不符则拒绝 → `{status, reason}` |
| POST | `/hosts/{id}/hostkey` | `{"fingerprint":"SHA256:…"}` 重新钉定;空串 = 下次连接重新记录 |
| POST | `/hosts/{id}/deploy` | `{kind(wg|vless), port?, server_name?, dest?, node_name?}` → 202 `{deploy_id}`;同一主机同时只允许一个部署(409) |
| GET | `/deploys?host_id=` | 部署历史(不含参数) |
| GET | `/deploys/{id}` | 含脱敏参数(去掉私钥) |
| GET | `/deploys/{id}/logs?after_id=` | 安装输出逐行;SSE 同时推送 `provision.log`/`provision.state`/`provision.host` |
| POST | `/deploys/{id}/rollback` | 用该次成功部署的参数重新下发 → 202 `{deploy_id}` |

脚本经 SSH 标准输入执行、不落盘;REALITY 部署后在远端做一次真实握手自检;成功后自动创建/更新对应出站节点(同一主机复用同一节点 id,规则引用不断)。

## 组件更新(M4)

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/components?refresh=1` | `{name(xray|mosdns|geodata), installed, has_previous, latest, job?, last_error, checked_at}`;`refresh` 立即查 GitHub |
| GET | `/components/{name}/versions` | 最近 20 个发布(含 prerelease 标记) |
| POST | `/components/{name}/update` | `{"version"}` 可省略 = 最新 → 202 `{item: Job}`;同时只跑一个任务(409);SSE 推送 `update.progress`/`update.done` |
| POST | `/components/{name}/rollback` | 恢复 `.prev` 版本并重启 |
| GET / PUT | `/components/{name}/schedule` | `{"schedule": "off" \| "daily HH:MM" \| "weekly <mon..sun> HH:MM" \| "monthly <1..28> HH:MM"}`,name 为 xray / mosdns / geodata;`/components/geodata-schedule` 为 geodata 的旧别名 |

| POST | `/components/{name}/install-bundled` | 从 `/usr/share/edgeroutegw/components`(edgeroutegw-components 包)安装;`GET /components` 的 `bundled.version` 显示可用版本 |
| POST | `/components/{name}/upload` | multipart:`file`(官方 zip)+ 可选 `sha256`;无 sha256 时按未校验安装并记事件 |
| GET / PUT | `/components/download-settings` | `{proxy, github_mirror, github_token, has_github_token, clear_github_token}`:下载代理(http/socks5)、GitHub 镜像前缀(https,对 API 与下载都生效;校验文件优先直连 GitHub)、可选的 GitHub token(只在未配置镜像时随 API 请求发送;GET 只回 has_github_token,PUT 留空保持、clear_github_token 清除) |

启动时和收到 SIGHUP 时,若某组件未安装而内置包里有,则自动种子化。

校验:Xray 用官方 `.dgst` 的 SHA2-256,Mosdns 用 GitHub 资产 digest,GeoData 用 `rules.zip.sha256sum`;校验失败不安装。二进制替换前跑 `version` 冒烟,单元重启后 2 秒内不在 active 状态则自动恢复上一版本。

## 备份与恢复(M5)

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/backup?include_secrets=1` | 导出期望状态 JSON(`format: edgeroutegw-backup/1`);带 `include_secrets` 时含节点凭证 |
| POST | `/restore` | 用导出文档整体替换节点/出站组/规则集/规则/设备策略/绕过/DNS/网络(保留 id,规则引用不变),作为一个新的 generation 下发 |

组件更新的 `name` 另支持 `edgeroutegw`(本体):从 GitHub 发布下载对应架构的 `.deb`,按 `SHA256SUMS` 校验,`dpkg -i` 后用 `systemd-run` 延迟 3 秒重启自身。本体没有 rollback 接口,回退用上一版 `.deb`。

CLI:`edgeroutegw import-v1 [--old-key aes.key] [--dry-run] [--replace] proxygw.db` 导入 1.x 数据库(映射见 `docs/UPGRADE-FROM-1.x.md`);`edgeroutegw doctor` 检查主机依赖、sysctl、端口与单元状态。

## 遥测与撤销(M6)

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/traffic?range=1h|24h|7d|30d` | 按桶聚合的上下行字节序列、区间合计、按节点合计、当前速率(`current.available=false` 表示 Xray 统计不可用)。采样每 10 秒读一次 `xray api statsquery`,每分钟落库,保留 90 天;direct/block 不计入合计。SSE 每次采样推 `traffic.sample` |
| GET | `/system/metrics` | 主机采样:`cpu_percent`、`cpus`、`load1/5/15`、`mem_*`、`swap_*`(字节)、`conntrack_count/max`(-1 = 未加载模块)、`uptime_s`;每 5 秒更新 |
| POST | `/node-probes` | 立即探测全部启用节点;结果 `{node_id, ms(-1=不通), error?, via(xray=经该节点出站 HTTP 实测;tcp=连接端口), checked_at}`,`GET /nodes` 的 `probe` 字段带最近一次结果;探测目标可用设置 `probe.url` 覆盖 |
| (旧) |  同步对所有启用节点做一次 TCP 连接测时,写入 `latency_ms`(-1 = 不可达);后台每 5 分钟自动一轮,SSE 推 `nodes.latency` |
| POST | `/changesets/{id}/revert` | 把配置恢复成该次变更之后的快照,作为新的 generation 下发;中间的变更标记 `reverted_by`。每次写操作保存完整快照,保留最近 200 次 |

## 失败语义(与 1.x 的差别)

- `node_failover_mode=normal`:规则指向的节点/组没有任何启用成员时,流量走**默认出站**(默认节点 → 全节点 leastPing → 直连);`strict` 时走 `block`,宁断勿漏。
- 下发失败不回滚数据库:期望状态保留,组件标记 `degraded`,`/state.last_error` 给出原因,按 1s→2s→…→60s 退避重试;30s 周期对齐会修复被外部改掉的 nft 表 / ip rule。
