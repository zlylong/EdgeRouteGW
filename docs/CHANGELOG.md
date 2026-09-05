## [Unreleased]

## [1.8.0] - 2026-09-05
### 🏁 稳定里程碑 (Stable Milestone)
1.8.0 作为稳定版发布，汇总自 1.7.25 起经**完整代码审计、升级路径加固、远程部署校验与压力测试**验证的全部修复。

**自 1.7.29 的增量：**
- **清理无效的 conntrack 调优**: `99-proxygw.conf` 中的 5 条 `net.netfilter.nf_conntrack.*` 从未生效——网关数据面走 TPROXY（nft 规则无 `ct state`），`nf_conntrack` 模块从不加载，这些键在 `systemd-sysctl` 运行时不存在，每次开机只产生 `cannot stat ... No such file` 报错（被 `|| true` 吞掉）。NAT 及其连接跟踪在远程节点/主路由侧，不在网关。已删除该块并修正 README/DEVELOPER 中"百万级连接跟踪"的失实描述（历史 CHANGELOG 作为记录保留）。压力测试已证 2700+ 并发隧道连接无需 conntrack 即可工作。

**累计修复回顾（详见 1.7.25–1.7.29 各条目）：**
- 安全审计（1.7.27）：SSH 凭证改 AES-256-GCM 认证加密；`x/crypto/ssh` 等依赖漏洞清零并升级至 Go 1.26（`govulncheck` 为 0）；发布产物 `SHA256SUMS` 校验；dig 参数、CSPRNG 失败、host-key 重钉定、高风险互斥锁四处加固；**修复 `aes.key` 被 git 跟踪导致每次升级清空全部远程节点 SSH 凭证的严重问题**。
- 三模式（1.7.24–1.7.26）：Mode A 不再被 ICMP 重定向绕过；Mode C 依赖的 `dig` 纳入安装；切换模式即时同步路由；FakeIP 不再被误发布为 OSPF 路由；重启/升级后路由短时重发。
- 远程部署（1.7.29）：节点 Xray 下载校验 XTLS 官方 `.dgst` 并按架构选择。
- 压力测试验证：单机 ~500 Mbps 隧道吞吐、2700+ 并发连接、内存有界（峰值 <700MB）、OOM/panic 0、控制面并发安全。

## [1.7.29] - 2026-09-05
### 🔒 远程节点部署加固 (Remote Deploy Hardening)
- **远程节点 Xray 下载现在校验且按架构选择**: VLESS 部署脚本此前从 `latest` 拉取写死的 `Xray-linux-64.zip`，不做任何完整性校验就以 root 解包进 `/usr/local/bin`。这有两个隐患：(1) 部署到 arm64 节点会装上跑不了的二进制（自 #26 起至少会被自检拦成 Failed，但本就该直接可用）；(2) root 二进制无校验，传输损坏或中间人替换都照装。现按 `uname -m` 选择 amd64/arm64 资产，并下载 XTLS 官方随发布提供的 `.dgst` 校验 `SHA2-256`，不匹配即中止部署。`install.sh` 里网关自身的 Xray 下载同样加了校验（离线/被墙路径允许无校验文件时告警放行，但校验文件存在且不匹配则致命）。在 VM105 上以真实 XTLS 发布验证：正确哈希通过、篡改一字节即失败。

## [1.7.28] - 2026-09-05
### 🐛 升级路径修复 (Upgrade Path)
- **重启/升级后路由发布不再干等 5 分钟**: #35 让"切换模式"立即发布，但"服务重启"仍未覆盖。重启时 `applyXrayConfig` 触发的首次同步跑在 Xray/Mosdns 尚未就绪的瞬间，`dig` 无应答（exit 9）后再无重试，只能等 `domainIPUpdater` 的 5 分钟 ticker；而 `update.sh` 与 `flush_cache.sh` 都会先 `DELETE FROM routes_table` 再重启，于是 **Mode B/C 每次升级后主路由都有最长 5 分钟没有路由**（v1.7.27 升级实测：05:34:33 清空，05:39:40 才由 ticker 回填，期间对账逻辑还把 FRR 侧 8 条路由当作孤儿剪掉）。现在启动后按 15s / 45s / 2min 做一轮短重试，约一分钟内收敛；ticker 本身不变，Mode A 不受影响。
- **`db_optimize.sh` 不再在每次升级时打印 Parse error**: `domain_geoip_lock` 表由后端在 Mode C 首次锁定 GeoIP 时按需创建，从未触发过的主机上不存在，索引语句报 `no such table`。sqlite3 不会中止，其余索引照常建立，但升级输出里总有一条刺眼的错误。现按表是否存在有条件执行并打印 `[SKIP]`。

