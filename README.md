# ProxyGW - 现代化的透明代理网关

ProxyGW 是一个高性能、易于使用的透明代理网关系统。它提供了美观的 Web 管理界面，让你能够轻松接管家庭或办公室的网络流量，实现智能分流。


![ProxyGW Dashboard](docs/assets/dashboard.jpg)

## ✨ 核心特性

- **开箱即用 (零编译极速版)**：提供完善的 Web 管理后台，安装全程下载预编译包，**无需搭建任何 Go/Node 编译环境**，极速部署，全图形化管理节点、规则与设备。
- **智能分流**：内置强大的域名和 IP 规则库，国内网站直连，特殊流量走代理，彻底告别卡顿与 DNS 污染。
- **无感接管**：支持全局网关接管 (Mode A)、纯 Fake-IP 旁路 (Mode B) 或 纯 OSPF 动态播报 (Mode C)，局域网设备无需设置即可科学访问网络。
- **远程节点部署**：独创的一键式远程节点部署系统，支持录入多台海外 Linux 主机，由网关中控自动通过 SSH 下发、配置、并实时监控 WireGuard/VLESS 隧道协议。
- **极致安全**：系统随机生成高强度初始密码，前端自带防爆破延时；SQLite 落库的 SSH 凭证由内存态 AES-256-CFB 动态加解密，Web UI 资源完全本地化。
- **内核级优化**：内置全自动的 Debian/Linux 内核参数与防火墙调优，暴力提升百万级 Conntrack 连接跟踪上限，全局开启 BBR 拥塞控制与 fq_codel 队列，自动封堵 IPv6 流量裸奔与内核路由死循环，榨干设备每一滴性能。

## 🚀 快速安装 (零编译极速部署)

得益于预编译架构，网关安装极其轻量，**不需要任何 Go 或 NodeJS 环境**，支持 `x86_64` 与 `ARM64` 架构。
推荐在纯净的 Debian 13 或 Ubuntu 24.04 服务器上使用 root 用户执行一键部署：

```bash
bash <(curl -s -4 -L https://raw.githubusercontent.com/zlylong/proxygw/main/scripts/install.sh)
```
*(注：由于底层包含强制的 Nftables 防环路策略，不建议在已有复杂防火墙规则的宿主机运行，推荐单独分配一个 LXC 或轻量级 VM)*

## 🔑 初始登录

为了系统安全，ProxyGW **没有默认固定密码**。首次安装完成后，请在服务器终端查看系统为您随机生成的初始密码：

```bash
cat /root/proxygw/config/bootstrap_password.txt
```

在浏览器中输入网关服务器的 IP 地址（如 `http://192.168.x.x/`），使用该初始密码登录。
**⚠ 强烈建议：请在首次登录后立即前往系统设置修改您的密码。** 修改后该 txt 文件将自动作废。

## 🕹️ 路由模式与使用指南

ProxyGW 设计了三种物理隔离的网络接管模式，以适应不同级别的家庭/办公网络拓扑。

### 🟢 Mode A: 全局网关劫持 (推荐新手使用)
在这个模式下，ProxyGW 作为局域网的“旁路由”存在，强行接管所有设备的流量。
**适用场景**：主路由是普通的家用路由器（如小米、TP-Link、华硕等），无需任何高级网络知识。

**举例使用方式：**
1. **全屋接管**：登录您家里的主路由器后台，找到 **DHCP 服务器设置**。将 **默认网关 (Default Gateway)** 和 **DNS 服务器** 修改为 ProxyGW 服务器的局域网 IP 地址（例如 `192.168.1.100`）。保存并重启路由器。此时，连上 WiFi 的所有设备都会自动翻墙。
2. **单设备独享（按需科学）**：如果您不想影响家人，只想让自己的手机或电脑走代理。只需在手机/电脑的 WiFi 设置中，将 IP 获取方式从“自动(DHCP)”改为“手动/静态”，然后把 **网关** 和 **DNS** 填成 ProxyGW 的 IP 即可。

