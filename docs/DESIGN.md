# EdgeRouteGW 2.0 设计方案

> 状态:v0.2 定稿(2026-10-20),第 7 节六项技术决定已拍板。基于 1.8.1 + main 未发布改动的功能盘点,目标是**功能对齐、架构重做**。
> 本文回答三个问题:1.x 哪里痛、2.0 后端怎么流转、2.0 前端怎么组织。实施路线见末尾。

---

## 0. 一页总结

| 维度 | 1.x 现状 | 2.0 目标 |
|---|---|---|
| 后端代码组织 | 139 个文件全在 `package main`,65+ 个包级变量,靠"函数变量 seam"做测试 | `cmd/` + `internal/` 分层,接口注入,无包级可变状态 |
| 配置下发模型 | 命令式:每个 handler 自己"写库 → 渲染 → systemctl restart",失败走 3s 防抖回退 | 声明式:写库只改"期望状态",单一 **Reconciler** 算差异、按驱动策略下发、结果可观测 |
| 数据层 | `CREATE TABLE IF NOT EXISTS` + `ALTER TABLE` 吞错,同一张表在两处定义;`policy='bal-ha-6-5'` 字串编码 | 版本化 SQL 迁移;出站组(故障转移/负载)成为一等实体;节点用稳定 UUID 引用 |
| 外部系统交互 | 到处 `exec` systemctl/ip/nft/vtysh/dig/xray-cli | 每个外部系统一个 Driver;Xray 走 gRPC、FRR 走内核路由 redistribute、systemd 走 D-Bus、DNS 走 miekg/dns |
| 状态感知 | 前端每 2s 轮询 `/status`(曾每次 fork 8 个子进程) | 后端推送:SSE 事件流,前端订阅 |
| 前端 | 1357 行单 HTML + 1412 行 app.js,Vue CDN,无构建、无组件、无路由,"普通/高级"全局开关 | Vite + Vue3 + TS + Pinia + Router,按任务组织的 7 个页面,渐进展开,内嵌进二进制 |
| 发布形态 | `git clone` 到 `/root/proxygw`,仓库里提交了 Xray/Mosdns 二进制(单文件 157MB×2) | 单一静态二进制 + `.deb`;核心组件在安装/升级时校验下载,仓库不再存二进制 |

---

## 1. 1.x 功能盘点(2.0 必须对齐的能力)

按现有 API/UI 梳理,2.0 不删功能,只重排:

- **接管模式**:Mode A(TPROXY 全局网关)/ Mode B(Fake-IP + OSPF 宣告 198.18.0.0/16)/ Mode C(真实 IP OSPF,域名周期解析推路由)。切换带 preflight(受保护端点与候选路由交集检测)。
- **节点**:8 种协议(VMess/VLESS/Trojan/SS/Socks/HTTP/WireGuard/Custom JSON),分享链接导入,启停、默认节点、测速、故障转移模式 normal/strict,HA 对(`bal-ha-主-备`)。
- **远程节点部署**:录入海外主机 SSH,一键部署 WireGuard 或 VLESS-REALITY,参数生成/重生成/历史回滚,主机指纹钉定,部署日志,5 分钟健康探测,批量部署(≤20)。
- **分流规则**:类型 domain/ip/geosite/geoip(+geolocation),策略 direct/proxy/block/指定节点/HA 组;分组、排序、重命名;域名通配语义与 Xray 一致(`c.com` / `*.c.com` / `**.c.com`,B/C 模式禁通配);`/geo/query` 查标签与展开。
- **设备策略**:LAN ACL(mac/ip → proxy/direct/reject)+ 默认策略;保护 IP(Mode A 下绕过 Xray 的目标 IP)。
- **DNS**:本地/远端上游、lazy cache、缓存大小/TTL、日志级别;Mosdns 配置渲染。
- **OSPF**:邻居数、已发布/待发布路由、推送批量/间隔/解析并发、发布白名单、清空待发布;脏路由过滤、RFC1918 超网拒绝、受保护端点排除、FRR 对齐。
- **观测**:实时连接(tail Xray access log + 规则关联)、事件日志(带 trace_id)、journalctl 五个服务、nftables 链计数器、流量速率/月累计/节点排行。
- **系统**:Xray/Mosdns/GeoData 更新与回滚(摘要校验 + 冒烟测试)、GeoData 定时更新 cron、管理口/服务口网卡角色、密码修改、生成配置只读查看、trace(目标会走哪条规则)、全组件 health check。
- **安全**:随机初始密码、登录限速、会话、高危操作确认头 + dry-run、SSH 凭证 AES-256-GCM、systemd 沙箱、供应链哈希校验。