## [1.7.27] - 2026-09-05
### 🔒 安全审计 (Security Audit)
- **⚠️ `update.sh` 此前每次运行都会清空全部远程节点的 SSH 凭证**: `config/aes.key` 被仓库跟踪，内容是源码里的公开占位常量；首次启动 `init()` 识别出常量后轮换为随机密钥并写回同一路径，而 `update.sh` 的 `git reset --hard origin/main` 会把它**覆盖回常量**——下次启动再轮换一把新密钥，用 legacy 常量解不开上一把密钥加密的行，于是按"外来行保留原样"跳过，凭证从此永久不可解密，部署/巡检全部认证失败且没有任何提示。现已解除跟踪并加入 `.gitignore`；`update.sh` 在 reset 前后备份/恢复该文件；迁移逻辑对解不开的行改为输出 `[CRITICAL]` 告警（含原因与处置）。**存量主机注意**：首次升级到本版仍由旧脚本执行 reset，无法从仓库侧挽救——升级前请先 `cp -p /root/proxygw/config/aes.key /root/aes.key.bak`，升级后若日志出现该 CRITICAL，将其复制回去并重启 proxygw 即可恢复。
本版由一次覆盖全部后端代码的审计产出：staticcheck / gosec / govulncheck 三套工具扫描，加上对认证、凭证加密、命令执行、输入校验、并发与发布链的人工审查。所有修复均在测试环境实测验证。
- **依赖漏洞清零**: `golang.org/x/crypto/ssh`（远程节点部署与巡检所用的 SSH 客户端库）存在 5 个可达漏洞，`x/net`、`quic-go` 各 1 个。升级至 `x/crypto v0.56.0`、`x/net v0.57.0`、`quic-go v0.59.1`。其中两个 SSH 死锁 DoS 仅在需要 Go 1.26 的版本中修复，故工具链升至 **Go 1.26**（`go.mod` 钉 `toolchain go1.26.8`，release workflow 同步）。此前 `go.mod` 钉在 go1.25.0，CI 与本地构建实际使用带 30 余个 stdlib 漏洞的工具链，正式版则因 workflow 写 `'1.25'` 而恰好拿到已修复的补丁版本。现在 `govulncheck ./...` 为零。
- **SSH 凭证改用 AES-256-GCM 认证加密**: 此前为 AES-256-CFB——IV 随机、密钥按安装生成，保密性没有问题，但 CFB **无认证**：篡改密文会解出被篡改的明文且不报错，而解密结果会直接交给 `ssh.ParsePrivateKey` 或 `sudo -S`。新格式 `ENC2:` 为 GCM（nonce‖密文‖tag），任何改动都会认证失败。旧 `ENC:` 行保持可读，**启动时自动重写为 GCM**（此前该步骤只在密钥轮换后运行，现每次启动执行）。树中不再有 CFB 写入代码。
- **发布产物校验和**: release 现随二进制发布 `SHA256SUMS`；`install.sh`/`update.sh` 下载以 root 运行的后端二进制后先校验再安装，**不匹配即中止并删除文件**。没有 `SHA256SUMS` 的旧 tag（含离线回退 tag）仅告警放行，保证现有安装路径不断。`check-release-chain.sh` 会在 workflow 不再发布该文件时失败。
- **四处加固**: (1) `dig` 无 `--` 终止符，以 `-`/`+`/`@` 开头的名字会被当作选项——上游校验已拒绝，现在唯一把字符串交给 root 子进程的位置也自行拒绝；(2) CSPRNG 失败时 `init()` 曾回退到源码里的公开常量密钥并持久化，现改为拒绝启动；(3) `POST /remote_nodes/:id/hostkey` 重新钉定 SSH host key 是唯一能静默把已存凭证导向另一台机器的操作，现与 deploy/mode/apply 一样要求确认令牌（前端从不调用此接口，无 UI 影响）；(4) 高风险互斥锁改为按组：`apply_config`、`mode_switch`、`network_config` 都重写同一批 Xray/Mosdns/nftables 配置，而后两者的 apply 函数内部无锁，此前 `/api/apply` 与 `/api/mode` 可并发交错写入。
- **清理死代码**: 移除 8 个无引用符号，其中 3 个是 1.7.20 被 `dig` 取代的 `host` 解析器残留——留在树里会让人误以为存在回退路径（#32 正是被这种误读掩盖了很久）。