### 🔵 Mode B: 混合 Fake-IP 模式 (零延迟 / 免防环路配置)
这是极其纯粹且强大的性能模式。对于域名流量，Mosdns 会开启 Fake-IP，OSPF 向主路由宣告虚拟的 IP 池 (`198.18.0.0/16`)。而对于具体的 IP 规则（如自定义的特定 IP 段），系统也会将其通过 OSPF 下发。兼顾了 Fake-IP 的无污染与特定 IP 拦截的需求。
**适用场景**：主路由是 ROS / OpenWrt 等支持 OSPF 的路由器。推荐不想折腾“防环路”的高级玩家。

**举例使用方式：**
1. 您的手机、电视和电脑的网关依然指向主路由器。**⚠ 但 DHCP 下发的 DNS 服务器，必须指向 ProxyGW 的 IP 地址**。
2. 在主路由器中配置 OSPF，将 ProxyGW 设为邻居。
3. **免疫环路**：因为您的海外节点真实 IP 不可能是 `198.18.x.x`，所以主路由永远不会将发往节点的包踢回给 ProxyGW，天然免疫 OSPF 环路。

### 🟣 Mode C: 纯 OSPF 动态旁路模式 (传统的真实 IP 分流)
关闭 Fake-IP 功能。Mosdns 返回真实的海外目标 IP，ProxyGW 会将静态 IP 以及真实 GeoIP (如 Netflix, Telegram 等网段) 动态推给主路由。
**适用场景**：对 Fake-IP 机制敏感（如某些严格校验目标 IP 的 P2P 游戏或 App报错）的高级玩家。

**💡 Mode C 独家特性：域名动态追踪与自愈**：
在纯 OSPF 模式下，路由器的物理分流只能依赖**真实 IP**。但得益于 ProxyGW 底层的后台守护进程（Daemon），当您在管理面板添加一条普通 `domain`（域名）规则时，系统会在后台周期性地对其进行真实的 DNS 解析，并将解析出的最新 IP 动态推送给主路由的 OSPF 路由表。
**优势**：完美解决了传统静态路由无法应对现代云服务 CDN 及 DNS 轮询 IP 频繁变动的问题，实现了域名路由的无感追踪与自愈。

**注意防环路**：由于 OSPF 会播报真实的海外网段，如果您的代理节点 IP 刚好在这个网段里，就会形成死循环断网！您必须在主路由器上配置 **源地址绕过 (PBR 策略路由)**：让来自 ProxyGW IP 的流量强制走外网，无视 OSPF 路由。
*(👉 ROS v7 示例：新建一个 `bypass_proxy` 路由表指向公网 WAN 口，然后执行 `/routing rule add src-address=<ProxyGW_IP>/32 action=lookup-only-in-table table=bypass_proxy`，详见[运维文档](./docs/OPERATIONS.md))*


## 📈 全面性能测试（2026-04-21）

本轮测试覆盖了 **路由展开性能 + Xray 配置并发生成性能 + 会话并发性能 + 并发安全性**，不再局限于 OSPF 路由下发链路。

```bash
cd /root/proxygw/backend

# 0) 并发安全检查（Race）
go test -race ./...

# 1) 全量单测
go test ./...

# 2) 路由展开链路（geoip / !cn）
go test -run '^$' -bench 'BenchmarkExtractGeoIPs' -benchmem -count=3

# 3) Xray 配置并发链路 + 会话并发链路
#    （含 cpu=1/4/8 多核维度）
go test -run '^$' -bench 'Benchmark(BuildBaseXrayConfig|BuildAndMarshalXrayConfigParallel|ValidateSessionParallel|CreateSessionParallel)' -benchmem -cpu=1,4,8 -count=3

# 4) Profile（热点定位）
# go test -run '^$' -bench 'BenchmarkExtractGeoIPsExcludeCNPrivate$' -benchmem -count=1 \
#   -cpuprofile /tmp/proxygw_geoip_cpu.prof -memprofile /tmp/proxygw_geoip_mem.prof
# go test -run '^$' -bench 'BenchmarkBuildAndMarshalXrayConfigParallel_ModeB$' -benchmem -count=1 \
#   -cpuprofile /tmp/proxygw_xray_cpu.prof -memprofile /tmp/proxygw_xray_mem.prof
```

测试环境：`Intel i7-6700T / amd64 / Debian / Go test`

### A) 路由展开性能（cpu=4，count=3）