---

## 2. 1.x 的结构性问题(为什么要重写而不是继续修)

1. **下发链路发散**。规则/节点/ACL/DNS/模式各有一条"写库 → 渲染 → 重启"的路径,互相用 `applyMutex`、`applyTimerMu`、`pendingMosdnsApply` 协调。生产事故(mosdns 每 3~8 秒被重启撞 start-limit)正是 `PUT /rules/reorder` → `xray api adrules` 失败 → 回退 restart xray+mosdns → UI 连发触发的。1.8.x 用"渲染后比对磁盘才重启"、"5s 去重"打补丁,但根因是没有单一的变更入口。
2. **状态散落在全局变量**。`db`、OSPF 日志环、缓存 map、各种 `Fn` seam 都是包级变量,测试靠替换函数变量再 `defer` 恢复;并发语义只能靠人记。
3. **数据模型把关系编码进字串**。`rules.policy` 存 `bal-ha-6-5`,节点删除/重导入后 HA 对悬空,Xray `-test` 还能通过,直到主节点挂了才暴露(1.8.1 修复记录)。
4. **到处 fork**。`/status` 曾每 2s fork 8 个进程;`dig` 成为 Mode C 唯一解析路径(缺了就静默发布 0 条路由);`vtysh` 批量下发在 FRR 未运行时刷错。
5. **前端不可维护**。单文件模板里 11 个 tab + 10 个 modal 靠 `activeTab === 'x'` 切换,所有状态在一个 `setup()` 里;"普通/高级"全局开关让同一页面出现两套 DOM。
6. **仓库和部署耦合**。运行时 `git pull` 升级,核心二进制进 git(仓库历史因此巨大),Bot 每日提交规则与二进制。

---

## 3. 2.0 后端设计

### 3.1 分层与目录

```
edgeroutegw/
├── cmd/edgeroutegw/          # main:装配依赖、启动 HTTP + Reconciler
├── internal/
│   ├── api/                  # HTTP 层(chi 或 gin),只做解码/校验/鉴权/编码
│   │   ├── v2/               # 路由与 handler,按资源分文件
│   │   └── sse/              # 事件流
│   ├── auth/                 # 密码(argon2id)、会话、API token、限速
│   ├── store/                # SQLite 访问:migrations/ 嵌入 SQL,queries/ 类型化查询(sqlc)
│   ├── model/                # 领域对象:Node、OutboundGroup、RuleSet、Rule、DevicePolicy、DNSConfig、NetworkConfig…
│   ├── desired/              # 从 store 读出一份不可变的 Snapshot(期望状态)
│   ├── render/               # 纯函数:Snapshot → 各组件 Artifact(xray.json、mosdns.yaml、nft ruleset、路由集合、ip rule)
│   ├── reconcile/            # Reconciler:生成差异、编排 Driver、记录结果、重试
│   ├── driver/               # 对外部系统的最小接口实现
│   │   ├── xray/             # gRPC(HandlerService/RoutingService/StatsService)+ 配置落盘 + 重启
│   │   ├── mosdns/           # 配置落盘 + 节流重启
│   │   ├── nft/              # 原子替换 table(不 flush ruleset)+ set 增量
│   │   ├── route/            # netlink 路由注入(protocol=edgeroutegw)供 FRR redistribute
│   │   ├── frr/              # 只管 ospfd 静态配置与邻居状态读取
│   │   ├── systemd/          # D-Bus:unit 状态、restart、journal 读取
│   │   └── sysctl/, iface/   # 内核参数、网卡枚举
│   ├── geo/                  # geoip/geosite .dat 解析、matcher 缓存、版本管理
│   ├── resolver/             # miekg/dns 查询本机 Mosdns,带 TTL 的缓存
│   ├── provision/            # 远程节点部署:任务队列、状态机、脚本生成、SSH、日志
│   ├── observe/              # 连接追踪、流量采样、事件总线、Prometheus 指标
│   ├── update/               # 组件(xray/mosdns/geodata/self)下载校验、staging、冒烟、回滚
│   └── migrate1x/            # 读 1.x proxygw.db 导入新 schema
├── web/                      # 前端工程(Vite),构建产物 embed 进二进制
├── packaging/                # .deb(nfpm)、systemd unit、sysctl、postinst
└── docs/
```