## [1.7.26] - 2026-09-05
### 🐛 沙箱权限修复 (Sandbox)
- **`disableSendRedirects` 此前被沙箱挡住、完全无效**: unit 的 `ProtectKernelTunables=yes` 会把 `/proc/sys` 挂成只读，v1.7.24 引入的关闭 ICMP 重定向逻辑因此每次写入都失败（`open /proc/sys/net/ipv4/conf/eth0/send_redirects: read-only file system`）——与 1.7.23 修的 nftables 暂存文件是同一类错误。全新安装看起来正常，是因为同批加入的 install.sh 循环在沙箱外以 root 执行、确实生效；但它只覆盖安装时已存在的网卡，而网卡每次开机都会重建，`conf.default` 又不会追溯覆盖 `systemd-sysctl` 执行时已存在的接口。于是**重启后网关重新开始发送 ICMP 重定向，Mode A 再次可被绕过**，而本该兜底的组件写不进去。现于三处 unit 定义中授予 `ReadWritePaths=/proc/sys/net/ipv4/conf`；实测只读告警归零、`eth0` 重启后保持 0。测试将 unit 的授权与代码实际写入的路径常量绑定，避免任一侧改名后再次静默失效。

## [1.7.25] - 2026-09-05
### 🐛 模式切换与路由发布修复 (Mode Switch & Route Publishing)
- **切换模式后立即同步路由**: 路由发布此前**只**由 `domainIPUpdater` 的 5 分钟 ticker 驱动，切入 Mode B/C 后主路由会一直持有上个模式的路由（或空），直到下一次 tick 偶然触发——最坏要等满 5 分钟。期间没有任何迹象：`POST /api/mode` 返回成功、服务全部健康、OSPF 邻居正常，而流量因主路由无路由可指静默绕过网关（实测：切到 C 后客户端立刻拉取 86KB，网关计数仅动约 1KB）。现由 finalize 步骤直接触发 `scheduleStaticRouteSync`（异步且自带合并，不会让切换阻塞在全量 DNS 解析上）。实测 C→A→C：切回后 **1 秒**内 8 条路由重新发布并被主路由学习。Mode C 仍保留既有已发布路由而不降级——那是其规则的解析结果，切换并不使其失效，降级只会让主路由先撤销再重新学习。
- **绝不把 FakeIP 发布为 OSPF 路由**: `isDirtyRouteIPv4` 过滤了默认路由、回环、link-local 与组播，却未排除 FakeDNS 地址池。假 IP 仅在铸造它的网关内部有意义（域名映射存放在 Xray FakeDNS 中，只有 Mode B 运行），发布出去等于让主路由指向一个 Mode A/C 无法解析的地址。B→C 切换时在途的解析仍可能返回池内地址并被持久化发布（实测出现 `198.18.216.7/32` 被主路由学习，尽管此时 FakeDNS 已关闭）。虽会在下一轮同步自愈，但代价是一个同步周期内主路由持有无效路由、且该规则的真实地址尚未发布。现直接拒绝 `198.18.0.0/15`；Mode B 的地址池是 `frr.conf` 里的单条静态 `198.18.0.0/16`，不经此路径，不受影响。

## [1.7.24] - 2026-09-05
### 🐛 三模式验证修复 (Mode Verification Fixes)
- **Mode A 被 ICMP 重定向静默绕过**: `99-proxygw.conf` 只设了 `conf.all` 与 `conf.default` 的 `send_redirects=0`，二者都覆盖不到安装时已存在的网卡——`default` 仅对之后创建的接口生效，而 `send_redirects` 的有效值是 `conf.all` 与 `conf.<iface>` 的**逻辑或**，因此 `eth0` 保持默认值 1 并继续发送重定向。Mode A 恰好是产生重定向的典型形态（网关为下一跳在同网段的客户端做路由），客户端收到后改为直连主路由，**流量彻底不再进入 TPROXY 链**，且无任何报错。实测：`eth0=1` 时客户端路由为 `via 主路由 ... cache <redirected>`、两个 nft 计数器恒为 0；置 0 并清缓存后路由回到网关，`proxy_default_v4` 立刻计到 55 包，而 Mode A 的 QUIC 阻断规则也首次命中（5/5 探测包）——此前它从未触发过，只因没有包能到达它。修复为在启动与安装时对每一张已存在的网卡显式写 0。
- **Mode C 因缺少 dig 完全不工作**: `ospf_dns_cache.go` 通过 shell 调用 `dig` 解析每一条 domain/geosite 规则，且自 1.7.20 移除 Go 解析器后这是**唯一**解析路径；而 `install.sh` 从未安装过它。全新安装上每次解析都失败（`exec: "dig": executable file not found in $PATH`），`routes_table` 恒为空，主路由收不到任何路由——**Mode C 的立身之本（解析域名并经 OSPF 播报真实 IP）在任何标准安装上都不成立**。Mode B 因 `198.18.0.0/16` 是 frr.conf 里的静态路由而仍能转发，但其保护主机解析同样失效。修复为在 install.sh 安装、update.sh 补装 dig 提供者，并在启动时给出一次明确告警（此前只有每条规则每轮一行、极易被误读为该规则自身的 DNS 问题）。

