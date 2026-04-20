# ProxyGW Changelog

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