原则:
- **只有 `cmd/` 装配**。所有依赖显式传入 struct,没有包级可变状态;测试用接口替身而不是替换函数变量。
- **`render/` 是纯函数**,给同一个 Snapshot 必须产出字节一致的 Artifact,这是"无变化不重启"和"dry-run 出 diff"的基础。
- **`driver/` 不含业务**。它不知道什么是规则,只知道"把这份 ruleset 原子替换上去"。

### 3.2 核心流程:期望状态 → Reconciler → Driver

```
 UI/API 写操作 ──► store(事务) ──► generation++ ──► 通知 Reconciler
                                                        │
                        ┌───────────────────────────────┘
                        ▼
              ① 取 Snapshot(gen=N)
              ② render:Snapshot → {xray, mosdns, nft, routes, iprule}
              ③ 与 last_applied(gen=M) 的 Artifact 逐个比对
              ④ 对有差异的组件,按驱动策略选择最小动作
                   xray : 出站/规则差异 → gRPC 热更新;inbound/日志等结构差异 → 写文件 + restart
                   mosdns: 内容 hash 变 → 写文件 + 节流 restart(最小间隔 10s,合并)
                   nft  : 仅 set 元素变 → `nft add/delete element`;链结构变 → 原子替换 table
                   routes: 集合 diff → netlink add/del(分批,受 push 参数限制)
              ⑤ 写 apply_log(gen, component, action, duration, error)
              ⑥ 成功 → last_applied=N;失败 → 标记组件 degraded,指数退避重试,SSE 广播
```

关键约定:

- **单实例串行**。Reconciler 是唯一会碰外部系统的协程(远程部署和组件更新除外,它们走独立队列,但完成后也只是写库触发 reconcile)。没有第二把锁。
- **合并窗口**。连续写操作在 300ms 内合并为一次 reconcile;UI 拖拽排序 20 次只会产生一次下发。这直接消灭 1.x 的 start-limit 事故。
- **幂等 + 可重放**。Reconciler 启动时也跑一遍同样流程,把"后端重启不该重启 Xray/mosdns"从特判变成自然结果。
- **失败不回滚数据**。1.x 在下发失败时回滚 DB,导致用户看到"保存成功又消失"。2.0 保留期望状态,把组件标为 degraded 并告诉用户哪一步失败、为什么;用户可以撤销上一次变更(见 3.5 的 changeset)。
- **预检在 reconcile 之前**。模式切换、OSPF 发布等高危写操作先 `render` 一份候选 Snapshot 做 preflight(受保护端点 ∩ 候选路由等),不通过直接 4xx,不进 store。

### 3.3 领域模型(schema 要点)