## [1.7.23] - 2026-09-05
### 🐛 部署与运行时修复 (Deployment & Runtime Fixes)
- **REALITY 隧道部署自检**: 远程节点部署完成后会实际发起一次 REALITY 握手并经隧道取一次 `https://<serverName>/`，失败则部署置为 `Failed` 并直接指出应更换的 `dest`。此前只要安装脚本退出码为 0 就标记 `Online`，`check` 也仅执行 `systemctl is-active xray`，导致隧道完全不通的节点被报告为健康，只有抓包才能发现。
- **nftables 规则集终于可被后端更新**: `applyNftablesConfig` 曾把校验用暂存文件写入 `/etc/nftables.conf.proxygw.new`，而 unit 的 `ProtectSystem=strict` 使 `/etc` 只读，且 `ReadWritePaths` 的 `-` 前缀会跳过尚不存在的路径，因此**每次 apply（含开机）都失败**。影响远超防火墙本身：`mac_proxy`/`ip_proxy`/`protected_ips` 等 7 个 nft set 从未被创建，**LAN ACL、按设备分流、保护 IP 列表在任何安装上都是静默失效的**。暂存文件改写入 `PrivateTmp` 下的 TempDir。
- **开机时 Xray 配置被误判为非法**: `Bootstrap` 在 `applyXrayConfig` 之后才创建 `/run/proxygw`，而生成的配置把 access log 指向该目录，Xray 因此以退出码 23 拒绝启动；`xray.service` 的 `RestartPreventExitStatus=23` 又阻止重试，服务保持停止。`/run` 位于 tmpfs，每次重启都会复现。目录创建已移至 `Bootstrap` 开头，四处硬编码路径统一由 `runtimeDir` 派生。
- **测试套件在 arm64 上恢复通过**: `helpers_test.go` 中 3 个用例硬编码了 amd64 的 `Xray-linux-64.zip`，在项目同样发布的 arm64 上必然失败（CI 仅跑 amd64 故长期未暴露）。改为与实现同源推导，并新增用例钉住各架构的 asset 名。
- **测试体系**: 修复 2 个因 DNS 重构（硬编码 127.0.0.1）导致的后端测试失败，新增 5 个测试脚本（`test_backend.sh`, `test_coverage.sh`, `test_benchmark.sh`, `test_frontend.sh`, `pre-commit.sh`），重写 `test_all.sh` 为主编排器（6 阶段），添加 Git pre-commit hook。
- **更新脚本 Git 直连修复**: `scripts/update.sh` 现在仅对 GitHub API/Release 资产下载使用本地 10809/10808 代理，`git fetch/reset` 阶段强制清空代理环境，避免 Debian Git/GnuTLS 经本地 HTTP 入站时出现 `TLS connection was non-properly terminated`。

## [1.7.22] - 2026-05-06
### 🐛 Mode B/C OSPF 发布修复
- **FRR 服务自愈**: 当 `/etc/frr/frr.conf` 已经是最新但 `frr.service` 因重启/更新处于 disabled 或 inactive 时，后端现在会自动 enable/start FRR，避免 `vtysh: failed to connect to any daemons` 导致规则只停留在 candidate、无法通过 OSPF 下发。
- **测试桩同步**: 修正 OSPF DNS 缓存相关测试桩签名，匹配当前解析函数的 remote/local 参数。

