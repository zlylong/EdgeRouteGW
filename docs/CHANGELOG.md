## [Unreleased]

### ✨ 新增 (Features)
- 新增数据库一键优化脚本 `scripts/db_optimize.sh`：
  - 自动备份 `proxygw.db`（时间戳命名）
  - `--index-only`：幂等创建关键索引 + `ANALYZE` + `PRAGMA optimize`
  - `--full`：在上述基础上执行 `VACUUM`（用于维护窗口）
- 安装/更新脚本自动接入低风险优化：`install.sh` 与 `update.sh` 在服务启动后自动尝试执行 `db_optimize.sh --index-only`（存在性检查 + 失败不阻断主流程）。

### ⚡ 性能与优化 (Optimizations)
- 补齐 `domain_geoip_lock` 访问路径索引：
  - `idx_dgl_domain_resolver_ver (domain, resolver_group, geodata_ver)`
  - 避免查询计划仅命中复合主键前缀导致的过滤不充分。
- 补齐 `gateway_events` 常用筛选路径索引：
  - `idx_gateway_events_module_level_id (module, level, id DESC)`
  - 降低按模块/级别倒序分页查询时的全表扫描概率。

### 📝 文档 (Docs)
- `docs/OPERATIONS.md` 新增数据库优化章节：触发信号、`--index-only/--full` 使用建议、维护窗口与锁影响说明。
- `README.md` 与运维文档补充“安装/升级后自动低风险 DB 优化”说明。

## [1.6.15] - 2026-04-29
### 🚀 稳定版发布 (Stable)
- 发布 v1.6.15 Stable，上线 OSPF 一键重置 Pending Set（仅清理 candidate/static），降低误操作恢复成本且不影响后续规则管理。

### ✨ 新增 (Features)
- OSPF 新增一键重置 Pending Set 操作入口（前端按钮 + 后端接口）。
- 新增接口 /api/ospf/reset_pending，支持 confirm=APPLY 执行确认。

### 🔒 安全与保护 (Safety)
- 接口纳入高危变更门禁（requireHighRiskMutationGuard）与 dry-run。
- 重置范围严格限定为 routes_table 中 status=candidate 且 source=static，不触碰已发布路由集。

## [1.6.14] - 2026-04-29
### 🚀 稳定版发布 (Stable)
- 发布 `v1.6.14 Stable`，补全 GeoIP/Geosite 标签校验回归测试并完成全量测试链路验证。

### ✅ 测试 (Tests)
- 新增 API 集成测试：`geoip=FASTLY`（含首尾空白）应被识别为有效标签并允许写入规则。
- 扩展测试 geodata 夹具：补充 `fastly` 的 GeoIP 与 Geosite 测试标签，覆盖标签存在性校验路径。
- 执行完整验证：`cd backend && go test ./...` 与 `scripts/check-release-chain.sh` 全部通过。

## [1.6.13] - 2026-04-25
### 🚀 稳定版发布 (Stable)
- 发布 v1.6.13 Stable，修复实时连接追踪高负载，并清理三模式下 Xray/Mosdns 不适配配置。

### 🐛 修复 (Fixes)
- 连接追踪关联原因增强：为未命中规则补充明确原因字段，便于快速定位规则未关联根因。
- 连接追踪 CPU 热点优化：geosite 匹配改为缓存化按需匹配，避免实时轮询时重复重开销扫描导致 CPU 飙升。
- 模式配置清理：按 Mode A/B/C 收敛 Xray 与 Mosdns 生成项，删除无效及模式不适配配置（仅 Mode B 启用 FakeDNS/FakeIP）。
- Mode C 路由收敛：同步时清理 FRR 历史孤儿 tag100 路由，避免长期残留污染发布集。