```
nodes               id(uuid), name, protocol, address, port, credential(enc), params(json), enabled, latency_ms, created/updated
outbound_groups     id, name, kind(failover|loadbalance), member_ids(json ordered), strategy(json)      ← 取代 bal-ha-a-b
rule_sets           id, name, position, enabled                                                            ← 取代 group_id/group_name 拼在 rules 里
rules               id, set_id, position, match_kind(domain|ip|geosite|geoip), match_value, action_kind(direct|proxy|block|node|group), action_ref
device_policies     id, kind(mac|ip|cidr), value, action(proxy|direct|reject), remark                     ← lan_acls
bypass_destinations id, cidr, remark                                                                        ← protected_ips
dns_config          单行:local_upstreams(json), remote_upstreams(json), cache{size,lazy,ttl}, log_level
network_config      单行:mode(A|B|C), mgmt_iface, service_iface, fakeip_pool, tproxy_port, ospf{batch,interval,workers,allowlist}
remote_hosts        id, name, ssh{host,port,user,auth_kind,credential(enc),host_key}, region, status, deployed_profile(json)
remote_deploys      id, host_id, kind(wg|vless), params(enc json), state, started/finished, log_ref        ← history+logs 合一,state 机驱动
routes_published    cidr PK, source(rule|geo|domain), rule_id, first_seen, last_seen, state(pending|published|failed|protected)
resolve_cache       domain, group, ips(json), ttl, expires_at, fail_count
geo_cache           tag, geodata_ver, payload(json)
events              id, ts, level, module, type, message, trace_id, actor, details(json)
apply_log           id, generation, component, action, ok, duration_ms, error
changesets          id, generation, actor, summary, snapshot_diff(json), created_at                         ← 可撤销
sessions / api_tokens / settings(仅剩 UI 偏好、cron 等真正的 KV)
```

- 迁移用嵌入式带编号 SQL(`store/migrations/0001_init.sql`…),启动时按版本顺序执行,拒绝"吞 duplicate column"。
- SQLite 驱动定为 `modernc.org/sqlite`(纯 Go):`CGO_ENABLED=0` 静态二进制,arm64 直接 `GOARCH=arm64 go build`,不再绑编译机 glibc(1.x 只能跑 Debian 13/Ubuntu 24.04 的限制随之解除)。写入微基准慢 1.5~3 倍但本项目写入量极低,无感。DSN/PRAGMA 写法收在 store 一处,实测不满意可换回 mattn 或 ncruces/go-sqlite3。
- 加密:仍用 AES-256-GCM,但密钥文件由 `postinst` 生成在 `/var/lib/edgeroutegw/keys/`,永不进仓库(1.7.27 的 `aes.key` 被 git 跟踪事故不再可能)。

### 3.4 Driver 具体选择与理由

| 组件 | 1.x | 2.0 | 理由 |
|---|---|---|---|
| Xray | exec `xray api adrules/ado/rmo`;Trace 与连接追踪在 Go 里重实现域名匹配 | **混合**:读侧走 gRPC(`StatsService` 统计、`RoutingService.TestRoute` 做 Trace、`SubscribeRoutingStats` 流式连接追踪);写侧(增删出站/规则)沿用已安装二进制的 CLI | gRPC 的 TestRoute/Subscribe 是 CLI 给不了的能力,且让 Xray 自己回答"走哪条规则",消灭两套匹配逻辑;写侧留 CLI 是因为热加出站需把 JSON 转成 protobuf,转换器版本若落后于已安装 Xray 会失败,CLI 客户端与服务端永远同版本。Reconciler 已合并写操作,fork 次数可忽略。2.0 稳定后再评估写侧迁 gRPC |
| Mosdns | restart | restart,但由 Reconciler 节流合并 | mosdns v5 无热重载;问题从来不是 restart 本身而是频率 |
| nftables | `nft -f` 整份 `flush ruleset` | `table inet edgeroutegw` 级别原子替换;ACL/保护 IP 变化只做 set 元素增删 | `flush ruleset` 会清掉宿主其它防火墙表;set 增量不中断连接 |
| OSPF 路由 | vtysh 逐条 `ip route <cidr> 127.0.0.1 tag 100`(FRR 静态路由)+ `redistribute static route-map`;DB/frr.conf/内核三处副本,靠 45s 对齐循环 | netlink 把路由写进独立内核表(table 100,自定义 protocol 号);FRR 固定配置 zebra `ip import-table 100` + ospfd `redistribute table 100` | 批量下发快一到两个数量级(geoip:!cn 约 58 万条);状态只剩 DB 与 table 100 两处,对齐=集合比较;frr.conf 装好后不再被改写;不污染网关主路由表。Mode B/C 下 LAN 流量在 prerouting 即被 TPROXY 截走,这些路由只为宣告而存在,放独立表不影响转发。升级时迁移脚本重写一次 frr.conf |
| systemd | exec systemctl | D-Bus(`github.com/coreos/go-systemd/v22/dbus`) | 拿到 ActiveState/SubState/NRestarts 结构化数据,订阅变更代替轮询 |
| DNS 解析 | exec `dig` | `miekg/dns` 查 127.0.0.1:53 | 拿真实 TTL,无外部依赖,可并发控制 |
| 日志 | exec journalctl | 保留 journalctl(避免 cgo sdjournal),加 2s 结果缓存 | 够用 |