## [1.7.21] - 2026-05-05
### 🛡️ 强制解析隔离 (Hardened Resolution)
- **物理截断解析回退**: 彻底移除了 `resolveDomainIPv4WithTTL` 中残留的 `host` 系统调用和 GeoIP 回退逻辑。
- **强制使用 dig 通道**: 所有的解析入口（包括兜底解析）现在都强制定向到 `127.0.0.1` 且仅通过 `dig` 命令行执行，彻底杜绝了 Go 原生解析器在解析失败时自动读取 `/etc/resolv.conf` 造成的污染残留。
- **零泄漏承诺**: 后端引擎现在不再具备任何绕过 Mosdns 直连公网 DNS 的代码路径。

## [1.7.20] - 2026-05-05
### 🛠️ 核心解析引擎替换 (Engine Swap)
- **引入 dig 作为解析核心**: 彻底废弃 Go 原生解析器，后端 OSPF 引擎改用系统受信任的 `dig` 工具直接向 `127.0.0.1` (Mosdns) 发起请求。
- **消除日志幻象 IP**: 解决了由于 Go 解析器读取 `/etc/resolv.conf` 占位符导致的日志中出现 `119.29.29.29` 的误导性报错。
- **解析行为一致性**: 确保后端程序看到的解析结果与用户在终端手动执行 `dig` 的结果 100% 对齐。

## [1.7.19] - 2026-05-05
### 🐛 深度修复 (Deep Fixes)
- **真正的架构精简**: 彻底移除后端 OSPF 引擎中残留的 SOCKS5 解析逻辑和 `host` 裸连后门代码。
- **强制委派 127.0.0.1**: 修正了前一版本中代码未正确合入的失误，现在后端解析确保 100% 仅向本地 Mosdns (`127.0.0.1`) 提议，利用 Mosdns 成熟的分流与隧道机制处理所有解析。
- **消除解析污染**: 杜绝了因 SOCKS5-in-SOCKS5 逻辑异常导致的解析回退，确保被墙域名解析结果与客户端完全一致。

## [1.7.18] - 2026-05-05
### ⚡ 架构精简与可靠性重构 (Simplified Architecture)
- **回归单一 DNS 来源**: 彻底废弃后端 OSPF 引擎中自建的 SOCKS5 解析逻辑和裸 UDP 回退后门。
- **强制委派 Mosdns**: 后端解析任务现在 100% 委派给本地 Mosdns (127.0.0.1)，利用其已验证的成熟分流与代理通道处理所有域名展开，消除架构冗余。
- **封死解析后门**: 移除所有 DNS 裸连 Fallback 逻辑，确保若本地安全解析失败则直接报错，绝不接收任何可能被污染的非代理通道结果。

## [1.7.16] - 2026-05-04 - 2026-05-04
### ✨ 核心重构 (Core Refactoring)
- **彻底抛弃底层 UDP 裸解析**: 后端 OSPF 解析引擎直接集成 SOCKS5 DNS-over-TCP 客户端拨号逻辑。现在对 `geosite/domain` 的展开解析，将强制使用您面板上设置的 `dns_remote`，并将其自动包裹进 TCP 流经由本地 10808 端口（Xray）加密送出，彻底绕过 Mosdns 的 fallback 分流误判，达到 100% 免疫 GFW 投毒。

## [1.7.15] - 2026-05-04
### 🐛 修复 (Bug Fixes)
- **防止 GFW DNS 投毒**: 将后端 OSPF 展开引擎的所有 geosite/domain DNS 解析请求统一路由至本地 Mosdns 实例 (127.0.0.1:53)，利用其内置的 SOCKS5 代理安全解析海外被墙域名，彻底斩断 OSPF 规则被投毒导致本地出现如 119.29.x.x 脏路由的问题。
- **防止缓存污染遗留**: 在系统的自动更新脚本 (`scripts/update.sh`) 与缓存清洗脚本中增加对 `domain_resolve_cache`, `routes_table`, `geosite_expand_cache` 的底层清洗动作。

## [1.7.14] - 2026-05-04
### 🐛 修复 (Bug Fixes)
- **DNS 解析尾巴修复**: 修复在设置内网 DNS 地址(如带路径或 scheme)时，错误地为其追加 socks5 代理尾巴的问题。

## [1.7.13] - 2026-05-04
### ✨ 新特性 (Features)
- **Mode C 解析深度优化**: OSPF 的域名展开强制实施 SOCKS5 解析绕过，确保被墙环境下的 DNS 解析与客户端代理流量处于一致路径。
- **内外网 DNS 智能隔离**: 自动检测 `dns_remote` 的地址，当配置为内网/回环地址（如 SmartDNS/AdGuard）时，自动剥离 SOCKS5 代理避免解析闭环黑洞。
- **动态缓存清理**: 面板更新 DNS 设置时自动清除后端路由域名的关联缓存（`domain_resolve_cache`），保证修改能即刻同步给 OSPF，无需手动重启。


