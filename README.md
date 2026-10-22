# EdgeRouteGW 2.0 — 透明代理网关

[English](README.en-US.md)

EdgeRouteGW 是一台放在局域网里的 Linux 透明代理网关:设备把网关或 DNS 指向它(Mode A),或者由主路由通过 OSPF 把需要代理的流量引过来(Mode B / C),它按规则把流量分到直连、节点、出站组或拦截。2.0 是在 1.x 基础上的整体重写:单二进制 + SQLite 的声明式控制面,所有配置先落成"期望状态",再由协调器渲染成 Xray / Mosdns / nftables / FRR / 内核 WireGuard 的配置并下发,出错自动回退。

## 核心特性

- **三种接管模式**:Mode A 全局 TPROXY 网关;Mode B Fake-IP + OSPF 旁路(只有代理域名的流量过来);Mode C 真实 IP + OSPF 旁路(域名规则周期解析成 IP 宣告给主路由)。
- **分流规则**:规则集 + 规则的有序列表,匹配 `domain / geosite / geoip / ip`,动作 直连 / 默认代理 / 拦截 / 指定节点 / 出站组;内置默认集(私网、国内直连)固定在最前,末尾的兜底策略与节点失效策略可见可改;页面内 Trace 直接看任一目标的去向。
- **节点与出站组**:VLESS / VMess / Trojan / Shadowsocks / SOCKS / HTTP / WireGuard;分享链接导入;故障转移组与负载均衡组(leastPing / leastLoad / random / roundRobin),每 30 秒探测;WireGuard 节点走内核隧道(吞吐比用户态高一倍以上),WARP 等自动退回用户态。
- **DNS**:Mosdns 分流,国内域名走本地上游、其余经节点走远端上游,默认 DoH/DoT;劫持 LAN 所有 53 端口查询、拒绝 DoT 防泄露;ECS 客户端子网、内网域名转发、AAAA 过滤;解析结果记录让观测页把 IP 标成域名。
- **托管主机**:填一台 VPS 的 SSH,一键在上面部署 WireGuard 或 VLESS-REALITY 服务端并生成对应节点,部署后自动做内核/网卡调优;主机密钥 TOFU。
- **组件更新**:Xray / Mosdns / GeoData 在页面内校验下载、冒烟测试、一键回退;按天/周/月自动更新;GitHub 不可达时可用内置离线包、本地上传、镜像前缀或代理;网关本体也能自升级。
- **可观测**:总览的主机监控(CPU / 内存 / 连接数 / 实时速率)、组件状态、流量图;观测页的实时连接(含 geo 标签与解析出的域名)、节点延迟、事件;生成的配置随时可查。
- **安全**:argon2id 密码、会话 + CSRF、只读 / 读写 API token、凭证 AES-GCM 加密、systemd 沙箱、下载全部校验;2.0 发布前做过一轮安全审计(见 [docs/SECURITY-AUDIT.md](docs/SECURITY-AUDIT.md))。
- **运行环境**:Debian 12/13、Ubuntu 22.04/24.04,amd64 / arm64,deb 包安装,不需要 Go 或 Node。

## 快速安装

能访问 GitHub 的网关,以 root 执行:

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/zlylong/EdgeRouteGW/main/scripts/install.sh)
```

不能访问 GitHub 的网关:在任何能下载的机器上取 [Releases](https://github.com/zlylong/EdgeRouteGW/releases) 里的 `edgeroutegw-full_<版本>_<架构>.tar.gz`(自带校验过的 Xray、Mosdns、GeoData),拷到网关后:

```bash
tar xzf edgeroutegw-full_*.tar.gz && cd edgeroutegw-full_* && bash install-offline.sh
```

建议给网关一台独立的 VM 或 LXC(它会接管 nftables 和策略路由),2 核 2G 足够。从 1.x 升级看 [docs/UPGRADE-FROM-1.x.md](docs/UPGRADE-FROM-1.x.md)。

## 初始登录

没有固定默认密码。安装完成后在网关上读取随机生成的初始密码:

```bash
cat /var/lib/edgeroutegw/bootstrap_password.txt
```

浏览器打开 `http://<网关 IP>/`,用它登录后必须先改密(改密前其它页面和接口都不可用),改完文件自动删除。

## 路由模式

| 模式 | 设备侧 | 主路由侧 | 适合 |
|---|---|---|---|
| **A · 全局网关** | 网关和 DNS 指向本机 | 无需配置 | 家庭、小办公室,最简单 |
| **B · Fake-IP 旁路** | 不改 | OSPF 学到 `198.18.0.0/16`,只把代理域名的流量引过来 | 不想改设备网关、零延迟 |
| **C · 真实 IP 旁路** | 不改 | OSPF 学到规则解析出的真实 IP(内核路由表 200) | 需要真实 IP 分流的传统部署 |

模式在「网络与模式」页切换;B / C 的主路由配置见 [docs/NETWORK_SETUP.md](docs/NETWORK_SETUP.md)。

## 文档

| 文档 | 内容 |
|---|---|
| [docs/ROUTING_RULES_GUIDE.md](docs/ROUTING_RULES_GUIDE.md) | 分流规则入门与原理:规则集、匹配语义、动作、出站组、兜底、设备策略、DNS 配合、三种模式的差异 |
| [docs/NETWORK_SETUP.md](docs/NETWORK_SETUP.md) | 主路由配套配置:RouterOS / OpenWrt 的 OSPF、DNS、防环路 |
| [docs/OPERATIONS.md](docs/OPERATIONS.md) | 运维手册:服务与路径、日志、备份恢复、密码重置、组件与自升级、调优、故障排查 |
| [docs/API.md](docs/API.md) | HTTP API(`/api/v2`):认证、token、所有端点与字段 |
| [docs/DEVELOPER.md](docs/DEVELOPER.md) | 架构、代码布局、构建测试、打包发布 |
| [docs/DESIGN.md](docs/DESIGN.md) | 2.0 设计稿与技术选型 |
| [docs/UPGRADE-FROM-1.x.md](docs/UPGRADE-FROM-1.x.md) | 从 1.x 迁移:备份、停旧、安装、导入、验证、回退 |
| [docs/SECURITY-AUDIT.md](docs/SECURITY-AUDIT.md) | 2.0 发布前的安全审计与修复清单 |
| [docs/CHANGELOG.md](docs/CHANGELOG.md) | 变更记录 |

## 升级

页面「系统 → 组件更新 → EdgeRouteGW → 更新到最新」会下载新版 deb、按 SHA256SUMS 校验、安装并自动重启;也可以 `apt install ./edgeroutegw_<版本>_<架构>.deb`。配置与数据都在 `/var/lib/edgeroutegw`,升级不动它们。

## 开发

```bash
make build          # Go 后端(需要 Go 1.26)
cd web && bun install && bun run build   # 前端(bun + Node 20,vue-tsc 需要 Node)
make test
```

详见 [docs/DEVELOPER.md](docs/DEVELOPER.md)。

## 致谢

[Xray-core](https://github.com/XTLS/Xray-core)、[Mosdns](https://github.com/IrineSistiana/mosdns)、[Loyalsoldier/v2ray-rules-dat](https://github.com/Loyalsoldier/v2ray-rules-dat)、[FRRouting](https://frrouting.org/)、[nftables](https://netfilter.org/projects/nftables/)、[WireGuard](https://www.wireguard.com/)。

## 许可

见 [LICENSE](LICENSE)。