### 3.5 API v2 约定

- 前缀 `/api/v2`,资源化 REST;所有写操作返回 `{generation, changeset_id}`。
- `GET /api/v2/state` 一次拿到:模式、各组件 health(active/degraded/down + 最近 apply 错误)、当前 generation 与 last_applied、版本信息。
- `GET /api/v2/events/stream`(SSE):`apply.started/finished/failed`、`component.health`、`traffic.sample`(2s)、`connection.new`、`provision.log`、`ospf.neighbor`。前端不再轮询。
- `POST /api/v2/plan` body = 一组候选写操作 → 返回 render diff(每组件的文本 diff + 将执行的动作 + preflight 结果),**不落库**。取代 1.x 的 `?dry_run=1`。
- `POST /api/v2/changesets/{id}/revert` 撤销某次变更(生成反向 changeset)。
- 高危操作(模式切换、重置 OSPF 待发布、更新组件、删除远程主机)保留二次确认,但由前端在 plan 结果上确认,服务端用 `Idempotency-Key` 防重放。
- 鉴权:HttpOnly SameSite=Strict 会话 cookie + `X-CSRF-Token`;另给自动化用的 Bearer API token(可按作用域 read/write)。密码 argon2id。可选 TOTP。
- 错误统一 `{error:{code,message,details}}`;`code` 稀疏枚举并写进 OpenAPI。
- OpenAPI 3.1 文件由代码注释生成并随二进制发布,前端用它生成 TS client。

### 3.6 后台任务

全部注册到一个 `scheduler`,统一 panic 恢复、抖动、停止信号与指标:

- `reconciler`(事件驱动 + 30s 兜底对齐:检查 ip rule / 路由表 / nft table 是否被外部改掉)
- `resolver.refresh`(Mode C 域名按 TTL 到期刷新,不再固定 5 分钟全量)
- `traffic.sampler`(2s 采样,1min 落库,保留策略在 `observe`)
- `provision.health`(5min 探测远程主机)
- `geodata.cron`(用户计划)
- `store.maintenance`(每日:过期缓存、事件保留、`PRAGMA optimize`)
- `update.check`(每日比对上游版本,只提示不自动装)

### 3.7 安装与运行形态

- 产物:`edgeroutegw_2.0.0_amd64.deb` / `arm64.deb`(nfpm 打包),内含二进制(含前端)、unit 文件、sysctl、nft 骨架、postinst(建用户/目录/密钥/初始密码)。
- 目录:`/usr/bin/edgeroutegw`,`/etc/edgeroutegw/`(少量静态配置,如监听地址),`/var/lib/edgeroutegw/`(db、keys、下载的 xray/mosdns/geodata),`/run/edgeroutegw/`(日志管道)。
- Xray/Mosdns/GeoData 由 `update` 模块在首启或 `edgeroutegw component install` 时校验下载,**仓库不再提交二进制**,Daily workflow 改为只更新"推荐版本清单 + 规则文本 + 校验和"。
- 权限:先以 root 跑(与 1.x 一致),但 unit 里 `CapabilityBoundingSet` 收敛到 `CAP_NET_ADMIN CAP_NET_RAW CAP_NET_BIND_SERVICE CAP_SYS_ADMIN(D-Bus 重启 unit 需 polkit 规则)`。第二阶段评估专用用户 + polkit。
- 升级:`apt install` 新 .deb,postinst 迁移 schema;或 UI 内自升级(下载 .deb → dpkg -i → systemd 重启自身,失败由 `ExecStartPre` 回滚到 `.prev`)。
- CLI 子命令:`edgeroutegw serve|migrate|import-v1 <old.db>|plan|reset-password|component install|doctor`。`doctor` 覆盖 1.x health_check 并补齐环境检查(port 53 占用、ip_forward、dig 不再需要)。