## [1.7.12] - 2026-05-03
### ✨ 新特性 (Features)
- **本地 HTTP 代理服务**: 在 Xray 中新增监听于 `127.0.0.1:10809` 的 HTTP 代理入站。这主要用于解决网关主机自身（不走 TProxy 的流量）在更新或下载资源时的网络阻断问题。

### ⚡ 优化 (Optimizations)
- **更新脚本自动代理感知**: 修改 `scripts/update.sh`，在启动时自动检测 `10809` (HTTP) 或 `10808` (SOCKS5) 本地端口。如果检测到代理服务已就绪，脚本将自动配置 `http_proxy` 环境变量，确保即使在严苛的网络环境下也能顺利完成 `git` 拉取与 GitHub Release 资源下载。


## [1.7.11] - 2026-05-03
### ⚡ 优化 (Optimizations)
- **更新脚本依赖补全 (Update Script Dependencies)**: 在 `scripts/update.sh` 中显式增加了 `sqlite3` 与 `wget` 作为前置安装依赖。这解决了在某些精简版 Linux 环境下，因缺失 `sqlite3` 二进制文件导致数据库索引优化脚本 (`db_optimize.sh`) 执行失败的问题。


## [1.7.10] - 2026-05-03
### 🐛 修复 (Fixes)
- **更新脚本路径冲突修复 (Update Script Path Conflict)**: 修复了 v1.7.9 引入的一个路径冲突问题。之前脚本将新二进制下载到项目目录内，导致随后的 `git clean -fd` 操作将其作为未跟踪文件错误删除。现在下载路径已改为系统的 `/tmp` 目录，确保文件替换过程稳健。


## [1.7.9] - 2026-05-03
### ⚡ 优化 (Optimizations)
- **更新脚本稳定性提升 (Update Script Connectivity)**: 调整了 `scripts/update.sh` 的执行顺序。现在脚本会优先完成 `git fetch`、GitHub API 请求及二进制文件下载，待所有网络依赖产物准备就绪后，再停止 `proxygw` 服务进行文件替换。这解决了在透明代理环境下，因更新脚本提前停止服务导致下载链路中断（无法使用代理更新）的问题。


## [1.7.8] - 2026-05-03
### 🐛 修复 (Fixes)
- **恢复实时连接追踪 (Real-time Connection Tracking)**:
    - **补齐 Xray 访问日志**: 修复了动态生成的 Xray 配置中缺失 `access` 日志路径的问题，确保底层引擎持续输出连接明细。
    - **优化 UI 过滤逻辑**: 移除了后端 API 对客户端子网的硬性限制。现在在多 VLAN 或 OSPF (Mode B/C) 组网环境下，所有进入网关的跨网段连接均能被正常捕获并展示在仪表盘中。


## [1.7.7] - 2026-05-03
### ⚡ Mode A 稳定性专项加固 (Mode A Stability Hardening)
- **修复 DNS 解析链路中断**: 为 Xray 补齐了 `10808` 端口的 SOCKS5 入站，确保 Mosdns 的远程解析请求（通过 SOCKS5）能被正确处理。此前由于缺少此入站，代理域名的 DNS 解析在缓存过期后会彻底失效，导致“无法转发”的现象。
- **完善 IPv6 自愈能力**: 扩展了策略路由巡检协程（Reconcile Loop），新增对 IPv6 `ip rule` 和 `ip route` 的定时检测与恢复，确保在启用 IPv6 的 LAN 环境下转发策略始终一致。
- **强化 TProxy 嗅探策略**: 在 Mode A 下为 SOCKS5 入站开启嗅探与 `routeOnly` 模式，确保即使是经由 Mosdns 发起的远程查询也能正确命中路由分流规则。

