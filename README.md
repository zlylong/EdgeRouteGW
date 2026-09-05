# EdgeRouteGW - 现代化的透明代理网关

EdgeRouteGW 是一个高性能、易于使用的透明代理网关系统。它提供了美观的 Web 管理界面，让你能够轻松接管家庭或办公室的网络流量，实现智能分流。


![EdgeRouteGW Dashboard](docs/assets/dashboard.jpg)

## ✨ 核心特性

- **开箱即用 (零编译极速版)**：提供完善的 Web 管理后台，安装全程下载预编译包，**无需搭建任何 Go/Node 编译环境**，极速部署，全图形化管理节点、规则与设备。
- **智能分流**：内置强大的域名和 IP 规则库，国内网站直连，特殊流量走代理，彻底告别卡顿与 DNS 污染。
- **无感接管**：支持全局网关接管 (Mode A)、纯 Fake-IP 旁路 (Mode B) 或 纯 OSPF 动态播报 (Mode C)，局域网设备无需设置即可科学访问网络。
- **远程节点部署**：独创的一键式远程节点部署系统，支持录入多台海外 Linux 主机，由网关中控自动通过 SSH 下发、配置、并实时监控 WireGuard/VLESS 隧道协议。
- **极致安全**：系统随机生成高强度初始密码，前端自带防爆破延时；SQLite 落库的 SSH 凭证由内存态 AES-256-GCM 动态加解密（带认证，篡改即拒绝），Web UI 资源完全本地化。
- **内核级优化**：内置全自动的 Debian/Linux 内核参数与防火墙调优，暴力提升百万级 Conntrack 连接跟踪上限，全局开启 BBR 拥塞控制与 fq_codel 队列，自动封堵 IPv6 流量裸奔与内核路由死循环，榨干设备每一滴性能。

## 🚀 快速安装 (零编译极速部署)

得益于预编译架构，网关安装极其轻量，**不需要任何 Go 或 NodeJS 环境**，支持 `x86_64` 与 `ARM64` 架构。
推荐在纯净的 Debian 13 或 Ubuntu 24.04 服务器上使用 root 用户执行一键部署：

```bash
bash <(curl -s -4 -L https://raw.githubusercontent.com/zlylong/EdgeRouteGW/main/scripts/install.sh)
```
*(注：由于底层包含强制的 Nftables 防环路策略，不建议在已有复杂防火墙规则的宿主机运行，推荐单独分配一个 LXC 或轻量级 VM)*

> 说明：安装/升级脚本会在服务启动后自动执行数据库低风险优化（`scripts/db_optimize.sh --index-only`），用于补齐关键索引与统计信息；完整 `VACUUM` 仍建议在维护窗口手动执行。

## 🔑 初始登录

为了系统安全，EdgeRouteGW **没有默认固定密码**。首次安装完成后，请在服务器终端查看系统为您随机生成的初始密码：

```bash
cat /root/proxygw/config/bootstrap_password.txt
```

在浏览器中输入网关服务器的 IP 地址（如 `http://192.168.x.x/`），使用该初始密码登录。
**⚠ 强烈建议：请在首次登录后立即前往系统设置修改您的密码。** 修改后该 txt 文件将自动作废。

## 🕹️ 路由模式与使用指南

EdgeRouteGW 设计了三种物理隔离的网络接管模式，以适应不同级别的家庭/办公网络拓扑。

### 🟢 Mode A: 全局网关劫持 (推荐新手使用)
在这个模式下，EdgeRouteGW 作为局域网的"旁路由"存在，强行接管所有设备的流量。
**适用场景**：主路由是普通的家用路由器（如小米、TP-Link、华硕等），无需任何高级网络知识。

**举例使用方式：**
1. **全屋接管**：登录您家里的主路由器后台，找到 **DHCP 服务器设置**。将 **默认网关 (Default Gateway)** 和 **DNS 服务器** 修改为 EdgeRouteGW 服务器的局域网 IP 地址（例如 `192.168.1.100`）。保存并重启路由器。此时，连上 WiFi 的所有设备都会自动翻墙。
2. **单设备独享（按需科学）**：如果您不想影响家人，只想让自己的手机或电脑走代理。只需在手机/电脑的 WiFi 设置中，将 IP 获取方式从"自动(DHCP)"改为"手动/静态"，然后把 **网关** 和 **DNS** 填成 EdgeRouteGW 的 IP 即可。

### 🔵 Mode B: 混合 Fake-IP 模式 (零延迟 / 免防环路配置)
这是极其纯粹且强大的性能模式。对于域名流量，Mosdns 会开启 Fake-IP，OSPF 向主路由宣告虚拟的 IP 池 (`198.18.0.0/16`)。而对于具体的 IP 规则（如自定义的特定 IP 段），系统也会将其通过 OSPF 下发。兼顾了 Fake-IP 的无污染与特定 IP 拦截的需求。
**适用场景**：主路由是 ROS / OpenWrt 等支持 OSPF 的路由器。推荐不想折腾"防环路"的高级玩家。

**举例使用方式：**
1. 您的手机、电视和电脑的网关依然指向主路由器。**⚠ 但 DHCP 下发的 DNS 服务器，必须指向 EdgeRouteGW 的 IP 地址**。
2. 在主路由器中配置 OSPF，将 EdgeRouteGW 设为邻居。
3. **免疫环路**：因为您的海外节点真实 IP 不可能是 `198.18.x.x`，所以主路由永远不会将发往节点的包踢回给 EdgeRouteGW，天然免疫 OSPF 环路。

