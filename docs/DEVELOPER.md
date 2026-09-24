# 开发者与架构指南

本文档面向对 EdgeRouteGW 进行二次开发、或希望深入了解其底层网络机制的资深开发者与网络工程师。

## 🏗️ 核心架构

EdgeRouteGW 是一个高度整合的网络系统。开发者坚信 **原生至上 (Native First)**，完全摒弃了 Docker 容器化带来的网络损耗、内核隔离复杂度以及额外开销，采用 Debian 原生裸机部署：

- **后端 (Go 1.26+)**：基于 Gin 框架，处理配置文件的动态生成、节点并发测速、系统服务守护任务与 Web API 支持。HTTP Server 带读头/空闲超时与 SIGTERM 优雅关闭，JSON 与静态资源默认 gzip。
- **前端 (Vue 3 + TailwindCSS)**：无打包器的 SPA，源码就是 `frontend/dist/index.html`（模板）与 `frontend/dist/libs/app.js`（应用脚本），使用 Vue 生产版构建与**预编译**的 Tailwind CSS（`frontend/dist/libs/app.css`），完全脱机可用。在这两个文件里新增 Tailwind 类名后需运行 `scripts/build_frontend_css.sh` 重新生成 CSS：脚本把 Tailwind v3.4.17 独立 CLI 下载到 `frontend/.cache/`（需要网络，不需要 Node），配置见 `frontend/tailwind.config.js`，运行时拼接的类名需列入 `safelist`。后端发送的 CSP 为 report-only，并允许 `'unsafe-eval'`（Vue 全局构建在浏览器内编译模板）。
- **数据持久化**：SQLite 3。通过 DSN 为**每个连接**启用 WAL、`busy_timeout=5000` 与 `synchronous=NORMAL`（PRAGMA 仅作用于单个连接，曾导致其余连接 `busy_timeout=0`），连接池上限 8；多处代码在持有游标时继续执行语句，因此不能把连接池缩到 1。

## 🚀 核心网络组件与协作

系统由以下四大底层组件协同工作：
1. **Xray-core**: 处理核心出入站流量、各种代理协议的加解密与 TLS/HTTP 嗅探。
2. **Mosdns (v5+)**: 负责智能 DNS 分流。不直接将 V2Ray 二进制文件塞给 Mosdns。后端系统基于纯 Go 手工实现了极轻量级 Protobuf 解码器，实现完全离线、本地毫秒级从二进制 geoip.dat 中动态提取任意分类（如 telegram、netflix）的 IPv4 CIDR 网段，并转交给 OSPF 发布路由。
3. **Nftables**: 负责 **Mode A** 模式下的 TProxy (透明代理) 底层流量劫持，将局域网物理流量强行导向 Xray。
4. **FRR (OSPF)**: 负责 **Mode B** 和 **Mode C** 模式下的动态旁路由宣告。Mode B 仅发布 Fake-IP 网段，Mode C 动态注入真实 GeoIP 网段。

## 🧠 网络分流与 Fake-IP 零延迟原理

传统的透明代理在进行 DNS 解析时往往存在泄露或上游网络延迟（等待国外 DNS 返回真实 IP）。EdgeRouteGW 实现了彻底的零延迟 Fake-IP 架构 (`198.18.0.0/16`) (对应 Mode B)：

1. **瞬时接管**：命中代理规则的域名（基于 `proxy_domains.txt`），Mosdns 将不再向上游发起真实的互联网解析请求，而是直接在 1 毫秒内返回一个保留的 `198.18.x.x` 假 IP。
2. **流量重定向**：客户端（手机/PC）拿着这个假 IP 发送 TCP/UDP 流量，流量到达网关后被 Nftables 的 TProxy 规则劫持并丢给 Xray 的入站端口。
3. **Sniffing 嗅探**：Xray 开启了强大的流量嗅探（http, tls, fakedns）。它不仅能从自己的内置映射表中找回假 IP 对应的真实域名，还能从 TLS 的 SNI 字段中提取出真实的请求域名（如 `youtube.com`）。
4. **规则出站**：Xray 拿到真实域名后，内部根据分流规则直接将流量打包发往远端代理节点，全程无需在本地等待真实 IP 解析。

**附：Mode B / Mode C 的域名与 OSPF 链路（当前实现）**

### 模式总览（关键约束）

- **Mode A**：
  - Xray / Mosdns 都不启用 FakeDNS/FakeIP。
  - `domain` 匹配只在 Xray 运行时处理，不做 DNS->OSPF 展开。