## [1.7.6] - 2026-05-03
### ⚡ 稳定性加固 (Stability Hardening)
- **日志轮转与自动清理**: `db_maintenance.go` 新增日志自动轮转逻辑，定期检查并截断过大的日志文件（Mosdns/Xray），防止 `/run` 或系统磁盘空间耗尽导致服务崩溃。
- **DNS 服务增强**: Mosdns 配置新增 `tcp_server` 支持，解决部分客户端在 UDP 不稳定时切换 TCP 解析失败的问题，提升 DNS 解析鲁棒性。
- **FakeIP 一致性保证**: 在 Mode B (FakeIP) 下，Xray 重启时同步重启 Mosdns，强制刷新其 FakeIP 映射缓存，避免 Xray 内部表丢失导致旧 FakeIP 流量无法路由的“断网”现象。
- **日志降噪**: 默认禁用 Xray 访问日志（Access Log），移除由后端高频 API 轮询产生的海量冗余 I/O，延长存储介质寿命。

## [1.7.5] - 2026-05-03
### ✨ 新增 (Features)
- **自动化数据库维护**: 新增 `db_maintenance.go` 模块，每日定时清理过期数据（API 审计、系统事件、流量历史、过期 DNS 缓存等）。
- **数据库周优化**: 每周日凌晨自动执行 `VACUUM` 与 `ANALYZE`，确保 SQLite 数据库性能不随运行时间下降。

## [1.7.4] - 2026-05-01
- 修复 Xray 流量接口 (API) 状态显示不准确的问题（由 "Active" 占位符改为实际月累计流量）。
- 修复节点流量排行（自然月）数据为空的问题。
- 优化流量统计口径：聚合 Xray 所有出站流量（含直连与自检），确保 API 始终有数据。
- 缩短流量排行落盘周期至 1 分钟，提升实时性。

### ✨ 新增 (Features)
- **Mosdns 配置页面增强**: 
  - 支持动态配置 `log_level`（日志等级）、`cache_size`（缓存条目数）和 `lazy_ttl`（懒加载生存时间）。
  - DNS 设置面板新增高级参数调节，优化解析性能与内存占用。
- **系统诊断与测试工具**:
  - 新增“诊断测试工具”面板，集成路由追踪与系统体检。
  - **路由追踪模拟**: 模拟输入域名/IP，实时显示命中的分流规则、出口策略及匹配原因。
  - **系统健康自检**: 一键检查数据库、Xray、Mosdns、GeoData 资产以及 OSPF/Nftables 的运行状态。

### ⚡ 性能与优化 (Optimizations)
- **GeoData 资源一致性**: 将 Mosdns 的 `geoip.dat` 和 `geosite.dat` 资源改为指向 Xray 完整资产目录的符号链接，确保两者规则集版本严格对齐，同时大幅减少磁盘冗余。
- **DNS 解析稳定性**: 优化了 Mosdns 的默认配置生成逻辑，使其在 A/B/C 三种模式下均能保持解析分流逻辑的一致性。

### 🐛 修复 (Fixes)
- **GitHub Actions: Daily Core & Rules Update 提交阶段失败修复**:
  - 修复 `.github/workflows/update-cores.yml` 在 `Commit and Push Changes` 阶段错误 `git add` 被 `.gitignore` 屏蔽的 GeoData 文件（`/core/**/*.dat`、`core/mosdns/geodata.ver`）导致任务失败的问题。
  - 调整为仅提交可跟踪产物（`core/xray/xray`、`core/mosdns/mosdns`、`core/mosdns/*.txt`），并同步更新提交信息。
- **网卡管理过滤 WireGuard 节点网口**:
  - `interface_options` 生成逻辑新增 WireGuard 网口过滤（如 `wg0`），避免节点隧道口出现在“网卡管理/网络角色选择”中。
  - 防止误将节点隧道接口设为管理/业务网卡，降低路由与管理面误配置风险。
- **Mode C OSPF 发布策略修正（直连 geosite/geoip 不发布）**:
  - 调整 Mode C 的 geosite 路由展开查询范围，仅处理 `proxy*` / `ha-*` 策略，移除 `direct*`。
  - 确保直连规则不会被错误物化为 OSPF 静态候选路由，避免“直连策略反向劫持”风险。
- **Web UI 布局与 Tab 逻辑修复**:
  - 修复了 HTML 标签未闭合导致的页面布局重叠/崩溃问题。
  - 重构了前端 Tab 切换的 `v-if / v-else-if` 条件链，确保各功能面板（Connections, Rules, DNS, Tools 等）在单页面应用中的渲染互斥性。
  - 修正了 `Nodes` 节点管理区块曾被意外设置为 `v-if` 导致 Tab 链中断的故障。