---

## 4. 2.0 前端设计

### 4.1 技术栈

- Vite + Vue 3 (`<script setup lang="ts">`) + TypeScript + Pinia + Vue Router。
- UI:Tailwind v4 + shadcn-vue(reka-ui 无样式组件),保持 1.x 的 Tailwind 视觉语言但有真正的组件库;深色模式;zh-CN / en i18n(vue-i18n)。
- 数据:由 OpenAPI 生成的 typed client + TanStack Query(缓存/失效/乐观更新);SSE 用一个 `useEventStream()` 组合式把事件分发到各 store。
- 图表:ECharts(流量/延迟),表格:TanStack Table(虚拟滚动,连接追踪上千行不卡)。
- 构建产物 `embed` 进 Go 二进制;开发时 Vite 代理到后端。
- 测试:Vitest 单测 + Playwright E2E(复用 1.x e2e 的按钮覆盖思路,但对准新 IA)。

### 4.2 信息架构(7 个页面,替代 1.x 的 11 个 tab + 普通/高级开关)

```
┌ 侧栏 ───────────────┐  ┌ 顶栏 ─────────────────────────────────────────────┐
│ 总览                 │  │ 模式徽章 ▸ 组件健康点 ×4 ▸ 变更状态(gen N 已生效/正在下发/失败) │
│ 节点                 │  └──────────────────────────────────────────────────┘
│ 分流                 │
│ DNS                  │   页面内容区:每页顶部 1 行摘要卡,其下是任务流
│ 网络与模式            │
│ 观测                 │
│ 系统                 │
└──────────────────────┘
```

1. **总览 Dashboard**
   - 四个组件卡(Xray / Mosdns / nftables / OSPF):状态、版本、最近一次 apply 结果,点击直达对应观测页。
   - 模式卡:当前模式、一句话说明、"切换"按钮(进向导)。
   - 流量:实时上下行 sparkline + 本月累计 + 节点流量排行(前 5)。
   - 节点健康条:每个启用节点一个色块(延迟/不可达)。
   - 最近事件 10 条(error/warn 优先)。
   - 若有 degraded 组件,顶部横幅给出"为什么 + 重试 + 查看 diff"。

2. **节点 Nodes**
   - Tab「出站节点」:表格(名称/协议/地址/延迟/本月流量/启用),行内启停、设默认、测速;右侧抽屉编辑,按协议渲染表单(VLESS 的 REALITY 字段不再塞 JSON 文本框,高级字段折叠在"原始 JSON"里)。
   - Tab「出站组」:新建故障转移组/负载组,拖拽成员排序,显示当前活动成员。取代隐藏在规则策略里的 `bal-ha`。
   - Tab「托管主机」(= 1.x 远程节点部署):主机列表 + 状态;"部署"走 3 步向导(连接 → 选择 WG/REALITY 与参数 → 执行);执行页实时日志(SSE);详情抽屉含参数版本历史与回滚;部署成功后一键"加入出站节点"。
   - 导入:粘贴分享链接/订阅,先预览解析结果再确认。

3. **分流 Routing**
   - 左:规则集列表(可拖拽排序、启停)。右:所选规则集的规则表(拖拽排序、批量添加、按类型着色)。
   - 每条规则的"动作"是一个选择器:直连 / 代理(默认出站) / 拦截 / 指定节点 / 指定出站组,不再手写字串。
   - 顶部常驻 **Trace 输入框**:输入域名或 IP,右侧实时显示"会命中哪条规则、走哪个出站、为什么",并高亮那一行。把 1.x 的测试工具搬到用户正在改规则的地方。
   - Tab「设备策略」:LAN ACL 表 + 默认策略开关;Tab「绕过目标」:保护 IP(仅 Mode A 展示,其它模式显示原因)。
   - 模式相关限制(B/C 禁通配域名)在输入时即时校验并给出解释,而不是提交后 400。
   - Geo 查询作为侧边工具抽屉:查标签、展开 CIDR 列表(分页 + 导出)。