- **Mode B（Hybrid FakeIP）**：
  - 仅 Mode B 启用 Xray `fakedns` 与 Mosdns `forward_fakeip`。
  - OSPF 仅发布 `198.18.0.0/16` + `ip/geoip` 静态路由。
  - `domain/geosite` 不做 DNS->OSPF 展开。
- **Mode C（纯 OSPF）**：
  - 禁用 FakeDNS/FakeIP。
  - `domain` 规则会做 DNS 解析并同步为 OSPF 候选静态路由。
  - `geosite` 仍优先走 geoip 提升与缓存链路，最终落为 CIDR。

### Mode C 域名/地理规则展开链路

当规则包含 `domain` / `geosite` 时，后端在 `collectStaticRoutesForMode()` + `syncStaticRoutesToOSPF()` 中按以下顺序构建候选路由：

1. **geosite 同名 geoip 优先直提 CIDR**
   - 先检查 `hasGeoIPTag(geoip.dat, <tag>)`。
   - 命中时直接展开对应 CIDR（无需 DNS 解析），路径最稳定。

2. **无同名 geoip 时走 geosite 域名展开缓存**
   - 从 `geosite_expand_cache(tag, geodata_ver)` 读取可解析种子域名（仅 `domain/full`，跳过 `keyword/regex`）。
   - 缓存 miss 时从按版本缓存的内存 geosite matcher 取该 tag 的条目（不再重新解析整个 `geosite.dat`）并回填缓存。

3. **域名解析缓存（按 resolver_group 分组）**
   - `domain_resolve_cache` key：`remote:<domain>` / `local:<domain>`。
   - 通过 `dig +noall +answer @127.0.0.1` 查询本机 Mosdns，取应答中最小的 A 记录 TTL（`+short` 会丢弃 TTL，此前所有域名都落到 300s 下限）。
   - TTL clamp 到 `300~3600s`；刷新失败有旧缓存时走 stale fallback。

4. **domain -> CIDR 提升与锁定**（仅对 geosite 展开出的域名；面板里直接添加的 `domain` 规则解析后按 `/32` 主机路由发布）
   - 对解析出的 IP 并发执行 `queryGeoIPBestCIDRsByIP()`。
   - 匹配策略是**高位优先**：命中更高位前缀后不再继续低位。
   - 同一 domain 先做 `reduceCIDRsPreferBroad()`；有结果则写入 `domain_geoip_lock(...)` 复用。

5. **全局收敛与下发**
   - 在 `syncStaticRoutesToOSPF()` 做全局 `pruneStaticRoutesPreferBroad()`。
   - 被更广网段覆盖的低位前缀会被剔除，再写入 `routes_table` 并增量下发 FRR。

6. **DB/FRR 状态对齐与热路径优化**
   - `reconcilePublishedRoutesWithFRR()` 周期对齐 `routes_table.status`（默认 45s），先读完游标再开写事务。
   - `ensureRouteCacheTables()`、`ensureDomainGeoIPLockTable()` 与 `getGeoDataVersion()` 已做去重与缓存（每个 `*sql.DB` 只执行一次 DDL）。
   - geosite/geoip matcher 按 geodata 版本缓存，重建在锁外进行，`hasGeoSiteTag`/`extractGeoSiteValues`/`extractGeoIPs*` 全部从 matcher 取数。
   - `readIntSettingWithDefault` 只在缺行或值变化时写库；OSPF 控制循环只在 Mode B/C 读取参数并遵守 `push_interval_seconds`。
   - `/api/status` 的版本/OS/提交信息缓存 60s；`/api/connections` 用一次 `IN (...)` 查询加 10s 刷新的反向索引做规则关联。

> 说明：OSPF 是三层协议，无法直接广播域名对象；Mode C 的本质是把域名规则稳定映射为 CIDR 集。
## 🧹 OSPF 脏路由过滤与清理机制（v1.5.19）

为避免将无效前缀注入 FRR/OSPF（引发黑洞、回环或无意义路由），后端对 `routes_table` 与静态路由同步链路实施了统一过滤策略。

### 1) 统一归一化入口

系统统一通过 `normalizeRouteKey()` 做路由键归一化与合法性判定：
- 单 IP 自动标准化为 `/32`（例如 `8.8.8.8 -> 8.8.8.8/32`）
- CIDR 统一转为标准网络地址（例如 `8.8.8.9/24 -> 8.8.8.0/24`）
- 非 IPv4、格式错误、非法掩码直接拒绝