| Benchmark | 平均耗时 | Min~Max | 平均内存 | 平均分配次数 | 规模 |
|---|---:|---:|---:|---:|---:|
| `BenchmarkExtractGeoIPsCN-4` | `234.83 ms/op` | `234.02~236.25 ms` | `102.84 MB/op` | `2,401,760 allocs/op` | - |
| `BenchmarkExtractGeoIPsExcludeCNPrivate-4` | `261.28 ms/op` | `259.26~262.76 ms` | `144.95 MB/op` | `2,401,779 allocs/op` | - |
| `BenchmarkExtractGeoIPsExcludeCNPrivate_Count-4` | `266.76 ms/op` | `263.45~271.19 ms` | `144.95 MB/op` | `2,401,781 allocs/op` | `586,504 cidr/op` |

### B) Xray 配置并发性能（cpu=4，count=3）

| Benchmark | 平均耗时 | 平均内存 | 平均分配次数 |
|---|---:|---:|---:|
| `BenchmarkBuildBaseXrayConfig_ModeB-4` | `7.04 µs/op` | `9,984 B/op` | `82 allocs/op` |
| `BenchmarkBuildBaseXrayConfigParallel_ModeB-4` | `4.85 µs/op` | `9,984 B/op` | `82 allocs/op` |
| `BenchmarkBuildAndMarshalXrayConfigParallel_ModeB-4` | `11.73 µs/op` | `16,281 B/op` | `243 allocs/op` |

并发收益（cpu=4）：
- `buildBaseXrayConfig(ModeB)` 从串行 `7.04 µs/op` 到并发 `4.85 µs/op`，约 **31.0% 提升**。

### C) 认证会话并发性能（cpu=4，count=3）

| Benchmark | 平均耗时 | 平均内存 | 平均分配次数 |
|---|---:|---:|---:|
| `BenchmarkValidateSessionParallel-4` | `374.9 ns/op` | `88 B/op` | `3 allocs/op` |
| `BenchmarkCreateSessionParallel-4` | `501.6 ns/op` | `122 B/op` | `4 allocs/op` |

### D) 并发安全性

- `go test -race ./...`：**PASS**

### E) Profile 热点结论

- `geoip:!cn` 展开热点：`extractGeoIPsExclude`、`net.IP.String`、`fmt.Sprintf`。
- Xray 配置并发生成热点：`buildBaseXrayConfig` 与 `encoding/json.Marshal` / `encoding/json.mapEncoder.encode`。
- 说明当前瓶颈主要在 **对象分配与 JSON 编码**，后续优化方向应聚焦结构化对象复用与编码路径降分配。

## 📚 文档指南
- [ROS 新手配置指南](./docs/ROS_SETUP.md) - MikroTik RouterOS OSPF/DNS 等配套设置新手教程
- [OpenWrt 新手配置指南](./docs/OPENWRT_SETUP.md) - OpenWrt OSPF/DNS/旁路等配套设置新手教程
- [路由分流规则入门](./docs/ROUTING_RULES_GUIDE.md) - GeoSite/GeoIP 及代理直连工作原理解析

如果您是系统管理员、网络工程师或希望进行二次开发的工程师，请查阅 `docs/` 目录下的深入文档：

- 🛠️ [开发者与架构指南](./docs/DEVELOPER.md) - 深入系统底层架构、源码结构、Fake-IP 零延迟原理与内核级调优。
- ⚙️ [运维与故障排查](./docs/OPERATIONS.md) - 系统管理员的服务管理、脚本升级、系统卸载与常见故障排除手册。
- 🔌 [后端 API 接口参考](./docs/API.md) - 面向开发者的 RESTful API 接口说明。


## 🙏 致谢 (Acknowledgments)

本项目底层的核心网络代理与 DNS 解析分流能力，离不开以下优秀的开源项目，特此致谢：

- [XTLS/Xray-core](https://github.com/XTLS/Xray-core) - 提供了极其强大且高性能的透明代理与协议卸载能力。
- [IrineSistiana/mosdns](https://github.com/IrineSistiana/mosdns) - 提供了灵活且高效的 DNS 转发、GeoIP 分流与防泄漏解析引擎。
- [Loyalsoldier/v2ray-rules-dat](https://github.com/loyalsoldier/v2ray-rules-dat) - 提供了极其全面且高频更新的地理数据包（GeoSite/GeoIP），是实现精准路由分流的基石。

感谢所有开源贡献者为构建更自由、开放的网络环境所做出的无私奉献！