4. **DNS**
   - 本地/远端上游编辑(每条上游一行,可测试响应时间)。
   - 缓存设置、日志级别。
   - 「测试解析」:输入域名,显示会走哪个上游组、返回什么(Fake-IP 模式标明是假 IP)。
   - 生成的 mosdns.yaml 只读折叠。

5. **网络与模式 Network**
   - 模式切换**向导**:选 A/B/C → 展示该模式的拓扑图与主路由侧必须做的事(DHCP 网关/DNS、OSPF 邻居、PBR 绕过)→ preflight 结果(受保护端点冲突、端口占用、ip_forward)→ 确认。
   - 网卡角色(管理口/服务口)选择,显示各口 IP。
   - OSPF 面板(B/C 模式):邻居列表与状态、已发布/待发布/失败路由数、推送参数、发布白名单、"重置待发布"(高危确认);路由表可搜索,每条显示来源(哪条规则/哪个域名)。
   - nftables 链计数器实时(SSE),帮助判断"流量有没有进 TPROXY"。

6. **观测 Observe**
   - 「连接」:实时流(SSE,暂停/继续),按客户端/目标/出站/规则过滤,点击目标 → 一键"为它加规则"。
   - 「事件」:结构化表格,按 module/level/时间过滤,trace_id 可跳转到对应 apply_log。
   - 「变更历史」:changeset 列表,每条可看 diff、可撤销。
   - 「服务日志」:五个服务 tab,行数/时间/级别/grep,自动滚动锁。
   - 「流量」:历史曲线(24h/7d/30d),节点维度切换。

7. **系统 System**
   - 组件更新:Xray/Mosdns/GeoData/EdgeRouteGW 本体,当前版本 vs 上游最新,更新带进度与回滚按钮;GeoData 定时计划。
   - 备份与恢复:导出/导入配置(含/不含凭证),从 1.x 数据库导入。
   - 安全:改密码、会话列表(踢出)、API token 管理、TOTP。
   - 生成配置:xray / mosdns / nftables / frr 四份,只读,带"与上次已生效版本的 diff"。
   - 关于:版本、commit、OS、许可、文档链接。

### 4.3 全局交互约定

- **变更状态指示器**(顶栏右):`已生效 gen 42` / `正在下发…` / `失败:Xray 热更新被拒,已回退重启`。点击展开本次 diff。取代 1.x 的"应用配置(Xray 重启)"按钮,用户不再需要知道什么时候要点它。
- **高危操作** = 先 plan 再确认:弹窗里展示将执行的动作列表与 preflight 结果,确认按钮带 loading 与防重复。
- **乐观更新**:列表操作立即反映,后台 apply 失败时在该行打标并给出原因。
- **渐进展开**代替"普通/高级"全局开关:每个表单的高级项放在折叠区,首次使用默认收起。
- 响应式:≥1024 侧栏常驻,<1024 折叠为抽屉;手机上能看总览、启停节点、看连接。
- 无障碍:所有图标按钮 `aria-label`,弹窗 `role=dialog` + 焦点圈,键盘可完成主要流程(沿用 1.x 已做的)。

---

## 5. 兼容与迁移

- **数据迁移**:`edgeroutegw import-v1 /root/proxygw/config/proxygw.db` 读取 1.x 全部表;`rules.policy` 的 `bal-ha-a-b` 转为出站组 + 规则引用;节点按旧 id 映射到新 uuid;远程节点凭证需要旧 `aes.key` 解密后用新密钥重加密(导入命令接受 `--old-key`)。
- **API**:v2 不兼容 v1。v1 没有第三方消费者(只有自带前端),不做双栈。
- **部署切换**:同机安装 2.0 .deb 时 postinst 检测 `/root/proxygw`,停止旧 unit、导入数据、接管 nft table 与 ip rule(表名不同,先加后删,不中断流量),旧目录保留供回退。
- **运行时文件路径变化**在 OPERATIONS 文档中给出对照表。