### 2) 脏路由判定规则（isDirtyRouteIPv4）

以下前缀被定义为脏路由，禁止入库与下发：
- `0.0.0.0/32` 与裸 `0.0.0.0`
- `0.0.0.0/0`（默认路由，不允许通过 OSPF 静态注入链路发布）
- `127.0.0.0/8`（Loopback）
- `169.254.0.0/16`（Link-local）
- `198.18.0.0/15`（Fake-IP 基准测试网段，由 Mode B 自身宣告，不得混入静态路由）
- `224.0.0.0/4` 及以上（Multicast / Reserved / Broadcast）
- **RFC1918 私网超集 (Supernets)**：如 `192.168.0.0/16` 等。为了安全，系统禁止将整个私网大网段通过 OSPF 宣告给主路由，必须使用更细粒度的子网或主机路由。违规项将被标记为 `failed_policy` 状态。

### 3) 受保护节点地址排除（防 OSPF 回弹环路）

系统会自动收集并保护以下地址，不允许进入 OSPF 静态发布集合：
- `nodes.address`（启用节点）
- `remote_nodes.ssh_host`
- `remote_node_wg.endpoint`
- `remote_node_vless.dest`

若以上字段是域名，后端会解析为 IPv4 后加入保护集。命中保护集的候选路由会被跳过，并记录日志：
`[OSPF] skipped <N> protected endpoint routes to avoid loop`。

### 4) 模式切换 Preflight 拦截

切换到 `Mode B/C` 前，后端会做预检查：

`candidate_ospf_routes ∩ protected_node_ips`

若存在交集，模式切换会被直接拒绝，返回明确错误：
`mode switch blocked: protected endpoint route conflict ...`。

这可以在配置生效前就阻断“网关流量被主路由回踢”的闭环。

### 5) 两层防护

- **写入前防护**：`/api/rules` 中 `type=ip` 的值改为复用 `normalizeRouteKey()` 校验，脏路由在 API 层直接拒绝。
- **下发前防护**：`syncStaticRoutesToOSPF()` 在归一化后再做“受保护地址排除”，防止历史数据或其他来源绕过 API 进入下发链路。

### 6) 启动自愈清理

启动时执行 `purgeDirtyRoutesTable()`：
- 扫描 `routes_table.ip`
- 对无法通过 `normalizeRouteKey()` 的记录做事务性删除
- 记录清理日志：`[OSPF] purged <N> dirty routes from routes_table`

这保证升级后即使存在历史脏数据，也会在服务启动阶段被自动剔除，不再反复参与 OSPF 同步。

## 🛡️ 系统安全沙箱 (Systemd Hardening)

后端守护进程 `proxygw.service` 运行在受限的 Systemd 权限沙箱中（`xray.service` / `mosdns.service` 目前没有沙箱指令，只设置了资源上限与重启策略）：
- `ProtectSystem=strict`: 锁定整个底层 Linux 文件系统为只读。
- `ReadWritePaths=-/root/proxygw -/usr/local/bin -/etc/frr -/etc/nftables.conf /proc/sys/net/ipv4/conf`: 基于最小权限原则，仅放开当前服务必要的读写路径（最后一项供禁用 ICMP 重定向的 sysctl 写入）。
- `ProtectKernelTunables=yes` / `ProtectControlGroups=yes` / `RestrictSUIDSGID=yes`: 其余内核可调项、cgroup 树只读，禁止创建 SUID/SGID 文件。
- `RuntimeDirectory=proxygw` + `RuntimeDirectoryPreserve=yes`: `ProtectSystem=strict` 下 `/run` 只读，Xray 访问日志与连接追踪使用的 `/run/proxygw` 由 systemd 创建并在后端重启时保留。
- `NoNewPrivileges=yes`: 彻底阻断任何形式的 SUID 提权操作。
- `PrivateTmp=yes`: 隔离系统临时文件空间。

## 📦 供应链防投毒 (Hash Validation)

