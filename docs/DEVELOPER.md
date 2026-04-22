# 开发者与架构指南

本文档面向对 ProxyGW 进行二次开发、或希望深入了解其底层网络机制的资深开发者与网络工程师。

## 🏗️ 核心架构

ProxyGW 是一个高度整合的网络系统。开发者坚信 **原生至上 (Native First)**，完全摒弃了 Docker 容器化带来的网络损耗、内核隔离复杂度以及额外开销，采用 Debian 原生裸机部署：

- **后端 (Go 1.25+)**：基于 Gin 框架，处理配置文件的动态生成、节点并发测速、系统服务守护任务与 Web API 支持。
- **前端 (Vue 3 + TailwindCSS)**：SPA 单页应用，极致的防抖与锁控制体验，编译产物存放在 `frontend/dist`，完全脱机可用。
- **数据持久化**：SQLite 3。已在代码层开启 **WAL (Write-Ahead Logging) 模式**，解决了默认模式下的高并发读写锁 (`database is locked`) 问题。

## 🚀 核心网络组件与协作

系统由以下四大底层组件协同工作：
1. **Xray-core**: 处理核心出入站流量、各种代理协议的加解密与 TLS/HTTP 嗅探。
2. **Mosdns (v5+)**: 负责智能 DNS 分流。不直接将 V2Ray 二进制文件塞给 Mosdns。后端系统基于纯 Go 手工实现了极轻量级 Protobuf 解码器，实现完全离线、本地毫秒级从二进制 geoip.dat 中动态提取任意分类（如 telegram、netflix）的 IPv4 CIDR 网段，并转交给 OSPF 发布路由。
3. **Nftables**: 负责 **Mode A** 模式下的 TProxy (透明代理) 底层流量劫持，将局域网物理流量强行导向 Xray。
4. **FRR (OSPF)**: 负责 **Mode B** 和 **Mode C** 模式下的动态旁路由宣告。Mode B 仅发布 Fake-IP 网段，Mode C 动态注入真实 GeoIP 网段。

## 🧠 网络分流与 Fake-IP 零延迟原理

传统的透明代理在进行 DNS 解析时往往存在泄露或上游网络延迟（等待国外 DNS 返回真实 IP）。ProxyGW 实现了彻底的零延迟 Fake-IP 架构 (`198.18.0.0/16`) (对应 Mode B)：

1. **瞬时接管**：命中代理规则的域名（基于 `proxy_domains.txt`），Mosdns 将不再向上游发起真实的互联网解析请求，而是直接在 1 毫秒内返回一个保留的 `198.18.x.x` 假 IP。
2. **流量重定向**：客户端（手机/PC）拿着这个假 IP 发送 TCP/UDP 流量，流量到达网关后被 Nftables 的 TProxy 规则劫持并丢给 Xray 的入站端口。
3. **Sniffing 嗅探**：Xray 开启了强大的流量嗅探（http, tls, fakedns）。它不仅能从自己的内置映射表中找回假 IP 对应的真实域名，还能从 TLS 的 SNI 字段中提取出真实的请求域名（如 `youtube.com`）。
4. **规则出站**：Xray 拿到真实域名后，内部根据分流规则直接将流量打包发往远端代理节点，全程无需在本地等待真实 IP 解析。

**附：关于 Mode C (纯 OSPF) 的 Geosite 降级机制**
在 Mode C 下，系统退化为传统的真实 IP 分流，不使用 Fake-IP。但由于 OSPF 无法广播域名，当系统遇到前端下发的 `geosite:xxx` 规则时，会触发 **Smart Fallback (智能回退)**：
后端会自动用相同的标签名称去 `geoip.dat` 中执行 Protobuf 解码提取。如果提取到了匹配的真实 CIDR 网段，将其交由 `ospfController` 静态注入 FRR 进程。
这一机制通过“以静态库掩盖动态解析”的方式，在物理网络层尽最大可能对齐了用户的直觉逻辑，避免高频 DNS 动态污染路由器 OSPF LSA 导致路由风暴。由于 CDN 特性，存在小概率漏网可能，此乃 Mode C 物理限制。

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
- `224.0.0.0/4` 及以上（Multicast / Reserved / Broadcast）

### 3) 两层防护

- **写入前防护**：`/api/rules` 中 `type=ip` 的值改为复用 `normalizeRouteKey()` 校验，脏路由在 API 层直接拒绝。
- **下发前防护**：`syncStaticRoutesToOSPF()` 调用 `addRoute()` 时统一走 `normalizeRouteKey()`，防止历史数据或其他来源绕过 API 进入下发链路。

### 4) 启动自愈清理

启动时执行 `purgeDirtyRoutesTable()`：
- 扫描 `routes_table.ip`
- 对无法通过 `normalizeRouteKey()` 的记录做事务性删除
- 记录清理日志：`[OSPF] purged <N> dirty routes from routes_table`

这保证升级后即使存在历史脏数据，也会在服务启动阶段被自动剔除，不再反复参与 OSPF 同步。

## 🛡️ 系统安全沙箱 (Systemd Hardening)

所有关键组件（ProxyGW Backend, Xray, Mosdns）的守护进程均运行在受限的 Systemd 权限沙箱中，防范 Shell 注入与越权攻击：
- `ProtectSystem=strict`: 锁定整个底层 Linux 文件系统为只读。
- `ReadWritePaths=-/root/proxygw -/usr/local/bin -/etc/frr`: 基于最小权限原则，仅放开当前服务必要的读写目录。
- `NoNewPrivileges=yes`: 彻底阻断任何形式的 SUID 提权操作。
- `PrivateTmp=yes`: 隔离系统临时文件空间。

## 📦 供应链防投毒 (Hash Validation)

为了防止公共加速节点或镜像站点（如 mirror.ghproxy.com）发起的中间人篡改攻击，后端在执行二进制与规则更新时，实施了严格的安全校验：
- **Xray 更新**：同步拉取 GitHub Release 中的 `.dgst` 文件，在内存中完成 SHA256/512 比对。若哈希不符，直接丢弃阻断安装。
- **GeoData 更新**：同步拉取 `.sha256sum` 并执行强校验，全程使用官方直连。

## 🔧 系统内核级调优

安装脚本已在 `/etc/sysctl.d/99-proxygw.conf` 自动完成了适用于透明代理网关的内核调优：
- 开启 BBR 拥塞控制与 fq 队列调度 (`net.ipv4.tcp_congestion_control = bbr`)。
- 放开 `nf_conntrack` 连接跟踪数上限至 104 万，防止大量并发请求导致内核丢包。
- 开启 TCP Fast Open 及 TCP Tw Reuse 优化短连接性能。

## 📊 `geoip:!cn` OSPF 展开性能测试（v1.5.14）

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