---

## 6. 实施路线(建议)

| 阶段 | 产出 | 验收 |
|---|---|---|
| M0 骨架 | 新目录、store+migrations、model、`cmd` 装配、CI(vet/test/race/nfpm) | `edgeroutegw serve` 起 HTTP,`/api/v2/state` 返回空状态 |
| M1 渲染与 Reconciler(Mode A) | render 四类 Artifact、Reconciler 主循环、xray/mosdns/nft/systemd Driver、节点与规则 CRUD、SSE | 用 1.x 导入的数据跑出字节一致的 xray.json;规则拖拽 20 次只触发 1 次下发 |
| M2 前端 Alpha | Vite 工程、总览/节点/分流/观测(连接+事件)四页、变更状态指示器 | Playwright 覆盖主流程;在测试机替换 1.x 验证 Mode A 全链路 |
| M3 Mode B/C | route(netlink)+frr Driver、resolver(miekg/dns)、OSPF 面板、模式向导、preflight | 与 1.x 在同一主路由上对比已发布路由集合一致 |
| M4 托管主机与更新 | provision 状态机 + SSE 日志、update 模块(含自升级)、系统页 | 部署 WG/REALITY 各一台成功;Xray 升级/回滚闭环 |
| M5 发布 | .deb、import-v1、文档、迁移脚本、2.0.0-rc | 10.100.130.253 从 1.7.21 迁到 2.0 不丢节点/规则/凭证 |

每阶段结束都能独立运行,M2 之后就可以在测试机替换 1.x 做真实流量验证。

---

## 7. 已拍板的技术决定(2026-10-20)

| # | 决定 | 结论 | 要点 |
|---|---|---|---|
| 1 | Xray 对接 | **混合**:读侧 gRPC,写侧 CLI | 见 3.4。写侧迁 gRPC 列为 2.x 评估项 |
| 2 | SQLite 驱动 | **modernc.org/sqlite** | 见 3.3。驱动选择隔离在 `store/`,一个文件可换 |
| 3 | OSPF 路由注入 | **netlink + FRR import-table** | 见 3.4。frr.conf 变为静态模板;`migrate1x` 负责重写旧机器的 frr.conf |
| 4 | UI 组件库 | **shadcn-vue + Tailwind v4** | 延续 1.x 的 Tailwind 视觉语言,组件源码进仓库可控;代价是比 Naive UI 多一些初期搭建量 |
| 5 | 运行用户 | **2.0 仍以 root 运行**,unit 内收敛 `CapabilityBoundingSet`;专用用户 + polkit 放 2.1 | 避免 2.0 同时承担网络栈重做与权限模型重做两类风险 |
| 6 | 开发位置 | **同仓库 `v2` 分支** | 新目录结构在 `v2` 上从零搭,不动 `main`;2.0.0 发布时 `main` 以 orphan 提交重建、剔除 `core/` 二进制历史,旧历史打 tag `v1-archive` 保留 |

### 7.1 第 6 项的执行细节

- `v2` 分支的第一个提交只保留 `LICENSE`、`docs/DESIGN.md` 与新骨架,不继承 `backend/`、`frontend/dist`、`core/`。
- 1.x 的 Daily workflow(自动提交 Xray/Mosdns 二进制与规则文本)在 `v2` 上禁用;2.0 的对应物是"推荐版本清单 + 校验和"文件,由 workflow 只改这个清单。
- `main` 继续接收 1.x 的修复直到 2.0.0 发布;2.0.0 时 `main` 指向 `v2`,老 `main` 以 `v1-archive` tag 与 `v1` 分支保留,供 1.x 用户回看与紧急修复。
- 仓库瘦身(剔除历史里的 157MB×2 Xray 二进制)通过 orphan 重建完成,不对已有 clone 做 `filter-repo`,避免打断 1.x 用户的 `git pull` 升级路径;`v1` 分支历史保持原样。