为了防止传输途中被篡改的二进制进入 root 运行的服务，后端与脚本在更新时实施了以下校验（全部直连 GitHub 官方地址）：
- **Xray 更新**：拉取与当前架构资产同名的 `.dgst`（`Xray-linux-64.zip.dgst` / `Xray-linux-arm64-v8a.zip.dgst`），比对 SHA2-256；不符即丢弃。
- **mosdns 更新**：以 GitHub Release API 中该资产的 `digest` 字段校验。
- **GeoData 更新**：拉取 `rules.zip.sha256sum` 校验；release tag 必须匹配 `^[0-9A-Za-z._-]{1,64}$` 才会拼进 URL；文件先写到 `.tmp` 再 `rename` 覆盖，Xray/mosdns 不会读到半截文件。
- **安装前冒烟**：解压出的 `xray`/`mosdns` 先执行 `version`，跑不起来的二进制不会替换线上文件；替换失败自动恢复 `.bak`。
- **GitHub 交互**：所有 API 调用检查 HTTP 状态码（限流的 403 不再被当成空结果），远程文本限制 4MB；同一时间只允许一个 HTTP 更新请求（其余 409）。
- **后端二进制（脚本）**：`install.sh` / `update.sh` 必须取得发布页 `SHA256SUMS` 并校验通过，否则中止（`PROXYGW_ALLOW_UNVERIFIED=1` 可显式跳过）。

已知边界：`install.sh` 首次安装 Xray 时若取不到 `.dgst` 只告警不中止，且不校验 mosdns 的初始下载；应用内更新路径没有这两个缺口。

## 🔁 配置下发与重启策略

- **mosdns**：`applyMosdnsConfig()` 先渲染 `config.yaml` 与 `proxy_domains.txt`，仅在内容与磁盘不同时写入，仅在有变化或服务未运行时重启。规则重排、direct 策略域名不会触发重启。Mode B 下 Xray 下发附带的 FakeIP 缓存刷新重启在 5s 内去重。
- **Xray**：规则/节点变更走动态路径（`xray api adrules/rmo/ado`），配置文件同步落盘；节点编辑/停用/删除只重同步该节点 tag 的出站。启动时若渲染结果与磁盘一致且服务运行中则跳过重启（`PROXYGW_FORCE_RESTART_ON_BOOT=1` 强制重启 Xray）。`/api/apply` 默认 `dynamic_xray=true`，只有热更新失败或显式传 `false` 才 `systemctl restart xray`；模式切换始终重启。GeoData 更新（定时或手动）无条件重启 mosdns 与 xray。
- **失败补偿**：设备分流、保护 IP、DNS 设置在下发失败时回滚数据库变更；动态路径失败时 3 秒防抖后回退为完整 apply（定时器使用独立的 `applyTimerMu`，不再与跨 `systemctl restart` 持有的 `applyMutex` 争抢）。
- **测试 seam**：`restartMosdnsFn`、`restartXrayFn`、`unitActiveFn`、`runXrayAPI`、`applyNftablesConfigFn`、`applyMosdnsConfigFn`、`probeVersion`、`probeUnitActive`、`runJournalctl`、`runDig` 等包级函数变量可在测试中替换。

### 关于 `config/` 下的示例文件

`config/xray-example.json` 与 `config/mosdns-example.yaml` 只是 **Mode B** 形态的示意（含 fakedns / `forward_fakeip` 与示例 geosite 规则），实际配置由后端按当前模式与规则表生成：Mode A/C 没有 FakeDNS/FakeIP，新实例默认不注入任何 geosite 规则。

## 🔧 系统内核级调优

安装脚本已在 `/etc/sysctl.d/99-proxygw.conf` 自动完成了适用于透明代理网关的内核调优：
- 开启 BBR 拥塞控制与 fq 队列调度 (`net.ipv4.tcp_congestion_control = bbr`)。
- 开启 TCP Fast Open 及 TCP Tw Reuse 优化短连接性能。

## 📊 `geoip:!cn` OSPF 展开性能测试（v1.5.14 历史数据）

> 下表数据来自 v1.5.14 的逐字节扫描实现。现在 `extractGeoIPs*` 直接从按版本缓存的 matcher 取数，首次构建 matcher 仍需完整解析一次文件，之后每次展开只剩字符串格式化开销；需要在装有 `core/mosdns/geoip.dat` 的主机上用 `./scripts/test_benchmark.sh --bench=ExtractGeoIPs`（`-benchtime=1x`）重跑才能得到当前数字。

测试环境：`Intel i7-6700T / amd64 / Debian / Go test benchmark`  
测试命令：

```bash
cd /root/proxygw/backend
go test -run '^$' -bench 'BenchmarkExtractGeoIPs' -benchmem -count=3
```

结果摘要（均值近似）：