### 📝 文档 (Docs)
- 文档统一说明 domain 规则语义（c.com/**.c.com/*.c.com）并对齐三模式 DNS/OSPF 约束。

## [1.6.12] - 2026-04-24
### 🚀 稳定版发布 (Stable)
- 发布 `v1.6.12 Stable`，按用户选择启用 Mode A 的“禁用 QUIC 以稳定 geosite 命中”策略。

### 🐛 修复 (Fixes)
- **Mode A 禁用 QUIC（UDP/443）**：新增高优先级路由规则 `mode-a-disable-quic`，将 `tproxy_in` 的 UDP/443 流量拦截，强制客户端回落 TCP/TLS。
- **稳定性提升**：避免 `geosite:anthropic` 在 QUIC 场景下因域名不可见导致落入 `default-fallback=direct` 的抖动现象。
- **回归测试**：新增 Mode A/Mode B 规则断言，确保仅 Mode A 注入 QUIC 阻断规则且顺序正确。

## [1.6.11] - 2026-04-24
### 🚀 稳定版发布 (Stable)
- 发布 `v1.6.11 Stable`，修复 Mode A 下 `www.anthropic.com` 等 HTTP/3(QUIC) 流量未命中 geosite 规则的问题。

### 🐛 修复 (Fixes)
- **QUIC 嗅探补齐（Mode A）**：`tproxy_in.sniffing.destOverride` 增加 `quic`，避免 UDP/443 流量仅按 IP 落入 `default-fallback=direct`。
- **Mode B 同步一致性**：Mode B 的 sniffing 同步补齐 `quic`（保留 `fakedns`），避免模式切换后行为不一致。
- **回归测试**：新增 base xray 配置测试，校验 Mode A/Mode B 均包含 QUIC sniffing。

## [1.6.10] - 2026-04-24
### 🚀 稳定版发布 (Stable)
- 发布 `v1.6.10 Stable`，按用户目标修正 Mode A 默认行为：仅命中规则流量走代理，其余默认直连。

### 🐛 修复 (Fixes)
- **Mode A 兜底策略修正**：`default-fallback` 在 Mode A 下默认固定为 `direct`（仅 `lan_default_policy=block` 时为 `block`），避免把未命中规则流量整体送代理。
- **规则语义对齐**：保留 geosite/domain 命中代理能力（如 `geosite:anthropic -> proxy-3`），同时无规则流量按直连处理，符合“只代理已配置规则”预期。
- **回归测试**：新增并覆盖 Mode A/Mode B 兜底行为测试，防止后续回归到全量代理。

## [1.6.9] - 2026-04-24
### 🚀 稳定版发布 (Stable)
- 发布 `v1.6.9 Stable`，修复 Mode A 下 Xray 未命中规则时默认回落 direct 导致 `anthropic.com` 等站点直连的问题。

### 🐛 修复 (Fixes)
- **Xray 默认兜底路由修复（Mode A）**：在动态生成路由规则末尾追加 `default-fallback`（`network: tcp,udp`），按 `lan_default_policy` 决定默认出口，避免未命中时回落到 outbounds 首项 `direct`。
- **策略一致性**：当 `lan_default_policy=proxy` 且存在可用代理节点时，兜底流量默认进入 `proxy-*`（单节点时固定到默认节点），确保与网关策略一致。
- **回归测试**：新增 `TestApplyXrayConfigAddsDefaultFallbackByLanPolicy`，覆盖兜底规则生成与出站选择逻辑，防止回归。

## [1.6.8] - 2026-04-24
### 🚀 稳定版发布 (Stable)
- 发布 `v1.6.8 Stable`，修复 Mode A 下 geosite 规则（如 anthropic）未进入 Mosdns 代理域集导致未走代理的问题。

### 🐛 修复 (Fixes)
- **Mosdns/Xray 对接修复（Mode A）**：将 `geosite` 且 `policy=proxy` 的规则同步展开到 `core/mosdns/proxy_domains.txt`，确保 DNS 分流与 Xray 路由一致。
- **动态应用修复**：新增/删除 `geosite` 规则时，和 `domain` 一样触发 Mosdns 配置应用，避免规则生效不完整。

## [1.6.7] - 2026-04-24
### 🚀 稳定版发布 (Stable)
- 发布 v1.6.7 Stable，优化透明代理嗅探策略。

### ⚡ 性能与优化 (Optimizations)
- **嗅探优化 (Xray Sniffing)**：在 Mode A (TProxy) 模式下开启 `routeOnly: true`。Xray 将仅使用嗅探出的域名进行路由分流决策（如 geosite 匹配），不再强行改写原始目标地址，显著提升透明代理环境下的连接稳定性。
- **远程节点同步**：同步更新远程节点部署脚本，为其 VLESS Reality 入站配置开启 `routeOnly: true`，确保内外环境行为一致。


## [1.6.6] - 2026-04-24
### 🚀 稳定版发布 (Stable)
- 发布 v1.6.6 Stable，修复连接追踪规则匹配 bug 与 HA 策略保存问题。

### 🐛 修复 (Fixes)
- **连接追踪**：修复  在匹配存储为  格式的路由表记录时失效的问题，确保  和  能正确关联。
- **策略保存**：后端规则创建接口允许  格式的路由策略，修复添加 HA 分流规则时报  的问题。

### ✨ UI 优化 (UI)
- **规则列表布局**：将“按组筛选”控件移至“规则类型”行内，优化界面空间利用率；隐藏“分组”列，降低表格拥挤度。
# ProxyGW Changelog

## [1.6.5] - 2026-04-23
### 🚀 稳定版发布 (Stable)
- 发布 `v1.6.5 Stable`，增强域名匹配语义并优化 UI 观测体验。

### ✨ 新特性 (Features)
- **域名通配符语义增强**：全面支持 `**.c.com`（匹配根域及任意层级子域）与 `*.c.com`（仅匹配根域及单层子域）规则。
- **Mosdns 单层通配符分流**：在 Mosdns 侧同步实现 `*.c.com` 的正则匹配，确保 Mode B (Fake-IP) 模式下的解析分流与 Xray 语义对齐。
- **连接追踪深度关联**：实时连接表格中的“规则编号”支持点击跳转，点击后自动定位至对应的分流规则。

### ⚡ 性能与 UI 优化 (Performance & UI)
- **UI 界面精简**：移除“实时连接追踪”中的冗余“路由策略”列，将策略名称（如 Proxy/Direct）合并至“规则匹配值”下方小字显示，信息更聚焦。
- **路径硬编码修正**：修复 `path_helper.go` 中对 `/root/proxygw` 的硬编码依赖，确保在 `proxygw_full` 等非默认路径下静态资源加载的稳定性。

## [1.6.4] - 2026-04-23
### 🚀 稳定版发布 (Stable)
- 发布 `v1.6.4 Stable`，聚焦可观测性增强与 Mode B OSPF 路由口径纠偏。

### ✨ 新特性 (Features)
- **全景流量改为自然月口径**：流量大盘改为“本月累计”，并新增按自然月统计的节点流量排行。
- **核心系统日志增强**：新增网关事件流、告警事件流、TProxy 命中计数视图，支持审计事件与节流告警可视化。

### 🐞 修复 (Bug Fixes)
- **Mode B 路由口径修正**：Mode B 不再对 `domain/geosite` 做 DNS->OSPF 展开；仅发布 FakeIP 网段与 `ip/geoip` 静态路由，避免无效解析和错误宣告。

## [1.6.3] - 2026-04-23
### 🚀 稳定版发布 (Stable)
- 发布 `v1.6.3 Stable`，聚焦域名规则处理链路文档化与 OSPF 热路径开销优化。

### ⚡ 性能优化 (Performance)
- **Route Cache 表初始化去重**：`ensureRouteCacheTables()` 改为“每个 DB 实例仅初始化一次”，移除同步热路径中的重复 DDL/迁移触发。
- **GeoData 版本读取缓存**：`getGeoDataVersion()` 增加内存缓存与文件签名检测，仅在 `geodata.ver` / `geosite.dat` 变化时刷新，降低频繁文件 I/O。
- **FRR 对齐降频**：`reconcilePublishedRoutesWithFRR()` 周期从 `15s` 调整为 `45s`，减少 `show running-config` 全量读取开销。

### 🐞 修复 (Bug Fixes)
- **默认分流规则移除**：初始化阶段不再自动注入 `geosite:cn` / `geosite:category-ads-all` / `!cn`，新实例规则列表默认空白，避免隐式策略污染。

### 📝 文档 (Docs)
- **域名处理算法文档更新**：`docs/DEVELOPER.md` 新增 Mode B/C 下 `domain/geosite` 从解析缓存、GeoIP 提升、锁定、全局裁剪到 OSPF 下发/FRR 对齐的完整链路说明。
- **规则使用文档更新**：`docs/ROUTING_RULES_GUIDE.md` 明确 `v1.6.3` 起不再注入默认规则，规则由用户显式定义。

## [1.6.2] - 2026-04-22
### 🚀 稳定版发布 (Stable)
- 发布 `v1.6.2 Stable`，合并 `rc.1~rc.3` 与后续优化修复。

### ⚡ 性能优化 (Performance)
- **GeoIP 匹配短路**：按高位优先（/8 → /16 → /24），命中更高位后停止低位匹配。
- **CIDR 收敛**：规则同步阶段全局执行“广网段优先裁剪”，自动剔除被上级网段覆盖的低位路由（如 /24、/32）。
- **路由爆量抑制**：修复历史 `domain_geoip_lock` 非 CIDR 锁值导致的全标签展开问题。

### 🐞 修复 (Bug Fixes)
- **规则页输入稳定性**：修复“分流规则匹配值输入框被轮询刷新打断”问题，输入聚焦期间暂停规则列表刷新。
- **GeoIP 锁兼容性**：读取锁时仅接受可规范化 CIDR；自动清理遗留无效锁记录，避免旧数据污染新算法。

## [1.6.2-rc.3] - 2026-04-22
### ✨ 新特性 (Features)
- **geosite→geoip 网段锁定**: 对 `geosite` 域名解析结果新增“命中 geoip 后提升到整网段并锁定”的机制，按 `geodata` 版本维度持久化，显著降低高波动域名导致的 OSPF 路由抖动。
- **版本感知重评估**: 锁定记录绑定 `geodata` 版本；当 geodata 更新后自动触发重新学习，避免旧网段长期固化。

### 🐞 修复 (Bug Fixes)
- **未命中 geoip 回退单 IP**: 对无法命中 geoip tag 的解析结果保持单 IP 发布，确保兼容性与覆盖完整性。

### 📦 发布 (Release)
- **预发布**: 发布 `v1.6.2-rc.3`，用于灰度验证 geoip 网段锁定与 OSPF 收敛稳定性。

## [1.6.2-rc.2] - 2026-04-22
### 🐞 修复 (Bug Fixes)
- **发布流程 RC 识别**: `release.yml` 在 tag 含 `-rc.` 时自动标记 `prerelease: true`，并使用 `Pre-release` 标题，避免 RC 误标为 Stable。

### 📦 发布 (Release)
- **预发布**: 发布 `v1.6.2-rc.2`，用于验证动态下发与分级 apply。

## [1.6.2-rc.1] - 2026-04-22
### ⚡ 性能优化 (Performance)
- **规则变更动态下发（A/B/C）**: `/api/rules` 增删改优先走 Xray Routing API 动态更新，避免每次规则变更都重启 Xray，显著降低流量抖动。
- **节点变更动态下发**: `/api/nodes` 的新增、编辑、删除、启停、设默认、导入优先走 Xray Handler/Routing API 动态更新；失败自动回退到原有调度重载。
- **Apply 分级执行**: `/api/apply` 新增可选参数 `mosdns` / `xray` / `dynamic_xray`，支持仅重载必要组件，避免无差别双重重启。

### 🐞 修复 (Bug Fixes)
- **动态策略映射稳定性修复**: 运行时策略 `proxy/ha` 解析为具体 `proxy-<id>-out`，避免动态下发阶段出现 `balancer ... not found`。
- **测试/冷启动容错**: 当 Xray 运行时未就绪时，动态路径会安全回退，避免在未部署完整二进制的环境触发无意义重载。

### 📦 发布 (Release)
- **预发布**: 发布 `v1.6.2-rc.1` 供灰度验证动态下发能力。

## [1.6.1] - 2026-04-22
### 🐞 修复 (Bug Fixes)
- **模式切换卡死修复**: 优化 Mode B/C 切换前置冲突检查路径，不再在 `/api/mode` 请求内触发 geosite 全量解析，避免在大规则集场景下请求长时间阻塞或超时。
- **DNS 解析超时收敛**: 为 `host` 查询加 5 秒超时控制，防止异常 DNS 查询无限占用切换链路。
- **更新脚本标签冲突修复**: `scripts/update.sh` 改为 `git fetch --force origin --tags`，修复稳定标签重指向后出现 `would clobber existing tag` 导致更新中断。
- **前端模式切换可观测性增强**: 模式切换失败时直接展示后端返回错误（HTTP 状态/具体 error），消除静默失败。

### 📦 发布 (Release)
- **稳定版发布**: 发布 `v1.6.1 Stable`，同步前端版本与安装/更新脚本 fallback 版本号。

## [1.6.0] - 2026-04-22
### 🚀 发布 (Release)
- **稳定版发布**: 发布 `v1.6.0 Stable`，同步前端版本显示、安装脚本与更新脚本 fallback 版本号。

### 🐞 修复 (Bug Fixes)
- **数据库迁移补齐**: 启动初始化新增 `lan_acls` 表与 `settings.lan_default_policy` 默认值，修复部分环境下 `/api/lan_acls` 500 连带影响页面状态的问题。

### 📝 文档 (Docs)
- **OpenWrt 防环路指引固化**: 延续并固化主路由源地址旁路（PBR）方案，用于避免 `ProxyGW -> 主路由 -> ProxyGW` 回弹环路。
- **运维排障口径统一**: 明确“应用层发布计数”与“主路由 OSPF 实际路由”可能存在短时/口径差异，排障以 FRR/主路由实时路由表为准。

## [1.5.21] - 2026-04-22
### 📝 文档 (Docs)
- **OpenWrt 防环路手册补齐**: `docs/OPENWRT_SETUP.md` 新增 Mode C 防环路 PBR 章节，提供“临时止血 + 热插拔持久化”两套配置，并给出验证命令。
- **运维文档扩展 OpenWrt 操作**: `docs/OPERATIONS.md` 的回弹环路章节增加 OpenWrt PBR 实操模板，覆盖 `ip rule` / `table 100` / `hotplug` 触发方式，便于跨网络环境复用。

## [1.5.20] - 2026-04-22
### 🐞 修复 (Bug Fixes)
- **OSPF 节点回弹环路防护**: 新增“受保护节点地址排除”机制，自动收集并排除 `nodes.address`、`remote_nodes.ssh_host`、`remote_node_wg.endpoint`、`remote_node_vless.dest`（含域名解析 IP），避免将上游节点地址反向宣告到 OSPF 形成闭环。
- **模式切换预检拦截**: 切换到 Mode B/C 前执行 `candidate_ospf_routes ∩ protected_node_ips` 交集检查；命中即阻断切换并返回明确冲突错误，防止配置生效后才出现全网抖动。
- **静态路由收集链路收敛**: 抽象统一的 OSPF 候选路由构建逻辑，确保“模式切换预检”和“定时同步下发”使用同一口径，避免策略漂移。

### 📝 文档 (Docs)
- **开发文档增强**: `docs/DEVELOPER.md` 增补“受保护节点地址排除”“模式切换 Preflight 拦截”与日志观测说明。
- **运维手册增强**: `docs/OPERATIONS.md` 增补 `proto static metric 20` 回弹环路判定与系统内置防护说明，附主路由 PBR 兜底配置模板。

### ✅ 测试 (Tests)
- 新增“模式切换预检拦截受保护地址冲突”测试。
- 新增“静态路由同步排除受保护节点地址”测试。

## [1.5.19] - 2026-04-22
### 🐞 修复 (Bug Fixes)
- **OSPF 脏路由硬过滤**: 路由归一化入口 `normalizeRouteKey()` 增加脏路由判定，拒绝 `0.0.0.0` / `0.0.0.0/32` / `0.0.0.0/0`、`127.0.0.0/8`、`169.254.0.0/16`、`224.0.0.0/4+` 等无效前缀，避免黑洞与无意义 OSPF 发布。
- **规则校验与下发链路统一**: `/api/rules` 的 `type=ip` 校验改为复用同一归一化逻辑，确保“可保存”与“可下发”口径一致，防止脏数据进入 `routes_table`。
- **历史脏数据启动自愈**: 新增启动阶段 `purgeDirtyRoutesTable()`，自动扫描并事务删除脏路由，升级后无需手工逐条清理。

### 📝 文档 (Docs)
- **开发文档补齐**: `docs/DEVELOPER.md` 新增“OSPF 脏路由过滤与清理机制”，明确判定规则、双层防护与启动清理流程。
- **运维文档补齐**: `docs/OPERATIONS.md` 新增异常前缀排障章节，提供日志核查、SQL 检查与一键重启清理指引。

### ✅ 测试 (Tests)
- 补充 `normalizeRouteKey` 与 `isValidIPOrCIDR` 的脏路由用例覆盖（`0.0.0.0`、`/0`、loopback、link-local、multicast 等）。

## [1.5.18] - 2026-04-22
### 🐞 修复 (Bug Fixes)
- **OSPF 发布状态一致性修复**: OSPF 控制器改为“`vtysh` 成功后再更新数据库状态”。`ADD` 失败不再误标 `published`，`DEL` 失败不再提前删库，彻底消除 DB 已发布但 FRR/ROS 未生效的状态漂移。
- **FRR mgmtd 批处理兼容修复**: 修复 `vtysh -f` 批文件包含 `conf t` 导致 `Unknown command[4]` 的问题，批处理改为直接下发 `ip route`/`no ip route`，确保在 mgmtd 模式可稳定应用。
- **OSPF 路由键规范化去重**: 新增统一路由键规范化（如 `1.1.1.1` 与 `1.1.1.1/32` 统一为 `1.1.1.1/32`），解决多规则命中同一 IP 时重复发布问题。

### ⚡ 性能优化 (Performance)
- **DNS 缓存迁移与清理**: 增加 `domain_resolve_cache` 旧键（无前缀）向 `remote:*` 的自动迁移与后台分批清理，减少策略切换后的缓存冷启动掉量，加快 geosite 收敛。

### ✅ 测试 (Tests)
- 新增 OSPF 下发一致性测试：覆盖 `vtysh` 成功/失败下 DB 状态不漂移。
- 新增路由规范化与跨规则去重测试。
- 新增旧 DNS 缓存键迁移与清理测试。

## [1.5.17] - 2026-04-21
### 🐞 修复 (Bug Fixes)
- **开发机 Web 服务启动阻塞修复**: 将 OSPF 静态路由同步从 `applyXrayConfig()` 启动链路中解耦，改为异步调度并合并 pending 请求，避免 `geosite` 大规模展开/DNS 刷新在进程启动时阻塞 Gin 监听端口，恢复开发机 Web 面板可访问性。
- **真实 DNS TTL 采集补强**: `domain` 缓存解析优先通过系统 `host -t A -v` 提取 `ANSWER SECTION` 中 A 记录的最小 TTL，并继续应用 `300~3600s` clamp；当 `host` 不可用或输出异常时自动回退到内置解析链路，保证服务不中断。
- **解析器健壮性增强**: `host` 输出解析器只消费 `ANSWER SECTION`，忽略 `ADDITIONAL SECTION` 等无关记录，避免把额外 A 记录误写入 OSPF 路由缓存。

### 📦 发布 (Release)
- **稳定版发布**: 发布 `v1.5.17 Stable`，同步更新前端版本号与安装/更新脚本 fallback 版本，确保 UI、脚本与 GitHub Release 对齐。

## [1.5.16] - 2026-04-21
### ✨ 新特性 (Features)
- **OSPF 增量推送控制上屏**: OSPF 页面新增 FRR `vtysh` 增量推送控制横条，可直接调节“每批最多推送条目”和“推送最短间隔（秒）”，保存后即时写入后端设置并参与控制器节流。
- **日志窗口自适应**: OSPF 增量控制器日志窗口改为随视口高度自适应，保留下方大窗口展示，避免固定高度下日志截断或空白过多。

### ⚡ 性能优化 (Performance)
- **增量推送批次放宽至 10W**: OSPF 推送条目数上限从 2000 提升到 100000，前后端同步收敛，适配大规模 CIDR/域名跟踪场景。

### 📦 发布 (Release)
- **发布链路对齐**: 同步更新前端版本号与安装/更新脚本 fallback 版本，确保 GitHub Release、UI 展示与自动化脚本一致。

## [1.5.15] - 2026-04-21
### ✨ 新特性 (Features)
- **远程节点部署自动 BBR 优化**: 在 WG/VLESS 远程部署脚本中新增 BBR 检测与启用流程。内核支持时自动写入 `net.core.default_qdisc=fq` 与 `net.ipv4.tcp_congestion_control=bbr` 并持久化；不支持时自动跳过且不中断部署。
- **远程部署增加 sudo 支持**: `ssh_user` 非 root 时，部署/健康检查/卸载命令自动通过 `sudo` 执行，兼容无密码 sudo 与密码 sudo 回退。

### 📦 发布 (Release)
- **系统级性能基准补齐**: 新增后端系统级 benchmark（Xray 配置并发构建、序列化开销、会话并发 create/validate），并同步更新首页 README 的全面性能测试章节。

## [1.5.14] - 2026-04-21
### 📦 发布 (Release)
- **性能测试数据文档化**: 新增 `geoip:!cn` OSPF 展开基准测试，结果已写入 `docs/DEVELOPER.md`（含 `ns/op`、`B/op`、`allocs/op` 与 `cidr/op`）。
- **基准测试补充**: 新增 `backend/helpers_benchmark_test.go`，用于持续验证 `extractGeoIPs` / `extractGeoIPsExclude` 性能与展开规模。

## [1.5.13] - 2026-04-21
### 📦 发布 (Release)
- **稳定版发布**: 发布 `v1.5.13 Stable`，包含 `geoip:!cn` 在 OSPF 模式下的可添加与可展开下发修复。
- **发布链路同步**: 版本号与 Changelog 对齐，供更新脚本与 GitHub Release 自动分发。

## [1.5.12] - 2026-04-21
### 🐞 修复 (Bug Fixes)
- **恢复 `geoip:!cn` 可添加能力**: 撤销上一版对 `!cn` 的错误拦截，前端可继续添加该规则。
- **OSPF 反向 GeoIP 展开实现**: 在 Mode B/C 同步阶段将 `!cn` 展开为静态 CIDR 集（默认排除 `cn` 与 `private`），并写入 `routes_table` 参与 OSPF 下发。
- **可观测性增强**: 新增展开数量日志，便于确认 `!cn` 规则是否已被后端成功展开与同步。

## [1.5.11] - 2026-04-21
### 🐞 修复 (Bug Fixes)
- **OSPF 模式下 `geoip:!cn` 规则防误用**: 修复在 Mode B/C 中添加 `!cn` 后“规则存在但路由不下发”的静默失败问题。后端现对该组合直接拒绝并返回明确错误提示。
- **静态路由同步逻辑纠偏**: `syncStaticRoutesToOSPF` 仅处理 `geoip` 规则，不再错误混入 `geosite`；对反向 geoip 标签输出显式日志，便于排障。

## [1.5.10] - 2026-04-21
### 🐞 修复 (Bug Fixes)
- **GeoIP 新增 `!cn` 规则支持（前端可见）**: 修复规则分类接口未暴露虚拟 GeoIP 标签的问题，`/api/rules/categories` 现在会返回 `!cn`，可在 UI 中直接选择并下发 `geoip:!cn` 分流规则。
- **规则分类返回安全性修复**: 分类接口返回值改为副本，避免缓存切片被意外改写带来的并发副作用。

## [1.5.9] - 2026-04-21
### 🐞 修复 (Bug Fixes)
- **新部署机器 FRR 配置页 404 修复**: 统一发布与部署编译方式为 `go build -o proxygw-backend .`（编译整个 backend 包），避免误用单文件编译导致路由注册缺失/接口不可用，确保 `/api/config/frr` 在新机器可正常访问。
- **运维文档与发布链路对齐**: 明确 systemd 二进制路径与编译产物名称一致性检查，降低“重启成功但仍跑旧二进制”的误判风险。

## [1.5.8] - 2026-04-20
### 📦 发布 (Release)
- **稳定版补发**: 补发 `v1.5.8 Stable`，修正 GitHub Release 标题与更新说明，确保 Release 页面与 `docs/CHANGELOG.md` 1:1 对齐。
- **发布工作流修复**: 修复 `.github/workflows/release.yml`，改为从 `docs/CHANGELOG.md` 自动提取对应版本段落作为 Release Notes，并统一使用 `ProxyGW <VERSION> Stable` 标题发布。

## [1.5.7] - 2026-04-20
### 🐞 修复 (Bug Fixes)
- **三模式配置纠偏**: 修复 Mode A 误启用 FakeDNS/FakeIP 的错误。现在仅 Mode B 使用 FakeIP/FakeDNS；Mode A 与 Mode C 均恢复为真实远程 DNS 解析，避免 Mode A 下 LAN ACL 旁路设备命中 FakeIP 黑洞。
- **模式切换原子回滚**: 重构 `/api/mode` 切换流程，改为按步骤应用 `nftables` / `FRR` / `Mosdns` / `Xray`，任一步失败即自动回滚到旧模式，避免数据库模式、路由状态和运行配置出现半切换污染。
- **模式回归测试补齐**: 新增三模式配置与切换回归测试，覆盖 FakeDNS/FakeIP 归属、切换失败自动回滚、切换成功后路由状态收敛，防止后续再把 Mode A 配坏。

## [1.5.6] - 2026-04-20
### 🐞 修复 (Bug Fixes)
- **远程节点创建恢复**: 修复 `backend/main.go` 中被破坏的 SQLite migration，恢复 `remote_nodes.ssh_host_key` 自动补列逻辑，解决新建远程节点时报 `Failed to insert node` 的问题。
- **远程节点参数重部署自愈**: 修复远程节点首次部署失败后再次重试时，`remote_node_wg` / `remote_node_vless` 仅执行 `UPDATE` 导致 0 行更新的问题。现改为 `UPSERT`，确保重部署后分享链接、端口、Reality/WG 参数都能正确落库。
- **远程节点导入到网关列表**: 修复远程节点详情页“导入至网关节点列表”按钮仅复制链接、不实际导入的假动作问题。现在按钮会直接调用 `/api/nodes/import` 并在成功后自动刷新本地节点列表。
- **导入空分享链接问题**: 修复由于远程 VLESS 参数未落库导致详情页 `share_link` 为空，进而触发“导入失败：节点分享链接为空”的问题。

## [1.5.5] - 2026-04-19
### ✨ 新特性 (Features)
- **底层架构重构**: OSPF 宣告引擎与 FakeIP / Xray 解耦，引入守护进程 `domainIPUpdater`。
- **Mode B 混合模式强化**: 现已支持在 Fake-IP 模式下动态下发指定的真实 IP 给 OSPF（实现 FakeIP 与 IP 路由双轨并行）。
- **Mode C 域名自愈追踪**: 在纯 OSPF 模式下，现已支持直接使用 Domain 域名规则。后台会自动监控并在 DNS IP（如 CDN 节点）发生改变时，全自动、无抖动地替换 OSPF 广播路由。

### ⚡ 性能优化 (Performance)
- **OSPF 路由下发极速批处理**: 重构了向 FRR (`vtysh`) 注入路由的逻辑。采用内存构建完整配置并单次文件注入的方式，将数百条 IP 路由的下发从 O(N) 降低至 O(1)，结合 SQLite 显式事务，彻底消除了大规模 GeoIP 路由同步时引起的 CPU 飙升与磁盘 I/O 阻塞。

### 🛡️ 安全与重构 (Security & Refactoring)
- **强随机数安全加强**: 修复了远程节点自动部署时使用弱伪随机数（`math/rand`）分配 WG 隧道 IP 与端口的漏洞，全面升级为 `crypto/rand` 密码学安全随机数生成器。
- **无用代码清理**: 利用静态分析工具清除了废弃的方法 (`isTrustedOrigin`, `getRelativePath`) 与多余的依赖引用，提升编译效率与代码整洁度。

### 🐞 修复 (Bug Fixes)
- 修复了此前在 OSPF 下发路由时高频重置路由状态导致的主路由 CPU 抖动问题。
- 完善并修正了官方说明文档中的路由工作模式逻辑。

## [1.5.0] - 2026-04-18
### 🚀 Features & Architectural Purity
- **架构净化 (Pure Architecture)**: 彻底移除了 Hysteria2、Sing-box 以及相关生态的残留支持，保持纯净的 Xray + Mosdns + FRR/Nftables 极简底层架构。
- **功能裁剪**: 彻底移除了“机场订阅”功能 (Backend 定时轮询与 Frontend UI)。网关定位回归纯粹的企业/家庭级节点管控，拒绝第三方不规范配置导致的崩溃隐患，确保极致稳定。
- **节点默认路由**: 实现了“默认节点”功能。在节点管理页可一键设置主代理节点，底层配置将热更新。
- **精准的无匹配兜底直连 (Fallback to Direct)**: 全面支持 Xray 原生的 `!cn` 倒装匹配模式。彻底移除了旧版的强制兜底代理规则，现在“未命中任何规则的流量”将 100% 精准穿透走 Direct 直连，杜绝国内流量意外绕回节点。
- **全景流量大盘**: 全新设计的前端仪表盘，替换掉冗长的静态架构介绍。利用底层 Xray 的高频 `tproxy_in` 标签提取与独立的后端轮询进程，带来毫秒级的上下行实时网速看板，以及持久化的高精度 24H 累计流量统计。
- **侧边栏优化**: 重排了全局导航菜单的优先级顺序并进行了文案瘦身（如改为“系统状态”、“实时连接追踪”、“设备分流规则”等），大幅提高日常运维的操作直觉。

### 🛡️ Security & Hardening (安全与健壮性)
- **抵御 MITM 的严格指纹验证 (Strict HostKey Verification)**: 在远程节点自动部署模块，废除了极度危险的 Trust-On-First-Use (TOFU) 盲信写入机制。首连必须核对 `SSH HostKey`（指纹支持 SHA256 / Base64），完全阻断了在跨洋网络遭遇中间人劫持并持久化注入假 Key 的攻击窗口。
- **消除远程命令注入面 (RCE Mitigation)**: 重构了生成远程部署 Bash 脚本的内部实现。彻底弃用脆弱的字符串拼接 JSON，改用 `json.Marshal` 加 `Base64` 双重封口渲染配置文本，杜绝因节点名称存在引号、回车等特俗字符而引发的代码污染或远端系统崩溃。
- **底层应用强隔离 (Concurrency Isolation)**: 引入了严厉的 `applyMutex` 互斥锁，彻底杜绝了高并发修改分流规则、添加节点时引起的 Mosdns/Xray 配置文件并行写入撕裂与文件锁定冲突。同时对配置写入的静默失败加入了强拦截拦截，配置保存失败立刻中断服务重启并向上游抛错。
- **部署并发队列限制 (Bounded Semaphore)**: 针对 Batch Deploy（批量部署 100+ 节点）的灾难级 Goroutine 井喷问题，新增了全局 Channel 容量锁（最大并行 3）。现在无论是批量建服还是定时检查，底层 SSH 握手都不会瞬间挤爆网关的 CPU 资源和系统 FD（文件句柄）池。
- **Fail-Fast 启动保护**: 治理了 `initDB()` 函数中对于 SQL 执行返回报错的漠视。如今网关启动时的核心表初始化一旦因为磁盘损坏或环境问题失败，进程将立刻 Panic（故障快停），绝对不带病运行。


## 2026-04-18 (v1.4.10: Zero Compilation & Robust Deployment)

### 🚀 Zero Compilation Architecture
- **Pre-compiled Releases**: 彻底重构了 `install.sh` 和 `update.sh` 安装部署脚本，去除了 `golang`, `nodejs`, `npm` 等所有重型编译依赖。脚本现直接从 GitHub Releases 下载预编译的对应架构 (`amd64`/`arm64`) 二进制文件，极大加快了安装与更新速度，并完美支持低性能旁路由或受限网络设备。
- **Lightweight Dependencies**: 现在安装过程仅需极少的轻量级网络与工具依赖 (`nftables`, `frr`, `curl`, `wget`, `unzip`, `iproute2`)。

### 🛡️ Robust Deployment & Network Tolerance
- **Three-Tier Version Fallback**: 在获取最新版本号时引入了**三重容错机制**：
  1. 优先尝试提取本地 Git 仓库中的 Tag 版本。
  2. 使用带超时与 3 次强制重试机制的 `curl` 轮询 GitHub API。
  3. 终极回退机制 (Hardcoded Fallback)：在遭遇强力网络阻断时，强制使用硬编码的后备稳定版本（`v1.4.10`）继续下载安装流程，确保脚本绝不报错中断。
- **Forced IPv4 Download**: 针对 IPv6 经常遭遇黑洞路由或未完全配置的环境，所有 `curl` 下载流程强制追加 `-4` 参数，显著提升下载与版本检测的成功率。
- **Boot & Password Polling Fix**: 修复了在重复安装或环境有残留进程时，由于旧服务抢占锁导致新进程无法启动，进而未能生成初始密码 `bootstrap_password.txt` 的致命 BUG。脚本现在会在安装最后一步强制 `systemctl restart proxygw`，并引入高达 15 次的自动重试轮询直至准确捕获密码。


## 2026-04-17 (v1.4.8: Core Component Management)

### ✨ New Features
- **Mosdns Update Management**: 在 Web UI 中新增防泄漏 DNS 引擎 (Mosdns) 的可视化版本管理与在线升级功能。
- **Mosdns Rollback**: 支持一键回滚 Mosdns 到上一次成功运行的本地备份版本，或通过下拉列表精确指定拉取 GitHub 上的历史发行版本 (涵盖所有架构)。
- **Version Sensing**: 仪表盘系统状态监控现在可以精确感知并显示当前实际运行的 Mosdns 内核版本号。

## 2026-04-17 (v1.4.7: Performance & Kernel Hardening)

### 🚀 Kernel & Firewall Hardening
- **Nftables Loop Prevention**: 彻底重写  透明代理接管栈。在  链强制增设  防环路跳出规则，从内核态硬性切断 Xray 发出流量被自身二次劫持的致命死循环。
- **IPv6 DNS/Traffic Leak Protection**: 在局域网透明代理链路中加入严苛的  规则。当客户端向双栈域名发起解析并试图通过 IPv6 通信时直接静默丢弃，强制回落至 IPv4 透明代理隧道，彻底封堵真 IP 裸奔漏洞。
- **IP Rule Idempotency (内存泄漏修复)**: 重构了 Go 后端关于策略路由 () 的下发逻辑，引入幂等性检查与自动清理。修复了由于网关频繁热重载导致底层路由表无限膨胀（叠加近百条重复规则）并最终耗尽 CPU 的高危 BUG。
- **Extreme Concurrency (Sysctl)**: 在自动化安装部署脚本  中固化 ProxyGW 专属的内核网络栈参数 ()：
  - 暴力提升连接跟踪表容量 ()，杜绝 P2P 和大并发场景下的  熔断。
  - 全局默认开启 **BBR** 拥塞控制算法与  队列，代理延迟与吞吐量提升至物理极限。
  - 正确开启 ，允许本机回环劫持，匹配透明代理的流量回注。

## 2026-04-17 (v1.4.6: Stable Release)

### 🐛 Bug Fixes
- **Mode C Geosite Route Injection**: 完美修复了在 Mode C (纯 OSPF 模式) 下，用户添加  域名规则时，无法将真实的 GeoIP 下发给主路由的缺陷。现在后端会自动将  的分类标签与  进行智能匹配降级，自动为代理域名提取出对应的真实 IP 网段并执行 OSPF 广播，彻底对齐用户直觉逻辑。

## 2026-04-17 (v1.4.5: Stable Release)

### 🐛 Bug Fixes
- **OSPF Route Injection (Mode B/C)**: 彻底修复了通过 UI 切换路由模式时 FRR 配置不重载，以及 Mode B 遗漏静态 Fake-IP 路由分发的致命 BUG。现在 Mode B 纯 Fake-IP 与 Mode C 真实 GeoIP 均能完美将 OSPF LSA 注入至主路由。

## 2026-04-17 (v1.4.0: Stable Release)

### 🚀 Architecture Refactoring (3-Mode Routing)
- **Mode A (全局网关接管)**: 专门针对新手和普通网络环境。启用 Nftables TProxy 强行接管所有物理流量，同时在底层强制终止 FRR (OSPF) 进程，彻底阻断任何不必要的路由通告，确保零路由污染。
- **Mode B (纯 Fake-IP)**: 专门针对高性能需求用户。Mosdns 开启全局 Fake-IP，FRR (OSPF) 严格只向主路由宣告  虚拟网段。物理隔绝真实的海外 GeoIP 下发，从根本上免疫 OSPF 环路。
- **Mode C (纯 OSPF)**: 专门针对需要真实 IP 的高级玩家。彻底从 Xray 和 Mosdns 配置中连根拔起 Fake-IP (FakeDNS) 组件，FRR (OSPF) 恢复全量 GeoIP 真实海外网段的动态下发。切换时自动抹除遗留路由缓存。

### 🔐 Security & Stability

### Fixed
- **DNS Configuration Regression**: 修复了  遗漏读取数据库的问题，现在用户在面板保存的 , ,  配置可以正确下发并真正生效至 Mosdns 引擎。
- **SSH Security (MITM)**: 强化远程节点部署架构。移除了高危的 ，引入了基于  的 TOFU (Trust On First Use) 机制，现在服务端能有效抵御针对部署链路的中间人劫持攻击。
- **Code Quality**: 移除了  中紧随  的多余数据库 PRAGMA 写入死代码，提升代码健壮性。

### 🚀 Features & Audit Fixes

### Added
- **Remote Deployment**: 新增“远程节点自动化部署”核心功能，支持对多台装有 Linux 的服务器进行一键下发和管理 WireGuard/VLESS Reality 节点。
- **Crypto Security**: 为存储在 SQLite 数据库中的 SSH 认证凭据 (密码 / Private Key) 引入了 AES-256-CFB 内存态加解密机制，彻底解决凭证“明文裸奔”高危隐患，并向前兼容历史明文数据。
- **Uninstallation Pipeline**: 远程节点在删除时，会自动异步派发 SSH 指令对远端机器的网卡、占用端口、残留配置文件及守护进程进行无痕清理 (Stop, Disable & Remove)。

### Changed
- **Share Link Encoder**: 修复了 VLESS 分享链接拼接时未对节点名称进行 URL 编码的问题，解决由此导致其他客户端剪贴板导入失败的截断瘫痪隐患。
- **Frontend Panel**: 物理移除了节点编辑面板中无用的“从分享链接解析”功能。

### Fixed
- **DNS Leak Protection**: 剔除了 WireGuard 配置自动生成中的  字段，恢复客户端退化使用网关级 DNS 的能力，使得 Mosdns 彻底夺回局域网内的 DNS 分流控制权。
- **Firewall Traversal**: 在 WireGuard  部署脚本中配对添加了  的  状态放行规则，以攻克远端服务器在开启 UFW / 严苛默认防火墙场景下的回程丢包断网问题。

## 2026-04-16 (v1.3.2: Bugfix - Link Parsing & Architecture Docs)
- **Bug Fix**: 修复前后端  链接解析逻辑，防止在导入节点时漏掉  (如 ) 和  流控参数，导致 Reality 握手失败。
- **Documentation**: 更新 UI 面板中的 Xray 透明代理架构分析，精准描述当前采用的最佳实践架构（Nftables 无脑劫持 + Xray Sniffing 与 Routing 分流）。

## 2026-04-16 (v1.3.1: Offline GeoIP Parsing & DNS Optimization)

### Added
- **Dynamic GeoIP Extraction**: 后端基于纯 Go 标准库实现了完全本地、脱机的 Protobuf 解析器。现在添加任何 geoip 代理规则（如 telegram, netflix 等），系统将直接在本地毫秒级从 geoip.dat 二进制文件中剥离对应的 IPv4 CIDR 网段并注入 OSPF。无需再依赖外部 raw.githubusercontent.com 接口。

### Changed
- 移除了前端与后端关于 DNS 实时解析流 (WebSocket) 的逻辑。因 Mosdns v5 默认日志为性能优化已精简，不再全量输出解析流。移除后进一步降低了后台的资源开销与 CPU 占用。
- 前端 DNS 面板 UI 重新排版布局。


## 2026-04-15 (Deep System & UI Optimization, Security, and Fake-IP)

### Added
- **Deep System Optimization**: 后端 SQLite 强制开启 PRAGMA journal_mode=WAL，读写全并发。
- **Deep System Optimization**: API 响应引入 Gzip 压缩。
- **Deep System Optimization**: /api/login 新增动态指数延迟与最大 10 次错误熔断机制。
- **Deep System Optimization**: 前端资源 (Vue3, Tailwind, FontAwesome) 完成全量本地化下载，支持 100% 离线脱机运行。
- **Fake-IP Architecture**: 完整引入了 Fake-IP 零延迟直通架构。Xray 开启内置 fakedns 引擎 (198.18.0.0/16)，OSPF (FRR) 在 Mode B 下静态宣告 198.18.0.0/16 路由。
- **Security**: Supply Chain Validation - Added rigorous SHA256/SHA512 hash verification for Xray-core downloads via .dgst files.
- **Security**: GeoData Validation - Added rules.zip.sha256sum signature verification for geosite and geoip downloads.
- **Security**: Systemd Sandboxing - Introduced ProtectSystem=strict, NoNewPrivileges=yes, PrivateTmp=yes to proxygw.service.
- **Security**: Initial Password Generation - Removed hardcoded admin password. First-time installs now generate a secure random password saved to config/bootstrap_password.txt.

### Changed
- 彻底移除了 watchDnsLogs 协程与相关陈旧的 OSPF 动态路由下发逻辑（由于 Fake-IP 架构已上线，此逻辑为无效高负载开销）。
- 修改 xray_service.go，TProxy 入站开启对 fakedns、http 和 tls 的 Sniffing。
- 修改 mosdns_service.go，命中 proxy_domains 时瞬间向 Xray 的 FakeDNS 请求假 IP 并阻塞式返回。
- scripts/install.sh upgraded to auto-deploy the hardened Systemd service and disable systemd-resolved DNS stub.
- README.md updated to reflect Fake-IP architecture and security posture.

### Fixed
- 彻底解决由于 DNS 解析时间与 OSPF 路由下发时间差导致的首包（TCP SYN）漏网与高延迟断流问题。

## 2026-04-14 (Round 3 Deep Governance)

### Added
- Service 层文件：mosdns_service.go、xray_service.go
- API 集成测试：api_integration_test.go（httptest + 临时 sqlite）
- 审计报告：AUDIT-2026-04-14-round3.md

### Changed
- applyMosdnsConfig 使用 renderMosdnsConfig() 生成配置
- applyXrayConfig 使用 buildBaseXrayConfig() 初始化基础结构
- 继续保持 geodata 官方 GitHub 更新链路

### Verification
- go test ./... -v 通过
- go vet ./... 通过
- go build -o proxygw-backend . 通过
- systemctl is-active proxygw/mosdns/xray 均为 active

## 2026-04-14 (Round 2 Deep Governance)

### Added
- 后端模块化路由文件：auth/dns/xray(update)/nodes/rules/system/config/api
- helpers.go 可测试纯函数集合
- helpers_test.go 回归测试（13 项）
- 文档：CHANGELOG.md、RELEASE_TEMPLATE.md、Round2 审计报告

### Changed
- main.go 从“路由+业务混合”改为“核心逻辑 + 路由装配”
- /api/update/xray 下载 URL 构建改为 helper 函数统一校验
- geodata 更新链路统一使用 GitHub 官方直连

### Fixed
- 修复文档中旧接口样例与请求头格式错误
- 修复运维文档中 build 命令与实际代码结构不一致问题

### Verification
- go test ./... -v 通过
- go build -o proxygw-backend . 通过
- systemctl is-active proxygw/mosdns/xray 均为 active