### 🟣 Mode C: 纯 OSPF 动态旁路模式 (传统的真实 IP 分流)
关闭 Fake-IP 功能。Mosdns 返回真实的海外目标 IP，EdgeRouteGW 会将静态 IP 以及真实 GeoIP (如 Netflix, Telegram 等网段) 动态推给主路由。
**适用场景**：对 Fake-IP 机制敏感（如某些严格校验目标 IP 的 P2P 游戏或 App报错）的高级玩家。

**💡 Mode C 独家特性：域名动态追踪与自愈**：
在纯 OSPF 模式下，路由器的物理分流只能依赖**真实 IP**。但得益于 EdgeRouteGW 底层的后台守护进程（Daemon），当您在管理面板添加一条普通 `domain`（域名）规则时，系统会在后台周期性地对其进行真实的 DNS 解析，并将解析出的最新 IP 动态推送给主路由的 OSPF 路由表。
**优势**：完美解决了传统静态路由无法应对现代云服务 CDN 及 DNS 轮询 IP 频繁变动的问题，实现了域名路由的无感追踪与自愈。

**注意防环路**：由于 OSPF 会播报真实的海外网段，如果您的代理节点 IP 刚好在这个网段里，就会形成死循环断网！您必须在主路由器上配置 **源地址绕过 (PBR 策略路由)**：让来自 EdgeRouteGW IP 的流量强制走外网，无视 OSPF 路由。
*(👉 ROS v7 示例：新建一个 `bypass_proxy` 路由表指向公网 WAN 口，然后执行 `/routing rule add src-address=<EdgeRouteGW_IP>/32 action=lookup-only-in-table table=bypass_proxy`，详见[运维文档](./docs/OPERATIONS.md))*

### 🧩 域名规则语义（与 Xray 官方语义保持一致）

- `c.com`：仅匹配根域（等价 `full:c.com`），**不匹配** `www.c.com`。
- `**.c.com`：匹配根域 + 任意层子域（等价 `domain:c.com`）。
- `*.c.com`：匹配根域 + 零或一层子域（等价正则）。

**模式限制（当前实现）**：
- `*.` / `**.` 通配域名规则仅允许在 **Mode A** 添加。
- Mode B / Mode C 仅允许普通域名（如 `c.com`），因为 OSPF 静态路由无法安全表达 wildcard 语义。
- **保护 IP 列表**: 在 Mode A 模式下，您可以添加"保护 IP"，这些 IP 将绕过 Xray 直接访问，用于防止规则错误导致断网。

## 📚 文档指南
- [主路由配套配置指南 (ROS/OpenWrt)](./docs/NETWORK_SETUP.md) - OSPF/DNS/PBR 设置教程
- [路由分流规则入门](./docs/ROUTING_RULES_GUIDE.md) - GeoSite/GeoIP 工作原理解析
- [运维与故障排查](./docs/OPERATIONS.md) - 服务管理、升级与系统卸载
- [开发者与架构指南](./docs/DEVELOPER.md) - 底层架构、源码结构与 API 参考
- [API 接口文档](./docs/API.md) - RESTful API 文档

## 🧪 测试指南

EdgeRouteGW 提供了完整的多层级测试脚本体系，所有测试脚本位于 `scripts/` 目录：

| 脚本 | 用途 |
|---|---|
| `test_all.sh` | **主编排器** — 按顺序执行所有 6 个阶段（后端 → 竞态检测 → 覆盖率 → 前端 E2E → 构建 → Git 状态） |
| `test_backend.sh` | 后端 Go 测试运行器（支持 `--race`、`--verbose`、`--short` 等参数） |
| `test_coverage.sh` | 后端覆盖率报告生成器（输出文本摘要 + HTML 可视化报告到 `coverage/`） |
| `test_benchmark.sh` | 基准测试运行器（支持 `--bench=Pattern` 筛选，`--count=N` 重复） |
| `test_frontend.sh` | 前端 Playwright E2E 按钮测试 |

```bash
# 运行全部测试
./scripts/test_all.sh

# 仅运行后端测试（含竞态检测）
./scripts/test_backend.sh --race

# 运行基准测试
./scripts/test_benchmark.sh --bench=GeoQuery

# 生成覆盖率报告
./scripts/test_coverage.sh
```

### 预提交检查

项目已安装 Git pre-commit hook，在每次 `git commit` 前自动执行：
1. 后端代码编译检查
2. affected package 的 `-short` 模式测试

如需绕过，使用 `git commit --no-verify`。

## 🙏 致谢 (Acknowledgments)

本项目底层的核心网络代理与 DNS 解析分流能力，离不开以下优秀的开源项目，特此致谢：

- [XTLS/Xray-core](https://github.com/XTLS/Xray-core) - 提供了极其强大且高性能的透明代理与协议卸载能力。
- [IrineSistiana/mosdns](https://github.com/IrineSistiana/mosdns) - 提供了灵活且高效的 DNS 转发、GeoIP 分流与防泄漏解析引擎。
- [Loyalsoldier/v2ray-rules-dat](https://github.com/loyalsoldier/v2ray-rules-dat) - 提供了极其全面且高频更新的地理数据包（GeoSite/GeoIP），是实现精准路由分流的基石。

感谢所有开源贡献者为构建更自由、开放的网络环境所做出的无私奉献！