| Benchmark | ns/op | B/op | allocs/op | 额外指标 |
|---|---:|---:|---:|---:|
| `BenchmarkExtractGeoIPsCN` | ~242ms | ~102.8MB | ~2,401,760 | - |
| `BenchmarkExtractGeoIPsExcludeCNPrivate` | ~264ms | ~144.9MB | ~2,401,779 | - |
| `BenchmarkExtractGeoIPsExcludeCNPrivate_Count` | ~262ms | ~144.9MB | ~2,401,780 | `586,504 cidr/op` |

结论：
- `!cn`（排除 `cn` 与 `private`）在当前实现下可稳定展开为约 **58.6 万** 条 CIDR，用于 B/C 模式静态路由同步。
- 相较 `geoip:cn`，反向展开额外开销约 `+20ms/op` 与 `+42MB/op`，属于预期（返回集更大）。

## 🧪 测试体系

EdgeRouteGW 提供了完整的多层级自动化测试体系，所有测试脚本位于 `scripts/` 目录。

### 测试脚本概览

| 脚本 | 用途 |
|---|---|
| `test_all.sh` | **主编排器** — 按顺序执行所有 6 个阶段（后端 → 竞态检测 → 覆盖率 → 前端 E2E → 构建 → Git 状态） |
| `test_backend.sh` | 后端 Go 测试运行器（支持 `--race`、`--verbose`、`--short` 参数） |
| `test_coverage.sh` | 后端覆盖率报告生成器（输出文本摘要 + HTML 可视化报告到 `coverage/`） |
| `test_benchmark.sh` | 基准测试运行器（支持 `--bench=Pattern` 筛选，`--count=N` 重复） |
| `test_frontend.sh` | 前端 Playwright E2E 按钮测试 |
| `pre-commit.sh` | Git pre-commit hook，提交前自动编译检查 + 短测试 |

### 快速入门

```bash
# 在项目根目录运行全部测试
./scripts/test_all.sh

# 仅运行后端测试
./scripts/test_backend.sh

# 运行后端测试 + 竞态检测
./scripts/test_backend.sh --race

# 运行基准测试
./scripts/test_benchmark.sh --bench=QueryGeoIP
./scripts/test_benchmark.sh --bench=. --count=3

# 生成覆盖率报告
./scripts/test_coverage.sh
# 浏览器打开 coverage/coverage.html 查看可视化结果
```

### 后端测试统计（当前）

- **测试总数**: 230+ 个功能测试，覆盖 API/OSPF/Rules/DNS/连接追踪/远程部署等模块
- **基准测试**: 11 个（`ExtractGeoIPs*` ×3、`QueryGeoIPTagsByIP`、`BuildBaseXrayConfig*` ×4、`ValidateSession`/`CreateSession`、`AttachRuleMatchMeta`；GeoIP 系列需要真实 `.dat` 文件，缺失时自动跳过）
- **代码覆盖率**: ~63%（backend 主包）
- **竞态检测**: 全部通过（`go test -race`）
- **测试套件**: `setupFeatureSuiteRouter` HTTP 集成测试（`t.TempDir()` 下通过 `openSQLite` 打开的文件数据库 + 种子数据，与生产相同的连接池/PRAGMA 配置）

### 安装 pre-commit hook

Git 钩子不随仓库分发，克隆后需要手动安装：

```bash
cp scripts/pre-commit.sh .git/hooks/pre-commit
chmod +x .git/hooks/pre-commit
```

hook 会在每次 `git commit` 前执行后端编译验证；若暂存了任何 `.go` 文件，再运行整个后端套件的 `-short` 测试。如需绕过：

```bash
git commit --no-verify
```

### 测试约定

1. **函数变量 mock 模式**: 对于外部系统调用（DNS 解析、SSH 执行等），后端使用函数变量模式（`var resolveDomainIPv4WithTTL = func(...)`）以便在测试中进行 mock。所有 mock 完成后需在 `defer` 中恢复。
2. **`setupFeatureSuiteRouter`**: 集成测试使用此函数创建带临时 SQLite 文件数据库 + seed 数据的 Gin 路由，并重置会话、登录计数、事件节流与 apply 定时器等全局状态。每个测试独立运行，互不干扰。
3. **临时目录隔离**: 每个测试通过 `t.TempDir()` 获得独立工作目录，避免文件系统冲突。
4. **benchmark 格式**: 基准测试使用 `go test -bench` 标准格式，输出直接对接 `benchstat` 进行回归分析。