- **OSPF 路由状态残留修复**:
  - 在路由下发逻辑中引入 `failed_policy` 状态，对违反私网拦截策略（如 192.168.0.0/16）的路由候选进行标记。
  - 解决了 Pending Set 中僵尸路由数据无法清除的顽疾，保持待发布列表整洁。
- **系统状态检测兼容性**:
  - 修复了 Xray 流量统计 API 参数错误（从 `-name` 更正为 `-pattern`），恢复了首页流量统计状态的实时显示。
  - 增强了 E2E 测试脚本的健壮性，自动处理登录与加载遮罩层，提升了 CI/CD 环节的稳定性。

## [1.7.3] - 2026-05-01
### 🚀 稳定版发布 (Stable)
- 发布 v1.7.3 Stable，优化 OSPF 路由发布的精准度，支持按“最长前缀匹配”提取最小子网，大幅降低 Mode C 下的路由表膨胀。

### ⚡ 性能与优化 (Optimizations)
- **GeoIP 匹配策略优化 (LPM)**:
  - 将 GeoIP 查找逻辑调整为“最长前缀匹配”（Longest Prefix Match）。
  - 当解析出的 IP 命中多个重叠网段时，优先提取并发布最小（最精确）的子网，而非之前的最大网段。
  - 显著缩小了 Mode C (OSPF) 模式下域名规则物化后的路由发布范围，减轻主路由器负担。

### ✅ 测试 (Tests)
- 新增 GeoIP 匹配测试用例 ，覆盖重叠网段提取逻辑。

## [1.7.2] - 2026-05-01
### 🚀 稳定版发布 (Stable)
- 发布 v1.7.2 Stable，修复 Mode C 下直连 geosite 规则被误发布到 OSPF 的问题，并完善网卡管理与发布链路稳定性。

### 🐛 修复 (Fixes)
- **Mode C OSPF 发布策略修正**:
  - geosite 静态路由展开仅处理 `proxy*` / `ha-*` 策略，移除 `direct*`，避免直连规则被误物化为 OSPF 候选路由。
  - 新增回归测试，确保直连 geosite 不再进入 `routes_table` 的 static 发布集。
- **网卡管理接口过滤增强**:
  - `interface_options` 过滤 WireGuard 节点网口（如 `wg0`），避免误选隧道接口作为管理/业务网卡。
- **Daily Core & Rules Update 工作流修复**:
  - 修复提交阶段误 `git add` 被 `.gitignore` 屏蔽的 geodata 文件导致失败。
- **安装/更新链路仓库地址统一**:
  - README、运维文档及 install/update 脚本统一切换到 `zlylong/EdgeRouteGW`。

## [1.7.1] - 2026-04-30
### 🚀 稳定版发布 (Stable)
- 发布 v1.7.1 Stable，完成项目品牌统一为 **EdgeRouteGW**，并将开源许可证切换为 **AGPL-3.0**。

### 🔁 变更 (Changes)
- 全仓库品牌文案由 ProxyGW 统一更新为 EdgeRouteGW（脚本、前端、系统服务描述、发布工作流与文档）。
- `LICENSE` 从 MIT 更新为 GNU AGPL-3.0。
- 发布链路元数据同步更新（前端版本标识、install/update fallback 版本、release 校验脚本）。

## [1.7.0] - 2026-04-30
### 🚀 稳定版发布 (Stable)
- 发布 v1.7.0 Stable，正式上线路由追踪诊断、系统自检工具、Geo 资产查询以及 Mosdns 深度配置能力。
- **功能面板整合**: 删除了独立的“Geo 查询验证”页签，将其合并至“诊断测试工具”中，使系统维护入口更聚焦。
- 修复了自 v1.6.15 以来积累的所有已知 UI 渲染与路由同步残留 Bug。

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
# EdgeRouteGW Changelog

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
- **核心系统日志增强**：新增告警事件流、TProxy 命中计数视图，支持节流告警与内核流量计数可视化。

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
- **OpenWrt 防环路指引固化**: 延续并固化主路由源地址旁路（PBR）方案，用于避免 `EdgeRouteGW -> 主路由 -> EdgeRouteGW` 回弹环路。
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
- **发布工作流修复**: 修复 `.github/workflows/release.yml`，改为从 `docs/CHANGELOG.md` 自动提取对应版本段落作为 Release Notes，并统一使用 `EdgeRouteGW <VERSION> Stable` 标题发布。

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
- **Extreme Concurrency (Sysctl)**: 在自动化安装部署脚本  中固化 EdgeRouteGW 专属的内核网络栈参数 ()：
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

